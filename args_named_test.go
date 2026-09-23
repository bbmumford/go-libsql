//go:build cgo
// +build cgo

package libsql

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestNamedArgumentsRefusedLocal(t *testing.T) {
	runMemoryAndFileTests(t, func(t *testing.T, db *sql.DB) {
		checkNamedArgumentsRefused(t, db, "local_named")
	})
}

func TestNamedArgumentsRefusedReplica(t *testing.T) {
	db, _ := openReplicaForTest(t, "")
	checkNamedArgumentsRefused(t, db, uniqueName("named"))
}

func checkNamedArgumentsRefused(t *testing.T, db *sql.DB, table string) {
	ctx := context.Background()
	var got int64
	err := db.QueryRowContext(ctx, "SELECT :b - :a", sql.Named("a", int64(2)), sql.Named("b", int64(40))).Scan(&got)
	if err == nil || !strings.Contains(err.Error(), `named argument "a" is not supported`) {
		t.Fatalf("SELECT :b - :a with named values = %d, err=%v; want a refusal", got, err)
	}

	mustExec(t, db, "CREATE TABLE "+table+"(v INTEGER)")
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (:v)", sql.Named("v", int64(1))); err == nil {
		t.Fatal("Exec with a named value was not refused")
	}
	if n := countRows(t, db, table); n != 0 {
		t.Fatalf("a refused Exec wrote %d rows", n)
	}

	// Named placeholders still bind by position when the values are unnamed.
	if err := db.QueryRowContext(ctx, "SELECT :b - :a", int64(40), int64(2)).Scan(&got); err != nil || got != 38 {
		t.Fatalf("SELECT :b - :a with positional 40, 2 = %d, %v; want 38", got, err)
	}
}
