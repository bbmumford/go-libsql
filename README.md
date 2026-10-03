# LibSQL package for Go

[libSQL](https://github.com/tursodatabase/libsql) is an open source, open contribution fork of SQLite.
This source repository contains libSQL API bindings for Go.

## Notice
This package comes with precompiled native libraries under `lib/`.
`linux amd64`, `linux arm64`, `darwin amd64`, `darwin arm64` and `windows amd64` are built from
libSQL 0.9.30 by `scripts/build-libs.sh` (darwin for macOS 11.0 and later; linux on Debian bookworm,
so a binary linked on bookworm needs at most GLIBC_2.34; windows for `x86_64-pc-windows-gnu` with
Debian bookworm's MinGW-w64 toolchain, gcc 12, so a Windows binary links it through a MinGW-w64 gcc).

## Features

* In-memory databases and local database files, like SQLite
* Remote database access to libSQL server
* In-app replica that syncs with a libSQL server

## Installing

```
go get github.com/bbmumford/go-libsql
```

`go-libsql` uses `CGO` to make calls to LibSQL. You must build your binaries with `CGO_ENABLED=1`.

The `database/sql` driver is registered as `libsql-cgo` (`libsql.DriverName`), so a binary can also
link `github.com/tursodatabase/libsql-client-go`, which registers `libsql`.

## Getting Started

### Connecting to the database

To connect to the database one needs to create a `libsql.Connector` using one of the factory functions: `libsql.NewEmbeddedReplicaConnector` or `libsql.NewEmbeddedReplicaConnectorWithAutoSync`.

Here's an example of obtaining a `sql.DB` object from `database/sql` package:

```
dbPath := // Path do db file on local disk
primaryUrl := // URL to primary database instance
connector := NewEmbeddedReplicaConnector(dbPath, primaryUrl, authToken)
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

If the connector is created with `libsql.NewEmbeddedReplicaConnectorWithAutoSync` then it will automatically fetch updates from a primary periodically.

For connectors created with `libsql.NewEmbeddedReplicaConnector` we need to fetch updates manually by calling `connector.Sync`

## Examples

Module with usage examples can be found in [example directory].

## License

This project is licensed under the [MIT license].

### Contribution

Unless you explicitly state otherwise, any contribution intentionally submitted
for inclusion in libSQL by you, shall be licensed as MIT, without any additional
terms or conditions.

[MIT license]: https://github.com/tursodatabase/go-libsql/blob/main/LICENSE
[example directory]: https://github.com/tursodatabase/go-libsql/tree/main/example
