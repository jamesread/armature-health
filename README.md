<div align = "center">
  <h1>armature-health</h1>

  Background health checks for SOA apps: find broken configuration, risky settings and recent errors, and show them to admins before users run into them.

  [![Maturity Badge](https://img.shields.io/badge/maturity-alpha-red.svg)](#none)
  [![Discord](https://img.shields.io/discord/846737624960860180?label=Discord%20Server)](https://discord.gg/jhYWWpNJ3v)
  [![Go Report Card](https://goreportcard.com/badge/github.com/jamesread/armature-health)](https://goreportcard.com/report/github.com/jamesread/armature-health)

</div>

Apps write checks for their own problems; this library runs them on a schedule, stores what they find, keeps issues stable across runs, resolves issues that go away, and sends one notification when a new warning or error appears.

Apps keep their own Connect RPCs, protobuf and UI. This module does not ship a Connect service.

## Packages

| Package | Role |
|---------|------|
| `health` (module root) | `Check`, `Issue`, `Severity`, and `Service` (runs checks, reconciles issues, lists, acknowledges, summarises) |
| `store` | Row types and the `Store` interface |
| `store/sqlstore` | `Store` on `*sql.DB` for SQLite and MySQL/MariaDB |
| `migrate/sqlite`, `migrate/mysql` | sql-migrate script (`health_issues`, `health_check_runs`) to copy into the app |
| `errorlog` | logrus hook that groups recent error log lines, and the `logs.recent_errors` check |
| `checks` | Checks most apps need: default admin password, risky settings |

## Usage

```go
import (
	health "github.com/jamesread/armature-health"
	"github.com/jamesread/armature-health/checks"
	"github.com/jamesread/armature-health/errorlog"
	"github.com/jamesread/armature-health/store/sqlstore"
)

errorLog := errorlog.New()
log.AddHook(errorLog)

svc := health.NewService(sqlstore.New(db, "sqlite"), []health.Check{
	MyBrokenLinksCheck{DB: db},
	checks.DefaultPassword{Product: "MyApp", Link: "/admin/users", UsesDefaultPassword: adminHasDefaultPassword},
	checks.RiskySettings{Getenv: os.Getenv, LogLevel: cfg.LogLevel, LogLevelSetting: "MYAPP_LOG_LEVEL"},
	errorlog.RecentErrorsCheck{Log: errorLog},
}, notifier, health.Options{})

go svc.RunPeriodically(ctx, time.Minute, 15*time.Minute)
```

Your RPC handlers then call `svc.ListIssues`, `svc.Statuses`, `svc.Summary`, `svc.Acknowledge`, `svc.Run` and `svc.RunAll`, and map the results to your protobuf.

## Writing a check

Implement `health.Check`:

```go
type MyBrokenLinksCheck struct{ DB *sql.DB }

func (c MyBrokenLinksCheck) ID() string    { return "navigation.broken_links" }
func (c MyBrokenLinksCheck) Title() string { return "Broken navigation links" }

func (c MyBrokenLinksCheck) Run(ctx context.Context) ([]health.Issue, error) {
	// ... return one Issue per problem currently present
}
```

- **Key** must be stable for the same problem across runs (e.g. `nav:42`). An issue no longer returned is resolved; one that recurs later is a new issue.
- **Severity**: `error` for things that break the app or security, `warning` for broken configuration, `info` for advice. Only warnings and errors are notified and counted in the summary.
- **Link** is the frontend path where the problem can be fixed.
- Returning an error (or panicking) records the run as failed and keeps the check's previous issues.

## Notifications

Pass a `health.Notifier` (anything with `SendNotification(ctx, eventCode, data)`) to `NewService`, or nil. A new warning or error sends `health.issue_raised` once, with data keys `check`, `check_id`, `severity`, `title` and `link`.

## Database

Copy `migrate/sqlite/health.sql` or `migrate/mysql/health.sql` into your app's migrations. `sqlstore.New(db, driver)` uses MySQL syntax when `driver` is `"mysql"` and SQLite otherwise.
