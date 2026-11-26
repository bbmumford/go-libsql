//go:build !cgo && (darwin || linux)

package libsql

import (
	"fmt"
	"path/filepath"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
)

// loadLibrary installs the embedded shared library (installLibrary), loads it
// (openVerified), resolves every binding's symbol in it and checks each comes
// from that file (checkProvenance), and returns the symbols' addresses in the
// bindings' order.
func loadLibrary(bindings []binding) ([]uintptr, error) {
	if len(embeddedLibrary) == 0 {
		return nil, ErrNoLibrary
	}
	path, err := installLibrary(embeddedLibrary, embeddedSHA256, libraryDir(embeddedSHA256), libraryFileName)
	if err != nil {
		return nil, err
	}
	h, err := openVerified(path, embeddedSHA256)
	if err != nil {
		return nil, err
	}
	return resolve(h, path, bindings)
}

// openVerified hashes the file at path and loads it only when its SHA-256 is
// sum, so a library changed on disk after it was installed is never loaded.
func openVerified(path, sum string) (uintptr, error) {
	got, err := fileSHA256(path)
	if err != nil {
		return 0, fmt.Errorf("libsql: read the libSQL shared library: %w", err)
	}
	if got != sum {
		return 0, fmt.Errorf("%w: %s has %s", ErrLibraryDigest, path, got)
	}
	h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return 0, fmt.Errorf("libsql: load the libSQL shared library: %w", err)
	}
	return h, nil
}

// resolve looks up every binding's symbol through the handle h of the library
// at path and checks each comes from that file.
func resolve(h uintptr, path string, bindings []binding) ([]uintptr, error) {
	addrs := make([]uintptr, len(bindings))
	for i, b := range bindings {
		a, err := purego.Dlsym(h, b.name)
		if err != nil {
			return nil, fmt.Errorf("libsql: the libSQL shared library does not export %s: %w", b.name, err)
		}
		addrs[i] = a
	}
	if err := checkProvenance(path, bindings, addrs); err != nil {
		return nil, err
	}
	return addrs, nil
}

// dlInfo is Dl_info in <dlfcn.h>.
type dlInfo struct {
	fname *byte
	fbase unsafe.Pointer
	sname *byte
	saddr unsafe.Pointer
}

// checkProvenance asks the dynamic loader which image holds each address and
// refuses any not in the file at path. A lookup through the library's handle
// also searches the images the library depends on, so a symbol the library
// does not export can resolve from another image, as the operating system's
// SQLite on macOS answers sqlite3_* names.
func checkProvenance(path string, bindings []binding, addrs []uintptr) error {
	dladdr, err := bindDladdr()
	if err != nil {
		return err
	}
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("libsql: resolve the libSQL shared library's path: %w", err)
	}
	for i, b := range bindings {
		var info dlInfo
		if dladdr(addrs[i], &info) == 0 || info.fname == nil {
			return fmt.Errorf("%w: the dynamic loader names no image for %s", ErrForeignSymbol, b.name)
		}
		from := goString(unsafe.Pointer(info.fname))
		got, err := filepath.EvalSymlinks(from)
		if err != nil {
			got = from
		}
		if got != want {
			return fmt.Errorf("%w: %s resolves from %s, not %s", ErrForeignSymbol, b.name, from, path)
		}
	}
	return nil
}

// bindDladdr binds the C library's dladdr.
func bindDladdr() (func(addr uintptr, info *dlInfo) int32, error) {
	names := []string{"libc.so.6", "libdl.so.2"}
	if runtime.GOOS == "darwin" {
		names = []string{"/usr/lib/libSystem.B.dylib"}
	}
	var last error
	for _, n := range names {
		h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			last = err
			continue
		}
		if _, err := purego.Dlsym(h, "dladdr"); err != nil {
			last = err
			continue
		}
		var dladdr func(addr uintptr, info *dlInfo) int32
		purego.RegisterLibFunc(&dladdr, h, "dladdr")
		return dladdr, nil
	}
	return nil, fmt.Errorf("libsql: bind dladdr to check where symbols resolve from: %w", last)
}
