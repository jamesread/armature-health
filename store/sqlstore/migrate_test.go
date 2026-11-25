package sqlstore_test

import (
	"context"
	"testing"

	"github.com/jamesread/armature-health/v2/internal/testdb"
	"github.com/jamesread/armature-health/v2/store"
	"github.com/jamesread/armature-health/v2/store/sqlstore"
)

// TestV2MigrationKeepsV1Data applies health.sql, writes v1 rows, then applies health_v2.sql
// and reads them back through the v2 store; then runs the v2 Down section.
func TestV2MigrationKeepsV1Data(t *testing.T) {
	for name, dialect := range testdb.Dialects {
		t.Run(name, func(t *testing.T) {
			db := dialect.Open(t)
			ctx := context.Background()
			dialect.Apply(t, db, "health.sql", true)
			if _, err := db.ExecContext(ctx, `
				INSERT INTO health_issues (check_id, issue_key, severity, title, detail, link, first_seen, last_seen, acknowledged_at, acknowledged_by)
				VALUES ('c', 'acked', 'error', 'Acked', '', '', 1, 1, 5, 42),
				       ('c', 'open', 'warning', 'Open', '', '', 1, 2, NULL, NULL)`); err != nil {
				t.Fatal(err)
			}

			dialect.Apply(t, db, "health_v2.sql", true)
			st := sqlstore.New(db, dialect.Driver)
			issues, err := st.ListIssues(ctx, true, true)
			if err != nil {
				t.Fatal(err)
			}
			byKey := map[string]store.Issue{}
			for _, issue := range issues {
				byKey[issue.IssueKey] = issue
			}
			if by := byKey["acked"].AcknowledgedBy; by == nil || *by != "42" {
				t.Fatalf("acknowledged_by not carried over: %v", by)
			}
			if byKey["open"].AcknowledgedBy != nil || byKey["open"].Attributes != nil {
				t.Fatalf("unexpected values on the unacknowledged issue: %+v", byKey["open"])
			}

			// v2 writes work on the migrated table.
			if err := st.ApplyIssueChanges(ctx, store.IssueChanges{Insert: []store.Issue{{
				CheckID: "c", IssueKey: "new", Severity: "info", Attributes: map[string]string{"action_id": "x"},
			}}}); err != nil {
				t.Fatal(err)
			}
			if err := st.SetIssueAcknowledged(ctx, byKey["open"].ID, true, "alice", 9); err != nil {
				t.Fatal(err)
			}

			// Down restores the v1 shape, keeping numeric acknowledgements and clearing others.
			dialect.Apply(t, db, "health_v2.sql", false)
			var acked, total int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM health_issues WHERE acknowledged_by = 42").Scan(&acked); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM health_issues WHERE acknowledged_at IS NOT NULL").Scan(&total); err != nil {
				t.Fatal(err)
			}
			if acked != 1 || total != 1 {
				t.Fatalf("after down: %d issues acknowledged by 42, %d acknowledged in total; want 1 and 1", acked, total)
			}
		})
	}
}

// TestMySQLIDsAreCaseSensitive checks that after health_v3.sql, check IDs and issue keys
// that differ only in case or trailing spaces are distinct.
func TestMySQLIDsAreCaseSensitive(t *testing.T) {
	dialect := testdb.Dialects["mysql"]
	db := dialect.NewMigrated(t)
	st := sqlstore.New(db, dialect.Driver)
	ctx := context.Background()
	if err := st.ApplyIssueChanges(ctx, store.IssueChanges{Insert: []store.Issue{
		{CheckID: "Foo", IssueKey: "k", Severity: "info"},
		{CheckID: "foo", IssueKey: "k", Severity: "info"},
		{CheckID: "foo ", IssueKey: "k", Severity: "info"},
	}}); err != nil {
		t.Fatal(err)
	}
	issues, err := st.ListOpenIssuesForCheck(ctx, "foo")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].CheckID != "foo" {
		t.Fatalf("expected only the exact check ID, got %+v", issues)
	}
	for _, id := range []string{"Foo", "foo"} {
		if err := st.SaveCheckRun(ctx, store.CheckRun{CheckID: id, Status: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	if runs, _ := st.ListCheckRuns(ctx); len(runs) != 2 {
		t.Fatalf("expected two check runs, got %+v", runs)
	}
}
