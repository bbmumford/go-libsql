# go-libsql

go-libsql is a Go `database/sql` driver for [libSQL](https://github.com/tursodatabase/libsql),
the open-source fork of SQLite: local database files, remote access to a libSQL server, and
embedded replicas that keep a local file in step with a libSQL primary. It is a fork of
[tursodatabase/go-libsql](https://github.com/tursodatabase/go-libsql), kept here with its own
build of the native libraries and these additions: the driver registers as `libsql-cgo`
(`DriverName`), `NewLocalConnector` opens and encrypts local files, replicas take
`WithEncryption`, `WithReadYourWrites`, `WithImmediateTransactions`, `WithRawText` and
`WithReadOnly` (a replica connector that refuses every statement it would send to the primary),
and `NewLocalConnector` refuses to open, under a key, a file that begins with the plaintext
SQLite header.

## Notice
This package comes with precompiled native libraries under `lib/`.
`linux amd64`, `linux arm64`, `darwin amd64`, `darwin arm64` and `windows amd64` are built from
libSQL 0.9.30 by `scripts/build-libs.sh` (darwin for macOS 11.0 and later; linux on Debian bookworm,
so a binary linked on bookworm needs at most GLIBC_2.34; windows for `x86_64-pc-windows-gnu` with
Debian bookworm's MinGW-w64 toolchain, gcc 12, so a Windows binary links it through a MinGW-w64 gcc).
The script's `strip` step removes the LLVM bitcode sections Rust's standard library brings into each
archive, and the windows archive's `.drectve` linker directives (DLL export lists, some of which Go
refuses), because Go 1.27.4 and later refuse to link an archive carrying them from the module cache or
from under the build directory; its `verify` step links a probe with Go 1.27.4 in both of those layouts.
The script builds libSQL with the patches in `scripts/libsql-patches`: a replica on libSQL's HTTP sync
path (the one it takes whenever the primary answers `/info`) is encrypted at rest with the key it is opened
with (its database file, its WAL and the file downloaded at bootstrap), an offline-writes database pushes
its pages unencrypted, and the C binding exports `libsql_complete`, which the statement splitter calls.
Its `encrypted` step runs the tests that open keyed databases of an in-process HTTP sync primary.

## Features

* In-memory databases and local database files, like SQLite
* Remote database access to libSQL server
* In-app replica that syncs with a libSQL server

## Installing

```
go get github.com/bbmumford/go-libsql
```

`go-libsql` calls libSQL's C binding. Built with cgo (`CGO_ENABLED=1`) it links the binding's static
library for the target. Built with cgo off on darwin or linux (amd64 and arm64) it loads the same
binding as a shared library embedded in the binary, through purego: the library is written once to
the user's cache directory, its SHA-256 is checked again just before it is loaded
(`ErrLibraryDigest`), and every symbol bound from it must resolve from that file and from no other
image, such as the system's SQLite (`ErrForeignSymbol`). On any other target a cgo-free build opens
nothing (`ErrNoLibrary`).

The `database/sql` driver is registered as `libsql-cgo` (`libsql.DriverName`), so a binary can also
link `github.com/tursodatabase/libsql-client-go`, which registers `libsql`.

## Getting Started

### Connecting to the database

To connect to the database one creates a `libsql.Connector` with `libsql.NewEmbeddedReplicaConnector(dbPath, primaryUrl, opts...)`.

Here's an example of obtaining a `sql.DB` object from `database/sql` package:

```
dbPath := // Path do db file on local disk
primaryUrl := // URL to primary database instance
connector, err := libsql.NewEmbeddedReplicaConnector(dbPath, primaryUrl, libsql.WithAuthToken(authToken))
if err != nil {
    return err
}
db := sql.OpenDB(connector)
defer db.Close()
```

Once `sql.DB` object is created one can use it as any other database that supports `database/sql` package.

Options on the replica connectors and `NewLocalConnector`:

* `WithImmediateTransactions(true)`: `BeginTx` opens transactions that can write with `BEGIN IMMEDIATE`.
* `WithRawText(true)`: TEXT values are returned as strings, never converted to `time.Time`.

### Local database files

`libsql.NewLocalConnector(path, opts...)` opens a file with no primary. `WithKey` (32 bytes) or
`WithHexKey` encrypts it with the passphrase base64(key), the passphrase a replica gets from
`WithEncryption(base64(key))`; `WithBusyTimeout` (5 s by default) and `WithQueryOnly` set the matching
PRAGMAs on every connection.

### Statements and arguments

`Exec` runs every statement of a multi-statement string in order and stops at the first error.
Arguments bind by position; named arguments (`sql.Named`) are refused.

### Fetching updates from primary database instance

A connector created with `libsql.WithSyncInterval(d)` fetches updates from the primary every `d`.

Without it, fetch updates by calling `connector.Sync()`.

## Examples

A module with usage examples is in the [example directory] (`example/`, module
`github.com/tursodatabase/go-libsql/example`).

## Dependencies

- No other package of this estate.
- `github.com/libsql/sqlite-antlr4-parser` and `github.com/antlr4-go/antlr/v4`, which split a
  multi-statement string into statements.
- The libSQL native library, precompiled under `lib/` (see Notice).

## Used by

Importers in the ORBTR estate:

- `github.com/bbmumford/tursoraft/replica`: every shared database's encrypted embedded replica.
- `github.com/ORBTR/worker/internal/convdb` (and the worker's `agents`, `conversation` and
  `scheduler` tests): the worker's conversation store.

## Platforms and build

Built with cgo, a C toolchain for the target links the precompiled static libraries, which cover
linux amd64 and arm64, darwin amd64 and arm64, and windows amd64 (see Notice). Built with cgo off,
the embedded shared libraries cover darwin and linux on amd64 and arm64
(`lib/<goos>_<goarch>/shared/libsql_experimental.dylib` or `.so`, one directory below the archives so
the cgo build's `-L` never finds them, built by `scripts/build-libs.sh` in the same
cargo build as the static libraries, with the same patches; each `embed_<goos>_<goarch>.go` names
its SHA-256). Only `native_cgo.go` and `native_purego.go` differ between the two builds, and the
whole test suite runs in both. The module's `go` directive is 1.20.

## Licence

MIT, upstream's licence, kept unchanged in [LICENSE](LICENSE): Copyright (c) 2017 Joshua Wise and
Copyright (c) 2023 Pekka Enberg.

Unless you explicitly state otherwise, any contribution intentionally submitted
for inclusion in libSQL by you, shall be licensed as MIT, without any additional
terms or conditions.

## Contributing

[AGENTS.md](AGENTS.md) holds the repository's two rules, for people and coding agents alike: no
temporary documents in the repository, and conventional commit messages that stand on their own.
[CLAUDE.md](CLAUDE.md) imports it for Claude Code.

[example directory]: example
