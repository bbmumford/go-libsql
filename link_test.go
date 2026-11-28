package libsql

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The directories native_cgo.go's #cgo LDFLAGS name with -L hold only the
// static archive: GNU ld and ld64 both take a shared library over an archive
// of the same name in one directory, which would make every cgo binary need
// libsql_experimental.so or .dylib at run time from a path it cannot know
// ("error while loading shared libraries"). The shared libraries a build with
// cgo off embeds live one directory down, in lib/<goos>_<goarch>/shared.
func TestTheCgoLinkDirectoriesHoldNoSharedLibrary(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("lib", "*_*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no lib/<goos>_<goarch> directory: %v", err)
	}
	for _, d := range dirs {
		for _, name := range []string{"libsql_experimental.so", "libsql_experimental.dylib", "libsql_experimental.dll", "sql_experimental.dll"} {
			if _, err := os.Stat(filepath.Join(d, name)); err == nil {
				t.Errorf("%s holds %s beside the archive the cgo build links, so the linker takes the shared library", d, name)
			}
		}
	}
}

// Every shared library the package embeds is under lib/<goos>_<goarch>/shared
// and carries no path of the machine that built it: the module is public, and a
// darwin library's install name is a path the dynamic loader would look up.
func TestTheSharedLibrariesNameNoBuildMachinePath(t *testing.T) {
	found, err := filepath.Glob(filepath.Join("lib", "*_*", "shared", "libsql_experimental.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 4 {
		t.Fatalf("lib/*/shared holds %d shared libraries (%v), want darwin and linux on amd64 and arm64", len(found), found)
	}
	for _, f := range found {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"/Users/", "/home/", "/private/tmp/", "/var/folders/"} {
			if n := bytes.Count(b, []byte(p)); n > 0 {
				t.Errorf("%s holds %d build-machine paths beginning %s", f, n, p)
			}
		}
	}
}
