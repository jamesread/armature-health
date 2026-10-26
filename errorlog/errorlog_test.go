package errorlog

import (
	"context"
	"errors"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestErrorLogGroupsMessagesByFingerprint(t *testing.T) {
	l := New()
	now := time.Unix(1_800_000_000, 0)
	l.now = func() time.Time { return now }

	l.record("Failed to create item: Error 1364 (HY000): Field 'starts' doesn't have a default value")
	l.record("Failed to create item: Error 1364 (HY000): Field 'finishes' doesn't have a default value")
	l.record(`Failed to load feed "abc": status 503 for 3f2c1a9e-1b2c-4d5e-8f90-123456789abc`)
	l.record(`Failed to load feed "xyz": status 502 for 00000000-1b2c-4d5e-8f90-123456789abc`)
	l.record("Something else entirely")

	issues, _ := RecentErrorsCheck{Log: l}.Run(context.Background())
	if len(issues) != 3 {
		t.Fatalf("expected 3 groups, got %d: %+v", len(issues), issues)
	}
	counts := map[int]int{}
	for _, issue := range issues {
		counts[issue.Count]++
	}
	if counts[2] != 2 || counts[1] != 1 {
		t.Fatalf("unexpected grouping %+v", issues)
	}

	now = now.Add(25 * time.Hour)
	if issues, _ := (RecentErrorsCheck{Log: l}).Run(context.Background()); len(issues) != 0 {
		t.Fatalf("errors older than a day should drop out, got %+v", issues)
	}
}

func TestErrorLogAsLogrusHook(t *testing.T) {
	l := New()
	logger := log.New()
	logger.AddHook(l)
	logger.WithError(errors.New("disk full")).Error("Failed to save")
	logger.Warn("not an error")

	issues, _ := RecentErrorsCheck{Log: l}.Run(context.Background())
	if len(issues) != 1 || issues[0].Title != "Logged error: Failed to save: disk full" {
		t.Fatalf("unexpected issues %+v", issues)
	}
}

func TestErrorLogEvictsTheOldest(t *testing.T) {
	l := New()
	now := time.Unix(1_800_000_000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < maxLoggedErrors; i++ {
		now = now.Add(time.Second)
		l.record(string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)))
	}
	now = now.Add(time.Second)
	l.record("newest")
	if _, ok := l.entries["aaa"]; ok || len(l.entries) != maxLoggedErrors {
		t.Fatalf("expected the oldest entry to be evicted, have %d entries", len(l.entries))
	}
}
