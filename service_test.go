package health_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	health "github.com/jamesread/armature-health"
	"github.com/jamesread/armature-health/internal/testdb"
	"github.com/jamesread/armature-health/store"
	"github.com/jamesread/armature-health/store/sqlstore"
)

type fakeCheck struct {
	issues []health.Issue
	err    error
	panics bool
}

func (c *fakeCheck) ID() string    { return "test.fake" }
func (c *fakeCheck) Title() string { return "Fake" }
func (c *fakeCheck) Run(context.Context) ([]health.Issue, error) {
	if c.panics {
		panic("boom")
	}
	return c.issues, c.err
}

type recordingNotifier struct {
	mu     sync.Mutex
	titles []string
}

func (n *recordingNotifier) SendNotification(_ context.Context, eventCode string, data map[string]interface{}) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if eventCode == health.NotificationEventIssueRaised {
		n.titles = append(n.titles, data["title"].(string))
	}
	return nil
}

func newTestService(t *testing.T, check health.Check) (*health.Service, store.Store, *recordingNotifier, *time.Time) {
	t.Helper()
	st := sqlstore.New(testdb.NewMigratedSQLite(t), "sqlite")
	notifier := &recordingNotifier{}
	now := time.Unix(1_800_000_000, 0)
	svc := health.NewService(st, []health.Check{check}, notifier, health.Options{Now: func() time.Time { return now }})
	return svc, st, notifier, &now
}

func openIssues(t *testing.T, st store.Store) []store.Issue {
	t.Helper()
	issues, err := st.ListIssues(context.Background(), false, true)
	if err != nil {
		t.Fatal(err)
	}
	return issues
}

func TestReconcileLifecycle(t *testing.T) {
	check := &fakeCheck{issues: []health.Issue{
		{Key: "a", Severity: health.SeverityError, Title: "A broke"},
		{Key: "b", Severity: health.SeverityInfo, Title: "B is odd"},
		{Key: "b", Severity: health.SeverityInfo, Title: "B again (ignored)"},
		{Title: "No key (ignored)"},
	}}
	svc, st, notifier, now := newTestService(t, check)
	ctx := context.Background()

	svc.RunAll(ctx)
	svc.Wait()
	if got := len(openIssues(t, st)); got != 2 {
		t.Fatalf("open issues after first run: %d", got)
	}
	if len(notifier.titles) != 1 || notifier.titles[0] != "A broke" {
		t.Fatalf("expected one notification for the error only, got %v", notifier.titles)
	}

	// Seen again: updated, not re-notified.
	*now = now.Add(time.Hour)
	check.issues[0].Title = "A still broke"
	svc.RunAll(ctx)
	svc.Wait()
	issues := openIssues(t, st)
	if len(issues) != 2 || len(notifier.titles) != 1 {
		t.Fatalf("second run: %d issues, notifications %v", len(issues), notifier.titles)
	}
	for _, issue := range issues {
		if issue.IssueKey == "a" && (issue.Title != "A still broke" || issue.LastSeen != now.Unix() || issue.FirstSeen == issue.LastSeen) {
			t.Fatalf("issue a not refreshed: %+v", issue)
		}
	}

	// Gone: resolved.
	check.issues = check.issues[1:2]
	svc.RunAll(ctx)
	if got := openIssues(t, st); len(got) != 1 || got[0].IssueKey != "b" {
		t.Fatalf("expected only b open, got %+v", got)
	}

	// Recurs: a new row and a new notification.
	check.issues = append(check.issues, health.Issue{Key: "a", Severity: health.SeverityError, Title: "A broke again"})
	svc.RunAll(ctx)
	svc.Wait()
	all, _ := st.ListIssues(ctx, true, true)
	if len(all) != 3 || len(notifier.titles) != 2 {
		t.Fatalf("recurrence: %d rows, notifications %v", len(all), notifier.titles)
	}

	// Long-resolved issues are pruned.
	*now = now.Add(31 * 24 * time.Hour)
	svc.RunAll(ctx)
	if all, _ := st.ListIssues(ctx, true, true); len(all) != 2 {
		t.Fatalf("expected the resolved issue to be pruned, got %+v", all)
	}
}

func TestAcknowledgedIssuesLeaveTheSummary(t *testing.T) {
	check := &fakeCheck{issues: []health.Issue{
		{Key: "a", Severity: health.SeverityError, Title: "A"},
		{Key: "b", Severity: health.SeverityWarning, Title: "B"},
	}}
	svc, _, _, _ := newTestService(t, check)
	ctx := context.Background()
	svc.RunAll(ctx)

	summary, _ := svc.Summary(ctx)
	if summary.Errors != 1 || summary.Warnings != 1 {
		t.Fatalf("summary %+v", summary)
	}
	issues, err := svc.ListIssues(ctx, false, false)
	if err != nil {
		t.Fatal(err)
	}
	var errorID int64
	for _, issue := range issues {
		if issue.CheckTitle != "Fake" {
			t.Fatalf("check title %q", issue.CheckTitle)
		}
		if issue.IssueKey == "a" {
			errorID = issue.ID
		}
	}
	if err := svc.Acknowledge(ctx, errorID, true, 1); err != nil {
		t.Fatal(err)
	}
	summary, _ = svc.Summary(ctx)
	if summary.Errors != 0 || summary.Warnings != 1 {
		t.Fatalf("summary after acknowledging %+v", summary)
	}
	if unacked, _ := svc.ListIssues(ctx, false, false); len(unacked) != 1 {
		t.Fatalf("expected the acknowledged issue to be hidden, got %+v", unacked)
	}
	if err := svc.Acknowledge(ctx, 9999, true, 1); !errors.Is(err, store.ErrIssueNotFound) {
		t.Fatalf("expected ErrIssueNotFound, got %v", err)
	}

	// Acknowledged issues still resolve normally.
	check.issues = nil
	svc.RunAll(ctx)
	if got, _ := svc.ListIssues(ctx, false, true); len(got) != 0 {
		t.Fatalf("expected everything resolved, got %+v", got)
	}
}

func TestFailingChecksKeepTheirIssues(t *testing.T) {
	check := &fakeCheck{issues: []health.Issue{{Key: "a", Severity: health.SeverityWarning, Title: "A"}}}
	svc, st, _, _ := newTestService(t, check)
	ctx := context.Background()
	svc.RunAll(ctx)

	for name, broken := range map[string]*fakeCheck{
		"error": {err: errors.New("database unavailable")},
		"panic": {panics: true},
	} {
		check.err, check.panics, check.issues = broken.err, broken.panics, nil
		svc.RunAll(ctx)
		if got := openIssues(t, st); len(got) != 1 {
			t.Fatalf("%s: issues should be left alone, got %+v", name, got)
		}
		statuses, _ := svc.Statuses(ctx)
		if len(statuses) != 1 || statuses[0].Status != health.StatusFailed || statuses[0].Error == "" {
			t.Fatalf("%s: run not recorded as failed: %+v", name, statuses)
		}
	}
}

func TestStatusesBeforeAndAfterRunning(t *testing.T) {
	svc, _, _, _ := newTestService(t, &fakeCheck{})
	ctx := context.Background()

	statuses, _ := svc.Statuses(ctx)
	if len(statuses) != 1 || statuses[0].Status != health.StatusPending || statuses[0].Title != "Fake" {
		t.Fatalf("before running: %+v", statuses)
	}
	if err := svc.Run(ctx, "test.fake"); err != nil {
		t.Fatal(err)
	}
	statuses, _ = svc.Statuses(ctx)
	if statuses[0].Status != health.StatusOK || statuses[0].LastRunAt == 0 {
		t.Fatalf("after running: %+v", statuses)
	}
	// Saving a second run updates the row rather than conflicting.
	if err := svc.Run(ctx, "test.fake"); err != nil {
		t.Fatal(err)
	}
}

func TestRunUnknownCheck(t *testing.T) {
	svc, _, _, _ := newTestService(t, &fakeCheck{})
	if err := svc.Run(context.Background(), "nope"); err == nil {
		t.Fatal("expected an error for an unknown check")
	}
	if got := svc.CheckTitle("nope"); got != "nope" {
		t.Fatalf("unregistered check title %q", got)
	}
}

func TestRunPeriodicallyStopsWithContext(t *testing.T) {
	check := &fakeCheck{issues: []health.Issue{{Key: "a", Severity: health.SeverityInfo}}}
	svc, st, _, _ := newTestService(t, check)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.RunPeriodically(ctx, 0, time.Hour)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for len(openIssues(t, st)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("first run did not happen")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunPeriodically did not stop")
	}
}
