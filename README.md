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
| `store/memstore` | In-memory `Store`, the default when `NewService` gets a nil store |
| `store/sqlstore` | Optional `Store` on `*sql.DB` for SQLite and MySQL/MariaDB |
| `migrate/sqlite`, `migrate/mysql` | sql-migrate script (`health_issues`, `health_check_runs`) to copy into the app |
| `errorlog` | logrus hook that groups recent error log lines, and the `logs.recent_errors` check |
| `checks` | Checks most apps need: default admin password, risky settings |

## Usage

```go
import (
	health "github.com/jamesread/armature-health/v2"
	"github.com/jamesread/armature-health/v2/checks"
	"github.com/jamesread/armature-health/v2/errorlog"
)

errorLog := errorlog.New()
log.AddHook(errorLog)

svc := health.NewService(nil, []health.Check{ // nil: keep issues in memory
	MyBrokenLinksCheck{DB: db},
	checks.DefaultPassword{Product: "MyApp", Link: "/admin/users", UsesDefaultPassword: adminHasDefaultPassword},
	checks.RiskySettings{Getenv: os.Getenv, LogLevel: cfg.LogLevel, LogLevelSetting: "MYAPP_LOG_LEVEL"},
	errorlog.RecentErrorsCheck{Log: errorLog},
}, notifier, health.Options{})

go svc.RunPeriodically(ctx, time.Minute, 15*time.Minute)
```

Your RPC handlers then call `svc.ListIssues`, `svc.Statuses`, `svc.Summary`, `svc.Acknowledge`, `svc.Run` and `svc.RunAll`, and map the results to your protobuf. `Acknowledge` takes the user as a string (a username, UUID or whatever your app uses), stored as `AcknowledgedBy`.

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

- **Key** must be stable for the same problem across runs (e.g. `nav:42`). An issue no longer returned is resolved; one that recurs later is a new issue. Issues without a key, and repeats of a key, are dropped with a warning in the log.
- **Severity**: `error` for things that break the app or security, `warning` for broken configuration, `info` for advice. Only warnings and errors are notified and counted in the summary.
- **Link** is the frontend path where the problem can be fixed.
- **Attributes** (optional `map[string]string`) are your own details, stored with the issue and returned on `IssueView`. Use them to filter issues by permission (e.g. `action_id`) or to fill extra UI columns. They are replaced each time the check finds the issue again.
- Returning an error (or panicking) records the run as failed and keeps the check's previous issues.

## Pushed issues

Some problems are found as they happen rather than by a check: a config value that fails to load, a file watcher error, a call to a deprecated API. Push these under a source ID instead of writing a check:

```go
svc := health.NewService(nil, checks, notifier, health.Options{
	SourceTitles: map[string]string{"api.deprecated": "Deprecated API calls", "config.load": "Configuration"},
})

// Each call adds an occurrence: "called 37 times, last seen 5 minutes ago".
svc.Raise(ctx, "api.deprecated", health.Issue{Key: "GET /api/old", Severity: health.SeverityWarning, Title: "Deprecated API called"})

// Resolve one when it goes away.
svc.Resolve(ctx, "watcher", "file:/etc/app/config.yaml")

// Or replace a source's whole set, e.g. after reloading config. Issues still present keep
// their first-seen time and are not notified again; missing ones are resolved.
svc.SetIssues(ctx, "config.load", configIssues)
```

A new pushed issue is notified like a check's. Raising an open issue again adds `Count` (or 1) to its occurrences, moves its last-seen time to now and replaces its title, detail, severity, link and attributes. A source ID must not also be a registered check's ID, because a check run resolves every issue of its ID that it does not return.

## Upgrading from v1

v2 changes the module path to `github.com/jamesread/armature-health/v2` and:

- adds `Attributes map[string]string` to `health.Issue` and `store.Issue`;
- identifies users by string: `Acknowledge(ctx, id, acknowledged, userID string)` and `AcknowledgedBy *string`;
- uses `*int64` instead of `sql.NullInt64` for `ResolvedAt` and `AcknowledgedAt`;
- keeps issues in memory when `NewService` is given a nil store (`store/memstore`);
- replaces `Store.InsertIssue`, `UpdateSeenIssue` and `ResolveIssue` with `ApplyIssueChanges`, applied atomically per check run.

With the SQL store, add `health_v2.sql` to your migrations after the `health.sql` you already applied. It adds the `attributes` column and turns `acknowledged_by` into a string column, keeping existing acknowledgements (integer ids become their decimal strings). Requires SQLite 3.35+ (for `DROP COLUMN`) or MySQL/MariaDB.

## Notifications

Pass a `health.Notifier` (anything with `SendNotification(ctx, eventCode, data)`) to `NewService`, or nil. A new warning or error sends `health.issue_raised` once, with data keys `check`, `check_id`, `severity`, `previous_severity`, `title`, `detail`, `link` and `attributes` (the issue's `map[string]string`, nil when it has none).

If an open issue is found again with a higher severity (info → warning → error), it is notified again with `previous_severity` set to the old severity (empty for a new issue). If it was acknowledged, the acknowledgement is cleared so it shows in the summary again. A lower severity changes neither.

## Storage

Issues and check runs are kept in memory when you pass a nil store (or `memstore.New()`). That needs no database, but restarts lose issue history, first-seen times and acknowledgements, and an issue that is still present is notified again after a restart.

To keep them across restarts, use the SQL store: copy the scripts in `migrate/sqlite/` or `migrate/mysql/` (`health.sql`, then `health_v2.sql`) into your app's migrations, then pass `sqlstore.New(db, driver)`. It uses MySQL syntax when `driver` is `"mysql"` and SQLite otherwise.

```go
import "github.com/jamesread/armature-health/v2/store/sqlstore"

svc := health.NewService(sqlstore.New(db, "sqlite"), checks, notifier, health.Options{})
```

You can implement `store.Store` for any other backend. `ApplyIssueChanges` must apply each check run's inserts, updates and resolutions atomically, so a request made during a run sees either the old issues or the new ones, never a mix.
