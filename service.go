package health

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/jamesread/armature-health/store"
)

// Options tunes a Service. Zero values use the defaults.
type Options struct {
	// Now returns the current time (default time.Now).
	Now func() time.Time
	// CheckTimeout bounds one check run (default 30s).
	CheckTimeout time.Duration
	// ResolvedRetention is how long resolved issues are kept (default 30 days).
	ResolvedRetention time.Duration
	// NotifyTimeout bounds one notification send (default 30s).
	NotifyTimeout time.Duration
}

func (o Options) withDefaults() Options {
	o.CheckTimeout = orDefault(o.CheckTimeout, 30*time.Second)
	o.ResolvedRetention = orDefault(o.ResolvedRetention, 30*24*time.Hour)
	o.NotifyTimeout = orDefault(o.NotifyTimeout, 30*time.Second)
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Service runs checks and reconciles their issues with the stored ones.
type Service struct {
	store    store.Store
	notifier Notifier
	checks   []Check
	opts     Options

	// runMu serialises runs so a manual "run now" and the background job never reconcile
	// the same check at once.
	runMu sync.Mutex
	// notifications tracks in-flight notification sends (see Wait).
	notifications sync.WaitGroup
}

// NewService creates a service for the given checks. notifier may be nil.
func NewService(st store.Store, checks []Check, notifier Notifier, opts Options) *Service {
	return &Service{
		store:    st,
		checks:   checks,
		notifier: notifier,
		opts:     opts.withDefaults(),
	}
}

// Checks returns the registered checks in order.
func (s *Service) Checks() []Check {
	return s.checks
}

// CheckTitle returns a check's title, or its ID when it is not registered (e.g. removed).
func (s *Service) CheckTitle(checkID string) string {
	if check := s.find(checkID); check != nil {
		return check.Title()
	}
	return checkID
}

func (s *Service) find(checkID string) Check {
	for _, check := range s.checks {
		if check.ID() == checkID {
			return check
		}
	}
	return nil
}

// RunAll runs every check, then prunes long-resolved issues.
func (s *Service) RunAll(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	for _, check := range s.checks {
		s.runCheck(ctx, check)
	}
	if err := s.store.PruneResolvedIssues(ctx, s.opts.Now().Add(-s.opts.ResolvedRetention).Unix()); err != nil {
		log.WithError(err).Warn("Health: failed to prune resolved issues")
	}
}

// Run runs one check by ID.
func (s *Service) Run(ctx context.Context, checkID string) error {
	check := s.find(checkID)
	if check == nil {
		return fmt.Errorf("unknown health check %q", checkID)
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	s.runCheck(ctx, check)
	return nil
}

// RunPeriodically runs all checks after initialDelay and then every interval, until ctx is
// done. It blocks.
func (s *Service) RunPeriodically(ctx context.Context, initialDelay, interval time.Duration) {
	log.WithField("interval", interval).Info("Health check job started")
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.RunAll(ctx)
			timer.Reset(interval)
		}
	}
}

// Wait blocks until notifications sent by earlier runs have finished.
func (s *Service) Wait() {
	s.notifications.Wait()
}

// Statuses lists every registered check with its last run.
func (s *Service) Statuses(ctx context.Context) ([]CheckStatus, error) {
	runs, err := s.store.ListCheckRuns(ctx)
	if err != nil {
		return nil, err
	}
	runByID := make(map[string]store.CheckRun, len(runs))
	for _, run := range runs {
		runByID[run.CheckID] = run
	}

	out := make([]CheckStatus, 0, len(s.checks))
	for _, check := range s.checks {
		status := CheckStatus{CheckID: check.ID(), Title: check.Title(), Status: StatusPending}
		if run, ok := runByID[check.ID()]; ok {
			status.Status, status.LastRunAt, status.DurationMs, status.Error = run.Status, run.LastRunAt, run.DurationMs, run.Error
		}
		out = append(out, status)
	}
	return out, nil
}

// ListIssues returns open issues (optionally with acknowledged and resolved ones), newest
// first, with their check titles.
func (s *Service) ListIssues(ctx context.Context, includeResolved, includeAcknowledged bool) ([]IssueView, error) {
	issues, err := s.store.ListIssues(ctx, includeResolved, includeAcknowledged)
	if err != nil {
		return nil, err
	}
	out := make([]IssueView, 0, len(issues))
	for _, issue := range issues {
		out = append(out, IssueView{Issue: issue, CheckTitle: s.CheckTitle(issue.CheckID)})
	}
	return out, nil
}

// Summary counts open, unacknowledged issues by severity.
func (s *Service) Summary(ctx context.Context) (store.Summary, error) {
	return s.store.Summary(ctx)
}

// Acknowledge acknowledges (or un-acknowledges) an issue on behalf of a user. Acknowledged
// issues leave the summary but still resolve normally. Returns store.ErrIssueNotFound for an
// unknown id.
func (s *Service) Acknowledge(ctx context.Context, id int64, acknowledged bool, userID int64) error {
	return s.store.SetIssueAcknowledged(ctx, id, acknowledged, userID, s.opts.Now().Unix())
}

func (s *Service) runCheck(ctx context.Context, check Check) {
	started := s.opts.Now()
	issues, err := s.safeRun(ctx, check)
	run := store.CheckRun{
		CheckID:    check.ID(),
		LastRunAt:  started.Unix(),
		DurationMs: s.opts.Now().Sub(started).Milliseconds(),
		Status:     StatusOK,
	}

	if err == nil {
		err = s.reconcile(ctx, check, issues)
	}
	if err != nil {
		run.Status = StatusFailed
		run.Error = err.Error()
		log.WithError(err).WithField("check", check.ID()).Warn("Health check failed")
	}
	if saveErr := s.store.SaveCheckRun(ctx, run); saveErr != nil {
		log.WithError(saveErr).WithField("check", check.ID()).Warn("Health: failed to save check run")
	}
}

// safeRun runs a check with a timeout and turns panics into errors.
func (s *Service) safeRun(ctx context.Context, check Check) (issues []Issue, err error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.CheckTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("check panicked: %v", r)
			log.WithField("check", check.ID()).Errorf("Health check panicked: %v\n%s", r, debug.Stack())
		}
	}()
	return check.Run(ctx)
}

// reconcile stores new issues, refreshes ones seen again, and resolves ones no longer found.
func (s *Service) reconcile(ctx context.Context, check Check, issues []Issue) error {
	openByKey, err := s.openIssuesByKey(ctx, check.ID())
	if err != nil {
		return err
	}

	now := s.opts.Now().Unix()
	seen := map[string]bool{}
	for _, issue := range uniqueByKey(issues) {
		seen[issue.Key] = true
		if err := s.upsert(ctx, check, issue, openByKey, now); err != nil {
			return err
		}
	}
	return s.resolveMissing(ctx, openByKey, seen, now)
}

func (s *Service) openIssuesByKey(ctx context.Context, checkID string) (map[string]store.Issue, error) {
	existing, err := s.store.ListOpenIssuesForCheck(ctx, checkID)
	if err != nil {
		return nil, fmt.Errorf("load open issues: %w", err)
	}
	openByKey := make(map[string]store.Issue, len(existing))
	for _, issue := range existing {
		openByKey[issue.IssueKey] = issue
	}
	return openByKey, nil
}

// uniqueByKey drops issues without a key and repeats of a key (the first one wins).
func uniqueByKey(issues []Issue) []Issue {
	seen := map[string]bool{}
	out := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.Key == "" || seen[issue.Key] {
			continue
		}
		seen[issue.Key] = true
		out = append(out, issue)
	}
	return out
}

// upsert refreshes the open issue with the same key, or inserts (and notifies) a new one.
func (s *Service) upsert(ctx context.Context, check Check, issue Issue, openByKey map[string]store.Issue, now int64) error {
	stored := store.Issue{
		CheckID:     check.ID(),
		IssueKey:    issue.Key,
		Severity:    string(normalizeSeverity(issue.Severity)),
		Title:       issue.Title,
		Detail:      issue.Detail,
		Link:        issue.Link,
		LastSeen:    now,
		Occurrences: max(issue.Count, 1),
	}

	if open, ok := openByKey[issue.Key]; ok {
		stored.ID = open.ID
		if err := s.store.UpdateSeenIssue(ctx, stored); err != nil {
			return fmt.Errorf("update issue %s: %w", issue.Key, err)
		}
		return nil
	}

	stored.FirstSeen = now
	if _, err := s.store.InsertIssue(ctx, stored); err != nil {
		return fmt.Errorf("insert issue %s: %w", issue.Key, err)
	}
	s.notify(check, stored)
	return nil
}

func (s *Service) resolveMissing(ctx context.Context, openByKey map[string]store.Issue, seen map[string]bool, now int64) error {
	for key, open := range openByKey {
		if seen[key] {
			continue
		}
		if err := s.store.ResolveIssue(ctx, open.ID, now); err != nil {
			return fmt.Errorf("resolve issue %s: %w", key, err)
		}
	}
	return nil
}

// notify sends the issue-raised event for new warnings and errors, without blocking the run.
func (s *Service) notify(check Check, issue store.Issue) {
	if s.notifier == nil || issue.Severity == string(SeverityInfo) {
		return
	}
	data := map[string]interface{}{
		"check":    check.Title(),
		"check_id": check.ID(),
		"severity": issue.Severity,
		"title":    issue.Title,
		"link":     issue.Link,
	}
	s.notifications.Add(1)
	go func() {
		defer s.notifications.Done()
		ctx, cancel := context.WithTimeout(context.Background(), s.opts.NotifyTimeout)
		defer cancel()
		if err := s.notifier.SendNotification(ctx, NotificationEventIssueRaised, data); err != nil {
			log.WithError(err).WithField("check", check.ID()).Warn("Health: failed to send notification")
		}
	}()
}

func normalizeSeverity(severity Severity) Severity {
	switch severity {
	case SeverityError, SeverityWarning:
		return severity
	default:
		return SeverityInfo
	}
}
