//go:build !cgo

package libsql

import (
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The native layer over the libSQL shared library, loaded once on first use
// (loader.go) and called through purego, for a build with cgo off
// (native.go). Every function the library binds is resolved and checked to
// come from that library before any is called (loadLibrary). A build whose
// library did not load fails every open with the loader's error.

// cReplicated is replicated in libsql.h.
type cReplicated struct {
	frameNo, framesSynced int32
}

// cConfig is libsql_config in libsql.h, field for field: Go lays these types
// out with the padding the C compiler gives them on the 64-bit targets the
// library is built for.
type cConfig struct {
	dbPath              *byte
	primaryURL          *byte
	authToken           *byte
	readYourWrites      int8
	encryptionKey       *byte
	syncInterval        int32
	withWebpki          int8
	offline             int8
	remoteEncryptionKey *byte
}

// cBlob is blob in libsql.h.
type cBlob struct {
	ptr unsafe.Pointer
	len int32
}

// functions are the C binding's calls as purego binds them, named as in
// libsql.h. An out-parameter for a handle or a message is a pointer to an
// unsafe.Pointer the library fills.
type functions struct {
	openFile                       func(url string, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	openRemote                     func(url, authToken string, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	openRemoteWithRemoteEncryption func(url, authToken, remoteKey string, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	openSyncWithConfig             func(cfg cConfig, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	sync2                          func(db unsafe.Pointer, out *cReplicated, msg *unsafe.Pointer) int32
	closeDB                        func(db unsafe.Pointer)
	connect                        func(db unsafe.Pointer, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	disconnect                     func(conn unsafe.Pointer)
	execute                        func(conn unsafe.Pointer, sql string, msg *unsafe.Pointer) int32
	query                          func(conn unsafe.Pointer, sql string, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	prepare                        func(conn unsafe.Pointer, sql string, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	freeStmt                       func(stmt unsafe.Pointer)
	bindInt                        func(stmt unsafe.Pointer, idx int32, v int64, msg *unsafe.Pointer) int32
	bindFloat                      func(stmt unsafe.Pointer, idx int32, v float64, msg *unsafe.Pointer) int32
	bindNull                       func(stmt unsafe.Pointer, idx int32, msg *unsafe.Pointer) int32
	bindString                     func(stmt unsafe.Pointer, idx int32, v string, msg *unsafe.Pointer) int32
	bindBlob                       func(stmt unsafe.Pointer, idx int32, v *byte, n int32, msg *unsafe.Pointer) int32
	executeStmt                    func(stmt unsafe.Pointer, msg *unsafe.Pointer) int32
	queryStmt                      func(stmt unsafe.Pointer, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	changes                        func(conn unsafe.Pointer) uint64
	lastInsertRowID                func(conn unsafe.Pointer) int64
	freeRows                       func(rows unsafe.Pointer)
	columnCount                    func(rows unsafe.Pointer) int32
	columnName                     func(rows unsafe.Pointer, col int32, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	columnType                     func(rows, row unsafe.Pointer, col int32, out *int32, msg *unsafe.Pointer) int32
	nextRow                        func(rows unsafe.Pointer, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	freeRow                        func(row unsafe.Pointer)
	getInt                         func(row unsafe.Pointer, col int32, out *int64, msg *unsafe.Pointer) int32
	getFloat                       func(row unsafe.Pointer, col int32, out *float64, msg *unsafe.Pointer) int32
	getBlob                        func(row unsafe.Pointer, col int32, out *cBlob, msg *unsafe.Pointer) int32
	getString                      func(row unsafe.Pointer, col int32, out *unsafe.Pointer, msg *unsafe.Pointer) int32
	freeBlob                       func(b cBlob)
	freeString                     func(s unsafe.Pointer)
	complete                       func(sql string) int32
}

// bindings names every symbol the package binds, with the field it fills.
func (f *functions) bindings() []binding {
	return []binding{
		{"libsql_open_file", &f.openFile},
		{"libsql_open_remote", &f.openRemote},
		{"libsql_open_remote_with_remote_encryption", &f.openRemoteWithRemoteEncryption},
		{"libsql_open_sync_with_config", &f.openSyncWithConfig},
		{"libsql_sync2", &f.sync2},
		{"libsql_close", &f.closeDB},
		{"libsql_connect", &f.connect},
		{"libsql_disconnect", &f.disconnect},
		{"libsql_execute", &f.execute},
		{"libsql_query", &f.query},
		{"libsql_prepare", &f.prepare},
		{"libsql_free_stmt", &f.freeStmt},
		{"libsql_bind_int", &f.bindInt},
		{"libsql_bind_float", &f.bindFloat},
		{"libsql_bind_null", &f.bindNull},
		{"libsql_bind_string", &f.bindString},
		{"libsql_bind_blob", &f.bindBlob},
		{"libsql_execute_stmt", &f.executeStmt},
		{"libsql_query_stmt", &f.queryStmt},
		{"libsql_changes", &f.changes},
		{"libsql_last_insert_rowid", &f.lastInsertRowID},
		{"libsql_free_rows", &f.freeRows},
		{"libsql_column_count", &f.columnCount},
		{"libsql_column_name", &f.columnName},
		{"libsql_column_type", &f.columnType},
		{"libsql_next_row", &f.nextRow},
		{"libsql_free_row", &f.freeRow},
		{"libsql_get_int", &f.getInt},
		{"libsql_get_float", &f.getFloat},
		{"libsql_get_blob", &f.getBlob},
		{"libsql_get_string", &f.getString},
		{"libsql_free_blob", &f.freeBlob},
		{"libsql_free_string", &f.freeString},
		{"libsql_complete", &f.complete},
	}
}

// binding is one symbol and the function field purego fills from it.
type binding struct {
	name string
	fn   any
}

var (
	loadOnce sync.Once
	fns      *functions
	loadErr  error
)

// lib returns the bound library, loading it on first use.
func lib() (*functions, *nativeErr) {
	loadOnce.Do(func() {
		f := &functions{}
		addrs, err := loadLibrary(f.bindings())
		if err != nil {
			loadErr = err
			return
		}
		for i, b := range f.bindings() {
			purego.RegisterFunc(b.fn, addrs[i])
		}
		fns = f
	})
	if loadErr != nil {
		return nil, &nativeErr{code: -1, msg: loadErr.Error(), hasMsg: true}
	}
	return fns, nil
}

// goString copies the NUL-terminated C string at p.
func goString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	return string(unsafe.Slice((*byte)(p), n))
}

// cString is a NUL-terminated copy of s for a struct field; the caller keeps
// it alive across the call.
func cString(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

// pError turns a status and message into a nativeErr, freeing the message;
// nil for status 0.
func pError(f *functions, status int32, msg unsafe.Pointer) *nativeErr {
	if status == 0 {
		return nil
	}
	e := &nativeErr{code: int(status)}
	if msg != nil {
		e.msg, e.hasMsg = goString(msg), true
		f.freeString(msg)
	}
	return e
}

func nOpenFile(dsn string) (dbHandle, *nativeErr) {
	f, le := lib()
	if le != nil {
		return nil, le
	}
	var db, msg unsafe.Pointer
	if e := pError(f, f.openFile(dsn, &db, &msg), msg); e != nil {
		return nil, e
	}
	return dbHandle(db), nil
}

func nOpenRemote(url, authToken, remoteEncryptionKey string) (dbHandle, *nativeErr) {
	f, le := lib()
	if le != nil {
		return nil, le
	}
	var db, msg unsafe.Pointer
	var status int32
	if remoteEncryptionKey == "" {
		status = f.openRemote(url, authToken, &db, &msg)
	} else {
		status = f.openRemoteWithRemoteEncryption(url, authToken, remoteEncryptionKey, &db, &msg)
	}
	if e := pError(f, status, msg); e != nil {
		return nil, e
	}
	return dbHandle(db), nil
}

func nOpenSync(c syncConfig) (dbHandle, *nativeErr) {
	f, le := lib()
	if le != nil {
		return nil, le
	}
	cfg := cConfig{dbPath: cString(c.path), primaryURL: cString(c.primaryURL), authToken: cString(c.authToken)}
	if c.encryptionKey != "" {
		cfg.encryptionKey = cString(c.encryptionKey)
	}
	if c.remoteEncryptionKey != "" {
		cfg.remoteEncryptionKey = cString(c.remoteEncryptionKey)
	}
	if c.readYourWrites {
		cfg.readYourWrites = 1
	}
	if c.offline {
		cfg.offline = 1
	}
	var db, msg unsafe.Pointer
	status := f.openSyncWithConfig(cfg, &db, &msg)
	runtime.KeepAlive(&cfg)
	if e := pError(f, status, msg); e != nil {
		return nil, e
	}
	return dbHandle(db), nil
}

func nSync(db dbHandle) (Replicated, *nativeErr) {
	f, le := lib()
	if le != nil {
		return Replicated{}, le
	}
	var rep cReplicated
	var msg unsafe.Pointer
	if e := pError(f, f.sync2(unsafe.Pointer(db), &rep, &msg), msg); e != nil {
		return Replicated{}, e
	}
	return Replicated{FrameNo: int(rep.frameNo), FramesSynced: int(rep.framesSynced)}, nil
}

// The functions below run only on a handle an open returned, so the library
// is loaded.

func nClose(db dbHandle) { fns.closeDB(unsafe.Pointer(db)) }

func nConnect(db dbHandle) (connHandle, *nativeErr) {
	var c, msg unsafe.Pointer
	if e := pError(fns, fns.connect(unsafe.Pointer(db), &c, &msg), msg); e != nil {
		return nil, e
	}
	return connHandle(c), nil
}

func nDisconnect(c connHandle) { fns.disconnect(unsafe.Pointer(c)) }

func nExecute(c connHandle, query string) *nativeErr {
	var msg unsafe.Pointer
	return pError(fns, fns.execute(unsafe.Pointer(c), query, &msg), msg)
}

func nQuery(c connHandle, query string) (rowsHandle, *nativeErr) {
	var rows, msg unsafe.Pointer
	if e := pError(fns, fns.query(unsafe.Pointer(c), query, &rows, &msg), msg); e != nil {
		return nil, e
	}
	return rowsHandle(rows), nil
}

func nPrepare(c connHandle, query string) (stmtHandle, *nativeErr) {
	var s, msg unsafe.Pointer
	if e := pError(fns, fns.prepare(unsafe.Pointer(c), query, &s, &msg), msg); e != nil {
		return nil, e
	}
	return stmtHandle(s), nil
}

func nFreeStmt(s stmtHandle) { fns.freeStmt(unsafe.Pointer(s)) }

func nBindInt(s stmtHandle, idx int, v int64) *nativeErr {
	var msg unsafe.Pointer
	return pError(fns, fns.bindInt(unsafe.Pointer(s), int32(idx), v, &msg), msg)
}

func nBindFloat(s stmtHandle, idx int, v float64) *nativeErr {
	var msg unsafe.Pointer
	return pError(fns, fns.bindFloat(unsafe.Pointer(s), int32(idx), v, &msg), msg)
}

func nBindBlob(s stmtHandle, idx int, v []byte) *nativeErr {
	var msg unsafe.Pointer
	var p *byte
	if len(v) > 0 {
		p = &v[0]
	}
	status := fns.bindBlob(unsafe.Pointer(s), int32(idx), p, int32(len(v)), &msg)
	runtime.KeepAlive(v)
	return pError(fns, status, msg)
}

func nBindString(s stmtHandle, idx int, v string) *nativeErr {
	var msg unsafe.Pointer
	return pError(fns, fns.bindString(unsafe.Pointer(s), int32(idx), v, &msg), msg)
}

func nBindNull(s stmtHandle, idx int) *nativeErr {
	var msg unsafe.Pointer
	return pError(fns, fns.bindNull(unsafe.Pointer(s), int32(idx), &msg), msg)
}

func nExecuteStmt(s stmtHandle) *nativeErr {
	var msg unsafe.Pointer
	return pError(fns, fns.executeStmt(unsafe.Pointer(s), &msg), msg)
}

func nQueryStmt(s stmtHandle) (rowsHandle, *nativeErr) {
	var rows, msg unsafe.Pointer
	if e := pError(fns, fns.queryStmt(unsafe.Pointer(s), &rows, &msg), msg); e != nil {
		return nil, e
	}
	return rowsHandle(rows), nil
}

func nChanges(c connHandle) int64 { return int64(fns.changes(unsafe.Pointer(c))) }

func nLastInsertRowID(c connHandle) int64 { return fns.lastInsertRowID(unsafe.Pointer(c)) }

func nFreeRows(r rowsHandle) { fns.freeRows(unsafe.Pointer(r)) }

func nColumnCount(r rowsHandle) int { return int(fns.columnCount(unsafe.Pointer(r))) }

func nColumnName(r rowsHandle, i int) (string, *nativeErr) {
	var p, msg unsafe.Pointer
	if e := pError(fns, fns.columnName(unsafe.Pointer(r), int32(i), &p, &msg), msg); e != nil {
		return "", e
	}
	s := goString(p)
	fns.freeString(p)
	return s, nil
}

func nNextRow(r rowsHandle) (rowHandle, *nativeErr) {
	var row, msg unsafe.Pointer
	if e := pError(fns, fns.nextRow(unsafe.Pointer(r), &row, &msg), msg); e != nil {
		return nil, e
	}
	return rowHandle(row), nil
}

func nFreeRow(row rowHandle) { fns.freeRow(unsafe.Pointer(row)) }

func nColumnType(r rowsHandle, row rowHandle, i int) (int, *nativeErr) {
	var t int32
	var msg unsafe.Pointer
	if e := pError(fns, fns.columnType(unsafe.Pointer(r), unsafe.Pointer(row), int32(i), &t, &msg), msg); e != nil {
		return 0, e
	}
	return int(t), nil
}

func nGetInt(row rowHandle, i int) (int64, *nativeErr) {
	var v int64
	var msg unsafe.Pointer
	if e := pError(fns, fns.getInt(unsafe.Pointer(row), int32(i), &v, &msg), msg); e != nil {
		return 0, e
	}
	return v, nil
}

func nGetFloat(row rowHandle, i int) (float64, *nativeErr) {
	var v float64
	var msg unsafe.Pointer
	if e := pError(fns, fns.getFloat(unsafe.Pointer(row), int32(i), &v, &msg), msg); e != nil {
		return 0, e
	}
	return v, nil
}

func nGetBlob(row rowHandle, i int) ([]byte, *nativeErr) {
	var b cBlob
	var msg unsafe.Pointer
	if e := pError(fns, fns.getBlob(unsafe.Pointer(row), int32(i), &b, &msg), msg); e != nil {
		return nil, e
	}
	v := make([]byte, int(b.len))
	if b.len > 0 {
		copy(v, unsafe.Slice((*byte)(b.ptr), int(b.len)))
	}
	fns.freeBlob(b)
	return v, nil
}

func nGetString(row rowHandle, i int) (string, *nativeErr) {
	var p, msg unsafe.Pointer
	if e := pError(fns, fns.getString(unsafe.Pointer(row), int32(i), &p, &msg), msg); e != nil {
		return "", e
	}
	s := goString(p)
	fns.freeString(p)
	return s, nil
}

// nComplete is false when the library did not load: the statement is then
// kept whole, and running it fails with the loader's error.
func nComplete(query string) bool {
	f, le := lib()
	if le != nil {
		return false
	}
	return f.complete(query) != 0
}
