package libsql

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// Local connections run Exec through the row-returning path, so statements
// that yield rows succeed and take effect exactly once.
func TestExecRowReturningLocal(t *testing.T) {
	db := openFileForTest(t)
	ctx := context.Background()

	mustExec(t, db, "PRAGMA journal_mode=WAL")
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode after Exec = %q, %v; want wal", mode, err)
	}

	mustExec(t, db, "CREATE TABLE r(id INTEGER PRIMARY KEY, v TEXT)")
	res := mustExec(t, db, "INSERT INTO r(v) VALUES ('a'), ('b') RETURNING id")
	if n, _ := res.RowsAffected(); n != 2 {
		t.Fatalf("RowsAffected of INSERT ... RETURNING = %d, want 2", n)
	}
	if id, _ := res.LastInsertId(); id != 2 {
		t.Fatalf("LastInsertId of INSERT ... RETURNING = %d, want 2", id)
	}
	if n := countRows(t, db, "r"); n != 2 {
		t.Fatalf("INSERT ... RETURNING through Exec left %d rows, want 2", n)
	}

	mustExec(t, db, "SELECT 1; PRAGMA busy_timeout=250")
	var busy int
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=250"); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 250 {
		t.Fatalf("busy_timeout after Exec on one connection = %d, %v; want 250", busy, err)
	}
}

// A statement that fails while stepping reports the failure from Exec.
func TestExecStepErrorLocal(t *testing.T) {
	runMemoryAndFileTests(t, func(t *testing.T, db *sql.DB) {
		mustExec(t, db, "CREATE TABLE u(id INTEGER PRIMARY KEY)")
		mustExec(t, db, "INSERT INTO u VALUES (1)")
		_, err := db.ExecContext(context.Background(), "INSERT INTO u VALUES (1)")
		if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			t.Fatalf("duplicate key through Exec: err=%v", err)
		}
		if !strings.HasPrefix(err.Error(), "failed to execute query INSERT INTO u VALUES (1)") {
			t.Fatalf("error does not name the statement: %v", err)
		}
		if n := countRows(t, db, "u"); n != 1 {
			t.Fatalf("rows after a failed insert = %d, want 1", n)
		}
	})
}
