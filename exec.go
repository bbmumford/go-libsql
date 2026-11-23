//go:build cgo
// +build cgo

package libsql

/*
#include <libsql.h>
#include <stdlib.h>

int sqlite3_complete(const char *sql);
*/
import "C"

import (
	"context"
	sqldriver "database/sql/driver"
	"fmt"
	"strings"
	"unsafe"
)

// ExecContext runs every statement in query, in order, on this connection and
// stops at the first statement that fails, returning its error. It serves
// database/sql's Exec, the stmt.ExecContext of PrepareContext, and the
// BEGIN/COMMIT/ROLLBACK of BeginTx and tx.
//
// The statements are cut here because the C binding runs only the first
// statement of a string on local and remote connections, and a replica
// connection runs them all but reports only the first failure; a schema
// script or migration handed to Exec would otherwise half-apply without an
// error. Arguments bind to one statement, so a query with several statements
// and arguments is refused rather than bound to the first statement only.
//
// The result is the last statement's, as sqlite3_changes and
// sqlite3_last_insert_rowid report after running the statements one by one.
func (c *conn) ExecContext(ctx context.Context, query string, args []sqldriver.NamedValue) (sqldriver.Result, error) {
	if c.opts.readOnly {
		if err := c.admit(query, len(args)); err != nil {
			return nil, err
		}
	}
	stmts := splitStatements(query)
	if len(stmts) <= 1 {
		return c.execOne(query, args)
	}
	if len(args) > 0 {
		return nil, fmt.Errorf("query holds %d statements; arguments bind to exactly one statement", len(stmts))
	}
	var res sqldriver.Result
	for _, s := range stmts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r, err := c.execOne(s, nil)
		if err != nil {
			return nil, err
		}
		res = r
	}
	return res, nil
}

// execOne runs one statement and reads the connection's change count and
// last row id after it.
func (c *conn) execOne(query string, args []sqldriver.NamedValue) (sqldriver.Result, error) {
	if c.local {
		return c.execLocal(query, args)
	}
	rows, err := c.execute(query, args, true)
	if err != nil {
		return nil, err
	}
	id := int64(C.libsql_last_insert_rowid(c.nativePtr))
	changes := int64(C.libsql_changes(c.nativePtr))
	if rows != nil {
		C.libsql_free_rows(rows)
	}
	return execResult{id, changes}, nil
}

// execLocal runs one statement on a connection with no primary through the
// row-returning path and steps through every row it yields, discarding them.
// The C binding's execute path fails a local statement that yields a row
// ("Execute returned rows") after the statement has already taken effect, so
// Exec of PRAGMA journal_mode=WAL, PRAGMA busy_timeout=... or INSERT ...
// RETURNING on that path returns an error for work that is done.
// NewLocalConnector's per-connection PRAGMAs (localSetup.prepare) run here.
func (c *conn) execLocal(query string, args []sqldriver.NamedValue) (sqldriver.Result, error) {
	rows, err := c.execute(query, args, false)
	if err != nil {
		return nil, err
	}
	if rows != nil {
		defer C.libsql_free_rows(rows)
		for {
			var row C.libsql_row_t
			var errMsg *C.char
			if status := C.libsql_next_row(rows, &row, &errMsg); status != 0 {
				return nil, libsqlError(fmt.Sprint("failed to execute query ", query), status, errMsg)
			}
			if row == nil {
				break
			}
			C.libsql_free_row(row)
		}
	}
	id := int64(C.libsql_last_insert_rowid(c.nativePtr))
	changes := int64(C.libsql_changes(c.nativePtr))
	return execResult{id, changes}, nil
}

// splitStatements cuts query into the statements SQLite runs one after
// another. A cut is made after a ';' only where sqlite3_complete, SQLite's own
// tokenizer, reports the text since the previous cut as a complete statement,
// so a ';' inside a string literal, a quoted identifier, a comment, or a
// CREATE TRIGGER ... BEGIN ... END body (CASE ... END inside it included)
// never cuts. Pieces holding only whitespace, comments and ';' are dropped.
// Text after the last cut that is not blank is kept as the final statement, so
// a last statement without a ';' still runs, and an unterminated one fails in
// the engine rather than being dropped here.
func splitStatements(query string) []string {
	var out []string
	start := 0
	for i := 0; i < len(query); i++ {
		if query[i] != ';' {
			continue
		}
		piece := query[start : i+1]
		if !sqliteComplete(piece) {
			continue
		}
		if !isBlankSQL(piece) {
			out = append(out, piece)
		}
		start = i + 1
	}
	if tail := query[start:]; !isBlankSQL(tail) {
		out = append(out, tail)
	}
	return out
}

func sqliteComplete(sql string) bool {
	cs := C.CString(sql)
	defer C.free(unsafe.Pointer(cs))
	return C.sqlite3_complete(cs) != 0
}

// isBlankSQL reports whether s holds only what SQLite's tokenizer skips
// between statements: whitespace, "--" line comments, "/* */" block comments
// (an unterminated one runs to the end of the input) and ';'.
func isBlankSQL(s string) bool {
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ';' || c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r':
			i++
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			nl := strings.IndexByte(s[i:], '\n')
			if nl < 0 {
				return true
			}
			i += nl + 1
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return true
			}
			i += 2 + end + 2
		default:
			return false
		}
	}
	return true
}
