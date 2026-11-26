package libsql

import (
	"path/filepath"
	"strings"
	"testing"
)

// A failed statement's error names its first keyword and a digest of it, never
// its text: a literal in the statement does not reach the error, on the path
// that executes a statement with no arguments, the path that prepares one with
// arguments, and a query's.
func TestErrorsNameTheStatementNotItsText(t *testing.T) {
	db, err := openLocalForTest(t, filepath.Join(t.TempDir(), "label.db"))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "secret-literal-4242"
	cases := map[string]func() error{
		"exec": func() error { _, err := db.Exec("INSERT INTO missing (note) VALUES ('" + secret + "')"); return err },
		"exec, an arg": func() error {
			_, err := db.Exec("INSERT INTO missing (note, n) VALUES ('"+secret+"', ?)", 1)
			return err
		},
		"query": func() error { _, err := db.Query("SELECT note FROM missing WHERE note = '" + secret + "'"); return err },
		"query, an arg": func() error {
			_, err := db.Query("SELECT note FROM missing WHERE note = '"+secret+"' AND n = ?", 1)
			return err
		},
	}
	for name, run := range cases {
		err := run()
		if err == nil {
			t.Fatalf("%s: a statement on a missing table succeeded", name)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error carries the statement's literal: %v", name, err)
		}
		if !strings.Contains(err.Error(), "statement sha256:") {
			t.Errorf("%s: the error does not name the statement by its digest: %v", name, err)
		}
	}
	if got := statementLabel("  insert into t values (1)"); !strings.HasPrefix(got, "INSERT statement sha256:") {
		t.Errorf("statementLabel = %q, want the uppercased first keyword", got)
	}
}
