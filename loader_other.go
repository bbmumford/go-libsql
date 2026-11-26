//go:build !cgo && !darwin && !linux

package libsql

// loadLibrary has no loader for this operating system yet.
func loadLibrary([]binding) ([]uintptr, error) { return nil, ErrNoLibrary }
