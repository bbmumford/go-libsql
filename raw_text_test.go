//go:build cgo
// +build cgo

package libsql

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

var dateLikeTexts = []string{"2024-01-02", "2024-01-02 03:04:05", "2024-01-02T03:04:05Z", "v2025-01-01", "plain"}

func TestRawTextLocal(t *testing.T) {
	dir := t.TempDir()
	raw, err := openLocalForTest(t, filepath.Join(dir, "raw.db"), WithRawText(true))
	if err != nil {
		t.Fatal(err)
	}
	checkRawText(t, raw, "raw_local", true)
	parsed, err := openLocalForTest(t, filepath.Join(dir, "parsed.db"))
	if err != nil {
		t.Fatal(err)
	}
	checkRawText(t, parsed, "parsed_local", false)
}

func TestRawTextReplica(t *testing.T) {
	raw, _ := openReplicaForTest(t, "", WithRawText(true))
	checkRawText(t, raw, uniqueName("raw"), true)
	parsed, _ := openReplicaForTest(t, "")
	checkRawText(t, parsed, uniqueName("parsed"), false)
}

// checkRawText stores date-like TEXT values and reads them back into any and
// into string. With raw every value returns as the stored string; without it
// the upstream conversion to time.Time stays in force.
func checkRawText(t *testing.T, db *sql.DB, table string, raw bool) {
	t.Helper()
	ctx := context.Background()
	mustExec(t, db, "CREATE TABLE "+table+"(k INTEGER PRIMARY KEY, v TEXT)")
	for i, s := range dateLikeTexts {
		mustExec(t, db, "INSERT INTO "+table+" VALUES (?, ?)", int64(i), s)
	}
	for i, s := range dateLikeTexts {
		var anyV any
		var strV string
		if err := db.QueryRowContext(ctx, "SELECT v, v FROM "+table+" WHERE k = ?", int64(i)).Scan(&anyV, &strV); err != nil {
			t.Fatal(err)
		}
		_, isTime := anyV.(time.Time)
		wantTime := !raw && (s == "2024-01-02" || s == "2024-01-02 03:04:05" || s == "2024-01-02T03:04:05Z")
		if isTime != wantTime {
			t.Errorf("raw=%v %q scanned into any as %T %v", raw, s, anyV, anyV)
		}
		if raw && (anyV != s || strV != s) {
			t.Errorf("raw %q came back as %v / %q", s, anyV, strV)
		}
	}
}
