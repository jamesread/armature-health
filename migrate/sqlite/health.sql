-- +migrate Up

-- Issues found by health checks. Times are unix seconds.
-- At most one open (resolved_at IS NULL) row per (check_id, issue_key); a recurrence after
-- resolution inserts a new row.
CREATE TABLE IF NOT EXISTS health_issues (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    check_id TEXT NOT NULL,
    issue_key TEXT NOT NULL,
    severity TEXT NOT NULL,
    title TEXT NOT NULL,
    detail TEXT NOT NULL,
    link TEXT NOT NULL,
    first_seen INTEGER NOT NULL,
    last_seen INTEGER NOT NULL,
    occurrences INTEGER NOT NULL DEFAULT 1,
    resolved_at INTEGER,
    acknowledged_at INTEGER,
    acknowledged_by INTEGER
);

CREATE INDEX IF NOT EXISTS idx_health_issues_check ON health_issues (check_id, issue_key, resolved_at);

-- Last run of each health check.
CREATE TABLE IF NOT EXISTS health_check_runs (
    check_id TEXT PRIMARY KEY,
    last_run_at INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    status TEXT NOT NULL,
    error TEXT NOT NULL
);

-- +migrate Down

DROP TABLE IF EXISTS health_check_runs;
DROP TABLE IF EXISTS health_issues;
