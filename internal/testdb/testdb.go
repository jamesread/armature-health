// Package testdb provides SQLite databases with the health schema applied, for tests.
package testdb

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	healthsqlite "github.com/jamesread/armature-health/v2/migrate/sqlite"
)

// NewSQLite returns a fresh, empty SQLite database.
func NewSQLite(tb testing.TB) *sql.DB {
	tb.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(tb.TempDir(), "test.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	return db
}

// NewMigratedSQLite returns a fresh SQLite database with every migrate/sqlite script applied.
func NewMigratedSQLite(tb testing.TB) *sql.DB {
	tb.Helper()
	db := NewSQLite(tb)
	names, err := fs.Glob(healthsqlite.FS, "*.sql")
	if err != nil {
		tb.Fatalf("list migrations: %v", err)
	}
	for _, name := range names { // fs.Glob returns names in lexical order
		Apply(tb, db, name, true)
	}
	return db
}

// Apply runs the Up (or Down) section of one migrate/sqlite script.
func Apply(tb testing.TB, db *sql.DB, name string, up bool) {
	tb.Helper()
	script, err := healthsqlite.FS.ReadFile(name)
	if err != nil {
		tb.Fatalf("read migration %s: %v", name, err)
	}
	upSQL, downSQL, _ := strings.Cut(string(script), "-- +migrate Down")
	section := downSQL
	if up {
		section = upSQL
	}
	if _, err := db.ExecContext(context.Background(), section); err != nil {
		tb.Fatalf("migrate %s: %v", name, err)
	}
}
