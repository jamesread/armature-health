// Package store persists health check issues and check runs.
package store

import (
	"context"
	"database/sql"
	"errors"
)

// ErrIssueNotFound is returned when an issue id does not exist.
var ErrIssueNotFound = errors.New("health issue not found")

// Issue is a stored issue found by a health check. Times are unix seconds.
type Issue struct {
	CheckID        string
	IssueKey       string
	Severity       string
	Title          string
	Detail         string
	Link           string
	ID             int64
	FirstSeen      int64
	LastSeen       int64
	Occurrences    int
	ResolvedAt     sql.NullInt64
	AcknowledgedAt sql.NullInt64
	AcknowledgedBy sql.NullInt64
}

// CheckRun is the last run of a health check.
type CheckRun struct {
	CheckID    string
	Status     string
	Error      string
	LastRunAt  int64
	DurationMs int64
}

// Summary counts open, unacknowledged issues by severity.
type Summary struct {
	Errors   int
	Warnings int
	Infos    int
}

// Store persists issues and check runs (see store/sqlstore for the SQL implementation).
type Store interface {
	// ListOpenIssuesForCheck returns a check's unresolved issues.
	ListOpenIssuesForCheck(ctx context.Context, checkID string) ([]Issue, error)
	// InsertIssue stores a newly found issue and returns its id.
	InsertIssue(ctx context.Context, issue Issue) (int64, error)
	// UpdateSeenIssue refreshes an open issue that a check found again.
	UpdateSeenIssue(ctx context.Context, issue Issue) error
	// ResolveIssue marks an open issue as resolved.
	ResolveIssue(ctx context.Context, id int64, at int64) error
	// ListIssues returns open issues (optionally with acknowledged and resolved ones), newest first.
	ListIssues(ctx context.Context, includeResolved, includeAcknowledged bool) ([]Issue, error)
	// SetIssueAcknowledged acknowledges (or un-acknowledges) an issue. Returns ErrIssueNotFound
	// for an unknown id.
	SetIssueAcknowledged(ctx context.Context, id int64, acknowledged bool, userID int64, at int64) error
	// Summary counts open, unacknowledged issues by severity.
	Summary(ctx context.Context) (Summary, error)
	// PruneResolvedIssues deletes issues resolved before the given time.
	PruneResolvedIssues(ctx context.Context, before int64) error
	// SaveCheckRun records the outcome of a check's latest run.
	SaveCheckRun(ctx context.Context, run CheckRun) error
	// ListCheckRuns returns the latest run of every check that has run.
	ListCheckRuns(ctx context.Context) ([]CheckRun, error)
}
