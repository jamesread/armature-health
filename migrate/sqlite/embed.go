// Package sqlite holds the sql-migrate health check schema for SQLite, for apps to copy or apply.
package sqlite

import "embed"

// FS contains health.sql (health_issues and health_check_runs).
//
//go:embed health.sql
var FS embed.FS
