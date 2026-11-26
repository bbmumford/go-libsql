//go:build cgo

package libsql

/*
#cgo CFLAGS: -I${SRCDIR}/lib/include
#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/lib/darwin_amd64
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/lib/darwin_arm64
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/lib/linux_amd64
#cgo linux,arm64 LDFLAGS: -L${SRCDIR}/lib/linux_arm64
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/lib/windows_amd64
#cgo LDFLAGS: -lsql_experimental
#cgo LDFLAGS: -lm
#cgo darwin LDFLAGS: -framework Security
#cgo darwin LDFLAGS: -framework CoreFoundation
#cgo windows LDFLAGS: -lws2_32 -luserenv -lbcrypt -lntdll -lcrypt32 -lsecur32 -lncrypt
#include <libsql.h>
#include <stdlib.h>

int libsql_complete(const char *sql);
*/
import "C"

import (
	"unsafe"
)

// The native layer over the static archive linked by cgo (native.go).

// cError turns a C status and message into a nativeErr, freeing the message;
// nil for status 0.
func cError(status C.int, msg *C.char) *nativeErr {
	if status == 0 {
		return nil
	}
	e := &nativeErr{code: int(status)}
	if msg != nil {
		e.msg, e.hasMsg = C.GoString(msg), true
		C.libsql_free_string(msg)
	}
	return e
}

func cDB(h dbHandle) C.libsql_database_t       { return C.libsql_database_t(unsafe.Pointer(h)) }
func cConn(h connHandle) C.libsql_connection_t { return C.libsql_connection_t(unsafe.Pointer(h)) }
func cStmt(h stmtHandle) C.libsql_stmt_t       { return C.libsql_stmt_t(unsafe.Pointer(h)) }
func cRows(h rowsHandle) C.libsql_rows_t       { return C.libsql_rows_t(unsafe.Pointer(h)) }
func cRow(h rowHandle) C.libsql_row_t          { return C.libsql_row_t(unsafe.Pointer(h)) }

// cStringOrNil is a C copy of s, or NULL for "", which the caller frees with
// C.free (free(NULL) does nothing).
func cStringOrNil(s string) *C.char {
	if s == "" {
		return nil
	}
	return C.CString(s)
}

func nOpenFile(dsn string) (dbHandle, *nativeErr) {
	cs := C.CString(dsn)
	defer C.free(unsafe.Pointer(cs))
	var db C.libsql_database_t
	var msg *C.char
	if e := cError(C.libsql_open_file(cs, &db, &msg), msg); e != nil {
		return nil, e
	}
	return dbHandle(unsafe.Pointer(db)), nil
}

func nOpenRemote(url, authToken, remoteEncryptionKey string) (dbHandle, *nativeErr) {
	cu := C.CString(url)
	defer C.free(unsafe.Pointer(cu))
	ct := C.CString(authToken)
	defer C.free(unsafe.Pointer(ct))
	var db C.libsql_database_t
	var msg *C.char
	var status C.int
	if remoteEncryptionKey == "" {
		status = C.libsql_open_remote(cu, ct, &db, &msg)
	} else {
		ck := C.CString(remoteEncryptionKey)
		defer C.free(unsafe.Pointer(ck))
		status = C.libsql_open_remote_with_remote_encryption(cu, ct, ck, &db, &msg)
	}
	if e := cError(status, msg); e != nil {
		return nil, e
	}
	return dbHandle(unsafe.Pointer(db)), nil
}

func nOpenSync(c syncConfig) (dbHandle, *nativeErr) {
	path := C.CString(c.path)
	defer C.free(unsafe.Pointer(path))
	url := C.CString(c.primaryURL)
	defer C.free(unsafe.Pointer(url))
	token := C.CString(c.authToken)
	defer C.free(unsafe.Pointer(token))
	key := cStringOrNil(c.encryptionKey)
	defer C.free(unsafe.Pointer(key))
	remoteKey := cStringOrNil(c.remoteEncryptionKey)
	defer C.free(unsafe.Pointer(remoteKey))
	cfg := C.libsql_config{
		db_path:               path,
		auth_token:            token,
		primary_url:           url,
		encryption_key:        key,
		remote_encryption_key: remoteKey,
	}
	if c.readYourWrites {
		cfg.read_your_writes = 1
	}
	if c.offline {
		cfg.offline = 1
	}
	var db C.libsql_database_t
	var msg *C.char
	if e := cError(C.libsql_open_sync_with_config(cfg, &db, &msg), msg); e != nil {
		return nil, e
	}
	return dbHandle(unsafe.Pointer(db)), nil
}

func nSync(db dbHandle) (Replicated, *nativeErr) {
	var rep C.replicated
	var msg *C.char
	if e := cError(C.libsql_sync2(cDB(db), &rep, &msg), msg); e != nil {
		return Replicated{}, e
	}
	return Replicated{FrameNo: int(rep.frame_no), FramesSynced: int(rep.frames_synced)}, nil
}

func nClose(db dbHandle) { C.libsql_close(cDB(db)) }

func nConnect(db dbHandle) (connHandle, *nativeErr) {
	var c C.libsql_connection_t
	var msg *C.char
	if e := cError(C.libsql_connect(cDB(db), &c, &msg), msg); e != nil {
		return nil, e
	}
	return connHandle(unsafe.Pointer(c)), nil
}

func nDisconnect(c connHandle) { C.libsql_disconnect(cConn(c)) }

func nExecute(c connHandle, query string) *nativeErr {
	cs := C.CString(query)
	defer C.free(unsafe.Pointer(cs))
	var msg *C.char
	return cError(C.libsql_execute(cConn(c), cs, &msg), msg)
}

func nQuery(c connHandle, query string) (rowsHandle, *nativeErr) {
	cs := C.CString(query)
	defer C.free(unsafe.Pointer(cs))
	var rows C.libsql_rows_t
	var msg *C.char
	if e := cError(C.libsql_query(cConn(c), cs, &rows, &msg), msg); e != nil {
		return nil, e
	}
	return rowsHandle(unsafe.Pointer(rows)), nil
}

func nPrepare(c connHandle, query string) (stmtHandle, *nativeErr) {
	cs := C.CString(query)
	defer C.free(unsafe.Pointer(cs))
	var s C.libsql_stmt_t
	var msg *C.char
	if e := cError(C.libsql_prepare(cConn(c), cs, &s, &msg), msg); e != nil {
		return nil, e
	}
	return stmtHandle(unsafe.Pointer(s)), nil
}

func nFreeStmt(s stmtHandle) { C.libsql_free_stmt(cStmt(s)) }

func nBindInt(s stmtHandle, idx int, v int64) *nativeErr {
	var msg *C.char
	return cError(C.libsql_bind_int(cStmt(s), C.int(idx), C.longlong(v), &msg), msg)
}

func nBindFloat(s stmtHandle, idx int, v float64) *nativeErr {
	var msg *C.char
	return cError(C.libsql_bind_float(cStmt(s), C.int(idx), C.double(v), &msg), msg)
}

func nBindBlob(s stmtHandle, idx int, v []byte) *nativeErr {
	p := C.CBytes(v)
	defer C.free(p)
	var msg *C.char
	return cError(C.libsql_bind_blob(cStmt(s), C.int(idx), (*C.uchar)(p), C.int(len(v)), &msg), msg)
}

func nBindString(s stmtHandle, idx int, v string) *nativeErr {
	cs := C.CString(v)
	defer C.free(unsafe.Pointer(cs))
	var msg *C.char
	return cError(C.libsql_bind_string(cStmt(s), C.int(idx), cs, &msg), msg)
}

func nBindNull(s stmtHandle, idx int) *nativeErr {
	var msg *C.char
	return cError(C.libsql_bind_null(cStmt(s), C.int(idx), &msg), msg)
}

func nExecuteStmt(s stmtHandle) *nativeErr {
	var msg *C.char
	return cError(C.libsql_execute_stmt(cStmt(s), &msg), msg)
}

func nQueryStmt(s stmtHandle) (rowsHandle, *nativeErr) {
	var rows C.libsql_rows_t
	var msg *C.char
	if e := cError(C.libsql_query_stmt(cStmt(s), &rows, &msg), msg); e != nil {
		return nil, e
	}
	return rowsHandle(unsafe.Pointer(rows)), nil
}

func nChanges(c connHandle) int64 { return int64(C.libsql_changes(cConn(c))) }

func nLastInsertRowID(c connHandle) int64 { return int64(C.libsql_last_insert_rowid(cConn(c))) }

func nFreeRows(r rowsHandle) { C.libsql_free_rows(cRows(r)) }

func nColumnCount(r rowsHandle) int { return int(C.libsql_column_count(cRows(r))) }

func nColumnName(r rowsHandle, i int) (string, *nativeErr) {
	var p, msg *C.char
	if e := cError(C.libsql_column_name(cRows(r), C.int(i), &p, &msg), msg); e != nil {
		return "", e
	}
	s := C.GoString(p)
	C.libsql_free_string(p)
	return s, nil
}

func nNextRow(r rowsHandle) (rowHandle, *nativeErr) {
	var row C.libsql_row_t
	var msg *C.char
	if e := cError(C.libsql_next_row(cRows(r), &row, &msg), msg); e != nil {
		return nil, e
	}
	return rowHandle(unsafe.Pointer(row)), nil
}

func nFreeRow(row rowHandle) { C.libsql_free_row(cRow(row)) }

func nColumnType(r rowsHandle, row rowHandle, i int) (int, *nativeErr) {
	var t C.int
	var msg *C.char
	if e := cError(C.libsql_column_type(cRows(r), cRow(row), C.int(i), &t, &msg), msg); e != nil {
		return 0, e
	}
	return int(t), nil
}

func nGetInt(row rowHandle, i int) (int64, *nativeErr) {
	var v C.longlong
	var msg *C.char
	if e := cError(C.libsql_get_int(cRow(row), C.int(i), &v, &msg), msg); e != nil {
		return 0, e
	}
	return int64(v), nil
}

func nGetFloat(row rowHandle, i int) (float64, *nativeErr) {
	var v C.double
	var msg *C.char
	if e := cError(C.libsql_get_float(cRow(row), C.int(i), &v, &msg), msg); e != nil {
		return 0, e
	}
	return float64(v), nil
}

func nGetBlob(row rowHandle, i int) ([]byte, *nativeErr) {
	var b C.blob
	var msg *C.char
	if e := cError(C.libsql_get_blob(cRow(row), C.int(i), &b, &msg), msg); e != nil {
		return nil, e
	}
	v := C.GoBytes(unsafe.Pointer(b.ptr), b.len)
	C.libsql_free_blob(b)
	return v, nil
}

func nGetString(row rowHandle, i int) (string, *nativeErr) {
	var p, msg *C.char
	if e := cError(C.libsql_get_string(cRow(row), C.int(i), &p, &msg), msg); e != nil {
		return "", e
	}
	s := C.GoString(p)
	C.libsql_free_string(p)
	return s, nil
}

func nComplete(query string) bool {
	cs := C.CString(query)
	defer C.free(unsafe.Pointer(cs))
	return C.libsql_complete(cs) != 0
}
