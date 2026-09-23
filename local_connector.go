//go:build cgo
// +build cgo

package libsql

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"strings"
	"time"
)

// defaultLocalBusyTimeout is the busy_timeout NewLocalConnector sets when
// WithBusyTimeout is not given. With SQLite's default of 0 a connection that
// meets another connection's lock fails at once with "database is locked";
// database/sql pools several connections per *sql.DB, so concurrent writers
// on one handle hit that on most contended statements.
const defaultLocalBusyTimeout = 5 * time.Second

// localKeyLen is the only key length NewLocalConnector accepts: the 32-byte
// group keys its callers hold.
const localKeyLen = 32

// sqliteHeader opens every plaintext SQLite database file.
var sqliteHeader = []byte("SQLite format 3\x00")

// localSetup is what NewLocalConnector's Connector runs on each connection
// Connector.Connect makes, before database/sql can use it.
type localSetup struct {
	path string
	// passphrase is base64.StdEncoding of the key, empty without one.
	passphrase  string
	busyTimeout time.Duration
	queryOnly   bool
}

// NewLocalConnector opens the database file at path on this machine, with no
// primary. Every connection it hands to database/sql is prepared first:
//
//   - With WithKey or WithHexKey: PRAGMA cipher='aes256cbc' and PRAGMA key,
//     then a read of sqlite_schema, so a wrong key fails when the connection
//     is made ("file is not a database") instead of at the first query.
//   - PRAGMA busy_timeout from WithBusyTimeout, 5 s when it is not given.
//   - PRAGMA query_only=1 with WithQueryOnly(true).
//
// The key must be exactly 32 bytes. It is applied as the passphrase
// base64.StdEncoding.EncodeToString(key) under the AES-256-CBC cipher, which
// is how NewEmbeddedReplicaConnector keys a replica file given
// WithEncryption(base64.StdEncoding.EncodeToString(key)): WithEncryption
// crosses the C boundary as a NUL-terminated UTF-8 string, so raw key bytes
// cannot be a replica's passphrase, and a host that holds a 32-byte group key
// hands the replica its base64 form. A replica's file therefore opens here
// under the same key, read-only with WithQueryOnly, while its primary is out
// of reach. The cipher derives its AES key from the first 32 bytes of the
// passphrase only, so what protects the file is the first 32 base64
// characters: the first 24 bytes (192 bits) of the key, on replica and local
// files alike.
//
// With a key, a file that begins with the plaintext SQLite header is refused
// here and again at every connect, so a configured key never guards a
// plaintext file. A new file is created encrypted at its first write.
//
// path is a filesystem path, not a file: URI. The connection options in
// conn_options.go apply as they do to replicas; the replica options are
// refused.
func NewLocalConnector(path string, opts ...Option) (*Connector, error) {
	if path == "" || strings.HasPrefix(path, "file:") || strings.HasPrefix(path, ":memory:") {
		return nil, fmt.Errorf("NewLocalConnector takes a filesystem path, got %q", path)
	}
	var cfg config
	errs := make([]error, 0, len(opts))
	for _, opt := range opts {
		if err := opt.apply(&cfg); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := cfg.refuseReplicaOnly(); err != nil {
		return nil, err
	}
	setup := &localSetup{path: path, busyTimeout: defaultLocalBusyTimeout}
	if cfg.busyTimeout != nil {
		setup.busyTimeout = *cfg.busyTimeout
	}
	if cfg.queryOnly != nil {
		setup.queryOnly = *cfg.queryOnly
	}
	if cfg.localKey != nil {
		setup.passphrase = base64.StdEncoding.EncodeToString(cfg.localKey)
		if err := refusePlaintext(path); err != nil {
			return nil, err
		}
	}
	nativeDbPtr, err := libsqlOpenLocal(path)
	if err != nil {
		return nil, err
	}
	return &Connector{nativeDbPtr: nativeDbPtr, local: true, setup: setup, opts: cfg.connOptions()}, nil
}

// WithKey sets the 32-byte key of a NewLocalConnector file.
func WithKey(key []byte) Option {
	return option(func(o *config) error {
		if o.localKey != nil {
			return errors.New("key already set")
		}
		if len(key) != localKeyLen {
			return fmt.Errorf("key must be %d bytes, got %d", localKeyLen, len(key))
		}
		o.localKey = append([]byte(nil), key...)
		return nil
	})
}

// WithHexKey sets the key of a NewLocalConnector file from its hex form,
// which must decode to 32 bytes.
func WithHexKey(hexKey string) Option {
	return option(func(o *config) error {
		key, err := hex.DecodeString(hexKey)
		if err != nil {
			return errors.New("hex key is not hexadecimal")
		}
		return WithKey(key).apply(o)
	})
}

// WithBusyTimeout sets how long a NewLocalConnector connection waits for
// another connection's lock before failing with "database is locked". Zero
// fails at once.
func WithBusyTimeout(timeout time.Duration) Option {
	return option(func(o *config) error {
		if o.busyTimeout != nil {
			return errors.New("busy timeout already set")
		}
		if timeout < 0 || timeout.Milliseconds() > math.MaxInt32 {
			return fmt.Errorf("busy timeout %v is out of range", timeout)
		}
		o.busyTimeout = &timeout
		return nil
	})
}

// WithQueryOnly makes every NewLocalConnector connection refuse writes
// (PRAGMA query_only).
func WithQueryOnly(queryOnly bool) Option {
	return option(func(o *config) error {
		if o.queryOnly != nil {
			return errors.New("query only already set")
		}
		o.queryOnly = &queryOnly
		return nil
	})
}

// refuseLocalOnly is called by the replica constructors, which have no use
// for these options; a caller passing them would otherwise believe a file is
// keyed or read-only when it is not.
func (c *config) refuseLocalOnly() error {
	if c.localKey != nil || c.busyTimeout != nil || c.queryOnly != nil {
		return errors.New("WithKey, WithHexKey, WithBusyTimeout and WithQueryOnly apply only to NewLocalConnector")
	}
	return nil
}

// refuseReplicaOnly is called by NewLocalConnector, which has no primary and
// keys its file with WithKey or WithHexKey.
func (c *config) refuseReplicaOnly() error {
	if c.authToken != nil || c.readYourWrites != nil || c.encryptionKey != nil ||
		c.remoteEncryptionKey != nil || c.syncInterval != nil {
		return errors.New("WithAuthToken, WithReadYourWrites, WithEncryption, WithRemoteEncryption and WithSyncInterval apply only to replica connectors; NewLocalConnector takes WithKey or WithHexKey")
	}
	return nil
}

// prepare runs the per-connection PRAGMAs on c. The key PRAGMA's error is
// reported without the statement, which holds the passphrase.
func (s *localSetup) prepare(c *conn) error {
	if s.passphrase != "" {
		if err := refusePlaintext(s.path); err != nil {
			return err
		}
		if _, err := c.execLocal("PRAGMA cipher='aes256cbc'", nil); err != nil {
			return fmt.Errorf("select the cipher for %s: %w", s.path, err)
		}
		if _, err := c.execLocal("PRAGMA key='"+s.passphrase+"'", nil); err != nil {
			return fmt.Errorf("apply the key to %s: %s", s.path, withoutStatement(err))
		}
	}
	if _, err := c.execLocal(fmt.Sprintf("PRAGMA busy_timeout=%d", s.busyTimeout.Milliseconds()), nil); err != nil {
		return err
	}
	if s.queryOnly {
		if _, err := c.execLocal("PRAGMA query_only=1", nil); err != nil {
			return err
		}
	}
	if s.passphrase != "" {
		// The read fails with "file is not a database" under another key and
		// with "database is locked" when another connection's lock outlasts
		// the busy timeout; the wrapped engine error names which, so the
		// message here names only the read.
		if _, err := c.execLocal("SELECT count(*) FROM sqlite_schema", nil); err != nil {
			return fmt.Errorf("read the schema of %s with the key applied: %w", s.path, err)
		}
	}
	return nil
}

// withoutStatement drops the first line of a libsqlError, which names the
// statement, and keeps the engine's message.
func withoutStatement(err error) string {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return msg[i+1:]
	}
	return "statement failed"
}

// refusePlaintext fails when path holds a plaintext SQLite database. A
// missing file, or one shorter than the header, is a database not yet
// written.
func refusePlaintext(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, len(sqliteHeader))
	if _, err := io.ReadFull(f, head); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil
		}
		return err
	}
	if bytes.Equal(head, sqliteHeader) {
		return fmt.Errorf("%s is a plaintext SQLite database but a key is configured", path)
	}
	return nil
}
