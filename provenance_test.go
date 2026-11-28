package libsql

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The static archives and shared libraries under lib/, the header and the
// libSQL patches match the provenance record beside them (lib/PROVENANCE):
// every library the record names hashes to the value it records, no library
// under lib/ goes unrecorded,
// and the patches hash to the record's patch-set digest. An archive swapped,
// rebuilt or patched without a rebuild that rewrites the record fails here.
func TestTheLibrariesMatchTheirProvenance(t *testing.T) {
	f, err := os.Open(filepath.Join("lib", "PROVENANCE"))
	if err != nil {
		t.Fatalf("the provenance record: %v", err)
	}
	defer f.Close()
	fields := map[string]string{}
	recorded := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		switch {
		case len(parts) == 3 && parts[0] == "sha256":
			recorded[parts[1]] = parts[2]
		case len(parts) == 2:
			fields[parts[0]] = parts[1]
		default:
			t.Fatalf("the provenance record holds a line it does not define: %q", line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"libsqlTag", "libsqlCommit", "patchSetSHA256"} {
		if fields[key] == "" {
			t.Errorf("the provenance record names no %s", key)
		}
	}
	if len(fields["libsqlCommit"]) != 40 {
		t.Errorf("libsqlCommit %q is not a full commit id", fields["libsqlCommit"])
	}
	sum := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	}
	var archives []string
	for _, pattern := range []string{"*/libsql_experimental.a", "*/shared/libsql_experimental.dylib", "*/shared/libsql_experimental.so"} {
		found, err := filepath.Glob(filepath.Join("lib", pattern))
		if err != nil {
			t.Fatal(err)
		}
		archives = append(archives, found...)
	}
	for _, a := range append(archives, filepath.Join("lib", "include", "libsql.h")) {
		key := filepath.ToSlash(a)
		want, ok := recorded[key]
		if !ok {
			t.Errorf("%s is not in the provenance record", key)
			continue
		}
		if got := sum(a); got != want {
			t.Errorf("%s hashes to %s, the record says %s", key, got, want)
		}
		delete(recorded, key)
	}
	for missing := range recorded {
		t.Errorf("the provenance record names %s, which is not under lib/", missing)
	}
	patches, err := filepath.Glob(filepath.Join("scripts", "libsql-patches", "*.patch"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(patches)
	h := sha256.New()
	for _, p := range patches {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		h.Write(b)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != fields["patchSetSHA256"] {
		t.Errorf("the patches hash to %s, the record says %s", got, fields["patchSetSHA256"])
	}
}
