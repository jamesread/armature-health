// Package mysql holds the sql-migrate health check schema for MySQL/MariaDB, for apps to copy or apply.
package mysql

import "embed"

// FS contains the migrations, to apply in file-name order: health.sql (health_issues and
// health_check_runs) and health_v2.sql (issue attributes, string user ids).
//
//go:embed *.sql
var FS embed.FS
