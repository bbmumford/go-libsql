package libsql

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// A build with cgo off loads libSQL's C binding as a shared library at run
// time instead of linking its static archive (native_purego.go). The library
// for the build's target is embedded in the binary (embed_*.go) with its
// SHA-256, written once into a per-user cache directory, hashed again and
// only then loaded, and every symbol bound from it must resolve from that file
// and from no other image the process has loaded.

var (
	// ErrNoLibrary is what every open returns in a build with cgo off for a
	// target this package embeds no libSQL shared library for.
	ErrNoLibrary = errors.New("libsql: this build embeds no libSQL shared library for " + runtime.GOOS + "/" + runtime.GOARCH)
	// ErrLibraryDigest means the shared library's bytes, embedded or on disk
	// when it was about to be loaded, are not the ones this build names.
	ErrLibraryDigest = errors.New("libsql: the libSQL shared library does not have the SHA-256 this build names")
	// ErrForeignSymbol means a symbol the package binds resolved from another
	// image than the libSQL shared library it loaded, such as the operating
	// system's own SQLite.
	ErrForeignSymbol = errors.New("libsql: a bound symbol resolves from another image than the libSQL shared library")
)

// fileSHA256 is the hex SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// libraryDir is the directory the shared library is written to: the user's
// cache directory, or the temporary directory when the process has none, under
// go-libsql/<the first 16 digits of its SHA-256>.
func libraryDir(sum string) string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "go-libsql", sum[:16])
}

// installLibrary writes data, whose SHA-256 must be sum, to dir/name unless a
// file with that digest is there already, and returns its path. The file is
// written beside its final name and renamed into place, so a reader never sees
// a part-written library.
func installLibrary(data []byte, sum, dir, name string) (string, error) {
	if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != sum {
		return "", fmt.Errorf("%w: the embedded copy", ErrLibraryDigest)
	}
	path := filepath.Join(dir, name)
	if got, err := fileSHA256(path); err == nil && got == sum {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("libsql: install the libSQL shared library: %w", err)
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return "", fmt.Errorf("libsql: install the libSQL shared library: %w", err)
	}
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return "", fmt.Errorf("libsql: install the libSQL shared library: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o500); err != nil {
		return "", fmt.Errorf("libsql: install the libSQL shared library: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("libsql: install the libSQL shared library: %w", err)
	}
	return path, nil
}
