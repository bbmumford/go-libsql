//go:build cgo
// +build cgo

package libsql

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"testing"
)

// clientDriverStub stands in for github.com/tursodatabase/libsql-client-go,
// which registers the name "libsql". Registering it here makes this test
// binary link both names, as a host binary that holds replicas and Hrana
// partition pools does; if this package still registered "libsql", the
// binary would panic in init before any test ran.
type clientDriverStub struct{}

var errClientStub = errors.New("client driver stub")

func (clientDriverStub) Open(string) (sqldriver.Conn, error) { return nil, errClientStub }

func init() {
	sql.Register("libsql", clientDriverStub{})
}

func TestDriverNameCoexistsWithClientDriver(t *testing.T) {
	var ours, client bool
	for _, name := range sql.Drivers() {
		switch name {
		case DriverName:
			ours = true
		case "libsql":
			client = true
		}
	}
	if DriverName != "libsql-cgo" || !ours || !client {
		t.Fatalf("DriverName=%q registered=%v, client name registered=%v", DriverName, ours, client)
	}

	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var one int
	if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 through %s = %d, %v", DriverName, one, err)
	}

	other, err := sql.Open("libsql", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Ping(); !errors.Is(err, errClientStub) {
		t.Fatalf(`"libsql" reached %v, want the client driver stub`, err)
	}
}
