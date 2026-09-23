//go:build cgo
// +build cgo

package libsql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const localMarker = "PLAINTEXT-MARKER-4711"

// testKey returns a 32-byte key; seed varies it. Byte 0 of seed 0 is NUL, so
// a key that crossed the C boundary as a string would be cut short.
func testKey(seed byte) []byte {
	k := make([]byte, localKeyLen)
	for i := range k {
		k[i] = byte(i) + seed
	}
	return k
}

func openLocalForTest(t *testing.T, path string, opts ...Option) (*sql.DB, error) {
	t.Helper()
	c, err := NewLocalConnector(path, opts...)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(c)
	t.Cleanup(func() {
		db.Close()
		c.Close()
	})
	return db, db.Ping()
}

func fileContains(t *testing.T, path string, needle []byte) bool {
	t.Helper()
	for _, p := range []string{path, path + "-wal", path + "-journal"} {
		b, err := os.ReadFile(p)
		if err == nil && bytes.Contains(b, needle) {
			return true
		}
	}
	return false
}

func TestLocalConnectorKeyedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyed.db")
	key := testKey(0)

	// H1: a keyed file is created and written.
	db, err := openLocalForTest(t, path, WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "CREATE TABLE t(v TEXT); INSERT INTO t VALUES ('"+localMarker+"')")
	db.Close()

	// H2: neither the header nor the row is readable on disk.
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		t.Fatalf("read %s: %d bytes, %v", path, len(b), err)
	}
	if bytes.HasPrefix(b, sqliteHeader) || fileContains(t, path, []byte(localMarker)) {
		t.Fatal("the keyed file holds the plaintext header or row")
	}

	// H3: the same key reads it back.
	db, err = openLocalForTest(t, path, WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if got := textColumn(t, db, "SELECT v FROM t"); len(got) != 1 || got[0] != localMarker {
		t.Fatalf("reopen with the key read %q", got)
	}
	db.Close()

	// H4: another key fails when the connection is made, and the error
	// carries neither key form.
	_, err = openLocalForTest(t, path, WithKey(testKey(1)))
	if err == nil || !strings.Contains(err.Error(), "file is not a database") {
		t.Fatalf("wrong key: err=%v", err)
	}
	for _, form := range []string{base64.StdEncoding.EncodeToString(testKey(1)), hex.EncodeToString(testKey(1))} {
		if strings.Contains(err.Error(), form) {
			t.Fatal("the error message carries the key")
		}
	}

	// H5: the hex form of the key opens it, and so does a plain connection
	// keyed with the base64 passphrase, the form a replica keyed
	// WithEncryption(base64(key)) uses.
	db, err = openLocalForTest(t, path, WithHexKey(hex.EncodeToString(key)))
	if err != nil {
		t.Fatal(err)
	}
	if got := textColumn(t, db, "SELECT v FROM t"); len(got) != 1 || got[0] != localMarker {
		t.Fatalf("hex key read %q", got)
	}
	plain, err := sql.Open(DriverName, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	conn, err := plain.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "PRAGMA key='"+base64.StdEncoding.EncodeToString(key)+"'"); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := conn.QueryRowContext(context.Background(), "SELECT v FROM t").Scan(&v); err != nil || v != localMarker {
		t.Fatalf("base64 passphrase on a plain connection read %q, %v", v, err)
	}

	// The cipher reads only the first 32 passphrase bytes, as NewLocalConnector
	// documents: the first 32 base64 characters open the file, 31 do not.
	for _, n := range []int{32, 31} {
		c, err := plain.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ExecContext(context.Background(), "PRAGMA key='"+base64.StdEncoding.EncodeToString(key)[:n]+"'"); err != nil {
			t.Fatal(err)
		}
		err = c.QueryRowContext(context.Background(), "SELECT v FROM t").Scan(&v)
		c.Close()
		if opened := err == nil; opened != (n == 32) {
			t.Fatalf("passphrase prefix of %d characters: opened=%v (%v)", n, opened, err)
		}
	}
}

func TestLocalConnectorRefusesPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.db")
	plain := openPlainAt(t, path)
	mustExec(t, plain, "CREATE TABLE t(v)")
	plain.Close()
	if _, err := NewLocalConnector(path, WithKey(testKey(0))); err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("keyed open of a plaintext file: err=%v", err)
	}

	// The check runs again at every connect: a plaintext file written after
	// the connector was made is refused too.
	later := filepath.Join(dir, "later.db")
	c, err := NewLocalConnector(later, WithKey(testKey(0)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p := openPlainAt(t, later)
	mustExec(t, p, "CREATE TABLE t(v)")
	p.Close()
	if _, err := c.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("connect over a plaintext file: err=%v", err)
	}
}

func openPlainAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(DriverName, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Q1: a query-only connector reads and refuses every write.
func TestLocalConnectorQueryOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ro.db")
	key := testKey(3)
	rw, err := openLocalForTest(t, path, WithKey(key))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, rw, "CREATE TABLE t(v TEXT); INSERT INTO t VALUES ('a')")
	rw.Close()

	ro, err := openLocalForTest(t, path, WithKey(key), WithQueryOnly(true))
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, ro, "t"); n != 1 {
		t.Fatalf("query-only read %d rows, want 1", n)
	}
	for _, q := range []string{"INSERT INTO t VALUES ('b')", "WITH x AS (SELECT 1) INSERT INTO t SELECT 'c' FROM x", "CREATE TABLE u(v)"} {
		if _, err := ro.ExecContext(context.Background(), q); err == nil || !strings.Contains(err.Error(), "readonly") {
			t.Fatalf("%s on a query-only connector: err=%v", q, err)
		}
	}
	if n := countRows(t, ro, "t"); n != 1 {
		t.Fatalf("rows after refused writes = %d, want 1", n)
	}
}

// A connection waits out another connection's write lock under the default
// busy timeout, and fails at once with WithBusyTimeout(0).
func TestLocalConnectorBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	ctx := context.Background()
	holder, err := openLocalForTest(t, path)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, holder, "CREATE TABLE t(v INTEGER)")
	waiter, err := openLocalForTest(t, path)
	if err != nil {
		t.Fatal(err)
	}
	impatient, err := openLocalForTest(t, path, WithBusyTimeout(0))
	if err != nil {
		t.Fatal(err)
	}

	lock, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := impatient.ExecContext(ctx, "INSERT INTO t VALUES (1)"); err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("insert under another writer's lock with busy_timeout 0: err=%v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := waiter.ExecContext(ctx, "INSERT INTO t VALUES (2)")
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if _, err := lock.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("insert waiting for the lock under the default busy timeout: %v", err)
	}
	if got := int64Column(t, holder, "SELECT v FROM t"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("rows = %v, want [2]", got)
	}
}

// Sixteen goroutines writing through one handle see no "database is locked".
func TestLocalConnectorConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stress.db")
	db, err := openLocalForTest(t, path, WithKey(testKey(5)))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(16)
	mustExec(t, db, "CREATE TABLE t(g INTEGER, i INTEGER)")
	const workers, each = 16, 50
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := db.ExecContext(context.Background(), "INSERT INTO t VALUES (?, ?)", int64(g), int64(i)); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent insert: %v", err)
	}
	if n := countRows(t, db, "t"); n != workers*each {
		t.Fatalf("rows = %d, want %d", n, workers*each)
	}
}

func TestLocalConnectorOptions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opts.db")
	refused := []struct {
		name string
		path string
		opts []Option
		want string
	}{
		{"short key", path, []Option{WithKey(make([]byte, 31))}, "key must be 32 bytes, got 31"},
		{"long key", path, []Option{WithKey(make([]byte, 33))}, "key must be 32 bytes, got 33"},
		{"not hex", path, []Option{WithHexKey("zz")}, "not hexadecimal"},
		{"short hex", path, []Option{WithHexKey(strings.Repeat("ab", 31))}, "key must be 32 bytes, got 31"},
		{"two keys", path, []Option{WithKey(testKey(0)), WithHexKey(hex.EncodeToString(testKey(0)))}, "key already set"},
		{"negative busy timeout", path, []Option{WithBusyTimeout(-time.Second)}, "out of range"},
		{"replica option", path, []Option{WithAuthToken("token")}, "apply only to replica connectors"},
		{"replica encryption", path, []Option{WithEncryption("k")}, "apply only to replica connectors"},
		{"file URI", "file:" + path, nil, "filesystem path"},
		{"memory", ":memory:", nil, "filesystem path"},
		{"empty path", "", nil, "filesystem path"},
	}
	for _, tc := range refused {
		if _, err := NewLocalConnector(tc.path, tc.opts...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want %q", tc.name, err, tc.want)
		}
	}
	// The replica constructors refuse the local options before they dial.
	for _, opt := range []Option{WithKey(testKey(0)), WithBusyTimeout(time.Second), WithQueryOnly(true)} {
		if _, err := NewEmbeddedReplicaConnector(path, "http://127.0.0.1:9", opt); err == nil || !strings.Contains(err.Error(), "apply only to NewLocalConnector") {
			t.Errorf("replica with a local option: err=%v", err)
		}
		if _, err := NewSyncedDatabaseConnector(path, "http://127.0.0.1:9", opt); err == nil || !strings.Contains(err.Error(), "apply only to NewLocalConnector") {
			t.Errorf("synced database with a local option: err=%v", err)
		}
	}
	// No key and a new file: an ordinary local database.
	db, err := openLocalForTest(t, filepath.Join(dir, "plain-new.db"))
	if err != nil {
		t.Fatal(err)
	}
	var busy int
	if err := db.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 5000 {
		t.Fatalf("default busy_timeout = %d, %v; want 5000", busy, err)
	}
}
