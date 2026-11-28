//go:build !cgo && linux && arm64

package libsql

import _ "embed"

// embeddedLibrary is libSQL's C binding built as a shared library for
// linux/arm64, from the same source and patches as
// lib/linux_arm64/libsql_experimental.a. It sits in a directory of its own, which
// the cgo build's -L does not search, so the cgo build links the archive.
//
//go:embed lib/linux_arm64/shared/libsql_experimental.so
var embeddedLibrary []byte

const (
	embeddedSHA256  = "f8608102c96e6a35746229d34566f382a5dc3a87055ba30bf6583b0b3f5a7f70"
	libraryFileName = "libsql_experimental.so"
)
