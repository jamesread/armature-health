// Package testdb provides SQLite databases with the health schema applied, for tests.
package testdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	healthsqlite "github.com/jamesread/armature-health/migrate/sqlite"
)

// NewMigratedSQLite returns a fresh SQLite database with migrate/sqlite applied.
func NewMigratedSQLite(tb testing.TB) *sql.DB {
	tb.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(tb.TempDir(), "test.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })

	script, err := healthsqlite.FS.ReadFile("health.sql")
	if err != nil {
		tb.Fatalf("read migration: %v", err)
	}
	up, _, _ := strings.Cut(string(script), "-- +migrate Down")
	if _, err := db.ExecContext(context.Background(), up); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	return db
}
