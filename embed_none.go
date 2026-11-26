//go:build !cgo && !(darwin && (arm64 || amd64)) && !(linux && (arm64 || amd64))

package libsql

// No libSQL shared library is embedded for this target: every open fails with
// ErrNoLibrary.
var embeddedLibrary []byte

const (
	embeddedSHA256  = "0000000000000000000000000000000000000000000000000000000000000000"
	libraryFileName = ""
)
