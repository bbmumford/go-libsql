#!/usr/bin/env bash
# Builds the libSQL C-binding static libraries that libsql.go links:
# lib/<goos>_<goarch>/libsql_experimental.a and lib/include/libsql.h.
#
# Consumes: a checkout of github.com/tursodatabase/libsql at LIBSQL_TAG
# (LIBSQL_SRC); rustup, which installs the toolchain pinned by the checkout's
# rust-toolchain.toml; the Xcode command-line tools for the darwin targets;
# docker for the linux and windows targets.
# Consumed by: the #cgo LDFLAGS lines of libsql.go
# (-L${SRCDIR}/lib/<goos>_<goarch> -lsql_experimental).
#
# Conditions:
#   - Refuses to build while the data volume has less than MIN_FREE_GIB free,
#     and never runs cargo clean, because the build machine is shared.
#   - Every build-machine path is mapped to a neutral prefix (rustc
#     --remap-path-prefix, C -ffile-prefix-map), because the libraries are
#     committed to a repository other people fetch; `verify` fails when a
#     local path survives in an archive.
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
#   - Both container steps install make: libsql-ffi's build script compiles
#     SQLite3MultipleCiphers (the encryption feature bindings/c enables)
#     through cmake's Unix Makefiles generator, and the slim image has no
#     make, so cmake stops at "CMAKE_MAKE_PROGRAM is not set".
#
# Usage:
#   LIBSQL_SRC=/path/to/libsql scripts/build-libs.sh [step...]
#   steps: header darwin_arm64 darwin_amd64 linux windows verify   (default: all)
set -euo pipefail

LIBSQL_TAG="${LIBSQL_TAG:-libsql-0.9.30}"
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
		export RUSTFLAGS="--remap-path-prefix=$LIBSQL_SRC=/libsql --remap-path-prefix=$CARGO_HOME_DIR=/cargo-home --remap-path-prefix=$tdir=/target --remap-path-prefix=$HOME=/home"
		export CFLAGS="-ffile-prefix-map=$LIBSQL_SRC=/libsql -ffile-prefix-map=$CARGO_HOME_DIR=/cargo-home -ffile-prefix-map=$tdir=/target -ffile-prefix-map=$HOME=/home"
		export CXXFLAGS="$CFLAGS"
		rustup target add "$target" >/dev/null
		cargo build --release --locked --target "$target" -j "$JOBS"
	)
	mkdir -p "$REPO/lib/$out"
	cp "$tdir/$target/release/libsql_experimental.a" "$REPO/lib/$out/libsql_experimental.a"
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

verify() {
	local fail=0 a n minos
	for a in "$REPO"/lib/*/libsql_experimental.a; do
		for p in "$LIBSQL_SRC" "$CARGO_HOME_DIR" "$WORK" "$HOME" /private/tmp; do
			n="$(strings -a "$a" | grep -c -F "$p" || true)"
			if [ "$n" != 0 ]; then
				echo "FAIL $a: $n strings contain $p" >&2
				fail=1
			fi
		done
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
			;;
		*/linux_*)
			if nm -u "$a" 2>/dev/null | grep -q -e '__isoc23_' ||
				nm -u "$a" 2>/dev/null | grep -q -w -E 'arc4random|arc4random_buf|arc4random_uniform|strlcpy|strlcat'; then
				echo "FAIL $a: references a glibc symbol newer than 2.34" >&2
				fail=1
			fi
			echo "ok   $a: no glibc > 2.34 symbol references"
			;;
		esac
	done
	if ! cmp -s "$LIBSQL_SRC/bindings/c/include/libsql.h" "$REPO/lib/include/libsql.h"; then
		echo "FAIL lib/include/libsql.h differs from $LIBSQL_TAG" >&2
		fail=1
	fi
	return "$fail"
}

steps=("$@")
[ "${#steps[@]}" -gt 0 ] || steps=(header darwin_arm64 darwin_amd64 linux windows verify)
check_tag
for s in "${steps[@]}"; do
	case "$s" in
	header) copy_header ;;
	darwin_arm64) build_darwin aarch64-apple-darwin darwin_arm64 ;;
	darwin_amd64) build_darwin x86_64-apple-darwin darwin_amd64 ;;
	linux) build_linux ;;
	windows) build_windows ;;
	verify) verify ;;
	*)
		echo "unknown step $s" >&2
		exit 2
		;;
	esac
done
