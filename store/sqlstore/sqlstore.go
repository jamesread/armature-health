// Package sqlstore implements store.Store on database/sql for SQLite and MySQL/MariaDB,
// using the schema in migrate/sqlite and migrate/mysql.
package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jamesread/armature-health/store"
)

// SQL stores health issues in health_issues and check runs in health_check_runs.
type SQL struct {
	db    *sql.DB
	mysql bool
}

var _ store.Store = (*SQL)(nil)

// New returns a store on db. driver is the database/sql driver name; "mysql" selects MySQL
// syntax, anything else SQLite.
func New(db *sql.DB, driver string) *SQL {
	return &SQL{db: db, mysql: driver == "mysql"}
}

const issueColumns = `id, check_id, issue_key, severity, title, detail, link, first_seen, last_seen,
	occurrences, resolved_at, acknowledged_at, acknowledged_by`

func (s *SQL) ListOpenIssuesForCheck(ctx context.Context, checkID string) ([]store.Issue, error) {
	return s.queryIssues(ctx, "SELECT "+issueColumns+" FROM health_issues WHERE check_id = ? AND resolved_at IS NULL", checkID)
}

func (s *SQL) InsertIssue(ctx context.Context, issue store.Issue) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO health_issues (check_id, issue_key, severity, title, detail, link, first_seen, last_seen, occurrences)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		issue.CheckID, issue.IssueKey, issue.Severity, issue.Title, issue.Detail, issue.Link,
		issue.FirstSeen, issue.LastSeen, issue.Occurrences)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *SQL) UpdateSeenIssue(ctx context.Context, issue store.Issue) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE health_issues SET severity = ?, title = ?, detail = ?, link = ?, last_seen = ?, occurrences = ?
		WHERE id = ?`,
		issue.Severity, issue.Title, issue.Detail, issue.Link, issue.LastSeen, issue.Occurrences, issue.ID)
	return err
}

func (s *SQL) ResolveIssue(ctx context.Context, id int64, at int64) error {
	_, err := s.db.ExecContext(ctx, "UPDATE health_issues SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL", at, id)
	return err
}

func (s *SQL) ListIssues(ctx context.Context, includeResolved, includeAcknowledged bool) ([]store.Issue, error) {
	var where []string
	if !includeResolved {
		where = append(where, "resolved_at IS NULL")
	}
	if !includeAcknowledged {
		where = append(where, "acknowledged_at IS NULL")
	}
	query := "SELECT " + issueColumns + " FROM health_issues"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	return s.queryIssues(ctx, query+" ORDER BY last_seen DESC, id DESC")
}

func (s *SQL) SetIssueAcknowledged(ctx context.Context, id int64, acknowledged bool, userID int64, at int64) error {
	var res sql.Result
	var err error
	if acknowledged {
		res, err = s.db.ExecContext(ctx, "UPDATE health_issues SET acknowledged_at = ?, acknowledged_by = ? WHERE id = ?", at, userID, id)
	} else {
		res, err = s.db.ExecContext(ctx, "UPDATE health_issues SET acknowledged_at = NULL, acknowledged_by = NULL WHERE id = ?", id)
	}
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %d", store.ErrIssueNotFound, id)
	}
	return nil
}

func (s *SQL) Summary(ctx context.Context) (store.Summary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT severity, COUNT(*) FROM health_issues
		WHERE resolved_at IS NULL AND acknowledged_at IS NULL
		GROUP BY severity`)
	if err != nil {
		return store.Summary{}, err
	}
	defer func() { _ = rows.Close() }()

	var summary store.Summary
	for rows.Next() {
		var severity string
		var count int
		if err := rows.Scan(&severity, &count); err != nil {
			return store.Summary{}, err
		}
		addToSummary(&summary, severity, count)
	}
	return summary, rows.Err()
}

func addToSummary(summary *store.Summary, severity string, count int) {
	switch severity {
	case "error":
		summary.Errors = count
	case "warning":
		summary.Warnings = count
	default:
		summary.Infos += count
	}
}

func (s *SQL) PruneResolvedIssues(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM health_issues WHERE resolved_at IS NOT NULL AND resolved_at < ?", before)
	return err
}

func (s *SQL) SaveCheckRun(ctx context.Context, run store.CheckRun) error {
	query := `INSERT INTO health_check_runs (check_id, last_run_at, duration_ms, status, error) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(check_id) DO UPDATE SET last_run_at = excluded.last_run_at, duration_ms = excluded.duration_ms,
			status = excluded.status, error = excluded.error`
	if s.mysql {
		query = `INSERT INTO health_check_runs (check_id, last_run_at, duration_ms, status, error) VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE last_run_at = VALUES(last_run_at), duration_ms = VALUES(duration_ms),
				status = VALUES(status), error = VALUES(error)`
	}
	_, err := s.db.ExecContext(ctx, query, run.CheckID, run.LastRunAt, run.DurationMs, run.Status, run.Error)
	return err
}

func (s *SQL) ListCheckRuns(ctx context.Context) ([]store.CheckRun, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT check_id, last_run_at, duration_ms, status, error FROM health_check_runs")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var runs []store.CheckRun
	for rows.Next() {
		var run store.CheckRun
		if err := rows.Scan(&run.CheckID, &run.LastRunAt, &run.DurationMs, &run.Status, &run.Error); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *SQL) queryIssues(ctx context.Context, query string, args ...any) ([]store.Issue, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var issues []store.Issue
	for rows.Next() {
		var i store.Issue
		if err := rows.Scan(&i.ID, &i.CheckID, &i.IssueKey, &i.Severity, &i.Title, &i.Detail, &i.Link,
			&i.FirstSeen, &i.LastSeen, &i.Occurrences, &i.ResolvedAt, &i.AcknowledgedAt, &i.AcknowledgedBy); err != nil {
			return nil, err
		}
		issues = append(issues, i)
	}
	return issues, rows.Err()
}
