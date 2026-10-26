// Package errorlog remembers recent error-level logrus entries, grouped by a normalised
// fingerprint, and reports them through RecentErrorsCheck.
//
//	errorLog := errorlog.New()
//	log.AddHook(errorLog)
//	checks = append(checks, errorlog.RecentErrorsCheck{Log: errorLog})
package errorlog

import (
	"context"
	"crypto/sha1" //nolint:gosec // issue keys only; kept for stable keys across versions
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	health "github.com/jamesread/armature-health"
)

const (
	recentErrorWindow = 24 * time.Hour
	maxLoggedErrors   = 500
	maxTitleMessage   = 160
)

// ErrorLog is a logrus hook that remembers recent error-level log entries, grouped by a
// normalised fingerprint, for RecentErrorsCheck. It only touches memory, so
// logging never blocks on the database.
type ErrorLog struct {
	entries map[string]*loggedError
	now     func() time.Time
	mu      sync.Mutex
}

type loggedError struct {
	firstSeen   time.Time
	lastSeen    time.Time
	fingerprint string
	lastMessage string
	count       int
}

// New returns an empty ErrorLog; add it to logrus with log.AddHook.
func New() *ErrorLog {
	return &ErrorLog{entries: map[string]*loggedError{}, now: time.Now}
}

// Levels implements logrus.Hook.
func (l *ErrorLog) Levels() []log.Level {
	return []log.Level{log.ErrorLevel, log.FatalLevel, log.PanicLevel}
}

// Fire implements logrus.Hook.
func (l *ErrorLog) Fire(entry *log.Entry) error {
	message := entry.Message
	if err, ok := entry.Data[log.ErrorKey]; ok && err != nil {
		message = fmt.Sprintf("%s: %v", message, err)
	}
	l.record(message)
	return nil
}

func (l *ErrorLog) record(message string) {
	fingerprint := fingerprintMessage(message)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if e, ok := l.entries[fingerprint]; ok {
		e.count++
		e.lastSeen = now
		e.lastMessage = message
		return
	}
	if len(l.entries) >= maxLoggedErrors {
		l.evictOldestLocked()
	}
	l.entries[fingerprint] = &loggedError{
		fingerprint: fingerprint,
		lastMessage: message,
		count:       1,
		firstSeen:   now,
		lastSeen:    now,
	}
}

func (l *ErrorLog) evictOldestLocked() {
	var oldest *loggedError
	for _, e := range l.entries {
		if oldest == nil || e.lastSeen.Before(oldest.lastSeen) {
			oldest = e
		}
	}
	if oldest != nil {
		delete(l.entries, oldest.fingerprint)
	}
}

// recent returns errors logged within the window (dropping older ones), most recent first.
func (l *ErrorLog) recent() []loggedError {
	cutoff := l.now().Add(-recentErrorWindow)

	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]loggedError, 0, len(l.entries))
	for key, e := range l.entries {
		if e.lastSeen.Before(cutoff) {
			delete(l.entries, key)
			continue
		}
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].lastSeen.After(out[j].lastSeen) })
	return out
}

var (
	singleQuotedValue = regexp.MustCompile(`'[^']*'`)
	doubleQuotedValue = regexp.MustCompile(`"[^"]*"`)
	uuidValue         = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	hexValue          = regexp.MustCompile(`(?i)\b(0x)?[0-9a-f]{8,}\b`)
	numberValue       = regexp.MustCompile(`\d+(\.\d+)?`)
)

// fingerprintMessage groups messages that differ only in values: quoted strings, UUIDs,
// hex IDs and numbers are replaced with placeholders.
func fingerprintMessage(message string) string {
	s := singleQuotedValue.ReplaceAllString(message, "'…'")
	s = doubleQuotedValue.ReplaceAllString(s, `"…"`)
	s = uuidValue.ReplaceAllString(s, "<uuid>")
	s = hexValue.ReplaceAllString(s, "<hex>")
	return numberValue.ReplaceAllString(s, "N")
}

// RecentErrorsCheck reports errors logged in the last 24 hours (ID "logs.recent_errors").
type RecentErrorsCheck struct {
	Log *ErrorLog
}

func (c RecentErrorsCheck) ID() string    { return "logs.recent_errors" }
func (c RecentErrorsCheck) Title() string { return "Recent errors in the server log" }

func (c RecentErrorsCheck) Run(ctx context.Context) ([]health.Issue, error) {
	var issues []health.Issue
	for _, e := range c.Log.recent() {
		sum := sha1.Sum([]byte(e.fingerprint)) //nolint:gosec // see import
		when := "once, at " + e.lastSeen.Format(time.RFC3339)
		if e.count > 1 {
			when = fmt.Sprintf("%d times between %s and %s", e.count, e.firstSeen.Format(time.RFC3339), e.lastSeen.Format(time.RFC3339))
		}
		issues = append(issues, health.Issue{
			Key:      "log:" + hex.EncodeToString(sum[:8]),
			Severity: health.SeverityError,
			Title:    "Logged error: " + truncate(e.lastMessage, maxTitleMessage),
			Detail:   fmt.Sprintf("Logged %s (counted since the server started). Latest message: %s", when, e.lastMessage),
			Count:    e.count,
		})
	}
	return issues, nil
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
