#!/usr/bin/env bash
# Builds the libSQL C-binding static libraries that native_cgo.go links,
# lib/<goos>_<goarch>/libsql_experimental.a and lib/include/libsql.h, and,
# from the same cargo build (the crate builds a cdylib beside its staticlib),
# the shared libraries a build with cgo off embeds and loads (embed_*.go):
# lib/<goos>_<goarch>/libsql_experimental.dylib or .so.
#
# Consumes: a checkout of github.com/tursodatabase/libsql at LIBSQL_TAG
# (LIBSQL_SRC); rustup, which installs the toolchain pinned by the checkout's
# rust-toolchain.toml; the Xcode command-line tools for the darwin targets;
# docker for the linux and windows targets.
# Consumed by: the #cgo LDFLAGS lines of native_cgo.go
# (-L${SRCDIR}/lib/<goos>_<goarch> -lsql_experimental), and the go:embed line
# and SHA-256 of each embed_<goos>_<goarch>.go, which are rewritten here when
# a shared library is copied (update_embed).
#
# Conditions:
#   - Refuses to build while the data volume has less than MIN_FREE_GIB free,
#     and never runs cargo clean, because the build machine is shared.
#   - Every build-machine path is mapped to a neutral prefix (rustc
#     --remap-path-prefix, C -ffile-prefix-map), because the libraries are
#     committed to a repository other people fetch; `verify` fails when a
#     local path survives in an archive, and also when any home-directory
#     path outside CRATE_BUILD_PATHS does, which is a path of whatever
#     machine produced the file rather than of this one. $HOME's map is given
#     first: rustc and the C compilers apply the last map that matches, and
#     LIBSQL_SRC, CARGO_HOME_DIR and the target directory may all sit under
#     $HOME, where $HOME's map alone would leave /home/<their path> behind.
#   - darwin objects are compiled with MACOSX_DEPLOYMENT_TARGET=11.0 so a
#     worker binary linking them runs on every macOS the worker supports;
#     `verify` fails on any object whose minimum OS is newer.
#   - linux objects are compiled in a Debian bookworm container so a binary
#     linking them needs at most GLIBC_2.34 (a trixie build references
#     GLIBC_2.38+ symbols and fails to load on bookworm).
#   - windows objects are cross-compiled in the same bookworm image with
#     Debian's MinGW-w64 toolchain for x86_64-pc-windows-gnu, the target
#     whose archive a cgo link through a MinGW-w64 gcc accepts; the container
#     paths (/src, /cargo-home, /target) are the only paths it records.
#   - `verify` reads the windows archive with objdump and fails unless every
#     member is coff-x86-64, because a cargo build that loses the
#     --target x86_64-pc-windows-gnu of build_windows writes a linux archive
#     to the same path, which no cgo link through a MinGW-w64 gcc accepts.
#   - `verify` fails the same way on a darwin or linux archive whose
#     architecture is not the one its directory names: the step list hands
#     build_darwin its rust target and its lib directory as two separate
#     words, and build_linux copies the container's native output into
#     lib/linux_arm64, so a swapped pair or a changed docker --platform puts
#     a sound archive of the other architecture in the directory libsql.go's
#     #cgo LDFLAGS line selects for that GOOS/GOARCH, where no link accepts
#     it. The minimum-OS and glibc-symbol checks pass such a file.
#   - Both container steps install make: libsql-ffi's build script compiles
#     SQLite3MultipleCiphers (the encryption feature bindings/c enables)
#     through cmake's Unix Makefiles generator, and the slim image has no
#     make, so cmake stops at "CMAKE_MAKE_PROGRAM is not set".
#   - `strip` removes the LLVM bitcode sections (__LLVM,__bitcode and
#     __LLVM,__cmdline in Mach-O, .llvmbc and .llvmcmd in ELF and COFF) from
#     every archive in place with the Rust toolchain's rust-objcopy (OBJCOPY
#     overrides it). cmd/go (Go 1.27.4 and later) scans every -L directory
#     under the build's working directory, the module cache or the temporary
#     directory, and refuses to link an archive with an LTO section,
#     so without this no consumer that requires go-libsql by version, and no
#     build run from this repository, links on a supported Go. The sections
#     come from the prebuilt standard-library members rustup ships
#     (compiler_builtins, std and their dependencies), which cargo links in
#     without recompiling, so no RUSTFLAGS setting keeps them out. The
#     windows archive also loses its .drectve sections: cmd/go refuses a
#     directive value holding "..", which rustc's -exclude-symbols: entries for
#     legacy-mangled compiler_builtins paths have, and the only other
#     directives (-export: of the libsql_* functions) shape a DLL's export
#     table, which no Go executable linking go-libsql needs.
#   - `verify` links a probe program that opens libsql.DriverName and reads
#     sqlite_version(), with VERIFY_GO (go1.27.4 by default, fetched through
#     GOTOOLCHAIN), in the two layouts cmd/go scans: go-libsql under the
#     build's working directory, and go-libsql from a module cache. darwin
#     targets link and every probe this machine can execute runs; linux and
#     windows targets run cmd/go's admission alone (go build -n scans the -L
#     directory before any C compiler starts), because linking them needs
#     that target's C toolchain. Each run writes a new verify-probe.* folder
#     under WORK.
#   - Every build step compiles LIBSQL_SRC's tree at LIBSQL_TAG with the
#     patches in scripts/libsql-patches/ applied in file-name order, copied
#     to WORK/libsql-<tag>-patched-<digest of the patches> the first time a
#     patch set is used and reused after, because LIBSQL_SRC stays the clean
#     checkout check_tag reads. The patches give a replica on libSQL's HTTP
#     sync path (the one it takes whenever the primary answers /info) the
#     local encryption key it is opened with: without them that replica's
#     database file, WAL and bootstrap download are written in plaintext
#     although libsql_config carries encryption_key.
#   - `verify` fails unless every archive defines libsql_complete and
#     libsql.h declares it: exec.go's statement splitter calls it, and the
#     patches add it.
#   - CONTAINER_PROXY, when set, is handed to both containers as http_proxy
#     and https_proxy, for a build machine whose containers reach Debian's
#     archive and the crate registry only through a proxy; LINUX_IMAGE must
#     then have apt sources that use https.
#   - `encrypted` runs the package tests that open a keyed embedded replica and
#     a keyed offline-writes database of an in-process primary answering the
#     HTTP sync protocol, with VERIFY_GO
#     against the archive of the host's GOOS/GOARCH (the only one this machine
#     runs), and fails unless the tests ran and found each database file
#     without the SQLite header and no file beside it holding a row.
#
#   - `provenance` writes lib/PROVENANCE: LIBSQL_TAG, LIBSQL_COMMIT (the
#     upstream commit the tag names, pinned here because a tag can move), the
#     SHA-256 of the patches concatenated in file-name order, and the SHA-256 of
#     every static archive, every shared library and the header; `verify` fails
#     when any of them differs from the record, and the package's
#     TestTheLibrariesMatchTheirProvenance checks the same on every test run, so
#     a swapped or rebuilt library, or a patch changed without a rebuild that
#     rewrites the record, is seen in the repository. A checkout that holds
#     LIBSQL_COMMIT must have LIBSQL_TAG at it (check_tag); an exported copy,
#     which carries the tree but not upstream's commits, is built and recorded
#     under the pinned commit.
#
# Usage:
#   LIBSQL_SRC=/path/to/libsql scripts/build-libs.sh [step...]
#   steps: header darwin_arm64 darwin_amd64 linux windows strip provenance verify encrypted   (default: all)
set -euo pipefail

LIBSQL_TAG="${LIBSQL_TAG:-libsql-0.9.30}"
LIBSQL_COMMIT="${LIBSQL_COMMIT:-0653c5788d77ef16a97c56ff3e9fdc11717a72d9}"
LIBSQL_SRC="${LIBSQL_SRC:?set LIBSQL_SRC to a tursodatabase/libsql checkout at $LIBSQL_TAG}"
LIBSQL_SRC="$(cd "$LIBSQL_SRC" && pwd -P)"
REPO="$(cd "$(dirname "$0")/.." && pwd -P)"
WORK="${WORK:-${TMPDIR:-/tmp}/go-libsql-build}"
mkdir -p "$WORK"
WORK="$(cd "$WORK" && pwd -P)"
CARGO_HOME_DIR="${CARGO_HOME_DIR:-$WORK/cargo-home}"
mkdir -p "$CARGO_HOME_DIR"
CARGO_HOME_DIR="$(cd "$CARGO_HOME_DIR" && pwd -P)"
MIN_FREE_GIB="${MIN_FREE_GIB:-6}"
DATA_VOLUME="${DATA_VOLUME:-/System/Volumes/Data}"
LINUX_IMAGE="${LINUX_IMAGE:-rust:1.95.0-slim-bookworm}"
JOBS="${JOBS:-4}"
OBJCOPY="${OBJCOPY:-}"
VERIFY_GO="${VERIFY_GO:-go1.27.4}"

# HOME_PATH_RE matches an absolute path under a user's home directory, in both
# spellings an archive's debug and panic strings carry. CRATE_BUILD_PATHS are
# the ones a crate bindings/c depends on ships inside its own prebuilt objects,
# so every archive carries them whoever runs the build: ring's pregenerated
# NASM objects and the toolchain's precompiled compiler_builtins and std.
# Consumed by verify, which fails on a match outside CRATE_BUILD_PATHS: the
# remaps of build_darwin and the /src, /cargo-home and /target of build_linux
# and build_windows leave this machine's home out of every archive they write,
# so such a path means the file came from another machine's build. A remapped
# $HOME keeps the dot of the directory under it (/home/.cargo), which a real
# home path does not have, so the allowance covers the remap target.
HOME_PATH_RE='([A-Za-z]:\\[A-Za-z0-9_.-]+\\[^ "]*|/Users/[A-Za-z0-9_.-]+/[^ "]*|/home/[A-Za-z0-9_.-]+/[^ "]*|/root/[^ "]*)'
CRATE_BUILD_PATHS='^(C:\\Users\\b\\p\\ring\\pregenerated\\|C:\\Users\\runneradmin\\\.cargo\\|/Users/runner/|/home/\.)'

check_tag() {
	local got
	got="$(git -C "$LIBSQL_SRC" describe --tags --exact-match 2>/dev/null || true)"
	if [ "$got" != "$LIBSQL_TAG" ]; then
		echo "LIBSQL_SRC is at '${got:-no tag}', want $LIBSQL_TAG" >&2
		exit 1
	fi
	if [ -n "$(git -C "$LIBSQL_SRC" status --porcelain)" ]; then
		echo "LIBSQL_SRC has local changes; build from a clean checkout" >&2
		exit 1
	fi
	if git -C "$LIBSQL_SRC" cat-file -e "$LIBSQL_COMMIT^{commit}" 2>/dev/null; then
		got="$(git -C "$LIBSQL_SRC" rev-parse "$LIBSQL_TAG^{commit}")"
		if [ "$got" != "$LIBSQL_COMMIT" ]; then
			echo "$LIBSQL_TAG is at $got in LIBSQL_SRC, want $LIBSQL_COMMIT" >&2
			exit 1
		fi
	fi
}

# patched_source prints the directory the build steps compile. A reused
# directory must still hold every patch (git apply -R --check), so a copy an
# interrupted run or a hand edit left behind is refused rather than built.
# GIT_CEILING_DIRECTORIES keeps git apply from treating a WORK that sits
# inside a repository as that repository's tree.
patched_source() {
	local digest dir tmp p
	digest="$(cat "$REPO"/scripts/libsql-patches/*.patch | shasum -a 256 | cut -c1-12)"
	dir="$WORK/libsql-$LIBSQL_TAG-patched-$digest"
	if [ ! -d "$dir" ]; then
		tmp="$dir.partial.$$"
		mkdir -p "$tmp"
		git -C "$LIBSQL_SRC" archive "$LIBSQL_TAG" | tar -x -C "$tmp"
		for p in "$REPO"/scripts/libsql-patches/*.patch; do
			(cd "$tmp" && GIT_CEILING_DIRECTORIES="$(dirname "$tmp")" git apply --whitespace=nowarn "$p") || {
				echo "patch $(basename "$p") does not apply to $LIBSQL_TAG" >&2
				exit 1
			}
		done
		mv "$tmp" "$dir"
	fi
	for p in "$REPO"/scripts/libsql-patches/*.patch; do
		(cd "$dir" && GIT_CEILING_DIRECTORIES="$(dirname "$dir")" git apply -R --check "$p" 2>/dev/null) || {
			echo "$dir does not hold patch $(basename "$p"); move it aside and rerun" >&2
			exit 1
		}
	done
	printf '%s\n' "$dir"
}

check_disk() {
	local free_kib
	[ -d "$DATA_VOLUME" ] || DATA_VOLUME=/
	free_kib="$(df -Pk "$DATA_VOLUME" | awk 'NR==2 {print $4}')"
	if [ "$free_kib" -lt $((MIN_FREE_GIB * 1024 * 1024)) ]; then
		echo "only $((free_kib / 1024 / 1024)) GiB free on $DATA_VOLUME (< $MIN_FREE_GIB); not building" >&2
		exit 1
	fi
}

# build_darwin <rust target> <lib dir name>
build_darwin() {
	local target="$1" out="$2" tdir="$WORK/target-darwin"
	check_disk
	(
		cd "$LIBSQL_SRC/bindings/c"
		export CARGO_HOME="$CARGO_HOME_DIR" CARGO_TARGET_DIR="$tdir"
		export MACOSX_DEPLOYMENT_TARGET=11.0
		export RUSTFLAGS="--remap-path-prefix=$HOME=/home --remap-path-prefix=$LIBSQL_SRC=/libsql --remap-path-prefix=$CARGO_HOME_DIR=/cargo-home --remap-path-prefix=$tdir=/target"
		export CFLAGS="-ffile-prefix-map=$HOME=/home -ffile-prefix-map=$LIBSQL_SRC=/libsql -ffile-prefix-map=$CARGO_HOME_DIR=/cargo-home -ffile-prefix-map=$tdir=/target"
		export CXXFLAGS="$CFLAGS"
		rustup target add "$target" >/dev/null
		cargo build --release --locked --target "$target" -j "$JOBS"
	)
	mkdir -p "$REPO/lib/$out"
	cp "$tdir/$target/release/libsql_experimental.a" "$REPO/lib/$out/libsql_experimental.a"
	cp "$tdir/$target/release/libsql_experimental.dylib" "$REPO/lib/$out/libsql_experimental.dylib"
	update_embed "$out" dylib
}

# update_embed <lib dir name> <extension> sets the SHA-256 that
# embed_<lib dir name>.go names to the shared library just copied, so a build
# with cgo off refuses any other bytes.
update_embed() {
	local out="$1" ext="$2" sum
	sum=$(shasum -a 256 "$REPO/lib/$out/libsql_experimental.$ext" | cut -d' ' -f1)
	perl -pi -e "s/^(\tembeddedSHA256 += \")[0-9a-f]*\"/\${1}$sum\"/" "$REPO/embed_$out.go"
	grep -q "embeddedSHA256  = \"$sum\"" "$REPO/embed_$out.go" || { echo "FAIL embed_$out.go does not name $sum"; return 1; }
}

# Both linux archives come from one arm64 bookworm container: aarch64
# natively and x86_64 through Debian's cross toolchain, so both compile
# against bookworm's glibc headers whatever the host architecture.
build_linux() {
	check_disk
	mkdir -p "$WORK/target-linux"
	docker run --rm --platform linux/arm64 \
		-v "$LIBSQL_SRC":/src -v "$CARGO_HOME_DIR":/cargo-home -v "$WORK/target-linux":/target \
		-e CARGO_HOME=/cargo-home -e CARGO_TARGET_DIR=/target -e JOBS="$JOBS" \
		${CONTAINER_PROXY:+-e http_proxy=$CONTAINER_PROXY -e https_proxy=$CONTAINER_PROXY} \
		"$LINUX_IMAGE" bash -euc '
			apt-get update -qq
			apt-get install -y -qq --no-install-recommends cmake make g++ \
				gcc-x86-64-linux-gnu g++-x86-64-linux-gnu libc6-dev-amd64-cross >/dev/null
			cd /src/bindings/c
			cargo build --release --locked -j "$JOBS"
			rustup target add x86_64-unknown-linux-gnu
			CC_x86_64_unknown_linux_gnu=x86_64-linux-gnu-gcc \
			CXX_x86_64_unknown_linux_gnu=x86_64-linux-gnu-g++ \
			CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_LINKER=x86_64-linux-gnu-gcc \
				cargo build --release --locked -j "$JOBS" --target x86_64-unknown-linux-gnu
		'
	mkdir -p "$REPO/lib/linux_arm64" "$REPO/lib/linux_amd64"
	cp "$WORK/target-linux/release/libsql_experimental.a" "$REPO/lib/linux_arm64/"
	cp "$WORK/target-linux/x86_64-unknown-linux-gnu/release/libsql_experimental.a" "$REPO/lib/linux_amd64/"
	cp "$WORK/target-linux/release/libsql_experimental.so" "$REPO/lib/linux_arm64/"
	cp "$WORK/target-linux/x86_64-unknown-linux-gnu/release/libsql_experimental.so" "$REPO/lib/linux_amd64/"
	update_embed linux_arm64 so
	update_embed linux_amd64 so
}

# The windows_amd64 archive comes from the linux image with Debian's
# MinGW-w64 cross toolchain, so no Windows host is needed. The crate also
# builds a cdylib, which needs the MinGW linker even though only the static
# archive is kept.
build_windows() {
	check_disk
	mkdir -p "$WORK/target-windows"
	docker run --rm --platform linux/arm64 \
		-v "$LIBSQL_SRC":/src -v "$CARGO_HOME_DIR":/cargo-home -v "$WORK/target-windows":/target \
		-e CARGO_HOME=/cargo-home -e CARGO_TARGET_DIR=/target -e JOBS="$JOBS" \
		${CONTAINER_PROXY:+-e http_proxy=$CONTAINER_PROXY -e https_proxy=$CONTAINER_PROXY} \
		"$LINUX_IMAGE" bash -euc '
			apt-get update -qq
			apt-get install -y -qq --no-install-recommends cmake make \
				gcc-mingw-w64-x86-64 g++-mingw-w64-x86-64 >/dev/null
			cd /src/bindings/c
			rustup target add x86_64-pc-windows-gnu
			CC_x86_64_pc_windows_gnu=x86_64-w64-mingw32-gcc \
			CXX_x86_64_pc_windows_gnu=x86_64-w64-mingw32-g++ \
			AR_x86_64_pc_windows_gnu=x86_64-w64-mingw32-ar \
			CARGO_TARGET_X86_64_PC_WINDOWS_GNU_LINKER=x86_64-w64-mingw32-gcc \
				cargo build --release --locked -j "$JOBS" --target x86_64-pc-windows-gnu
		'
	mkdir -p "$REPO/lib/windows_amd64"
	cp "$WORK/target-windows/x86_64-pc-windows-gnu/release/libsql_experimental.a" "$REPO/lib/windows_amd64/"
}

copy_header() {
	mkdir -p "$REPO/lib/include"
	cp "$LIBSQL_SRC/bindings/c/include/libsql.h" "$REPO/lib/include/libsql.h"
}

# find_objcopy sets OBJCOPY to the rust-objcopy (an llvm-objcopy, which reads
# Mach-O, ELF and COFF archives alike) every Rust toolchain ships in its
# rustlib, unless OBJCOPY names another one.
find_objcopy() {
	local host
	[ -n "$OBJCOPY" ] && return 0
	host="$(rustc -vV | sed -n 's/^host: //p')"
	OBJCOPY="$(rustc --print sysroot)/lib/rustlib/$host/bin/rust-objcopy"
	if [ ! -x "$OBJCOPY" ]; then
		echo "no rust-objcopy at $OBJCOPY; set OBJCOPY to an llvm-objcopy" >&2
		exit 1
	fi
}

# strip_bitcode rewrites each archive in place without its bitcode sections
# (and the windows archive without its .drectve sections); llvm-objcopy keeps
# every other section, symbol and the archive's index.
strip_bitcode() {
	local a
	find_objcopy
	for a in "$REPO"/lib/*/libsql_experimental.a; do
		case "$a" in
		*/darwin_*)
			"$OBJCOPY" --remove-section=__LLVM,__bitcode --remove-section=__LLVM,__cmdline "$a"
			;;
		*/windows_*)
			"$OBJCOPY" --remove-section=.llvmbc --remove-section=.llvmcmd --remove-section=.drectve "$a"
			;;
		*)
			"$OBJCOPY" --remove-section=.llvmbc --remove-section=.llvmcmd "$a"
			;;
		esac
		echo "ok   $a: bitcode sections removed"
	done
}

# recorded_libs prints every library and header lib/PROVENANCE records, in the
# order it records them: the static archives, the shared libraries, the header.
recorded_libs() {
	local a
	for a in "$REPO"/lib/*/libsql_experimental.a "$REPO"/lib/*/libsql_experimental.dylib "$REPO"/lib/*/libsql_experimental.so "$REPO"/lib/include/libsql.h; do
		[ -f "$a" ] && echo "$a"
	done
}

# provenance writes lib/PROVENANCE (see the conditions above) from the files
# the build steps left under lib/.
provenance() {
	local a
	{
		echo '# The libSQL C-binding libraries under lib/ and the source they are built from:'
		echo '# libSQL at libsqlTag, which is upstream commit libsqlCommit, with the patches in'
		echo '# scripts/libsql-patches/ applied in file-name order (patchSetSHA256 is the SHA-256'
		echo '# of their bytes concatenated in that order), and the SHA-256 of every static'
		echo '# archive, every shared library and the header. scripts/build-libs.sh writes it'
		echo '# (step provenance) and checks it (step verify); TestTheLibrariesMatchTheirProvenance'
		echo '# checks it on every test run, so a library, the header or a patch that changes'
		echo '# without a rebuild that rewrites this record fails there. One "key value" or'
		echo '# "sha256 path value" per line.'
		echo "libsqlTag $LIBSQL_TAG"
		echo "libsqlCommit $LIBSQL_COMMIT"
		echo "patchSetSHA256 $(cat "$REPO"/scripts/libsql-patches/*.patch | shasum -a 256 | cut -d' ' -f1)"
		recorded_libs | while read -r a; do
			echo "sha256 ${a#"$REPO"/} $(shasum -a 256 "$a" | cut -d' ' -f1)"
		done
	} > "$REPO/lib/PROVENANCE"
}

# check_provenance fails when a library, the header or the patch set differs
# from what lib/PROVENANCE records.
check_provenance() {
	local fail=0 rec="$REPO/lib/PROVENANCE" a want got
	if [ ! -f "$rec" ]; then
		echo "FAIL lib/PROVENANCE is missing" >&2
		return 1
	fi
	for a in $(recorded_libs); do
		want="$(awk -v p="${a#"$REPO"/}" '$1 == "sha256" && $2 == p { print $3 }' "$rec")"
		got="$(shasum -a 256 "$a" | cut -d' ' -f1)"
		if [ "$want" != "$got" ]; then
			echo "FAIL ${a#"$REPO"/} hashes to $got, lib/PROVENANCE records ${want:-nothing}" >&2
			fail=1
		fi
	done
	want="$(awk '$1 == "patchSetSHA256" { print $2 }' "$rec")"
	got="$(cat "$REPO"/scripts/libsql-patches/*.patch | shasum -a 256 | cut -d' ' -f1)"
	if [ "$want" != "$got" ]; then
		echo "FAIL the patches hash to $got, lib/PROVENANCE records ${want:-nothing}" >&2
		fail=1
	fi
	return "$fail"
}

verify() {
	local fail=0 a n minos members coff foreign want archs matched
	for a in "$REPO"/lib/*/libsql_experimental.a; do
		for p in "$LIBSQL_SRC" "$CARGO_HOME_DIR" "$WORK" "$HOME" /private/tmp; do
			n="$(strings -a "$a" | grep -c -F "$p" || true)"
			if [ "$n" != 0 ]; then
				echo "FAIL $a: $n strings contain $p" >&2
				fail=1
			fi
		done
		foreign="$(strings -a "$a" | grep -oE "$HOME_PATH_RE" | grep -vE "$CRATE_BUILD_PATHS" | sort -u || true)"
		if [ -n "$foreign" ]; then
			echo "FAIL $a: $(printf '%s\n' "$foreign" | wc -l | tr -d ' ') home-directory paths of another machine, first $(printf '%s\n' "$foreign" | head -1)" >&2
			fail=1
		fi
		case "$a" in
		*/darwin_*)
			minos="$(otool -l "$a" | awk '
				$1 == "cmd" { vmin = ($2 == "LC_VERSION_MIN_MACOSX") }
				$1 == "minos" { print $2 }
				$1 == "version" && vmin { print $2 }
			' | sort -uV | tail -1)"
			if [ -n "$minos" ] && [ "$(printf '%s\n11.0\n' "$minos" | sort -V | tail -1)" != 11.0 ]; then
				echo "FAIL $a: newest object minimum macOS is $minos (> 11.0)" >&2
				fail=1
			fi
			echo "ok   $a: minimum macOS ${minos:-unknown}"
			case "$a" in
			*/darwin_arm64/*) want=arm64 ;;
			*) want=x86_64 ;;
			esac
			archs="$(lipo -archs "$a" 2>/dev/null || true)"
			if [ "$archs" != "$want" ]; then
				echo "FAIL $a: lipo reads '${archs:-no Mach-O architecture}', want $want" >&2
				fail=1
			else
				echo "ok   $a: $want"
			fi
			;;
		*/linux_*)
			if nm -u "$a" 2>/dev/null | grep -q -e '__isoc23_' ||
				nm -u "$a" 2>/dev/null | grep -q -w -E 'arc4random|arc4random_buf|arc4random_uniform|strlcpy|strlcat'; then
				echo "FAIL $a: references a glibc symbol newer than 2.34" >&2
				fail=1
			fi
			echo "ok   $a: no glibc > 2.34 symbol references"
			case "$a" in
			*/linux_arm64/*) want=elf64-littleaarch64 ;;
			*) want=elf64-x86-64 ;;
			esac
			members="$(objdump -f "$a" 2>/dev/null | grep -c 'file format' || true)"
			matched="$(objdump -f "$a" 2>/dev/null | grep -c "file format $want" || true)"
			if [ "$members" = 0 ] || [ "$members" != "$matched" ]; then
				echo "FAIL $a: $matched of $members members are $want" >&2
				fail=1
			else
				echo "ok   $a: $matched members, every one $want"
			fi
			;;
		*/windows_*)
			members="$(objdump -f "$a" 2>/dev/null | grep -c 'file format' || true)"
			coff="$(objdump -f "$a" 2>/dev/null | grep -c 'file format coff-x86-64' || true)"
			if [ "$members" = 0 ] || [ "$members" != "$coff" ]; then
				echo "FAIL $a: $coff of $members members are coff-x86-64" >&2
				fail=1
			else
				echo "ok   $a: $coff members, every one coff-x86-64"
			fi
			;;
		esac
	done
	for a in "$REPO"/lib/*/libsql_experimental.a; do
		if nm -g "$a" 2>/dev/null | grep -E ' T _?libsql_complete$' >/dev/null; then
			echo "ok   $a: defines libsql_complete"
		else
			echo "FAIL $a: does not define libsql_complete" >&2
			fail=1
		fi
	done
	if ! grep -q 'libsql_complete' "$REPO/lib/include/libsql.h"; then
		echo "FAIL lib/include/libsql.h does not declare libsql_complete" >&2
		fail=1
	fi
	if ! cmp -s "$LIBSQL_SRC/bindings/c/include/libsql.h" "$REPO/lib/include/libsql.h"; then
		echo "FAIL lib/include/libsql.h differs from $LIBSQL_TAG" >&2
		fail=1
	fi
	check_provenance || fail=1
	verify_probe || fail=1
	return "$fail"
}

# probe_source writes into directory $1 the program verify_probe links: it
# opens an in-memory database through libsql.DriverName and prints
# sqlite_version(), so a probe that runs has called into the archive.
probe_source() {
	cat >"$1/main.go" <<'EOF'
package main

import (
	"database/sql"
	"fmt"
	"os"

	libsql "github.com/bbmumford/go-libsql"
)

func main() {
	db, err := sql.Open(libsql.DriverName, ":memory:")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	var v string
	if err := db.QueryRow("select sqlite_version()").Scan(&v); err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	fmt.Println(v)
}
EOF
}

# mkzip_source writes into directory $1 a program that zips the checkout
# os.Args[1] as the module zip os.Args[3], every path under the
# module@version prefix os.Args[2], leaving out dot files and directories and
# nested modules as the module proxy does; verify_probe serves the zip from a
# file proxy for the module-cache layout.
mkzip_source() {
	cat >"$1/mkzip.go" <<'EOF'
package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root, prefix, out := os.Args[1], os.Args[2], os.Args[3]
	f, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	zw := zip.NewWriter(f)
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		w, err := zw.Create(prefix + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		panic(err)
	}
}
EOF
}

# runs_here reports whether this machine executes a darwin binary built for
# GOARCH $1: natively, or an amd64 one through Rosetta on arm64.
runs_here() {
	case "$(uname -m)/$1" in
	arm64/arm64 | x86_64/amd64) return 0 ;;
	arm64/amd64) arch -x86_64 /usr/bin/true 2>/dev/null ;;
	*) return 1 ;;
	esac
}

# verify_probe builds the probe for every lib/ target with VERIFY_GO, twice:
# "build-dir" builds from $REPO itself, so the -L directory libsql.go's #cgo
# LDFLAGS line names is under the working directory (the probe module comes
# in through a go.work and replaces go-libsql with $REPO); "module-cache"
# serves this checkout as version v0.0.0-verify from a file proxy into a new
# GOMODCACHE, as a consumer requiring go-libsql by version receives it
# (GONOPROXY=none, because a GOPRIVATE naming github.com/bbmumford would send
# the fetch past the file proxy to the repository).
# Either refusal is a FAIL naming the target, the layout and cmd/go's reason.
verify_probe() {
	local goroot gob pv proxy goproxy a t goos goarch layout out n rc got fail=0
	local v=v0.0.0-verify
	goroot="$(GOTOOLCHAIN="$VERIFY_GO" go env GOROOT)" || {
		echo "FAIL cannot run $VERIFY_GO through GOTOOLCHAIN" >&2
		return 1
	}
	gob="$goroot/bin/go"
	echo "ok   probe toolchain $("$gob" env GOVERSION)"
	# The module-cache layout's GOMODCACHE starts empty, so go-libsql's own
	# requirements are fetched: from this machine's module cache first, so a
	# network outage does not read as a refused archive, then GOPROXY.
	goproxy="file://$("$gob" env GOMODCACHE)/cache/download,$("$gob" env GOPROXY)"
	pv="$(mktemp -d "$WORK/verify-probe.XXXXXX")"
	proxy="$pv/proxy/github.com/bbmumford/go-libsql/@v"
	mkdir -p "$pv/build-dir" "$pv/module-cache" "$pv/modcache" "$proxy"
	for layout in build-dir module-cache; do
		probe_source "$pv/$layout"
		cp "$REPO/go.sum" "$pv/$layout/go.sum"
	done
	printf 'module verifyprobe\n\ngo 1.20\n\nrequire github.com/bbmumford/go-libsql v0.0.0\n\nreplace github.com/bbmumford/go-libsql => %s\n' "$REPO" >"$pv/build-dir/go.mod"
	printf 'go 1.20\n\nuse ./build-dir\n' >"$pv/go.work"
	printf 'module verifyprobe\n\ngo 1.20\n\nrequire github.com/bbmumford/go-libsql %s\n' "$v" >"$pv/module-cache/go.mod"
	mkzip_source "$pv"
	(cd "$pv" && GOTOOLCHAIN=local GOWORK=off GOFLAGS= "$gob" run ./mkzip.go "$REPO" "github.com/bbmumford/go-libsql@$v" "$proxy/$v.zip")
	cp "$REPO/go.mod" "$proxy/$v.mod"
	printf '{"Version":"%s"}\n' "$v" >"$proxy/$v.info"
	echo "$v" >"$proxy/list"
	for a in "$REPO"/lib/*/libsql_experimental.a; do
		t="$(basename "$(dirname "$a")")"
		goos="${t%_*}"
		goarch="${t#*_}"
		# A darwin probe links with the host's clang; any other target's link
		# needs its own C toolchain, so -n stops after cmd/go's admission.
		n=""
		[ "$goos" = darwin ] || n="-n"
		for layout in build-dir module-cache; do
			out="$pv/probe-$layout-$t"
			rc=0
			if [ "$layout" = build-dir ]; then
				(cd "$REPO" && GOTOOLCHAIN=local GOWORK="$pv/go.work" GOFLAGS= \
					CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" \
					"$gob" build $n -o "$out" verifyprobe) >"$out.log" 2>&1 || rc=$?
			else
				(cd "$pv/module-cache" && GOTOOLCHAIN=local GOWORK=off GOFLAGS=-mod=mod \
					GOMODCACHE="$pv/modcache" GOPROXY="file://$pv/proxy,$goproxy" \
					GONOPROXY=none GONOSUMDB=github.com/bbmumford/go-libsql \
					CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" \
					"$gob" build $n -o "$out" .) >"$out.log" 2>&1 || rc=$?
			fi
			if [ "$rc" != 0 ]; then
				# -n prints every command it would run, so the reason is
				# cmd/go's own refusal line, else the log's last line.
				echo "FAIL $a: $VERIFY_GO refused the $layout probe: $(grep -m1 -E ': (disallowed|invalid) ' "$out.log" || tail -1 "$out.log")" >&2
				fail=1
			elif [ -n "$n" ]; then
				echo "ok   $a: $VERIFY_GO admits it in the $layout layout (no $goos C toolchain here to link)"
			elif runs_here "$goarch"; then
				if got="$("$out" 2>&1)"; then
					echo "ok   $a: the $layout probe links and runs, sqlite $got"
				else
					echo "FAIL $a: the $layout probe links but fails: $got" >&2
					fail=1
				fi
			else
				echo "ok   $a: the $layout probe links (this machine cannot run $goarch)"
			fi
		done
	done
	return "$fail"
}

# verify_encrypted fails closed: a test that did not run (renamed, skipped
# without cgo) is a failure, not a pass.
verify_encrypted() {
	local out t
	out="$(cd "$REPO" && GOTOOLCHAIN="$VERIFY_GO" GOWORK=off GOFLAGS= CGO_ENABLED=1 \
		go test -count=1 -v -run '^(TestKeyedReplicaOnHTTPSyncIsEncryptedAtRest|TestKeyedSyncedDatabaseOnHTTPSyncIsEncryptedAtRest|TestUnkeyedReplicaOnHTTPSyncIsPlaintext)$' . 2>&1)" || {
		printf '%s\n' "$out" >&2
		echo "FAIL a keyed replica on the HTTP sync path is not encrypted at rest" >&2
		return 1
	}
	for t in TestKeyedReplicaOnHTTPSyncIsEncryptedAtRest TestKeyedSyncedDatabaseOnHTTPSyncIsEncryptedAtRest TestUnkeyedReplicaOnHTTPSyncIsPlaintext; do
		if ! printf '%s\n' "$out" | grep -q -F -- "--- PASS: $t ("; then
			printf '%s\n' "$out" >&2
			echo "FAIL $t did not run" >&2
			return 1
		fi
	done
	echo "ok keyed replica on the HTTP sync path is encrypted at rest"
}

steps=("$@")
[ "${#steps[@]}" -gt 0 ] || steps=(header darwin_arm64 darwin_amd64 linux windows strip provenance verify encrypted)
check_tag
LIBSQL_SRC="$(patched_source)"
for s in "${steps[@]}"; do
	case "$s" in
	header) copy_header ;;
	darwin_arm64) build_darwin aarch64-apple-darwin darwin_arm64 ;;
	darwin_amd64) build_darwin x86_64-apple-darwin darwin_amd64 ;;
	linux) build_linux ;;
	windows) build_windows ;;
	strip) strip_bitcode ;;
	provenance) provenance ;;
	verify) verify ;;
	encrypted) verify_encrypted ;;
	*)
		echo "unknown step $s" >&2
		exit 2
		;;
	esac
done
