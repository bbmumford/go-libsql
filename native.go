package libsql

import (
	"fmt"
	"unsafe"
)

// The native layer is the libSQL C binding's calls as Go functions over
// opaque handles (nOpenFile, nConnect, nQuery, ...). The rest of the package
// is written against it alone, so one driver serves both builds:
// native_cgo.go implements it with cgo against the static archive linked
// into the binary, and native_purego.go with purego against the shared
// library loaded at run time (loader.go), for a build with cgo off.
//
// Each handle is the C pointer the binding returned, held as an
// unsafe.Pointer to memory the C library owns; nil is no object.
type (
	dbHandle   unsafe.Pointer
	connHandle unsafe.Pointer
	stmtHandle unsafe.Pointer
	rowsHandle unsafe.Pointer
	rowHandle  unsafe.Pointer
)

// nativeErr is a failed C call: its status code and, when the binding gave
// one, its message.
type nativeErr struct {
	code   int
	msg    string
	hasMsg bool
}

// syncConfig is libsql_config: the fields openSyncConnector sets.
type syncConfig struct {
	path, primaryURL, authToken        string
	readYourWrites                     bool
	encryptionKey, remoteEncryptionKey string
	offline                            bool
}

// libsqlError is the error of a failed C call, message first.
func libsqlError(message string, e *nativeErr) error {
	if e.hasMsg {
		return fmt.Errorf("%s\nerror code = %d: %v", message, e.code, e.msg)
	}
	return fmt.Errorf("%s\nerror code = %d", message, e.code)
}
