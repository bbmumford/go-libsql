//go:build cgo
// +build cgo

package libsql

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// remoteForTest opens the test primary itself over Hrana, so a test reads
// what the primary holds rather than what a replica pulled.
func remoteForTest(t *testing.T) *sql.DB {
	t.Helper()
	url, token := primaryForTest(t)
	db, err := sql.Open(DriverName, url+"?authToken="+token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func scalarInt(t *testing.T, db *sql.DB, query string) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRowContext(context.Background(), query).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

// An INSERT on a read-only replica connector is refused before libSQL sees
// it, so it never reaches the primary.
func TestReadOnlyReplicaRefusesWriteLocally(t *testing.T) {
	primary := remoteForTest(t)
	table := uniqueName("ro_insert")
	mustExec(t, primary, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, v TEXT)")
	mustExec(t, primary, "INSERT INTO "+table+" (v) VALUES ('seed')")

	ro, _ := openReplicaForTest(t, "", WithReadOnly())
	_, err := ro.ExecContext(context.Background(), "INSERT INTO "+table+" (v) VALUES ('from the read-only replica')")
	primary = remoteForTest(t)
	if n := countRows(t, primary, table); n != 1 {
		t.Errorf("the primary holds %d rows of %s, want 1: the INSERT reached it", n, table)
	}
	if !errors.Is(err, ErrReadOnlyReplica) {
		t.Errorf("INSERT on a read-only replica: err = %v, want ErrReadOnlyReplica", err)
	}
}

// Every statement libSQL would send to the primary is refused, through Exec,
// Query, Prepare and BeginTx, and the primary's rows, schema and pragmas do
// not move.
func TestReadOnlyReplicaRefusesEveryWrite(t *testing.T) {
	primary := remoteForTest(t)
	table := uniqueName("ro_every")
	mustExec(t, primary, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, v TEXT)")
	mustExec(t, primary, "INSERT INTO "+table+" (v) VALUES ('seed')")
	schemaBefore := scalarInt(t, primary, "SELECT count(*) FROM sqlite_schema")
	versionBefore := scalarInt(t, primary, "PRAGMA user_version")

	ro, _ := openReplicaForTest(t, "", WithReadOnly())
	ctx := context.Background()
	for _, q := range []string{
		"INSERT INTO " + table + " (v) VALUES ('x')",
		"INSERT INTO " + table + " (v) VALUES ('x') RETURNING id",
		"UPDATE " + table + " SET v = 'x'",
		"DELETE FROM " + table,
		"REPLACE INTO " + table + " (id, v) VALUES (1, 'x')",
		"WITH n(v) AS (SELECT 'x') INSERT INTO " + table + " (v) SELECT v FROM n",
		"CREATE TABLE " + table + "_made (x)",
		"CREATE INDEX " + table + "_idx ON " + table + " (v)",
		"DROP TABLE " + table,
		"ALTER TABLE " + table + " ADD COLUMN w TEXT",
		"ATTACH DATABASE 'other.db' AS other",
		"VACUUM",
		"PRAGMA user_version = 7",
		"PRAGMA foreign_keys = OFF",
		"PRAGMA user_version",
		"BEGIN",
		"BEGIN IMMEDIATE",
		"EXPLAIN SELECT 1",
		"SELECT 1; DELETE FROM " + table,
		"SAVEPOINT s1",
		"COMMIT",
	} {
		if _, err := ro.ExecContext(ctx, q); !errors.Is(err, ErrReadOnlyReplica) {
			t.Errorf("Exec %q: err = %v, want ErrReadOnlyReplica", q, err)
		}
	}
	if rows, err := ro.QueryContext(ctx, "INSERT INTO "+table+" (v) VALUES ('q') RETURNING id"); !errors.Is(err, ErrReadOnlyReplica) {
		if rows != nil {
			rows.Close()
		}
		t.Errorf("Query of INSERT ... RETURNING: err = %v, want ErrReadOnlyReplica", err)
	}
	if st, err := ro.PrepareContext(ctx, "DELETE FROM "+table); !errors.Is(err, ErrReadOnlyReplica) {
		if st != nil {
			st.Close()
		}
		t.Errorf("Prepare of DELETE: err = %v, want ErrReadOnlyReplica", err)
	}
	if tx, err := ro.BeginTx(ctx, nil); !errors.Is(err, ErrReadOnlyReplica) {
		if tx != nil {
			tx.Rollback()
		}
		t.Errorf("BeginTx without ReadOnly: err = %v, want ErrReadOnlyReplica", err)
	}

	// A fresh connection reads the primary afterwards: the first one's Hrana
	// stream expires while it sits idle through the refusals above.
	primary = remoteForTest(t)
	if n := countRows(t, primary, table); n != 1 {
		t.Errorf("the primary holds %d rows of %s, want 1", n, table)
	}
	if got := textColumn(t, primary, "SELECT v FROM "+table); len(got) != 1 || got[0] != "seed" {
		t.Errorf("the primary's row = %q, want [seed]", got)
	}
	if n := scalarInt(t, primary, "SELECT count(*) FROM sqlite_schema"); n != schemaBefore {
		t.Errorf("the primary's schema holds %d objects, %d before", n, schemaBefore)
	}
	if v := scalarInt(t, primary, "PRAGMA user_version"); v != versionBefore {
		t.Errorf("the primary's user_version = %d, %d before", v, versionBefore)
	}
}

// SELECT, WITH ... SELECT, a read pragma and a read transaction run on the
// replica's own file: each still sees the row count of the last pull after the
// primary gained a row, which a statement sent to the primary would not.
func TestReadOnlyReplicaReadsLocally(t *testing.T) {
	primary := remoteForTest(t)
	table := uniqueName("ro_local")
	mustExec(t, primary, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, v TEXT)")
	mustExec(t, primary, "INSERT INTO "+table+" (v) VALUES ('a'), ('b')")

	ro, connector := openReplicaForTest(t, "", WithReadOnly())
	mustExec(t, primary, "INSERT INTO "+table+" (v) VALUES ('c')")
	ctx := context.Background()

	if n := countRows(t, ro, table); n != 2 {
		t.Errorf("SELECT count(*) on the replica = %d, want the pulled 2", n)
	}
	if n := scalarInt(t, ro, "WITH c AS (SELECT count(*) AS n FROM "+table+") SELECT n FROM c"); n != 2 {
		t.Errorf("WITH ... SELECT on the replica = %d, want 2", n)
	}
	info, err := ro.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("PRAGMA table_info on the replica: %v", err)
	}
	columns := 0
	for info.Next() {
		columns++
	}
	info.Close()
	if columns != 2 {
		t.Errorf("PRAGMA table_info on the replica: %d rows, want 2", columns)
	}

	tx, err := ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("BeginTx ReadOnly: %v", err)
	}
	var inTx int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&inTx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit of the read transaction: %v", err)
	}
	if inTx != 2 {
		t.Errorf("count inside the read transaction = %d, want 2", inTx)
	}

	c, err := ro.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecContext(ctx, "BEGIN READONLY"); err != nil {
		t.Fatalf("BEGIN READONLY: %v", err)
	}
	if _, err := c.ExecContext(ctx, "BEGIN READONLY"); !errors.Is(err, ErrReadOnlyReplica) {
		t.Errorf("a second BEGIN READONLY: err = %v, want ErrReadOnlyReplica", err)
	}
	if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("COMMIT: %v", err)
	}
	c.Close()

	if _, err := connector.Sync(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, ro, table); n != 3 {
		t.Errorf("after a pull the replica counts %d rows, want 3", n)
	}
}

func TestWithReadOnlyOnlyOnReplicas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local.db")
	if _, err := NewLocalConnector(path, WithReadOnly()); err == nil || !strings.Contains(err.Error(), "WithReadOnly apply only to replica connectors") {
		t.Errorf("NewLocalConnector with WithReadOnly: err = %v", err)
	}
	var cfg config
	if err := WithReadOnly().apply(&cfg); err != nil || !cfg.readOnly || !cfg.connOptions().readOnly {
		t.Fatalf("WithReadOnly: err=%v readOnly=%v", err, cfg.readOnly)
	}
	if err := WithReadOnly().apply(&cfg); err == nil {
		t.Error("WithReadOnly twice was accepted")
	}
}

func TestReadOnlyBeginStatement(t *testing.T) {
	iso := func(l sql.IsolationLevel) sqldriver.IsolationLevel { return sqldriver.IsolationLevel(l) }
	cases := []struct {
		immediate bool
		opts      sqldriver.TxOptions
		want      string
		readOnly  bool // the error is ErrReadOnlyReplica
	}{
		{false, sqldriver.TxOptions{ReadOnly: true}, "BEGIN READONLY", false},
		{true, sqldriver.TxOptions{ReadOnly: true}, "BEGIN READONLY", false},
		{true, sqldriver.TxOptions{ReadOnly: true, Isolation: iso(sql.LevelSerializable)}, "BEGIN READONLY", false},
		{false, sqldriver.TxOptions{}, "", true},
		{true, sqldriver.TxOptions{}, "", true},
		{false, sqldriver.TxOptions{ReadOnly: true, Isolation: iso(sql.LevelSerializable)}, "", false},
	}
	for _, tc := range cases {
		c := &conn{opts: connOptions{immediateTransactions: tc.immediate, readOnly: true}}
		got, err := c.beginStatement(tc.opts)
		if got != tc.want || (tc.want == "") == (err == nil) || errors.Is(err, ErrReadOnlyReplica) != tc.readOnly {
			t.Errorf("immediate=%v %+v: got %q, %v; want %q (ErrReadOnlyReplica %v)", tc.immediate, tc.opts, got, err, tc.want, tc.readOnly)
		}
	}
}

// The allow-list, statement by statement, with the read-transaction state
// each starts in and the state it leaves.
func TestReadOnlyGateClasses(t *testing.T) {
	type tc struct {
		query  string
		args   int
		inTx   bool
		admit  bool
		after  bool
		reason string // in the refusal message
	}
	cases := []tc{
		{query: "SELECT 1", admit: true},
		{query: "  select * from t ;", admit: true},
		{query: "/* lead */ SELECT 1 -- tail", admit: true},
		{query: "SELECT ';' AS semi, \"a;b\", [c;d], `e;f` FROM t", admit: true},
		{query: "SELECT 1; SELECT 2;", admit: true},
		{query: "WITH a AS (SELECT 1) SELECT * FROM a", admit: true},
		{query: "with recursive a(n) as (select 1 union all select n + 1 from a where n < 3) select n from a", admit: true},
		{query: "WITH a AS MATERIALIZED (SELECT 1), b AS NOT MATERIALIZED (SELECT ')') SELECT * FROM a, b", admit: true},
		{query: "PRAGMA table_info(t)", admit: true},
		{query: "PRAGMA main.table_info('t')", admit: true},
		{query: "PRAGMA index_list = t", admit: true},
		{query: "PRAGMA table_list", admit: true},
		{query: "PRAGMA encoding", admit: true},
		{query: "BEGIN READONLY", admit: true, after: true},
		{query: "begin readonly transaction", admit: true, after: true},
		{query: "COMMIT", inTx: true, admit: true},
		{query: "END TRANSACTION", inTx: true, admit: true},
		{query: "ROLLBACK", inTx: true, admit: true},
		{query: "SELECT 1", inTx: true, admit: true, after: true},
		{query: "SAVEPOINT s", inTx: true, admit: true, after: true},
		{query: "RELEASE s", inTx: true, admit: true, after: true},
		{query: "RELEASE SAVEPOINT s", inTx: true, admit: true, after: true},
		{query: "ROLLBACK TO s", inTx: true, admit: true, after: true},
		{query: "ROLLBACK TRANSACTION TO SAVEPOINT s", inTx: true, admit: true, after: true},

		{query: "", reason: "no statement"},
		{query: " ; -- only a comment", reason: "no statement"},
		{query: "INSERT INTO t VALUES (1)", reason: "INSERT"},
		{query: "insert into t values (1)", reason: "INSERT"},
		{query: "UPDATE t SET a = 1", reason: "UPDATE"},
		{query: "DELETE FROM t", reason: "DELETE"},
		{query: "REPLACE INTO t VALUES (1)", reason: "REPLACE"},
		{query: "CREATE TABLE x (a)", reason: "CREATE"},
		{query: "DROP TABLE t", reason: "DROP"},
		{query: "ALTER TABLE t RENAME TO u", reason: "ALTER"},
		{query: "ATTACH 'x' AS y", reason: "ATTACH"},
		{query: "DETACH y", reason: "DETACH"},
		{query: "VACUUM", reason: "VACUUM"},
		{query: "REINDEX", reason: "REINDEX"},
		{query: "ANALYZE", reason: "ANALYZE"},
		{query: "VALUES (1)", reason: "VALUES"},
		{query: "EXPLAIN SELECT 1", reason: "EXPLAIN"},
		{query: "EXPLAIN QUERY PLAN SELECT 1", reason: "EXPLAIN"},
		{query: "EXPLAIN PRAGMA table_info(t)", reason: "EXPLAIN"},
		{query: "PRAGMA foreign_keys", reason: "read pragmas"},
		{query: "PRAGMA foreign_keys = OFF", reason: "read pragmas"},
		{query: "PRAGMA user_version = 1", reason: "read pragmas"},
		{query: "PRAGMA wal_checkpoint", reason: "read pragmas"},
		{query: "PRAGMA TABLE_INFO(t)", reason: "read pragmas"},
		{query: "PRAGMA \"table_info\"(t)", reason: "read pragmas"},
		{query: "PRAGMA encoding = 'UTF-16le'", reason: "sets it"},
		{query: "PRAGMA table_info(t) x", reason: "cannot class"},
		{query: "PRAGMA table_info(a, b)", reason: "cannot class"},
		{query: "BEGIN", reason: "BEGIN READONLY"},
		{query: "BEGIN IMMEDIATE", reason: "BEGIN READONLY"},
		{query: "BEGIN DEFERRED TRANSACTION", reason: "BEGIN READONLY"},
		{query: "BEGIN READONLY x", reason: "BEGIN READONLY"},
		{query: "WITH a AS (SELECT 1) INSERT INTO t SELECT * FROM a", reason: "other than SELECT"},
		{query: "WITH a AS (SELECT 1) DELETE FROM t", reason: "other than SELECT"},
		{query: "WITH a AS (SELECT 1) UPDATE t SET x = 1", reason: "other than SELECT"},
		{query: "WITH a AS (SELECT 1) VALUES (1)", reason: "other than SELECT"},
		{query: "WITH a AS (SELECT 1", reason: "cannot class"},
		{query: "WITH a (SELECT 1) SELECT 1", reason: "cannot class"},
		{query: "WITH a AS NOT x (SELECT 1) SELECT 1", reason: "cannot class"},
		{query: "PRAGMA 'main'.table_info(t)", reason: "cannot class"},
		{query: "SELECT 1; INSERT INTO t VALUES (1)", reason: "INSERT"},
		{query: "SELECT 'unterminated", reason: "does not lex"},
		{query: "SELECT 1 # 2", reason: "does not lex"},
		{query: "(SELECT 1)", reason: "keyword"},
		{query: "ſELECT 1", reason: "keyword"},
		{query: "COMMIT", reason: "no read transaction"},
		{query: "ROLLBACK", reason: "no read transaction"},
		{query: "SAVEPOINT s", reason: "savepoint outside"},
		{query: "RELEASE s", reason: "savepoint outside"},
		{query: "ROLLBACK TO s", reason: "savepoint outside"},
		{query: "ROLLBACK TRANSACTION TO", inTx: true, after: true, reason: "cannot class"},
		{query: "BEGIN READONLY", inTx: true, after: true, reason: "already open"},
		{query: "BEGIN READONLY", args: 1, reason: "runs alone"},
		{query: "BEGIN READONLY; SELECT 1", reason: "runs alone"},
		{query: "SELECT 1; COMMIT", inTx: true, after: true, reason: "runs alone"},
	}
	for _, c := range cases {
		after, err := admitStatements(c.query, c.args, c.inTx)
		if c.admit {
			if err != nil || after != c.after {
				t.Errorf("%q (inTx %v): after=%v err=%v; want admitted, after=%v", c.query, c.inTx, after, err, c.after)
			}
			continue
		}
		if !errors.Is(err, ErrReadOnlyReplica) || !strings.Contains(err.Error(), c.reason) || after != c.after {
			t.Errorf("%q (inTx %v): after=%v err=%v; want ErrReadOnlyReplica naming %q, after=%v", c.query, c.inTx, after, err, c.reason, c.after)
		}
	}
}

// classifyStatement takes one statement as splitStatements cut it; a ';' its
// own lexing finds before the end means the two tokenizers disagree, and the
// statement is refused.
func TestReadOnlyGateRefusesAnInteriorSemicolon(t *testing.T) {
	if class, _, why := classifyStatement("SELECT 1; DELETE FROM t"); class != classRefused || why != "more than one statement" {
		t.Errorf("classifyStatement with an interior ';' = %v, %q", class, why)
	}
	if class, _, _ := classifyStatement("SELECT 1;;"); class != classRead {
		t.Errorf("classifyStatement with trailing ';' = %v, want a read", class)
	}
}

// A refusal names the leading keyword and why, never the statement's text.
func TestReadOnlyRefusalCarriesNoStatementText(t *testing.T) {
	for _, q := range []string{
		"PRAGMA key = 'correct-horse-battery'",
		"INSERT INTO t VALUES ('correct-horse-battery')",
		"correct-horse-battery",
	} {
		_, err := admitStatements(q, 0, false)
		if !errors.Is(err, ErrReadOnlyReplica) || strings.Contains(err.Error(), "correct") || strings.Contains(err.Error(), "CORRECT") {
			t.Errorf("%q: err = %v", q, err)
		}
	}
}
