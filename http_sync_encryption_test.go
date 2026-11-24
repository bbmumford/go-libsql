//go:build cgo
// +build cgo

package libsql

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The HTTP sync protocol a primary answers when it serves /info:
// GET /info, GET /export/{generation} (the database file the replica starts
// from), GET /sync/{generation}/{start}/{end} (WAL frames after it, 24-byte
// header plus one 4096-byte page each; a 400 carrying the generation ends it)
// and POST /sync/{generation}/{start}/{end} (frames an offline-writes
// database pushes).
// libSQL's remote replica takes this path whenever /info answers 200, which
// the in-process primary below does, so these tests run without a network or
// a sqld.

const (
	httpSyncExportMarker = "HTTP-SYNC-EXPORT-MARKER-5d2e"
	httpSyncFrameMarker  = "HTTP-SYNC-FRAME-MARKER-8b41"
	httpSyncPushMarker   = "HTTP-SYNC-PUSH-MARKER-27c9"
	httpSyncFrameSize    = 24 + 4096
	httpSyncRows         = 300
)

// httpSyncPrimary is the in-process primary and the frames pushed to it.
type httpSyncPrimary struct {
	*httptest.Server
	mu     sync.Mutex
	pushed []byte
}

func (p *httpSyncPrimary) pushedFrames() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.pushed...)
}

// newHTTPSyncPrimary serves a plaintext database over the HTTP sync
// protocol: httpSyncRows rows holding httpSyncExportMarker in the exported
// file and httpSyncRows rows holding httpSyncFrameMarker in the WAL frames of
// generation 1 after it. Pushed frames are kept, not applied.
func newHTTPSyncPrimary(t *testing.T) *httpSyncPrimary {
	t.Helper()
	path := filepath.Join(t.TempDir(), "primary.db")
	db, err := sql.Open(DriverName, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	fill := func(marker string) {
		mustExec(t, db, fmt.Sprintf(
			"WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM c WHERE i < %d) "+
				"INSERT INTO t(v) SELECT '%s-' || i FROM c", httpSyncRows, marker))
	}
	drain(t, db, "PRAGMA journal_mode = WAL")
	mustExec(t, db, "CREATE TABLE t(v TEXT)")
	fill(httpSyncExportMarker)
	drain(t, db, "PRAGMA wal_checkpoint(TRUNCATE)")
	export, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(export, sqliteHeader) || len(export)%4096 != 0 || len(export) <= 4096 {
		t.Fatalf("primary export: %d bytes, %v", len(export), err)
	}
	drain(t, db, "PRAGMA wal_autocheckpoint = 0")
	fill(httpSyncFrameMarker)
	wal, err := os.ReadFile(path + "-wal")
	if err != nil || len(wal) <= 32 || (len(wal)-32)%httpSyncFrameSize != 0 {
		t.Fatalf("primary WAL: %d bytes, %v", len(wal), err)
	}
	frames := wal[32:]
	count := len(frames) / httpSyncFrameSize

	p := &httpSyncPrimary{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodGet && len(parts) == 1 && parts[0] == "info":
			w.Write([]byte(`{"current_generation":1}`))
		case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "export" && parts[1] == "1":
			w.Write(export)
		case r.Method == http.MethodGet && len(parts) == 4 && parts[0] == "sync" && parts[1] == "1":
			start, err1 := strconv.Atoi(parts[2])
			end, err2 := strconv.Atoi(parts[3])
			if err1 != nil || err2 != nil || start < 1 || start > count {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"generation":1}`))
				return
			}
			if end > count+1 {
				end = count + 1
			}
			w.Write(frames[(start-1)*httpSyncFrameSize : (end-1)*httpSyncFrameSize])
		case r.Method == http.MethodPost && len(parts) >= 4 && parts[0] == "sync" && parts[1] == "1":
			end, err := strconv.Atoi(parts[3])
			body, rerr := io.ReadAll(r.Body)
			if err != nil || rerr != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			p.mu.Lock()
			p.pushed = append(p.pushed, body...)
			p.mu.Unlock()
			fmt.Fprintf(w, `{"status":"ok","generation":1,"max_frame_no":%d}`, end-1)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.Server.Close)
	return p
}

// drain runs a statement that returns rows and reads them all.
func drain(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	rows.Close()
}

func httpSyncCount(t *testing.T, db *sql.DB, marker string) int {
	t.Helper()
	got := textColumn(t, db, "SELECT count(*) FROM t WHERE v LIKE '"+marker+"%'")
	n, _ := strconv.Atoi(strings.Join(got, ""))
	return n
}

// plaintextFiles lists every file in dir holding one of the markers.
func plaintextFiles(t *testing.T, dir string, markers ...string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range markers {
			if bytes.Contains(b, []byte(m)) {
				found = append(found, e.Name()+" holds "+m)
			}
		}
	}
	return found
}

// A keyed embedded replica of a primary on the HTTP sync path keeps no
// plaintext on disk: the replica file has no SQLite header, neither the file,
// its WAL, its -info metadata nor any file beside them holds a row, and the
// data reopens with the key and not without it.
func TestKeyedReplicaOnHTTPSyncIsEncryptedAtRest(t *testing.T) {
	srv := newHTTPSyncPrimary(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "replica.db")
	key := testKey(11)
	keyed := WithEncryption(base64.StdEncoding.EncodeToString(key))

	connector, err := NewEmbeddedReplicaConnector(path, srv.URL, WithAuthToken("token"), keyed)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	if e, f := httpSyncCount(t, db, httpSyncExportMarker), httpSyncCount(t, db, httpSyncFrameMarker); e != httpSyncRows || f != httpSyncRows {
		t.Fatalf("replica rows: export %d, frames %d, want %d each", e, f, httpSyncRows)
	}
	db.Close()
	connector.Close()

	if _, err := os.Stat(path + "-info"); err != nil {
		t.Fatalf("the replica did not take the HTTP sync path (no -info file): %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) < len(sqliteHeader) {
		t.Fatalf("read replica file: %d bytes, %v", len(b), err)
	}
	if bytes.HasPrefix(b, sqliteHeader) {
		t.Fatal("the keyed replica file is plaintext: it starts with the SQLite header")
	}
	if leaks := plaintextFiles(t, dir, httpSyncExportMarker, httpSyncFrameMarker); len(leaks) != 0 {
		t.Fatalf("plaintext rows on disk: %v", leaks)
	}

	connector, err = NewEmbeddedReplicaConnector(path, srv.URL, WithAuthToken("token"), keyed)
	if err != nil {
		t.Fatalf("reopen with the key: %v", err)
	}
	db = sql.OpenDB(connector)
	if n := httpSyncCount(t, db, httpSyncFrameMarker); n != httpSyncRows {
		t.Fatalf("reopened replica rows %d, want %d", n, httpSyncRows)
	}
	db.Close()
	connector.Close()

	if c, err := NewEmbeddedReplicaConnector(path, srv.URL, WithAuthToken("token")); err == nil {
		c.Close()
		t.Fatal("the replica reopened without its key")
	}

	ro, err := openLocalForTest(t, path, WithKey(key), WithQueryOnly(true))
	if err != nil {
		t.Fatal(err)
	}
	if n := httpSyncCount(t, ro, httpSyncExportMarker); n != httpSyncRows {
		t.Fatalf("local read of the replica file: %d rows, want %d", n, httpSyncRows)
	}
	if _, err := openLocalForTest(t, path, WithKey(testKey(12)), WithQueryOnly(true)); err == nil || !strings.Contains(err.Error(), "file is not a database") {
		t.Fatalf("replica file under another key: err=%v", err)
	}
}

// An unkeyed replica on the same path writes the SQLite header and the rows
// in plaintext, so the checks above read a file the primary actually filled.
func TestUnkeyedReplicaOnHTTPSyncIsPlaintext(t *testing.T) {
	srv := newHTTPSyncPrimary(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "replica.db")
	connector, err := NewEmbeddedReplicaConnector(path, srv.URL, WithAuthToken("token"))
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	if n := httpSyncCount(t, db, httpSyncFrameMarker); n != httpSyncRows {
		t.Fatalf("replica rows %d, want %d", n, httpSyncRows)
	}
	db.Close()
	connector.Close()
	b, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(b, sqliteHeader) {
		t.Fatalf("unkeyed replica file: %d bytes, %v", len(b), err)
	}
	if leaks := plaintextFiles(t, dir, httpSyncExportMarker); len(leaks) == 0 {
		t.Fatal("no file of the unkeyed replica holds the exported rows")
	}
}

// A keyed offline-writes database (NewSyncedDatabaseConnector) on the same
// path keeps its own files encrypted and pushes its frames with the pages
// unencrypted, the form the primary stores them in.
func TestKeyedSyncedDatabaseOnHTTPSyncIsEncryptedAtRest(t *testing.T) {
	srv := newHTTPSyncPrimary(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "offline.db")
	key := testKey(13)
	connector, err := NewSyncedDatabaseConnector(path, srv.URL, WithAuthToken("token"), WithEncryption(base64.StdEncoding.EncodeToString(key)))
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	mustExec(t, db, "INSERT INTO t(v) VALUES (?)", httpSyncPushMarker)
	if _, err := connector.Sync(); err != nil {
		t.Fatalf("push: %v", err)
	}
	if n := httpSyncCount(t, db, httpSyncPushMarker); n != 1 {
		t.Fatalf("offline row count %d, want 1", n)
	}
	db.Close()
	connector.Close()

	pushed := srv.pushedFrames()
	if len(pushed) == 0 || len(pushed)%httpSyncFrameSize != 0 {
		t.Fatalf("pushed %d bytes, want whole frames", len(pushed))
	}
	if !bytes.Contains(pushed, []byte(httpSyncPushMarker)) {
		t.Fatal("the pushed frames carry the row's page encrypted")
	}
	b, err := os.ReadFile(path)
	if err != nil || bytes.HasPrefix(b, sqliteHeader) {
		t.Fatalf("offline database file: %d bytes, plaintext header %v, %v", len(b), bytes.HasPrefix(b, sqliteHeader), err)
	}
	if leaks := plaintextFiles(t, dir, httpSyncExportMarker, httpSyncFrameMarker, httpSyncPushMarker); len(leaks) != 0 {
		t.Fatalf("plaintext rows on disk: %v", leaks)
	}
	ro, err := openLocalForTest(t, path, WithKey(key), WithQueryOnly(true))
	if err != nil {
		t.Fatal(err)
	}
	if n := httpSyncCount(t, ro, httpSyncPushMarker); n != 1 {
		t.Fatalf("local read of the offline file: %d rows, want 1", n)
	}
}
