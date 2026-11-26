//go:build !cgo && darwin && arm64

package libsql

import _ "embed"

// embeddedLibrary is libSQL's C binding built as a shared library for
// darwin/arm64, from the same source and patches as
// lib/darwin_arm64/libsql_experimental.a.
//
//go:embed lib/darwin_arm64/libsql_experimental.dylib
var embeddedLibrary []byte

const (
	embeddedSHA256  = "afb8f588d739b533531034536665ec3dd7beba37ce9ddbd0b8193b0cb9628236"
	libraryFileName = "libsql_experimental.dylib"
)
