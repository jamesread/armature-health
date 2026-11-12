// Package testdb provides SQLite and MySQL databases with the health schema applied, for
// tests. MySQL databases need HEALTH_TEST_MYSQL_DSN (e.g. "root:pw@tcp(127.0.0.1:3306)/");
// tests that ask for one are skipped without it.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	healthmysql "github.com/jamesread/armature-health/v2/migrate/mysql"
	healthsqlite "github.com/jamesread/armature-health/v2/migrate/sqlite"
)

// MySQLDSNEnv names the environment variable holding the MySQL server to test against.
const MySQLDSNEnv = "HEALTH_TEST_MYSQL_DSN"

// Dialect is a database the SQL store supports, with its migrations.
type Dialect struct {
	// Migrations holds the migrate/<dialect> scripts.
	Migrations fs.FS
	// Open returns a fresh, empty database (or skips the test).
	Open func(tb testing.TB) *sql.DB
	// Driver is the sqlstore.New driver name.
	Driver string
}

// Dialects lists every supported database: SQLite always, MySQL when MySQLDSNEnv is set.
var Dialects = map[string]Dialect{
	"sqlite": {Driver: "sqlite", Migrations: healthsqlite.FS, Open: NewSQLite},
	"mysql":  {Driver: "mysql", Migrations: healthmysql.FS, Open: NewMySQL},
}

// NewMigrated returns a fresh database of the dialect with every migration applied.
func (d Dialect) NewMigrated(tb testing.TB) *sql.DB {
	tb.Helper()
	db := d.Open(tb)
	names, err := fs.Glob(d.Migrations, "*.sql")
	if err != nil {
		tb.Fatalf("list migrations: %v", err)
	}
	for _, name := range names { // fs.Glob returns names in lexical order
		d.Apply(tb, db, name, true)
	}
	return db
}

// Apply runs the Up (or Down) section of one migration script.
func (d Dialect) Apply(tb testing.TB, db *sql.DB, name string, up bool) {
	tb.Helper()
	script, err := fs.ReadFile(d.Migrations, name)
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
	return Dialects["sqlite"].NewMigrated(tb)
}

// NewMySQL creates an empty database with a random name on the MySQLDSNEnv server and drops
// it when the test ends. The test is skipped when MySQLDSNEnv is not set.
func NewMySQL(tb testing.TB) *sql.DB {
	tb.Helper()
	cfg := mysqlConfig(tb)
	admin := openMySQL(tb, cfg)
	name := "health_test_" + randomSuffix(tb)
	exec(tb, admin, "CREATE DATABASE "+name)
	tb.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name)
	})

	cfg.DBName = name
	return openMySQL(tb, cfg)
}

func mysqlConfig(tb testing.TB) *mysql.Config {
	tb.Helper()
	dsn := os.Getenv(MySQLDSNEnv)
	if dsn == "" {
		tb.Skipf("set %s to test against MySQL", MySQLDSNEnv)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		tb.Fatalf("parse %s: %v", MySQLDSNEnv, err)
	}
	cfg.MultiStatements = true // migration scripts hold several statements
	return cfg
}

// openMySQL opens cfg and closes it when the test ends (after cleanups registered later).
func openMySQL(tb testing.TB, cfg *mysql.Config) *sql.DB {
	tb.Helper()
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		tb.Fatalf("open mysql: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	return db
}

func exec(tb testing.TB, db *sql.DB, query string) {
	tb.Helper()
	if _, err := db.ExecContext(context.Background(), query); err != nil {
		tb.Fatalf("%s: %v", query, err)
	}
}

func randomSuffix(tb testing.TB) string {
	tb.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b)
}
