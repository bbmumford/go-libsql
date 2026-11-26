package libsql

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBeginStatement(t *testing.T) {
	iso := func(l sql.IsolationLevel) sqldriver.IsolationLevel { return sqldriver.IsolationLevel(l) }
	cases := []struct {
		immediate bool
		opts      sqldriver.TxOptions
		want      string
		wantErr   string
	}{
		{false, sqldriver.TxOptions{}, "BEGIN", ""},
		{false, sqldriver.TxOptions{ReadOnly: true}, "", "read only transactions are not supported"},
		{false, sqldriver.TxOptions{Isolation: iso(sql.LevelSerializable)}, "", "isolation level"},
		{true, sqldriver.TxOptions{}, "BEGIN IMMEDIATE", ""},
		{true, sqldriver.TxOptions{Isolation: iso(sql.LevelSerializable)}, "BEGIN IMMEDIATE", ""},
		{true, sqldriver.TxOptions{ReadOnly: true}, "BEGIN", ""},
		{true, sqldriver.TxOptions{ReadOnly: true, Isolation: iso(sql.LevelSerializable)}, "BEGIN", ""},
		{true, sqldriver.TxOptions{Isolation: iso(sql.LevelReadCommitted)}, "", "isolation level"},
	}
	for _, tc := range cases {
		c := &conn{opts: connOptions{immediateTransactions: tc.immediate}}
		got, err := c.beginStatement(tc.opts)
		if got != tc.want || (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("immediate=%v %+v: got %q, %v; want %q, %q", tc.immediate, tc.opts, got, err, tc.want, tc.wantErr)
		}
	}
}

// BeginTx under WithImmediateTransactions holds the write lock before its
// first statement; a ReadOnly transaction and a transaction without the
// option do not.
func TestImmediateTransactionsLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tx.db")
	ctx := context.Background()
	immediate, err := openLocalForTest(t, path, WithImmediateTransactions(true))
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := openLocalForTest(t, path)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := openLocalForTest(t, path, WithBusyTimeout(0))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, immediate, "CREATE TABLE t(v INTEGER)")

	writerWaits := func() bool {
		c, err := probe.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, err = c.ExecContext(ctx, "BEGIN IMMEDIATE")
		if err == nil {
			_, _ = c.ExecContext(ctx, "ROLLBACK")
			return false
		}
		if !strings.Contains(err.Error(), "database is locked") {
			t.Fatalf("probe BEGIN IMMEDIATE: %v", err)
		}
		return true
	}

	for _, tc := range []struct {
		name   string
		db     *sql.DB
		opts   *sql.TxOptions
		locked bool
	}{
		{"immediate", immediate, nil, true},
		{"immediate serializable", immediate, &sql.TxOptions{Isolation: sql.LevelSerializable}, true},
		{"immediate read-only", immediate, &sql.TxOptions{ReadOnly: true}, false},
		{"deferred", deferred, nil, false},
	} {
		tx, err := tc.db.BeginTx(ctx, tc.opts)
		if err != nil {
			t.Fatalf("%s: BeginTx: %v", tc.name, err)
		}
		if got := writerWaits(); got != tc.locked {
			t.Errorf("%s: another writer blocked=%v, want %v", tc.name, got, tc.locked)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}

// Read-then-update increments in BeginTx from sixteen goroutines on one
// local handle all land: localSetup.prepare gives every connection
// database/sql opens the busy timeout, so a transaction waits for another's
// lock instead of failing at once. The timeout is a minute, not the 5 s
// default TestLocalConnectorOptions pins, because this checks that every
// increment lands, not how long one waits: WithImmediateTransactions makes
// each BeginTx take the RESERVED lock before it reads, so the sixteen writers
// serialise through all 400 transactions and one can wait past the default.
func TestImmediateTransactionsLocalIncrements(t *testing.T) {
	db, err := openLocalForTest(t, filepath.Join(t.TempDir(), "inc.db"), WithImmediateTransactions(true), WithBusyTimeout(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(16)
	mustExec(t, db, "CREATE TABLE c(id INTEGER PRIMARY KEY, v INTEGER); INSERT INTO c VALUES (1, 0)")
	errs := runIncrements(t, []*sql.DB{db}, 16, 25, "c")
	if len(errs) > 0 {
		t.Fatalf("%d of 400 increments failed, first: %v", len(errs), errs[0])
	}
	if got := int64Column(t, db, "SELECT v FROM c"); len(got) != 1 || got[0] != 400 {
		t.Fatalf("counter = %v, want 400", got)
	}
}

// Two replicas of one primary, 200 read-then-update increments each inside
// BeginTx, lose none.
func TestImmediateTransactionsReplica(t *testing.T) {
	skipOnSqldBefore024(t)
	table := uniqueName("inc")
	a, ca := openReplicaForTest(t, "", WithImmediateTransactions(true))
	b, cb := openReplicaForTest(t, "", WithImmediateTransactions(true))
	mustExec(t, a, "CREATE TABLE "+table+"(id INTEGER PRIMARY KEY, v INTEGER); INSERT INTO "+table+" VALUES (1, 0)")
	if _, err := cb.Sync(); err != nil {
		t.Fatal(err)
	}
	errs := runIncrements(t, []*sql.DB{a, b}, 2, 200, table)
	if len(errs) > 0 {
		t.Fatalf("%d of 400 increments failed, first: %v", len(errs), errs[0])
	}
	if _, err := ca.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := int64Column(t, a, "SELECT v FROM "+table); len(got) != 1 || got[0] != 400 {
		t.Fatalf("counter = %v, want 400", got)
	}
}

// runIncrements starts workers goroutines, spread over dbs, each running
// each transactions that read the counter and write it back plus one.
func runIncrements(t *testing.T, dbs []*sql.DB, workers, each int, table string) []error {
	t.Helper()
	ctx := context.Background()
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(db *sql.DB) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				err := func() error {
					tx, err := db.BeginTx(ctx, nil)
					if err != nil {
						return err
					}
					defer tx.Rollback()
					var v int64
					if err := tx.QueryRowContext(ctx, "SELECT v FROM "+table+" WHERE id = 1").Scan(&v); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET v = ? WHERE id = 1", v+1); err != nil {
						return err
					}
					return tx.Commit()
				}()
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}(dbs[w%len(dbs)])
	}
	wg.Wait()
	return errs
}
