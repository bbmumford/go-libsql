package libsql

import (
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
)

// connOptions are the per-connection behaviours set by the options in this
// file. NewEmbeddedReplicaConnector, NewSyncedDatabaseConnector and
// NewLocalConnector store them on the Connector, and Connector.Connect copies
// them into every conn. Connectors from sql.Open take no options and keep
// the zero value: a deferred BEGIN, and date-like TEXT returned as time.Time.
type connOptions struct {
	immediateTransactions bool
	rawText               bool
	// readOnly is WithReadOnly's: every statement passes the gate in
	// readonly.go before any C call.
	readOnly bool
}

func (c *config) connOptions() connOptions {
	var o connOptions
	if c.immediateTransactions != nil {
		o.immediateTransactions = *c.immediateTransactions
	}
	if c.rawText != nil {
		o.rawText = *c.rawText
	}
	o.readOnly = c.readOnly
	return o
}

// WithRawText(true) makes rows.Next return every TEXT value as a string.
// Without it a TEXT value that parses as one of ten date and time layouts
// (RFC 3339, "2006-01-02 15:04:05", "2006-01-02", ...) is returned as a
// time.Time in UTC, so a column holding an identifier, a version or a user
// string that happens to look like a date comes back as a different type,
// and scanning it into a string yields the value reformatted as RFC 3339.
func WithRawText(raw bool) Option {
	return option(func(o *config) error {
		if o.rawText != nil {
			return errors.New("raw text already set")
		}
		o.rawText = &raw
		return nil
	})
}

// WithImmediateTransactions(true) makes BeginTx open every transaction that
// can write with BEGIN IMMEDIATE, which takes the write lock (on a replica,
// the primary's) before the first statement. LevelSerializable is accepted
// and means the same; a ReadOnly transaction opens with BEGIN.
//
// Without it BeginTx sends a deferred BEGIN, which takes the write lock only
// at the first write: two replicas that each read a row and then update it
// inside a transaction either fail with "database is locked" or lose one of
// the updates, and on a local file a transaction that has read can fail its
// first write with "database is locked" while another connection writes,
// without waiting out the busy timeout (SQLite skips the busy handler where
// waiting could deadlock).
func WithImmediateTransactions(immediate bool) Option {
	return option(func(o *config) error {
		if o.immediateTransactions != nil {
			return errors.New("immediate transactions already set")
		}
		o.immediateTransactions = &immediate
		return nil
	})
}

// beginStatement is the statement BeginTx sends for opts. Without
// WithImmediateTransactions it is BEGIN, and ReadOnly and every isolation
// level other than the default are refused. Under WithReadOnly it is BEGIN
// READONLY, the one BEGIN libSQL's replica connection keeps on the replica,
// and a transaction without ReadOnly is refused with ErrReadOnlyReplica.
func (c *conn) beginStatement(opts sqldriver.TxOptions) (string, error) {
	level := sql.IsolationLevel(opts.Isolation)
	if c.opts.readOnly {
		if !opts.ReadOnly {
			return "", refusal("BEGIN", "a transaction that can write goes to the primary; pass sql.TxOptions{ReadOnly: true}")
		}
		if level != sql.LevelDefault && (!c.opts.immediateTransactions || level != sql.LevelSerializable) {
			return "", fmt.Errorf("isolation level %d is not supported", opts.Isolation)
		}
		return "BEGIN READONLY", nil
	}
	if !c.opts.immediateTransactions {
		if opts.ReadOnly {
			return "", errors.New("read only transactions are not supported")
		}
		if level != sql.LevelDefault {
			return "", fmt.Errorf("isolation level %d is not supported", opts.Isolation)
		}
		return "BEGIN", nil
	}
	if level != sql.LevelDefault && level != sql.LevelSerializable {
		return "", fmt.Errorf("isolation level %d is not supported", opts.Isolation)
	}
	if opts.ReadOnly {
		return "BEGIN", nil
	}
	return "BEGIN IMMEDIATE", nil
}
