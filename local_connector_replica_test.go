//go:build cgo
// +build cgo

package libsql

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A replica keyed WithEncryption(base64(key)) keeps no plaintext on disk, and
// its file opens read-only through NewLocalConnector with the same 32-byte
// key while the replica is closed: the offline read path of a replica host.
func TestLocalConnectorOpensReplicaFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replica.db")
	key := testKey(7)
	table := uniqueName("x1")
	marker := "REPLICA-MARKER-" + table

	db, connector := openReplicaForTest(t, path, WithEncryption(base64.StdEncoding.EncodeToString(key)))
	mustExec(t, db, "CREATE TABLE "+table+"(v TEXT)")
	mustExec(t, db, "INSERT INTO "+table+" VALUES (?)", marker)
	if _, err := connector.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := textColumn(t, db, "SELECT v FROM "+table); len(got) != 1 || got[0] != marker {
		t.Fatalf("replica read %q", got)
	}
	db.Close()
	connector.Close()

	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		t.Fatalf("read replica file: %d bytes, %v", len(b), err)
	}
	if bytes.HasPrefix(b, sqliteHeader) || fileContains(t, path, []byte(marker)) {
		t.Fatal("the encrypted replica file holds the plaintext header or row")
	}

	ro, err := openLocalForTest(t, path, WithKey(key), WithQueryOnly(true))
	if err != nil {
		t.Fatal(err)
	}
	if got := textColumn(t, ro, "SELECT v FROM "+table); len(got) != 1 || got[0] != marker {
		t.Fatalf("local read of the replica file = %q", got)
	}
	if _, err := ro.ExecContext(context.Background(), "INSERT INTO "+table+" VALUES ('offline')"); err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("write through the query-only local connector: err=%v", err)
	}
	ro.Close()

	if _, err := openLocalForTest(t, path, WithKey(testKey(8)), WithQueryOnly(true)); err == nil || !strings.Contains(err.Error(), "file is not a database") {
		t.Fatalf("replica file under another key: err=%v", err)
	}
}
