package health

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/jamesread/armature-health/v2/store"
	"github.com/jamesread/armature-health/v2/store/memstore"
)

// Options tunes a Service. Zero values use the defaults.
type Options struct {
	// Now returns the current time (default time.Now).
	Now func() time.Time
	// SourceTitles names the sources of pushed issues (see Service.Raise), by source ID.
	SourceTitles map[string]string
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
	// pushMu serialises pushed issues (Raise, Resolve, SetIssues). It is separate from runMu
	// so raising never waits for a slow check run; sources and checks never share an ID.
	pushMu sync.Mutex
	// notifications tracks in-flight notification sends (see Wait).
	notifications sync.WaitGroup
}

// NewService creates a service for the given checks. st may be nil to keep issues in memory
// (see store/memstore); pass a store/sqlstore store to keep them across restarts. notifier
// may be nil.
func NewService(st store.Store, checks []Check, notifier Notifier, opts Options) *Service {
	if st == nil {
		st = memstore.New()
	}
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

// CheckTitle returns a check's title, a pushed-issue source's title from
// Options.SourceTitles, or otherwise the ID itself (e.g. for a removed check).
func (s *Service) CheckTitle(checkID string) string {
	if check := s.find(checkID); check != nil {
		return check.Title()
	}
	if title, ok := s.opts.SourceTitles[checkID]; ok {
		return title
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
// unknown id. userID is the app's identifier for the user (e.g. a username or UUID).
func (s *Service) Acknowledge(ctx context.Context, id int64, acknowledged bool, userID string) error {
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
		err = s.reconcile(ctx, check.ID(), check.Title(), issues)
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

// reconcile stores new issues, refreshes ones seen again, and resolves ones no longer found,
// in one store call so readers never see a half-updated list.
func (s *Service) reconcile(ctx context.Context, checkID, title string, issues []Issue) error {
	openByKey, err := s.openIssuesByKey(ctx, checkID)
	if err != nil {
		return err
	}
	return s.apply(ctx, checkID, title, planChanges(checkID, uniqueByKey(checkID, issues), openByKey, s.opts.Now().Unix()))
}

// apply saves changes in one store call, then notifies the inserted issues.
func (s *Service) apply(ctx context.Context, checkID, title string, changes store.IssueChanges) error {
	if err := s.store.ApplyIssueChanges(ctx, changes); err != nil {
		return fmt.Errorf("save issues: %w", err)
	}
	for _, inserted := range changes.Insert {
		s.notify(checkID, title, inserted)
	}
	return nil
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

// uniqueByKey drops, with a warning, issues without a key and repeats of a key (the first
// one wins).
func uniqueByKey(checkID string, issues []Issue) []Issue {
	seen := map[string]bool{}
	out := make([]Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.Key == "" {
			log.WithFields(log.Fields{"check": checkID, "title": issue.Title}).Warn("Health: dropped an issue with no Key; give every issue a stable Key")
			continue
		}
		if seen[issue.Key] {
			log.WithFields(log.Fields{"check": checkID, "key": issue.Key}).Warn("Health: dropped an issue with a repeated Key; the first one is kept")
			continue
		}
		seen[issue.Key] = true
		out = append(out, issue)
	}
	return out
}

// planChanges updates the open issue with the same key, inserts the rest, and resolves open
// issues that were not found.
func planChanges(checkID string, issues []Issue, openByKey map[string]store.Issue, now int64) store.IssueChanges {
	changes := store.IssueChanges{ResolvedAt: now}
	seen := make(map[string]bool, len(issues))
	for _, issue := range issues {
		seen[issue.Key] = true
		stored := toStored(checkID, issue, now)
		if open, ok := openByKey[issue.Key]; ok {
			stored.ID, stored.FirstSeen = open.ID, open.FirstSeen
			changes.Update = append(changes.Update, stored)
			continue
		}
		changes.Insert = append(changes.Insert, stored)
	}
	for key, open := range openByKey {
		if !seen[key] {
			changes.Resolve = append(changes.Resolve, open.ID)
		}
	}
	return changes
}

func toStored(checkID string, issue Issue, now int64) store.Issue {
	return store.Issue{
		CheckID:     checkID,
		IssueKey:    issue.Key,
		Severity:    string(normalizeSeverity(issue.Severity)),
		Title:       issue.Title,
		Detail:      issue.Detail,
		Link:        issue.Link,
		Attributes:  store.CloneAttributes(issue.Attributes),
		FirstSeen:   now,
		LastSeen:    now,
		Occurrences: max(issue.Count, 1),
	}
}

// notify sends the issue-raised event for new warnings and errors, without blocking the run.
func (s *Service) notify(checkID, title string, issue store.Issue) {
	if s.notifier == nil || issue.Severity == string(SeverityInfo) {
		return
	}
	data := map[string]interface{}{
		"check":      title,
		"check_id":   checkID,
		"severity":   issue.Severity,
		"title":      issue.Title,
		"detail":     issue.Detail,
		"link":       issue.Link,
		"attributes": store.CloneAttributes(issue.Attributes),
	}
	s.notifications.Add(1)
	go func() {
		defer s.notifications.Done()
		ctx, cancel := context.WithTimeout(context.Background(), s.opts.NotifyTimeout)
		defer cancel()
		if err := s.notifier.SendNotification(ctx, NotificationEventIssueRaised, data); err != nil {
			log.WithError(err).WithField("check", checkID).Warn("Health: failed to send notification")
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
