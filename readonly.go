//go:build cgo
// +build cgo

package libsql

import (
	"errors"
	"fmt"
	"strings"
)

// ErrReadOnlyReplica is the refusal of a statement on a connector opened with
// WithReadOnly. errors.Is finds it in every such refusal; the message names
// the statement's leading keyword and why, never the statement's text.
var ErrReadOnlyReplica = errors.New("libsql: a read-only replica refuses this statement")

// WithReadOnly makes a replica connector (NewEmbeddedReplicaConnector,
// NewSyncedDatabaseConnector) refuse, with ErrReadOnlyReplica and before the C
// library sees it, every statement outside an allow-list of reads the
// replica answers from its own file:
//
//   - SELECT, and WITH ... SELECT;
//   - PRAGMA of one of libSQL's read pragmas (readPragmas), encoding only
//     without a value;
//   - a read transaction: BEGIN READONLY, then COMMIT, END or ROLLBACK, with
//     SAVEPOINT, RELEASE and ROLLBACK TO inside it. Each runs as a call of its
//     own, with no arguments. BeginTx with sql.TxOptions{ReadOnly: true}
//     sends BEGIN READONLY; every other BeginTx is refused.
//
// The gate exists because the C library's replica connection runs locally
// only what its own parser classes a read and sends every other statement to
// the primary, a plain BEGIN, BEGIN IMMEDIATE and EXPLAIN included, and it
// refuses PRAGMA query_only=1 outright, so a replica cannot be made
// query-only the way WithQueryOnly makes a local file. A statement the gate
// cannot class is refused.
func WithReadOnly() Option {
	return option(func(o *config) error {
		if o.readOnly {
			return errors.New("read only already set")
		}
		o.readOnly = true
		return nil
	})
}

// readPragmas are the pragmas the C library's replica connection classes as
// reads and runs on the local file. It matches the name exactly as written,
// so the gate does too.
var readPragmas = map[string]bool{
	"table_list": true, "index_list": true, "table_info": true, "table_xinfo": true,
	"index_info": true, "index_xinfo": true, "pragma_list": true, "compile_options": true,
	"database_list": true, "function_list": true, "module_list": true, "encoding": true,
}

// statementKeywords are the words an SQLite statement begins with. A
// refusal names the leading word only when it is one of them, so a refusal
// message never carries text a caller wrote in place of a keyword.
var statementKeywords = map[string]bool{
	"ALTER": true, "ANALYZE": true, "ATTACH": true, "BEGIN": true, "COMMIT": true, "CREATE": true,
	"DELETE": true, "DETACH": true, "DROP": true, "END": true, "EXPLAIN": true, "INSERT": true,
	"PRAGMA": true, "REINDEX": true, "RELEASE": true, "REPLACE": true, "ROLLBACK": true,
	"SAVEPOINT": true, "SELECT": true, "UPDATE": true, "VACUUM": true, "VALUES": true, "WITH": true,
}

// admit checks query for this connection and records the transaction state
// it leaves. ExecContext and QueryContext call it on a read-only connector
// before any C call.
func (c *conn) admit(query string, args int) error {
	next, err := admitStatements(query, args, c.readTx)
	if err != nil {
		return err
	}
	c.readTx = next
	return nil
}

// stmtClass is what the gate makes of one statement.
type stmtClass uint8

const (
	classRefused stmtClass = iota
	classRead
	classBeginRead
	classEndRead
	classSavepoint
)

// admitStatements reports whether every statement of query may run on a
// read-only connector holding the read transaction state inReadTx, and the
// state they leave. args is the number of bound arguments.
//
// The state follows the C library's replica connection, which sets its own
// before it runs a statement: BEGIN READONLY opens a read transaction only
// outside one, COMMIT, END and ROLLBACK close one only inside one, and
// SAVEPOINT, RELEASE and ROLLBACK TO are taken only inside one. The library
// fails each of them in the other state itself; the gate refuses them first,
// so every transaction statement it admits is one the library runs on the
// file. A transaction statement runs alone and with no arguments, so the
// state recorded here is the state the library holds after that one call.
func admitStatements(query string, args int, inReadTx bool) (bool, error) {
	stmts := splitStatements(query)
	if len(stmts) == 0 {
		return inReadTx, refusal("", "no statement")
	}
	for _, s := range stmts {
		class, lead, why := classifyStatement(s)
		if class == classRefused {
			return inReadTx, refusal(lead, why)
		}
		if class == classRead {
			continue
		}
		if len(stmts) != 1 || args != 0 {
			return inReadTx, refusal(lead, "a transaction statement runs alone and takes no arguments")
		}
		switch class {
		case classBeginRead:
			if inReadTx {
				return inReadTx, refusal(lead, "a read transaction is already open")
			}
			return true, nil
		case classEndRead:
			if !inReadTx {
				return inReadTx, refusal(lead, "no read transaction is open")
			}
			return false, nil
		case classSavepoint:
			if !inReadTx {
				return inReadTx, refusal(lead, "a savepoint outside a read transaction")
			}
			return true, nil
		}
	}
	return inReadTx, nil
}

func refusal(lead, why string) error {
	if lead == "" {
		return fmt.Errorf("%w: %s", ErrReadOnlyReplica, why)
	}
	return fmt.Errorf("%w: %s: %s", ErrReadOnlyReplica, lead, why)
}

// classifyStatement classes one statement as splitStatements cut it. It
// returns the statement's leading keyword (upper case, empty when the
// statement does not start with a word) for the refusal message.
//
// The statement is lexed again here and refused when its own lexing finds a
// ';' before its end: splitStatements cuts with SQLite's tokenizer and the
// replica connection parses with libSQL's, so a statement both see as one is
// the only kind admitted.
func classifyStatement(stmt string) (stmtClass, string, string) {
	toks, ok := lexSQL(stmt)
	if !ok {
		return classRefused, "", "the statement does not lex"
	}
	for len(toks) > 0 && toks[len(toks)-1].is(";") {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return classRefused, "", "no statement"
	}
	for _, t := range toks {
		if t.is(";") {
			return classRefused, "", "more than one statement"
		}
	}
	if toks[0].kind != tokWord {
		return classRefused, "", "the statement does not begin with a keyword"
	}
	lead := strings.ToUpper(toks[0].text)
	if !statementKeywords[lead] || !toks[0].keyword(lead) {
		return classRefused, "", "the statement does not begin with a statement keyword"
	}
	rest := toks[1:]
	switch lead {
	case "SELECT":
		return classRead, lead, ""
	case "WITH":
		return classifyWith(lead, rest)
	case "PRAGMA":
		return classifyPragma(lead, rest)
	case "BEGIN":
		if len(rest) > 0 && rest[0].keyword("READONLY") && transactionTail(rest[1:]) {
			return classBeginRead, lead, ""
		}
		return classRefused, lead, "only BEGIN READONLY stays on the replica; every other BEGIN goes to the primary"
	case "COMMIT", "END":
		if transactionTail(rest) {
			return classEndRead, lead, ""
		}
	case "ROLLBACK":
		if transactionTail(rest) {
			return classEndRead, lead, ""
		}
		if len(rest) > 0 && rest[0].keyword("TRANSACTION") {
			rest = rest[1:]
		}
		if len(rest) > 0 && rest[0].keyword("TO") {
			rest = rest[1:]
			if len(rest) > 0 && rest[0].keyword("SAVEPOINT") {
				rest = rest[1:]
			}
			if len(rest) == 1 && rest[0].isName() {
				return classSavepoint, lead, ""
			}
		}
	case "SAVEPOINT":
		if len(rest) == 1 && rest[0].isName() {
			return classSavepoint, lead, ""
		}
	case "RELEASE":
		if len(rest) > 0 && rest[0].keyword("SAVEPOINT") {
			rest = rest[1:]
		}
		if len(rest) == 1 && rest[0].isName() {
			return classSavepoint, lead, ""
		}
	default:
		return classRefused, lead, "not a read the replica answers from its own file"
	}
	return classRefused, lead, "a statement the gate cannot class"
}

// transactionTail reports whether toks is what may follow BEGIN READONLY,
// COMMIT, END or ROLLBACK: nothing, TRANSACTION, or TRANSACTION and a name.
func transactionTail(toks []sqlToken) bool {
	switch len(toks) {
	case 0:
		return true
	case 1:
		return toks[0].keyword("TRANSACTION")
	case 2:
		return toks[0].keyword("TRANSACTION") && toks[1].isName() && !toks[1].keyword("TO")
	}
	return false
}

// classifyWith admits WITH [RECURSIVE] name [(columns)] AS [[NOT] MATERIALIZED]
// (select) [, ...] SELECT ...; the statement a WITH clause leads into is the
// only part that can write, so it must be SELECT.
func classifyWith(lead string, toks []sqlToken) (stmtClass, string, string) {
	i := 0
	if i < len(toks) && toks[i].keyword("RECURSIVE") {
		i++
	}
	for {
		if i >= len(toks) || !toks[i].isName() {
			return classRefused, lead, "a WITH clause the gate cannot class"
		}
		i++
		if i < len(toks) && toks[i].is("(") {
			if i = skipGroup(toks, i); i < 0 {
				return classRefused, lead, "a WITH clause the gate cannot class"
			}
		}
		if i >= len(toks) || !toks[i].keyword("AS") {
			return classRefused, lead, "a WITH clause the gate cannot class"
		}
		i++
		if i < len(toks) && toks[i].keyword("NOT") {
			i++
			if i >= len(toks) || !toks[i].keyword("MATERIALIZED") {
				return classRefused, lead, "a WITH clause the gate cannot class"
			}
			i++
		} else if i < len(toks) && toks[i].keyword("MATERIALIZED") {
			i++
		}
		if i >= len(toks) || !toks[i].is("(") {
			return classRefused, lead, "a WITH clause the gate cannot class"
		}
		if i = skipGroup(toks, i); i < 0 {
			return classRefused, lead, "a WITH clause the gate cannot class"
		}
		if i < len(toks) && toks[i].is(",") {
			i++
			continue
		}
		break
	}
	if i < len(toks) && toks[i].keyword("SELECT") {
		return classRead, lead, ""
	}
	return classRefused, lead, "a WITH clause leads into a statement other than SELECT"
}

// skipGroup returns the index after the parenthesised group that opens at
// toks[i], or -1 when it does not close.
func skipGroup(toks []sqlToken, i int) int {
	depth := 0
	for ; i < len(toks); i++ {
		switch {
		case toks[i].is("("):
			depth++
		case toks[i].is(")"):
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// classifyPragma admits PRAGMA [schema.]name, PRAGMA [schema.]name(value) and
// PRAGMA [schema.]name = value for a name in readPragmas; encoding takes no
// value, because with one it is the setting form.
func classifyPragma(lead string, toks []sqlToken) (stmtClass, string, string) {
	if len(toks) >= 2 && toks[1].is(".") {
		if !toks[0].isName() {
			return classRefused, lead, "a pragma the gate cannot class"
		}
		toks = toks[2:]
	}
	if len(toks) == 0 || toks[0].kind != tokWord || !readPragmas[toks[0].text] {
		return classRefused, lead, "not one of libSQL's read pragmas"
	}
	name, value := toks[0].text, toks[1:]
	if len(value) == 0 {
		return classRead, lead, ""
	}
	if name == "encoding" {
		return classRefused, lead, "PRAGMA encoding with a value sets it"
	}
	switch {
	case len(value) >= 3 && value[0].is("(") && value[len(value)-1].is(")") && pragmaValue(value[1:len(value)-1]):
		return classRead, lead, ""
	case len(value) >= 2 && value[0].is("=") && pragmaValue(value[1:]):
		return classRead, lead, ""
	}
	return classRefused, lead, "a pragma the gate cannot class"
}

// pragmaValue reports whether toks is one pragma argument: a name, a string
// or a number, a number optionally signed.
func pragmaValue(toks []sqlToken) bool {
	if len(toks) == 2 && (toks[0].is("-") || toks[0].is("+")) {
		toks = toks[1:]
	}
	if len(toks) != 1 {
		return false
	}
	switch toks[0].kind {
	case tokWord, tokName, tokString, tokNumber:
		return true
	}
	return false
}

// sqlToken is one token of the gate's lexer.
type sqlToken struct {
	kind tokKind
	text string
}

type tokKind uint8

const (
	// tokWord is a bare word: a keyword or an unquoted name.
	tokWord tokKind = iota + 1
	// tokName is a quoted name: "x", `x` or [x].
	tokName
	// tokString is a string literal: 'x'.
	tokString
	// tokNumber is a numeric literal.
	tokNumber
	// tokPunct is one byte of punctuation or an operator.
	tokPunct
)

func (t sqlToken) is(punct string) bool { return t.kind == tokPunct && t.text == punct }

// keyword reports whether t is the word, compared in ASCII case only:
// SQLite's keywords are ASCII, and Unicode folding would take a word SQLite
// reads as a name for a keyword.
func (t sqlToken) keyword(word string) bool {
	if t.kind != tokWord || len(t.text) != len(word) {
		return false
	}
	for i := 0; i < len(word); i++ {
		a, b := t.text[i], word[i]
		if 'a' <= a && a <= 'z' {
			a -= 'a' - 'A'
		}
		if 'a' <= b && b <= 'z' {
			b -= 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func (t sqlToken) isName() bool { return t.kind == tokWord || t.kind == tokName }

// lexSQL cuts s into tokens, skipping whitespace and comments as SQLite does.
// It reports false for an unterminated quote and for a byte SQLite does not
// take as whitespace, punctuation or part of a word, a number or a quote, so
// nothing it cannot lex is classed.
func lexSQL(s string) ([]sqlToken, bool) {
	var out []sqlToken
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r':
			i++
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			nl := strings.IndexByte(s[i:], '\n')
			if nl < 0 {
				return out, true
			}
			i += nl + 1
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return out, true
			}
			i += 2 + end + 2
		case c == '\'' || c == '"' || c == '`' || c == '[':
			closer := c
			if c == '[' {
				closer = ']'
			}
			j := i + 1
			for {
				k := strings.IndexByte(s[j:], closer)
				if k < 0 {
					return nil, false
				}
				j += k + 1
				// A doubled quote inside the quote is one quote character;
				// SQLite has no doubling inside [...].
				if closer != ']' && j < len(s) && s[j] == closer {
					j++
					continue
				}
				break
			}
			kind := tokName
			if c == '\'' {
				kind = tokString
			}
			out = append(out, sqlToken{kind, s[i:j]})
			i = j
		case isWordStart(c):
			j := i + 1
			for j < len(s) && (isWordStart(s[j]) || isDigit(s[j]) || s[j] == '$') {
				j++
			}
			out = append(out, sqlToken{tokWord, s[i:j]})
			i = j
		case isDigit(c) || c == '.' && i+1 < len(s) && isDigit(s[i+1]):
			j := i + 1
			for j < len(s) && (isDigit(s[j]) || isWordStart(s[j]) || s[j] == '.' ||
				(s[j] == '+' || s[j] == '-') && (s[j-1] == 'e' || s[j-1] == 'E')) {
				j++
			}
			out = append(out, sqlToken{tokNumber, s[i:j]})
			i = j
		case strings.IndexByte("();,.=-+*/%<>!|&~?:@$", c) >= 0:
			out = append(out, sqlToken{tokPunct, s[i : i+1]})
			i++
		default:
			return nil, false
		}
	}
	return out, true
}

// isWordStart reports whether c starts a word: an ASCII letter, '_', or a
// byte of a UTF-8 sequence, which SQLite takes as part of an identifier.
func isWordStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= 0x80
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
