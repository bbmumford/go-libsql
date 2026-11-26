//go:build !cgo && darwin && amd64

package libsql

import _ "embed"

// embeddedLibrary is libSQL's C binding built as a shared library for
// darwin/amd64, from the same source and patches as
// lib/darwin_amd64/libsql_experimental.a.
//
//go:embed lib/darwin_amd64/libsql_experimental.dylib
var embeddedLibrary []byte

const (
	embeddedSHA256  = "18a1d5117ce1df5ce66f118501e28917d12998a903df48399d49d510e078eaaf"
	libraryFileName = "libsql_experimental.dylib"
)
