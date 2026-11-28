//go:build !cgo && darwin && arm64

package libsql

import _ "embed"

// embeddedLibrary is libSQL's C binding built as a shared library for
// darwin/arm64, from the same source and patches as
// lib/darwin_arm64/libsql_experimental.a. It sits in a directory of its own, which
// the cgo build's -L does not search, so the cgo build links the archive.
//
//go:embed lib/darwin_arm64/shared/libsql_experimental.dylib
var embeddedLibrary []byte

const (
	embeddedSHA256  = "41b9f48ce991a8f86912fd7bdeaae22212160f4619cab5db283af45320211242"
	libraryFileName = "libsql_experimental.dylib"
)
