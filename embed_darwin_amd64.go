//go:build !cgo && darwin && amd64

package libsql

import _ "embed"

// embeddedLibrary is libSQL's C binding built as a shared library for
// darwin/amd64, from the same source and patches as
// lib/darwin_amd64/libsql_experimental.a. It sits in a directory of its own, which
// the cgo build's -L does not search, so the cgo build links the archive.
//
//go:embed lib/darwin_amd64/shared/libsql_experimental.dylib
var embeddedLibrary []byte

const (
	embeddedSHA256  = "f30b411236dac4cdd9655e51dbfb6d4802494ffb93e6794b97cd29b7c717bde5"
	libraryFileName = "libsql_experimental.dylib"
)
