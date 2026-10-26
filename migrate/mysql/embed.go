// Package mysql holds the sql-migrate health check schema for MySQL/MariaDB, for apps to copy or apply.
package mysql

import "embed"

// FS contains health.sql (health_issues and health_check_runs).
//
//go:embed health.sql
var FS embed.FS
