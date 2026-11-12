// Package sqlstore implements store.Store on database/sql for SQLite and MySQL/MariaDB,
// using the schema in migrate/sqlite and migrate/mysql.
package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jamesread/armature-health/v2/internal/attrs"
	"github.com/jamesread/armature-health/v2/store"
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
	occurrences, resolved_at, acknowledged_at, acknowledged_by, attributes`

func (s *SQL) ListOpenIssuesForCheck(ctx context.Context, checkID string) ([]store.Issue, error) {
	return s.queryIssues(ctx, "SELECT "+issueColumns+" FROM health_issues WHERE check_id = ? AND resolved_at IS NULL", checkID)
}

// ApplyIssueChanges applies all the changes in one transaction, so readers never see part of
// them.
func (s *SQL) ApplyIssueChanges(ctx context.Context, changes store.IssueChanges) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := applyChanges(ctx, tx, changes); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func applyChanges(ctx context.Context, tx *sql.Tx, changes store.IssueChanges) error {
	if err := insertIssues(ctx, tx, changes.Insert); err != nil {
		return err
	}
	if err := updateIssues(ctx, tx, changes.Update); err != nil {
		return err
	}
	return resolveIssues(ctx, tx, changes.Resolve, changes.ResolvedAt)
}

func insertIssues(ctx context.Context, tx *sql.Tx, issues []store.Issue) error {
	for _, issue := range issues {
		attributes, err := encodeAttributes(issue.Attributes)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO health_issues (check_id, issue_key, severity, title, detail, link, first_seen, last_seen, occurrences, attributes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			issue.CheckID, issue.IssueKey, issue.Severity, issue.Title, issue.Detail, issue.Link,
			issue.FirstSeen, issue.LastSeen, issue.Occurrences, attributes); err != nil {
			return fmt.Errorf("insert issue %s: %w", issue.IssueKey, err)
		}
	}
	return nil
}

func updateIssues(ctx context.Context, tx *sql.Tx, issues []store.Issue) error {
	for _, issue := range issues {
		attributes, err := encodeAttributes(issue.Attributes)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE health_issues SET severity = ?, title = ?, detail = ?, link = ?, attributes = ?, last_seen = ?, occurrences = ?
			WHERE id = ?`,
			issue.Severity, issue.Title, issue.Detail, issue.Link, attributes, issue.LastSeen, issue.Occurrences, issue.ID); err != nil {
			return fmt.Errorf("update issue %s: %w", issue.IssueKey, err)
		}
	}
	return nil
}

func resolveIssues(ctx context.Context, tx *sql.Tx, ids []int64, at int64) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "UPDATE health_issues SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL", at, id); err != nil {
			return fmt.Errorf("resolve issue %d: %w", id, err)
		}
	}
	return nil
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

func (s *SQL) SetIssueAcknowledged(ctx context.Context, id int64, acknowledged bool, userID string, at int64) error {
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
	// MySQL counts changed rows, not matched ones, so 0 can also mean "already in that state".
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return s.requireIssue(ctx, id)
	}
	return nil
}

// requireIssue returns ErrIssueNotFound when no issue has the id.
func (s *SQL) requireIssue(ctx context.Context, id int64) error {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM health_issues WHERE id = ?", id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
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
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

func scanIssue(rows *sql.Rows) (store.Issue, error) {
	var i store.Issue
	var resolvedAt, acknowledgedAt sql.NullInt64
	var acknowledgedBy sql.NullString
	var attributes string
	if err := rows.Scan(&i.ID, &i.CheckID, &i.IssueKey, &i.Severity, &i.Title, &i.Detail, &i.Link,
		&i.FirstSeen, &i.LastSeen, &i.Occurrences, &resolvedAt, &acknowledgedAt, &acknowledgedBy, &attributes); err != nil {
		return i, err
	}
	i.ResolvedAt = ptr(resolvedAt.Int64, resolvedAt.Valid)
	i.AcknowledgedAt = ptr(acknowledgedAt.Int64, acknowledgedAt.Valid)
	i.AcknowledgedBy = ptr(acknowledgedBy.String, acknowledgedBy.Valid)
	var err error
	i.Attributes, err = decodeAttributes(attributes)
	return i, err
}

func ptr[T any](v T, valid bool) *T {
	if !valid {
		return nil
	}
	return &v
}

func encodeAttributes(attributes map[string]string) (string, error) {
	if len(attributes) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(attributes)
	return string(b), err
}

func decodeAttributes(s string) (map[string]string, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	var attributes map[string]string
	if err := json.Unmarshal([]byte(s), &attributes); err != nil {
		return nil, fmt.Errorf("decode attributes: %w", err)
	}
	return attrs.Clone(attributes), nil
}
