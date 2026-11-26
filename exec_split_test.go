package libsql

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	trigger := "CREATE TRIGGER tr AFTER INSERT ON t BEGIN\n" +
		"  UPDATE t SET y = CASE WHEN NEW.x > 0 THEN 'pos' ELSE 'neg' END WHERE rowid = NEW.rowid;\n" +
		"  SELECT CASE WHEN NEW.x = 13 THEN RAISE(ABORT, 'no;pe') END;\n" +
		"END;"
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"one without semicolon", "SELECT 1", []string{"SELECT 1"}},
		{"one with semicolon", "SELECT 1;", []string{"SELECT 1;"}},
		{"two", "CREATE TABLE a(x); CREATE TABLE b(x)", []string{"CREATE TABLE a(x);", "CREATE TABLE b(x)"}},
		{"blank only", "  ;; -- a comment ;\n /* ; */ ;\n", nil},
		{"empty", "", nil},
		{"quoted semicolons", `INSERT INTO t VALUES('a;b'); INSERT INTO "we;ird" VALUES(1);`,
			[]string{`INSERT INTO t VALUES('a;b');`, `INSERT INTO "we;ird" VALUES(1);`}},
		{"trailing comment", "SELECT 1; -- after ; this", []string{"SELECT 1;"}},
		{"comments around", "/* a; */ SELECT 1; SELECT 2 /* b; */", []string{"/* a; */ SELECT 1;", "SELECT 2 /* b; */"}},
		{"trigger with CASE END", trigger + "\nINSERT INTO t(x) VALUES (1)", []string{trigger, "INSERT INTO t(x) VALUES (1)"}},
		{"trigger without final semicolon", strings.TrimSuffix(trigger, ";"), []string{strings.TrimSuffix(trigger, ";")}},
		{"unterminated string", "SELECT 'open; SELECT 2", []string{"SELECT 'open; SELECT 2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitStatements(tc.query)
			for i := range got {
				got[i] = strings.TrimSpace(got[i])
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("splitStatements(%q)\n got %q\nwant %q", tc.query, got, tc.want)
			}
		})
	}
}

func TestExecMultiStatementLocal(t *testing.T) {
	runMemoryAndFileTests(t, func(t *testing.T, db *sql.DB) {
		checkMultiStatementExec(t, db, "local")
	})
}

func TestExecMultiStatementReplica(t *testing.T) {
	db, _ := openReplicaForTest(t, "")
	checkMultiStatementExec(t, db, uniqueName("ms"))
}

// checkMultiStatementExec runs the multi-statement cases against db; prefix
// keeps table names apart on a shared primary.
func checkMultiStatementExec(t *testing.T, db *sql.DB, prefix string) {
	ctx := context.Background()
	a, b, q, trg := prefix+"_a", prefix+"_b", prefix+"_q", prefix+"_trg"
	nosuch := prefix + "_nosuch"

	mustExec(t, db, "CREATE TABLE "+a+"(x INTEGER); CREATE TABLE "+b+"(x)")
	if countRows(t, db, a) != 0 || countRows(t, db, b) != 0 {
		t.Fatal("both tables of one Exec must exist and be empty")
	}

	_, err := db.ExecContext(ctx, "INSERT INTO "+nosuch+" VALUES (0); INSERT INTO "+a+" VALUES (-1)")
	if err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("failing first statement: err=%v", err)
	}
	if n := countRows(t, db, a); n != 0 {
		t.Fatalf("a statement after a failed one ran: %d rows", n)
	}

	_, err = db.ExecContext(ctx, "INSERT INTO "+a+" VALUES (1); INSERT INTO "+nosuch+" VALUES (2); INSERT INTO "+a+" VALUES (3)")
	if err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("failing middle statement: err=%v", err)
	}
	if got := int64Column(t, db, "SELECT x FROM "+a+" ORDER BY x"); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("rows after a failing middle statement = %v, want [1]", got)
	}

	_, err = db.ExecContext(ctx, "INSERT INTO "+b+" VALUES (1); INSERT INTO "+b+" VALUES ('x', 'too many')")
	if err == nil {
		t.Fatal("a failing second statement returned no error")
	}
	if n := countRows(t, db, b); n != 1 {
		t.Fatalf("rows after a failing second statement = %d, want 1", n)
	}

	_, err = db.ExecContext(ctx, "INSERT INTO "+b+" VALUES (?); INSERT INTO "+b+" VALUES (?)", int64(7), int64(8))
	if err == nil || !strings.Contains(err.Error(), "arguments bind to exactly one statement") {
		t.Fatalf("several statements with arguments: err=%v", err)
	}
	if n := countRows(t, db, b); n != 1 {
		t.Fatalf("a refused Exec wrote rows: %d, want 1", n)
	}

	mustExec(t, db, "CREATE TABLE "+q+"(v TEXT); INSERT INTO "+q+" VALUES ('a;b'); -- c;d\nINSERT INTO "+q+" VALUES ('e' /* ; */)")
	if got := textColumn(t, db, "SELECT v FROM "+q+" ORDER BY rowid"); !reflect.DeepEqual(got, []string{"a;b", "e"}) {
		t.Fatalf("quoted and commented semicolons: rows %q", got)
	}

	mustExec(t, db, "CREATE TABLE "+trg+"(x INTEGER, y TEXT);\n"+
		"CREATE TRIGGER "+trg+"_ai AFTER INSERT ON "+trg+" BEGIN\n"+
		"  UPDATE "+trg+" SET y = CASE WHEN NEW.x > 0 THEN 'pos' WHEN NEW.x < 0 THEN 'neg' ELSE 'zero' END WHERE rowid = NEW.rowid;\n"+
		"  SELECT CASE WHEN NEW.x = 13 THEN RAISE(ABORT, 'unlucky') END;\n"+
		"END;\n"+
		"INSERT INTO "+trg+"(x) VALUES (5), (-3), (0);")
	if got := textColumn(t, db, "SELECT y FROM "+trg+" ORDER BY rowid"); !reflect.DeepEqual(got, []string{"pos", "neg", "zero"}) {
		t.Fatalf("trigger with CASE ... END: y = %q", got)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO "+trg+"(x) VALUES (13)"); err == nil || !strings.Contains(err.Error(), "unlucky") {
		t.Fatalf("trigger RAISE: err=%v", err)
	}

	res := mustExec(t, db, "INSERT INTO "+a+" VALUES (10); INSERT INTO "+a+" VALUES (11), (12)")
	if n, err := res.RowsAffected(); err != nil || n != 2 {
		t.Fatalf("RowsAffected of the last statement = %d, %v; want 2", n, err)
	}
}
