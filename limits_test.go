package health_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	health "github.com/jamesread/armature-health/v2"
	"github.com/jamesread/armature-health/v2/store"
)

type idCheck struct {
	id     string
	issues []health.Issue
}

func (c *idCheck) ID() string                                  { return c.id }
func (c *idCheck) Title() string                               { return c.id }
func (c *idCheck) Run(context.Context) ([]health.Issue, error) { return c.issues, nil }

func TestOversizedFieldsAreBoundedNotFatal(t *testing.T) {
	longKey := strings.Repeat("k", 300)
	otherLongKey := strings.Repeat("k", 299) + "j"
	attributes := map[string]string{"big": strings.Repeat("v", 5000)}
	for i := range 100 {
		attributes[fmt.Sprintf("a%03d", i)] = strings.Repeat("é", 1000)
	}
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			check := &fakeCheck{issues: []health.Issue{
				{Key: "small", Severity: health.SeverityWarning, Title: "Small"},
				{
					Key: longKey, Severity: health.SeverityError,
					Title:      strings.Repeat("t", 1000),
					Detail:     strings.Repeat("d", 70000),
					Link:       "/" + strings.Repeat("l", 600),
					Attributes: attributes,
				},
				{Key: otherLongKey, Severity: health.SeverityWarning, Title: "Other long key \xff"},
			}}
			svc, st, notifier, now := newTestService(t, newStore, check)
			ctx := context.Background()
			svc.RunAll(ctx)
			svc.Wait()

			statuses, err := svc.Statuses(ctx)
			if err != nil || statuses[0].Status != health.StatusOK {
				t.Fatalf("statuses %+v, err %v", statuses, err)
			}
			issues := openIssues(t, st)
			if len(issues) != 3 || len(notifier.titles) != 3 {
				t.Fatalf("expected 3 issues and notifications, got %d and %d", len(issues), len(notifier.titles))
			}
			for _, issue := range issues {
				assertBounded(t, issue)
			}

			// Hashed keys are stable: the next run updates the same issues.
			*now = now.Add(time.Minute)
			svc.RunAll(ctx)
			svc.Wait()
			if got := openIssues(t, st); len(got) != 3 || len(notifier.titles) != 3 {
				t.Fatalf("second run: %d issues, %d notifications", len(got), len(notifier.titles))
			}
		})
	}
}

func assertBounded(t *testing.T, issue store.Issue) {
	t.Helper()
	if len(issue.IssueKey) > health.MaxKeyBytes || !utf8.ValidString(issue.Title) {
		t.Fatalf("key %q (%d bytes) or title %q not bounded", issue.IssueKey, len(issue.IssueKey), issue.Title)
	}
	if utf8.RuneCountInString(issue.Title) > health.MaxTitleRunes || utf8.RuneCountInString(issue.Detail) > health.MaxDetailRunes {
		t.Fatalf("title or detail not bounded: %d, %d runes", utf8.RuneCountInString(issue.Title), utf8.RuneCountInString(issue.Detail))
	}
	if strings.HasPrefix(issue.Link, "/l") {
		t.Fatalf("oversized link kept: %q", issue.Link)
	}
	if v, ok := issue.Attributes["big"]; ok && utf8.RuneCountInString(v) > health.MaxAttributeValueRunes {
		t.Fatalf("attribute value not bounded: %d runes", utf8.RuneCountInString(v))
	}
}

func TestResolveAndRaiseWithLongKeys(t *testing.T) {
	longKey := strings.Repeat("x", 400)
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			svc, st, _, _ := newPushService(t, newStore)
			ctx := context.Background()
			for range 2 {
				if err := svc.Raise(ctx, "config.load", health.Issue{Key: longKey, Severity: health.SeverityWarning}); err != nil {
					t.Fatal(err)
				}
			}
			if issues := openIssues(t, st); len(issues) != 1 || issues[0].Occurrences != 2 {
				t.Fatalf("expected one issue raised twice, got %+v", issues)
			}
			if err := svc.Resolve(ctx, "config.load", longKey); err != nil {
				t.Fatal(err)
			}
			if issues := openIssues(t, st); len(issues) != 0 {
				t.Fatalf("expected the long-keyed issue resolved, got %+v", issues)
			}
			if err := svc.Raise(ctx, strings.Repeat("s", health.MaxIDBytes+1), health.Issue{Key: "a"}); err == nil {
				t.Fatal("expected an error for a too-long source ID")
			}
		})
	}
}

type hangingCheck struct{ release chan struct{} }

func (c *hangingCheck) ID() string    { return "test.hangs" }
func (c *hangingCheck) Title() string { return "Hangs" }
func (c *hangingCheck) Run(context.Context) ([]health.Issue, error) {
	<-c.release // ignores its context
	return nil, nil
}

func TestCheckTimeoutAbandonsAHungCheck(t *testing.T) {
	hung := &hangingCheck{release: make(chan struct{})}
	other := &idCheck{id: "test.other", issues: []health.Issue{{Key: "a", Severity: health.SeverityInfo}}}
	svc := health.NewService(nil, []health.Check{hung, other}, nil, health.Options{CheckTimeout: 20 * time.Millisecond})
	ctx := context.Background()

	done := make(chan struct{})
	go func() { svc.RunAll(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunAll blocked on a check that ignores its context")
	}
	assertStatus(t, svc, "test.hangs", health.StatusFailed)
	assertStatus(t, svc, "test.other", health.StatusOK)

	// While the abandoned Run is still going, the check is not started again.
	if err := svc.Run(ctx, "test.hangs"); err != nil {
		t.Fatal(err)
	}
	if status := statusOf(t, svc, "test.hangs"); !strings.Contains(status.Error, "has not returned") {
		t.Fatalf("expected a still-running error, got %+v", status)
	}

	close(hung.release)
	deadline := time.Now().Add(5 * time.Second)
	for statusOf(t, svc, "test.hangs").Status != health.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("check did not run again after returning")
		}
		_ = svc.Run(ctx, "test.hangs")
		time.Sleep(5 * time.Millisecond)
	}
}

func statusOf(t *testing.T, svc *health.Service, checkID string) health.CheckStatus {
	t.Helper()
	statuses, err := svc.Statuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.CheckID == checkID {
			return status
		}
	}
	t.Fatalf("no status for %s", checkID)
	return health.CheckStatus{}
}

func assertStatus(t *testing.T, svc *health.Service, checkID, want string) {
	t.Helper()
	if got := statusOf(t, svc, checkID); got.Status != want {
		t.Fatalf("%s: status %+v, want %s", checkID, got, want)
	}
}

func TestDuplicateAndInvalidCheckIDsAreDropped(t *testing.T) {
	first := &idCheck{id: "dup", issues: []health.Issue{{Key: "a", Severity: health.SeverityError}}}
	second := &idCheck{id: "dup", issues: []health.Issue{{Key: "b", Severity: health.SeverityError}}}
	invalid := &idCheck{id: ""}
	notifier := &recordingNotifier{}
	svc := health.NewService(nil, []health.Check{first, second, invalid, nil}, notifier, health.Options{})
	if got := svc.Checks(); len(got) != 1 || got[0] != first {
		t.Fatalf("expected only the first dup check, got %v", got)
	}
	for range 3 {
		svc.RunAll(context.Background())
	}
	svc.Wait()
	if len(notifier.titles) != 1 {
		t.Fatalf("expected one notification, got %d", len(notifier.titles))
	}
}

// ackDuringLoad acknowledges every open issue just after the service loads them, as an admin
// acknowledging during a run would.
type ackDuringLoad struct {
	store.Store
	armed bool
}

func (s *ackDuringLoad) ListOpenIssuesForCheck(ctx context.Context, checkID string) ([]store.Issue, error) {
	issues, err := s.Store.ListOpenIssuesForCheck(ctx, checkID)
	for _, issue := range issues {
		if s.armed {
			_ = s.SetIssueAcknowledged(ctx, issue.ID, true, "bob", 1)
		}
	}
	return issues, err
}

func TestEscalationClearsAnAcknowledgementMadeDuringTheRun(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			st := &ackDuringLoad{Store: newStore(t)}
			check := &fakeCheck{issues: []health.Issue{{Key: "a", Severity: health.SeverityWarning}}}
			svc := health.NewService(st, []health.Check{check}, nil, health.Options{})
			ctx := context.Background()
			svc.RunAll(ctx)

			st.armed = true
			check.issues[0].Severity = health.SeverityError
			svc.RunAll(ctx)
			if summary, _ := svc.Summary(ctx); summary.Errors != 1 {
				t.Fatalf("escalated issue should be unacknowledged, summary %+v", summary)
			}
		})
	}
}

func TestNotificationsPerRunAreCapped(t *testing.T) {
	var issues []health.Issue
	for i := range 25 {
		issues = append(issues, health.Issue{Key: fmt.Sprint(i), Severity: health.SeverityError, Title: fmt.Sprint(i)})
	}
	issues = append(issues, health.Issue{Key: "info", Severity: health.SeverityInfo})
	notifier := &recordingNotifier{}
	svc := health.NewService(nil, []health.Check{&fakeCheck{issues: issues}}, notifier, health.Options{MaxNotifications: 5})
	svc.RunAll(context.Background())
	svc.Wait()
	if len(notifier.titles) != 5 {
		t.Fatalf("expected 5 notifications, got %d", len(notifier.titles))
	}
	if summary, _ := svc.Summary(context.Background()); summary.Errors != 25 {
		t.Fatalf("every issue should still be stored, summary %+v", summary)
	}
}

func TestRunPeriodicallyWithZeroIntervalDoesNotSpin(t *testing.T) {
	check := &countingCheck{}
	svc := health.NewService(nil, []health.Check{check}, nil, health.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	svc.RunPeriodically(ctx, 0, 0)
	if n := check.runs.Load(); n != 1 {
		t.Fatalf("expected one run in 200ms with the default interval, got %d", n)
	}
}

type countingCheck struct{ runs atomic.Int32 }

func (c *countingCheck) ID() string    { return "test.counting" }
func (c *countingCheck) Title() string { return "Counting" }
func (c *countingCheck) Run(context.Context) ([]health.Issue, error) {
	c.runs.Add(1)
	return nil, nil
}
