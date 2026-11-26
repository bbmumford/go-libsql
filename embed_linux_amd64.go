//go:build !cgo && linux && amd64

package libsql

import _ "embed"

// embeddedLibrary is libSQL's C binding built as a shared library for
// linux/amd64, from the same source and patches as
// lib/linux_amd64/libsql_experimental.a.
//
//go:embed lib/linux_amd64/libsql_experimental.so
var embeddedLibrary []byte

const (
	embeddedSHA256  = "94a72dd7dcf03c9b01183537e2b384b0c313412cc09b5c98381959eb38dbdcde"
	libraryFileName = "libsql_experimental.so"
)
