//go:build cgo
// +build cgo

package libsql

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Helpers for the tests of DriverName, multi-statement Exec, named
// arguments, NewLocalConnector and the connection options. Replica tests read
// LIBSQL_PRIMARY_URL (and LIBSQL_AUTH_TOKEN when the primary needs one), the
// variables libsql_test.go reads, and skip without them.

var uniqueSeq atomic.Int64

// uniqueName returns a table name no other test run against the same primary
// uses.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), uniqueSeq.Add(1))
}

func primaryForTest(t *testing.T) (url, token string) {
	t.Helper()
	url = os.Getenv("LIBSQL_PRIMARY_URL")
	if url == "" {
		t.Skip("LIBSQL_PRIMARY_URL is not set")
	}
	return url, os.Getenv("LIBSQL_AUTH_TOKEN")
}

// skipOnSqldBefore024 skips a test that needs sqld 0.24 or later when the test
// primary reports an older sqld on /version. sqld 0.23.0 never answers a
// statement inside a replica's BEGIN IMMEDIATE transaction while a second
// replica waits in its own BEGIN IMMEDIATE, so the test would hang rather
// than fail. A primary without /version is not skipped.
func skipOnSqldBefore024(t *testing.T) {
	t.Helper()
	url, _ := primaryForTest(t)
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(url, "/") + "/version")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	var major, minor, patch int
	if _, err := fmt.Sscanf(string(body), "sqld %d.%d.%d", &major, &minor, &patch); err != nil {
		return
	}
	if major == 0 && minor < 24 {
		t.Skipf("primary is %s; this test needs sqld 0.24 or later", strings.TrimSpace(string(body)))
	}
}

// openReplicaForTest opens an embedded replica of the test primary at path
// (a new file in a temporary directory when path is empty) and closes it when
// the test ends.
func openReplicaForTest(t *testing.T, path string, opts ...Option) (*sql.DB, *Connector) {
	t.Helper()
	url, token := primaryForTest(t)
	if path == "" {
		path = filepath.Join(t.TempDir(), "replica.db")
	}
	if token != "" {
		opts = append(opts, WithAuthToken(token))
	}
	connector, err := NewEmbeddedReplicaConnector(path, url, opts...)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() {
		db.Close()
		connector.Close()
	})
	return db, connector
}

// openFileForTest opens a local database file through the registered driver.
func openFileForTest(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(DriverName, "file:"+filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) sql.Result {
	t.Helper()
	res, err := db.ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return res
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func int64Column(t *testing.T, db *sql.DB, query string) []int64 {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func textColumn(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
