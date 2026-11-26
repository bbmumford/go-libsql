package libsql

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// statementLabel names a statement in an error without its text: its first
// keyword and the first eight bytes of its SHA-256, for example
// "INSERT statement sha256:1f0c...". A statement's literals can hold personal
// data and the driver's errors reach its callers' logs, so no error carries the
// statement; the keyword says what kind of statement failed and the digest
// ties the error to the statement for whoever holds its text. The execute,
// query and prepare errors of libsql.go and exec.go are its callers.
func statementLabel(query string) string {
	sum := sha256.Sum256([]byte(query))
	kw := strings.TrimSpace(query)
	end := 0
	for end < len(kw) && (kw[end] >= 'a' && kw[end] <= 'z' || kw[end] >= 'A' && kw[end] <= 'Z') {
		end++
	}
	if end == 0 {
		return fmt.Sprintf("statement sha256:%x", sum[:8])
	}
	return fmt.Sprintf("%s statement sha256:%x", strings.ToUpper(kw[:end]), sum[:8])
}
