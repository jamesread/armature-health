package health_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	health "github.com/jamesread/armature-health/v2"
	"github.com/jamesread/armature-health/v2/internal/testdb"
	"github.com/jamesread/armature-health/v2/store"
	"github.com/jamesread/armature-health/v2/store/memstore"
	"github.com/jamesread/armature-health/v2/store/sqlstore"
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
	titles     []string
	details    []string
	previous   []string
	attributes []map[string]string
	mu         sync.Mutex
}

func (n *recordingNotifier) SendNotification(_ context.Context, eventCode string, data map[string]interface{}) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if eventCode == health.NotificationEventIssueRaised {
		n.titles = append(n.titles, data["title"].(string))
		n.details = append(n.details, data["detail"].(string))
		n.previous = append(n.previous, data["previous_severity"].(string))
		n.attributes = append(n.attributes, data["attributes"].(map[string]string))
	}
	return nil
}

type newStoreFunc func(t *testing.T) store.Store

// stores lists every store.Store implementation; each service test runs against all of them.
// The MySQL one skips unless testdb.MySQLDSNEnv is set.
var stores = func() map[string]newStoreFunc {
	out := map[string]newStoreFunc{
		"memstore": func(*testing.T) store.Store { return memstore.New() },
	}
	for name, dialect := range testdb.Dialects {
		out["sqlstore-"+name] = func(t *testing.T) store.Store {
			t.Helper()
			return sqlstore.New(dialect.NewMigrated(t), dialect.Driver)
		}
	}
	return out
}()

func TestNilStoreDefaultsToMemory(t *testing.T) {
	check := &fakeCheck{issues: []health.Issue{{Key: "a", Severity: health.SeverityWarning, Title: "A"}}}
	svc := health.NewService(nil, []health.Check{check}, nil, health.Options{})
	ctx := context.Background()
	svc.RunAll(ctx)
	if summary, err := svc.Summary(ctx); err != nil || summary.Warnings != 1 {
		t.Fatalf("summary %+v, err %v", summary, err)
	}
}

func newTestService(t *testing.T, newStore newStoreFunc, check health.Check) (*health.Service, store.Store, *recordingNotifier, *time.Time) {
	t.Helper()
	st := newStore(t)
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
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			check := &fakeCheck{issues: []health.Issue{
				{Key: "a", Severity: health.SeverityError, Title: "A broke", Detail: "Fix A", Attributes: map[string]string{"action_id": "a1"}},
				{Key: "b", Severity: health.SeverityInfo, Title: "B is odd"},
				{Key: "b", Severity: health.SeverityInfo, Title: "B again (ignored)"},
				{Title: "No key (ignored)"},
			}}
			svc, st, notifier, now := newTestService(t, newStore, check)
			ctx := context.Background()

			svc.RunAll(ctx)
			svc.Wait()
			if got := len(openIssues(t, st)); got != 2 {
				t.Fatalf("open issues after first run: %d", got)
			}
			if len(notifier.titles) != 1 || notifier.titles[0] != "A broke" {
				t.Fatalf("expected one notification for the error only, got %v", notifier.titles)
			}
			if notifier.details[0] != "Fix A" {
				t.Fatalf("notification detail %q", notifier.details[0])
			}
			if got := notifier.attributes[0]; got["action_id"] != "a1" {
				t.Fatalf("notification attributes %v", got)
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
		})
	}
}

func TestAcknowledgedIssuesLeaveTheSummary(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			check := &fakeCheck{issues: []health.Issue{
				{Key: "a", Severity: health.SeverityError, Title: "A"},
				{Key: "b", Severity: health.SeverityWarning, Title: "B"},
			}}
			svc, _, _, _ := newTestService(t, newStore, check)
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
			if err := svc.Acknowledge(ctx, errorID, true, "alice"); err != nil {
				t.Fatal(err)
			}
			acked, _ := svc.ListIssues(ctx, false, true)
			for _, issue := range acked {
				if issue.ID == errorID && (issue.AcknowledgedBy == nil || *issue.AcknowledgedBy != "alice" || issue.AcknowledgedAt == nil) {
					t.Fatalf("acknowledgement not recorded: %+v", issue)
				}
			}
			summary, _ = svc.Summary(ctx)
			if summary.Errors != 0 || summary.Warnings != 1 {
				t.Fatalf("summary after acknowledging %+v", summary)
			}
			if unacked, _ := svc.ListIssues(ctx, false, false); len(unacked) != 1 {
				t.Fatalf("expected the acknowledged issue to be hidden, got %+v", unacked)
			}
			// Un-acknowledging twice is not an error (MySQL reports no changed rows the second time).
			for range 2 {
				if err := svc.Acknowledge(ctx, errorID, false, "alice"); err != nil {
					t.Fatal(err)
				}
			}
			if err := svc.Acknowledge(ctx, errorID, true, "alice"); err != nil {
				t.Fatal(err)
			}
			for _, acknowledged := range []bool{true, false} {
				if err := svc.Acknowledge(ctx, 9999, acknowledged, "alice"); !errors.Is(err, store.ErrIssueNotFound) {
					t.Fatalf("expected ErrIssueNotFound, got %v", err)
				}
			}

			// Acknowledged issues still resolve normally.
			check.issues = nil
			svc.RunAll(ctx)
			if got, _ := svc.ListIssues(ctx, false, true); len(got) != 0 {
				t.Fatalf("expected everything resolved, got %+v", got)
			}
		})
	}
}

func TestAttributesAreKeptAndRefreshed(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			attributes := map[string]string{"action_id": "restart", "config_file": "config.yaml"}
			check := &fakeCheck{issues: []health.Issue{
				{Key: "a", Severity: health.SeverityWarning, Title: "A", Attributes: attributes},
				{Key: "b", Severity: health.SeverityWarning, Title: "B"},
			}}
			svc, st, _, _ := newTestService(t, newStore, check)
			ctx := context.Background()
			svc.RunAll(ctx)
			attributes["action_id"] = "changed by the check after returning"

			byKey := func() map[string]store.Issue {
				out := map[string]store.Issue{}
				for _, issue := range openIssues(t, st) {
					out[issue.IssueKey] = issue
				}
				return out
			}
			issues := byKey()
			if got := issues["a"].Attributes; len(got) != 2 || got["action_id"] != "restart" || got["config_file"] != "config.yaml" {
				t.Fatalf("attributes of a: %v", got)
			}
			if got := issues["b"].Attributes; got != nil {
				t.Fatalf("expected nil attributes for b, got %v", got)
			}

			// Callers can't change stored attributes through a returned issue.
			issues["a"].Attributes["action_id"] = "mutated"
			if got := byKey()["a"].Attributes["action_id"]; got != "restart" {
				t.Fatalf("stored attributes changed through a returned map: %q", got)
			}

			// Seen again with different attributes: replaced.
			check.issues[0].Attributes = map[string]string{"action_id": "stop"}
			svc.RunAll(ctx)
			if got := byKey()["a"].Attributes; len(got) != 1 || got["action_id"] != "stop" {
				t.Fatalf("attributes not refreshed: %v", got)
			}
		})
	}
}

// escalationSteps drives one issue through severity changes and checks the outcome after each.
// set changes the issue's severity and runs (or raises) it.
func escalationSteps(t *testing.T, svc *health.Service, notifier *recordingNotifier, set func(health.Severity)) {
	t.Helper()
	ctx := context.Background()
	issue := func() health.IssueView {
		t.Helper()
		issues, err := svc.ListIssues(ctx, false, true)
		if err != nil || len(issues) != 1 {
			t.Fatalf("expected one open issue, got %+v (%v)", issues, err)
		}
		return issues[0]
	}
	step := func(severity health.Severity, wantAcked bool, wantNotifications int, wantPrevious string) {
		t.Helper()
		set(severity)
		svc.Wait()
		notifier.mu.Lock()
		defer notifier.mu.Unlock()
		if acked := issue().AcknowledgedAt != nil; acked != wantAcked {
			t.Fatalf("%s: acknowledged %v, want %v", severity, acked, wantAcked)
		}
		if len(notifier.previous) != wantNotifications {
			t.Fatalf("%s: %d notifications, want %d", severity, len(notifier.previous), wantNotifications)
		}
		if wantNotifications > 0 && notifier.previous[wantNotifications-1] != wantPrevious {
			t.Fatalf("%s: previous_severity %q, want %q", severity, notifier.previous[wantNotifications-1], wantPrevious)
		}
	}
	ack := func() {
		t.Helper()
		if err := svc.Acknowledge(ctx, issue().ID, true, "alice"); err != nil {
			t.Fatal(err)
		}
	}

	step(health.SeverityInfo, false, 0, "")        // info is never notified
	step(health.SeverityWarning, false, 1, "info") // info -> warning notifies
	ack()
	step(health.SeverityError, false, 2, "warning") // escalation clears the acknowledgement
	ack()
	step(health.SeverityWarning, true, 2, "warning") // de-escalation changes nothing
	step(health.SeverityWarning, true, 2, "warning") // nor does the same severity
}

func TestEscalationFromChecks(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			check := &fakeCheck{}
			svc, _, notifier, _ := newTestService(t, newStore, check)
			escalationSteps(t, svc, notifier, func(severity health.Severity) {
				check.issues = []health.Issue{{Key: "a", Severity: severity, Title: "A"}}
				svc.RunAll(context.Background())
			})
		})
	}
}

func TestEscalationFromRaise(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			svc, _, notifier, _ := newPushService(t, newStore)
			escalationSteps(t, svc, notifier, func(severity health.Severity) {
				if err := svc.Raise(context.Background(), "src", health.Issue{Key: "a", Severity: severity, Title: "A"}); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestFailingChecksKeepTheirIssues(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			check := &fakeCheck{issues: []health.Issue{{Key: "a", Severity: health.SeverityWarning, Title: "A"}}}
			svc, st, _, _ := newTestService(t, newStore, check)
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
		})
	}
}

func TestStatusesBeforeAndAfterRunning(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t, newStore, &fakeCheck{})
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
		})
	}
}

func TestRunUnknownCheck(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t, newStore, &fakeCheck{})
			if err := svc.Run(context.Background(), "nope"); err == nil {
				t.Fatal("expected an error for an unknown check")
			}
			if got := svc.CheckTitle("nope"); got != "nope" {
				t.Fatalf("unregistered check title %q", got)
			}
		})
	}
}

func TestRunPeriodicallyStopsWithContext(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			check := &fakeCheck{issues: []health.Issue{{Key: "a", Severity: health.SeverityInfo}}}
			svc, st, _, _ := newTestService(t, newStore, check)
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
		})
	}
}
