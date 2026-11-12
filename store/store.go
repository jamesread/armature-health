// Package store persists health check issues and check runs.
package store

import (
	"context"
	"errors"
)

// ErrIssueNotFound is returned when an issue id does not exist.
var ErrIssueNotFound = errors.New("health issue not found")

// Issue is a stored issue found by a health check. Times are unix seconds; nil optional
// fields mean "not set".
type Issue struct {
	ResolvedAt     *int64
	AcknowledgedAt *int64
	// AcknowledgedBy is the app's identifier for the user (e.g. a username or UUID).
	AcknowledgedBy *string
	// Attributes are app-defined details (e.g. "action_id"); nil when there are none.
	Attributes  map[string]string
	CheckID     string
	IssueKey    string
	Severity    string
	Title       string
	Detail      string
	Link        string
	ID          int64
	FirstSeen   int64
	LastSeen    int64
	Occurrences int
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

// IssueChanges is one check run's changes to its issues. A store applies them all at once,
// so readers see either none or all of them.
type IssueChanges struct {
	// Insert holds newly found issues (their ID is ignored).
	Insert []Issue
	// Update holds open issues found again, by ID. Their severity, title, detail, link,
	// attributes, last-seen time and occurrences are replaced.
	Update []Issue
	// Unacknowledge holds the IDs of issues whose acknowledgement is cleared (because their
	// severity increased).
	Unacknowledge []int64
	// Resolve holds the IDs of open issues no longer found.
	Resolve []int64
	// ResolvedAt is the time recorded on resolved issues.
	ResolvedAt int64
}

// Store persists issues and check runs. store/memstore keeps them in memory (the default);
// store/sqlstore keeps them in SQLite or MySQL/MariaDB.
type Store interface {
	// ListOpenIssuesForCheck returns a check's unresolved issues.
	ListOpenIssuesForCheck(ctx context.Context, checkID string) ([]Issue, error)
	// ApplyIssueChanges applies one check run's changes atomically.
	ApplyIssueChanges(ctx context.Context, changes IssueChanges) error
	// ListIssues returns open issues (optionally with acknowledged and resolved ones), newest first.
	ListIssues(ctx context.Context, includeResolved, includeAcknowledged bool) ([]Issue, error)
	// SetIssueAcknowledged acknowledges (or un-acknowledges) an issue. Returns ErrIssueNotFound
	// for an unknown id.
	SetIssueAcknowledged(ctx context.Context, id int64, acknowledged bool, userID string, at int64) error
	// Summary counts open, unacknowledged issues by severity.
	Summary(ctx context.Context) (Summary, error)
	// PruneResolvedIssues deletes issues resolved before the given time.
	PruneResolvedIssues(ctx context.Context, before int64) error
	// SaveCheckRun records the outcome of a check's latest run.
	SaveCheckRun(ctx context.Context, run CheckRun) error
	// ListCheckRuns returns the latest run of every check that has run.
	ListCheckRuns(ctx context.Context) ([]CheckRun, error)
}
