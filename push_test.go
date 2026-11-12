package health_test

import (
	"context"
	"errors"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	health "github.com/jamesread/armature-health/v2"
	"github.com/jamesread/armature-health/v2/store"
)

func newPushService(t *testing.T, newStore newStoreFunc) (*health.Service, store.Store, *recordingNotifier, *time.Time) {
	t.Helper()
	st := newStore(t)
	notifier := &recordingNotifier{}
	now := time.Unix(1_800_000_000, 0)
	svc := health.NewService(st, []health.Check{&fakeCheck{}}, notifier, health.Options{
		Now:          func() time.Time { return now },
		SourceTitles: map[string]string{"api.deprecated": "Deprecated API calls"},
	})
	return svc, st, notifier, &now
}

func TestRaiseCountsRepeats(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			svc, st, notifier, now := newPushService(t, newStore)
			ctx := context.Background()
			first := now.Unix()
			raise := health.Issue{Key: "GET /api/old", Severity: health.SeverityWarning, Title: "Deprecated API called",
				Attributes: map[string]string{"endpoint": "/api/old"}}

			for range 37 {
				*now = now.Add(time.Minute)
				if err := svc.Raise(ctx, "api.deprecated", raise); err != nil {
					t.Fatal(err)
				}
			}
			svc.Wait()
			issues, _ := svc.ListIssues(ctx, false, true)
			if len(issues) != 1 {
				t.Fatalf("expected one issue, got %+v", issues)
			}
			got := issues[0]
			if got.Occurrences != 37 || got.LastSeen != now.Unix() || got.FirstSeen != first+60 {
				t.Fatalf("occurrences %d, first %d, last %d", got.Occurrences, got.FirstSeen, got.LastSeen)
			}
			if got.CheckTitle != "Deprecated API calls" || got.Attributes["endpoint"] != "/api/old" {
				t.Fatalf("title %q, attributes %v", got.CheckTitle, got.Attributes)
			}
			if len(notifier.titles) != 1 {
				t.Fatalf("expected one notification, got %v", notifier.titles)
			}

			// A check run does not touch pushed issues.
			svc.RunAll(ctx)
			if len(openIssues(t, st)) != 1 {
				t.Fatal("check run resolved a pushed issue")
			}

			// Resolve, then raising again is a new issue with a new notification.
			if err := svc.Resolve(ctx, "api.deprecated", raise.Key); err != nil {
				t.Fatal(err)
			}
			if err := svc.Resolve(ctx, "api.deprecated", "never raised"); err != nil {
				t.Fatal(err)
			}
			if len(openIssues(t, st)) != 0 {
				t.Fatal("issue not resolved")
			}
			if err := svc.Raise(ctx, "api.deprecated", raise); err != nil {
				t.Fatal(err)
			}
			svc.Wait()
			if open := openIssues(t, st); len(open) != 1 || open[0].Occurrences != 1 || len(notifier.titles) != 2 {
				t.Fatalf("after re-raising: %+v, notifications %v", open, notifier.titles)
			}
		})
	}
}

func TestSetIssuesReplacesASource(t *testing.T) {
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			svc, st, notifier, now := newPushService(t, newStore)
			ctx := context.Background()
			unset := health.Issue{Key: "env:HOME", Severity: health.SeverityWarning, Title: "HOME is not set"}
			bad := health.Issue{Key: "action:42", Severity: health.SeverityError, Title: "Bad action"}

			if err := svc.SetIssues(ctx, "config.load", []health.Issue{unset, bad}); err != nil {
				t.Fatal(err)
			}
			firstSeen := now.Unix()
			*now = now.Add(time.Hour)
			if err := svc.SetIssues(ctx, "config.load", []health.Issue{unset}); err != nil {
				t.Fatal(err)
			}
			svc.Wait()
			open := openIssues(t, st)
			if len(open) != 1 || open[0].IssueKey != "env:HOME" || open[0].FirstSeen != firstSeen || open[0].LastSeen != now.Unix() {
				t.Fatalf("after reload: %+v", open)
			}
			if len(notifier.titles) != 2 {
				t.Fatalf("still-present issues must not notify again: %v", notifier.titles)
			}
			if title := svc.CheckTitle("config.load"); title != "config.load" {
				t.Fatalf("untitled source should fall back to its ID, got %q", title)
			}
			if err := svc.SetIssues(ctx, "config.load", nil); err != nil {
				t.Fatal(err)
			}
			if len(openIssues(t, st)) != 0 {
				t.Fatal("SetIssues(nil) should resolve everything")
			}
		})
	}
}

func TestPushRejectsBadInput(t *testing.T) {
	svc, _, _, _ := newPushService(t, stores["memstore"])
	ctx := context.Background()
	if err := svc.Raise(ctx, "src", health.Issue{Title: "no key"}); !errors.Is(err, health.ErrMissingKey) {
		t.Fatalf("expected ErrMissingKey, got %v", err)
	}
	for name, err := range map[string]error{
		"raise into a check": svc.Raise(ctx, "test.fake", health.Issue{Key: "k"}),
		"resolve in a check": svc.Resolve(ctx, "test.fake", "k"),
		"set a check":        svc.SetIssues(ctx, "test.fake", nil),
		"empty source":       svc.Raise(ctx, "", health.Issue{Key: "k"}),
	} {
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDroppedIssuesAreLogged(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	svc, _, _, _ := newTestService(t, stores["memstore"], &fakeCheck{issues: []health.Issue{
		{Title: "Forgot the key"},
		{Key: "a", Title: "A"},
		{Key: "a", Title: "A again"},
	}})
	svc.RunAll(context.Background())

	var warnings []string
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel {
			warnings = append(warnings, entry.Message)
		}
	}
	if len(warnings) != 2 {
		t.Fatalf("expected warnings for the missing and repeated keys, got %q", warnings)
	}
}
