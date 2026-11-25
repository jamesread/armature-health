// Package mysql holds the sql-migrate health check schema for MySQL/MariaDB, for apps to copy or apply.
package mysql

import "embed"

// FS contains the migrations, to apply in file-name order: health.sql (health_issues and
// health_check_runs), health_v2.sql (issue attributes, string user ids) and health_v3.sql
// (utf8mb4 text, byte-for-byte check IDs and issue keys).
//
//go:embed *.sql
var FS embed.FS
