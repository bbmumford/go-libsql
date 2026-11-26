//go:build !cgo && (darwin || linux)

package libsql

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A build with cgo off opens a local file, binds and reads back every value
// type, and opens a replica of a primary speaking libSQL's HTTP sync protocol,
// through the shared library it loads at run time.
func TestOpensWithoutCgo(t *testing.T) {
	db, err := sql.Open(DriverName, "file:"+filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec(t, db, "CREATE TABLE v (i INTEGER, f REAL, s TEXT, b BLOB, n TEXT)")
	mustExec(t, db, "INSERT INTO v VALUES (?, ?, ?, ?, ?)", int64(42), 2.5, "text", []byte{0, 1, 2, 255}, nil)
	var (
		i int64
		f float64
		s string
		b []byte
		n sql.NullString
	)
	if err := db.QueryRow("SELECT i, f, s, b, n FROM v").Scan(&i, &f, &s, &b, &n); err != nil {
		t.Fatal(err)
	}
	if i != 42 || f != 2.5 || s != "text" || string(b) != "\x00\x01\x02\xff" || n.Valid {
		t.Fatalf("read back %d %v %q %v %v", i, f, s, b, n)
	}

	srv := newHTTPSyncPrimary(t)
	c, err := NewEmbeddedReplicaConnector(filepath.Join(t.TempDir(), "replica.db"), srv.URL, WithAuthToken("token"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := sql.OpenDB(c)
	defer r.Close()
	if got := httpSyncCount(t, r, httpSyncFrameMarker); got != httpSyncRows {
		t.Fatalf("the replica holds %d rows of the primary's frames, want %d", got, httpSyncRows)
	}
}

// The loader refuses a shared library whose bytes are not the ones this build
// names: one changed on disk before it is loaded, and embedded bytes that do
// not match; and it replaces a changed file at the install path.
func TestSharedLibraryRefusesATamperedFile(t *testing.T) {
	path, err := installLibrary(embeddedLibrary, embeddedSHA256, t.TempDir(), libraryFileName)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0xff
	tampered := filepath.Join(t.TempDir(), libraryFileName)
	if err := os.WriteFile(tampered, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openVerified(tampered, embeddedSHA256); !errors.Is(err, ErrLibraryDigest) {
		t.Fatalf("openVerified of a changed library = %v, want ErrLibraryDigest", err)
	}
	if _, err := installLibrary(b, embeddedSHA256, t.TempDir(), libraryFileName); !errors.Is(err, ErrLibraryDigest) {
		t.Fatalf("installLibrary of bytes with another digest = %v, want ErrLibraryDigest", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, libraryFileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := installLibrary(embeddedLibrary, embeddedSHA256, dir, libraryFileName)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fileSHA256(p); err != nil || got != embeddedSHA256 {
		t.Fatalf("the changed file at the install path was kept: %s, %v", got, err)
	}
}

// Every symbol the package binds resolves from the loaded libSQL library and
// from no other image; a name the library does not export is refused, on
// macOS because the system's SQLite answers it.
func TestEverySymbolComesFromTheLibrary(t *testing.T) {
	if _, e := lib(); e != nil {
		t.Fatalf("load: %s", e.msg)
	}
	path := filepath.Join(libraryDir(embeddedSHA256), libraryFileName)
	h, err := openVerified(path, embeddedSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(h, path, (&functions{}).bindings()); err != nil {
		t.Fatalf("the package's bindings: %v", err)
	}
	var complete func(string) int32
	_, err = resolve(h, path, []binding{{"sqlite3_complete", &complete}})
	if err == nil {
		t.Fatal("sqlite3_complete, which the libSQL library does not export, was bound")
	}
	if runtime.GOOS == "darwin" && !errors.Is(err, ErrForeignSymbol) {
		t.Fatalf("on macOS the system SQLite answers sqlite3_complete: err = %v, want ErrForeignSymbol", err)
	}
}
