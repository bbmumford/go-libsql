package libsql

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/antlr4-go/antlr/v4"
	"github.com/libsql/sqlite-antlr4-parser/sqliteparser"
	"github.com/libsql/sqlite-antlr4-parser/sqliteparserutils"
)

// DriverName is the database/sql driver name this package registers, for
// sql.Open callers that use the URL forms driver.OpenConnector accepts.
// It is not "libsql" because github.com/tursodatabase/libsql-client-go
// registers that name, and sql.Register panics at init when one binary links
// two packages registering the same name.
const DriverName = "libsql-cgo"

func init() {
	sql.Register(DriverName, driver{})
}

type config struct {
	authToken           *string
	readYourWrites      *bool
	encryptionKey       *string
	remoteEncryptionKey *string
	syncInterval        *time.Duration
	// Set only by the NewLocalConnector options in local_connector.go.
	localKey    []byte
	busyTimeout *time.Duration
	queryOnly   *bool
	// Set by the connection options in conn_options.go, for every connector.
	immediateTransactions *bool
	rawText               *bool
	// Set by WithReadOnly in readonly.go, for the replica connectors.
	readOnly bool
}

type Option interface {
	apply(*config) error
}

type option func(*config) error

type Replicated struct {
	FrameNo      int
	FramesSynced int
}

func (o option) apply(c *config) error {
	return o(c)
}

func WithAuthToken(authToken string) Option {
	return option(func(o *config) error {
		if o.authToken != nil {
			return fmt.Errorf("authToken already set")
		}
		if authToken == "" {
			return fmt.Errorf("authToken must not be empty")
		}
		o.authToken = &authToken
		return nil
	})
}

func WithReadYourWrites(readYourWrites bool) Option {
	return option(func(o *config) error {
		if o.readYourWrites != nil {
			return fmt.Errorf("read your writes already set")
		}
		o.readYourWrites = &readYourWrites
		return nil
	})
}

func WithEncryption(key string) Option {
	return option(func(o *config) error {
		if o.encryptionKey != nil {
			return fmt.Errorf("encryption key already set")
		}
		if key == "" {
			return fmt.Errorf("encryption key must not be empty")
		}
		o.encryptionKey = &key
		return nil
	})
}

func WithRemoteEncryption(key string) Option {
	return option(func(o *config) error {
		if o.remoteEncryptionKey != nil {
			return fmt.Errorf("remote encryption key already set")
		}
		if key == "" {
			return fmt.Errorf("remote encryption key must not be empty")
		}
		o.remoteEncryptionKey = &key
		return nil
	})
}

func WithSyncInterval(interval time.Duration) Option {
	return option(func(o *config) error {
		if o.syncInterval != nil {
			return fmt.Errorf("sync interval already set")
		}
		o.syncInterval = &interval
		return nil
	})
}

func NewEmbeddedReplicaConnector(dbPath string, primaryUrl string, opts ...Option) (*Connector, error) {
	var config config
	errs := make([]error, 0, len(opts))
	for _, opt := range opts {
		if err := opt.apply(&config); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := config.refuseLocalOnly(); err != nil {
		return nil, err
	}
	authToken := ""
	if config.authToken != nil {
		authToken = *config.authToken
	}
	readYourWrites := true
	if config.readYourWrites != nil {
		readYourWrites = *config.readYourWrites
	}
	encryptionKey := ""
	if config.encryptionKey != nil {
		encryptionKey = *config.encryptionKey
	}
	remoteEncryptionKey := ""
	if config.remoteEncryptionKey != nil {
		remoteEncryptionKey = *config.remoteEncryptionKey
	}
	syncInterval := time.Duration(0)
	if config.syncInterval != nil {
		syncInterval = *config.syncInterval
	}
	connector, err := openSyncConnector(dbPath, primaryUrl, authToken, readYourWrites, encryptionKey, remoteEncryptionKey, syncInterval, false)
	if err != nil {
		return nil, err
	}
	connector.opts = config.connOptions()
	return connector, nil
}

func NewSyncedDatabaseConnector(dbPath string, primaryUrl string, opts ...Option) (*Connector, error) {
	var config config
	errs := make([]error, 0, len(opts))
	for _, opt := range opts {
		if err := opt.apply(&config); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := config.refuseLocalOnly(); err != nil {
		return nil, err
	}
	authToken := ""
	if config.authToken != nil {
		authToken = *config.authToken
	}
	readYourWrites := true
	if config.readYourWrites != nil {
		readYourWrites = *config.readYourWrites
	}
	encryptionKey := ""
	if config.encryptionKey != nil {
		encryptionKey = *config.encryptionKey
	}
	remoteEncryptionKey := ""
	if config.remoteEncryptionKey != nil {
		remoteEncryptionKey = *config.remoteEncryptionKey
	}
	syncInterval := time.Duration(0)
	if config.syncInterval != nil {
		syncInterval = *config.syncInterval
	}
	connector, err := openSyncConnector(dbPath, primaryUrl, authToken, readYourWrites, encryptionKey, remoteEncryptionKey, syncInterval, true)
	if err != nil {
		return nil, err
	}
	connector.opts = config.connOptions()
	return connector, nil
}

type driver struct{}

func (d driver) Open(dbAddress string) (sqldriver.Conn, error) {
	connector, err := d.OpenConnector(dbAddress)
	if err != nil {
		return nil, err
	}
	return connector.Connect(context.Background())
}

func (d driver) OpenConnector(dbAddress string) (sqldriver.Connector, error) {
	if strings.HasPrefix(dbAddress, ":memory:") {
		return openLocalConnector(dbAddress)
	}
	u, err := url.Parse(dbAddress)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "file":
		return openLocalConnector(dbAddress)
	case "http":
		fallthrough
	case "https":
		fallthrough
	case "libsql":
		authToken := u.Query().Get("authToken")
		remoteEncryptionKey := u.Query().Get("remoteEncryptionKey")
		u.RawQuery = ""
		return openRemoteConnector(u.String(), authToken, remoteEncryptionKey)
	}
	return nil, fmt.Errorf("unsupported URL scheme: %s\nThis driver supports only URLs that start with libsql://, file:, https:// or http://", u.Scheme)
}

func libsqlSync(nativeDbPtr dbHandle) (Replicated, error) {
	rep, e := nSync(nativeDbPtr)
	if e != nil {
		return Replicated{0, 0}, libsqlError("failed to sync database ", e)
	}
	return rep, nil
}

func openLocalConnector(dbPath string) (*Connector, error) {
	nativeDbPtr, err := libsqlOpenLocal(dbPath)
	if err != nil {
		return nil, err
	}
	return &Connector{nativeDbPtr: nativeDbPtr, local: true}, nil
}

func openRemoteConnector(primaryUrl, authToken, remoteEncryptionKey string) (*Connector, error) {
	nativeDbPtr, err := libsqlOpenRemote(primaryUrl, authToken, remoteEncryptionKey)
	if err != nil {
		return nil, err
	}
	return &Connector{nativeDbPtr: nativeDbPtr}, nil
}

func openSyncConnector(dbPath, primaryUrl, authToken string, readYourWrites bool, encryptionKey string, remoteEncrytionKey string, syncInterval time.Duration, offline bool) (*Connector, error) {
	var closeCh chan struct{}
	var closeAckCh chan struct{}
	nativeDbPtr, err := libsqlOpenWithSync(dbPath, primaryUrl, authToken, readYourWrites, encryptionKey, remoteEncrytionKey, offline)
	if err != nil {
		return nil, err
	}
	if _, err := libsqlSync(nativeDbPtr); err != nil {
		nClose(nativeDbPtr)
		return nil, err
	}
	if syncInterval != 0 {
		closeCh = make(chan struct{}, 1)
		closeAckCh = make(chan struct{}, 1)
		go func() {
			for {
				timerCh := make(chan struct{}, 1)
				go func() {
					time.Sleep(syncInterval)
					timerCh <- struct{}{}
				}()
				select {
				case <-closeCh:
					closeAckCh <- struct{}{}
					return
				case <-timerCh:
					if _, err := libsqlSync(nativeDbPtr); err != nil {
						fmt.Println(err)
					}
				}
			}
		}()
	}
	return &Connector{nativeDbPtr: nativeDbPtr, closeCh: closeCh, closeAckCh: closeAckCh}, nil
}

type Connector struct {
	nativeDbPtr dbHandle
	closeCh     chan<- struct{}
	closeAckCh  <-chan struct{}
	// local marks a database with no primary (file: and :memory: opens and
	// NewLocalConnector); conn.execOne reads it through conn.local.
	local bool
	// setup prepares every connection of a NewLocalConnector; nil otherwise.
	setup *localSetup
	// opts is copied into every conn Connect makes.
	opts connOptions
}

// Deprecated: Sync is deprecated and will be removed in a future release. Use the `tursogo` package instead. Learn more: https://tur.so/newsync
func (c *Connector) Sync() (Replicated, error) {
	return libsqlSync(c.nativeDbPtr)
}

func (c *Connector) Close() error {
	if c.closeCh != nil {
		c.closeCh <- struct{}{}
		<-c.closeAckCh
		c.closeCh = nil
		c.closeAckCh = nil
	}
	if c.nativeDbPtr != nil {
		nClose(c.nativeDbPtr)
	}
	c.nativeDbPtr = nil
	return nil
}

func (c *Connector) Connect(ctx context.Context) (sqldriver.Conn, error) {
	nativeConnPtr, err := libsqlConnect(c.nativeDbPtr)
	if err != nil {
		return nil, err
	}
	cn := &conn{nativePtr: nativeConnPtr, local: c.local, opts: c.opts}
	if c.setup != nil {
		if err := c.setup.prepare(cn); err != nil {
			nDisconnect(nativeConnPtr)
			return nil, err
		}
	}
	return cn, nil
}

func (c *Connector) Driver() sqldriver.Driver {
	return driver{}
}

func libsqlOpenLocal(dataSourceName string) (dbHandle, error) {
	db, e := nOpenFile(dataSourceName)
	if e != nil {
		return nil, libsqlError(fmt.Sprint("failed to open local database ", dataSourceName), e)
	}
	return db, nil
}

func libsqlOpenRemote(url, authToken, remoteEncryptionKey string) (dbHandle, error) {
	db, e := nOpenRemote(url, authToken, remoteEncryptionKey)
	if e != nil {
		return nil, libsqlError(fmt.Sprint("failed to open remote database ", url), e)
	}
	return db, nil
}

func libsqlOpenWithSync(dbPath, primaryUrl, authToken string, readYourWrites bool, encryptionKey string, remoteEncrytionKey string, offline bool) (dbHandle, error) {
	db, e := nOpenSync(syncConfig{
		path: dbPath, primaryURL: primaryUrl, authToken: authToken, readYourWrites: readYourWrites,
		encryptionKey: encryptionKey, remoteEncryptionKey: remoteEncrytionKey, offline: offline,
	})
	if e != nil {
		return nil, libsqlError(fmt.Sprintf("failed to open database %s %s", dbPath, primaryUrl), e)
	}
	return db, nil
}

func libsqlConnect(db dbHandle) (connHandle, error) {
	conn, e := nConnect(db)
	if e != nil {
		return nil, libsqlError("failed to connect to database", e)
	}
	return conn, nil
}

type conn struct {
	nativePtr connHandle
	local     bool
	opts      connOptions
	// readTx is whether a read transaction is open, as libSQL's replica
	// connection holds it; read and set only under opts.readOnly (admit).
	readTx bool
}

func (c *conn) Prepare(query string) (sqldriver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *conn) Begin() (sqldriver.Tx, error) {
	return c.BeginTx(context.Background(), sqldriver.TxOptions{})
}

func (c *conn) Close() error {
	nDisconnect(c.nativePtr)
	return nil
}

type ParamsInfo struct {
	NamedParameters           []string
	PositionalParametersCount int
}

func isPositionalParameter(param string) (ok bool, err error) {
	re := regexp.MustCompile(`\?([0-9]*).*`)
	match := re.FindSubmatch([]byte(param))
	if match == nil {
		return false, nil
	}

	posS := string(match[1])
	if posS == "" {
		return true, nil
	}

	return true, fmt.Errorf("unsuppoted positional parameter. This driver does not accept positional parameters with indexes (like ?<number>)")
}

func removeParamPrefix(paramName string) (string, error) {
	if paramName[0] == ':' || paramName[0] == '@' || paramName[0] == '$' {
		return paramName[1:], nil
	}
	return "", fmt.Errorf("all named parameters must start with ':', or '@' or '$'")
}

func extractParameters(stmt string) (nameParams []string, positionalParamsCount int, err error) {
	statementStream := antlr.NewInputStream(stmt)
	sqliteparser.NewSQLiteLexer(statementStream)
	lexer := sqliteparser.NewSQLiteLexer(statementStream)

	allTokens := lexer.GetAllTokens()

	nameParamsSet := make(map[string]bool)

	for _, token := range allTokens {
		tokenType := token.GetTokenType()
		if tokenType == sqliteparser.SQLiteLexerBIND_PARAMETER {
			parameter := token.GetText()

			isPositionalParameter, err := isPositionalParameter(parameter)
			if err != nil {
				return []string{}, 0, err
			}

			if isPositionalParameter {
				positionalParamsCount++
			} else {
				paramWithoutPrefix, err := removeParamPrefix(parameter)
				if err != nil {
					return []string{}, 0, err
				} else {
					nameParamsSet[paramWithoutPrefix] = true
				}
			}
		}
	}
	nameParams = make([]string, 0, len(nameParamsSet))
	for k := range nameParamsSet {
		nameParams = append(nameParams, k)
	}

	return nameParams, positionalParamsCount, nil
}

func parseStatement(sql string) ([]string, []ParamsInfo, error) {
	stmts, _ := sqliteparserutils.SplitStatement(sql)

	stmtsParams := make([]ParamsInfo, len(stmts))
	for idx, stmt := range stmts {
		nameParams, positionalParamsCount, err := extractParameters(stmt)
		if err != nil {
			return nil, nil, err
		}
		stmtsParams[idx] = ParamsInfo{nameParams, positionalParamsCount}
	}
	return stmts, stmtsParams, nil
}

func (c *conn) PrepareContext(ctx context.Context, query string) (sqldriver.Stmt, error) {
	if c.opts.readOnly {
		// The state is recorded when the statement runs, through ExecContext
		// or QueryContext, which check it again.
		if _, err := admitStatements(query, 0, c.readTx); err != nil {
			return nil, err
		}
	}
	stmts, paramInfos, err := parseStatement(query)
	if err != nil {
		return nil, err
	}
	if len(stmts) != 1 {
		return nil, fmt.Errorf("only one statement is supported got %d", len(stmts))
	}
	numInput := -1
	if len(paramInfos[0].NamedParameters) == 0 {
		numInput = paramInfos[0].PositionalParametersCount
	}
	return &stmt{c, query, numInput}, nil
}

func (c *conn) BeginTx(ctx context.Context, opts sqldriver.TxOptions) (sqldriver.Tx, error) {
	begin, err := c.beginStatement(opts)
	if err != nil {
		return nil, err
	}
	_, err = c.ExecContext(ctx, begin, nil)
	if err != nil {
		return nil, err
	}
	return &tx{c}, nil
}

func (c *conn) executeNoArgs(query string, exec bool) (rowsHandle, error) {
	var rows rowsHandle
	var e *nativeErr
	if exec {
		e = nExecute(c.nativePtr, query)
	} else {
		rows, e = nQuery(c.nativePtr, query)
	}
	if e != nil {
		return nil, libsqlError("failed to execute "+statementLabel(query), e)
	}
	return rows, nil
}

func (c *conn) execute(query string, args []sqldriver.NamedValue, exec bool) (rowsHandle, error) {
	// The C binding binds by position only: a sql.Named value would bind in
	// argument order whatever its name (SELECT :b - :a with a=2, b=40 returns
	// -38). A name is refused rather than bound to the wrong parameter.
	for _, arg := range args {
		if arg.Name != "" {
			return nil, fmt.Errorf("named argument %q is not supported: arguments bind by position", arg.Name)
		}
	}
	if len(args) == 0 {
		return c.executeNoArgs(query, exec)
	}
	stmt, e := nPrepare(c.nativePtr, query)
	if e != nil {
		return nil, libsqlError("failed to prepare "+statementLabel(query), e)
	}
	defer nFreeStmt(stmt)

	for _, arg := range args {
		var e *nativeErr
		idx := arg.Ordinal
		switch v := arg.Value.(type) {
		case int64:
			e = nBindInt(stmt, idx, v)
		case float64:
			e = nBindFloat(stmt, idx, v)
		case []byte:
			e = nBindBlob(stmt, idx, v)
		case string:
			e = nBindString(stmt, idx, v)
		case nil:
			e = nBindNull(stmt, idx)
		case bool:
			var valueInt int64
			if v {
				valueInt = 1
			}
			e = nBindInt(stmt, idx, valueInt)
		case time.Time:
			e = nBindString(stmt, idx, v.Format(time.RFC3339Nano))
		default:
			return nil, fmt.Errorf("unsupported type %T", arg.Value)
		}
		if e != nil {
			return nil, libsqlError(fmt.Sprintf("failed to bind argument no. %d with value %v and type %T", idx, arg.Value, arg.Value), e)
		}
	}

	var rows rowsHandle
	if exec {
		e = nExecuteStmt(stmt)
	} else {
		rows, e = nQueryStmt(stmt)
	}
	if e != nil {
		return nil, libsqlError("failed to execute "+statementLabel(query), e)
	}
	return rows, nil
}

type execResult struct {
	id      int64
	changes int64
}

func (r execResult) LastInsertId() (int64, error) {
	return r.id, nil
}

func (r execResult) RowsAffected() (int64, error) {
	return r.changes, nil
}

type stmt struct {
	conn     *conn
	sql      string
	numInput int
}

func (s *stmt) Close() error {
	return nil
}

func (s *stmt) NumInput() int {
	return s.numInput
}

func convertToNamed(args []sqldriver.Value) []sqldriver.NamedValue {
	if len(args) == 0 {
		return nil
	}
	result := make([]sqldriver.NamedValue, 0, len(args))
	for idx := range args {
		result = append(result, sqldriver.NamedValue{Ordinal: idx, Value: args[idx]})
	}
	return result
}

func (s *stmt) Exec(args []sqldriver.Value) (sqldriver.Result, error) {
	return s.ExecContext(context.Background(), convertToNamed(args))
}

func (s *stmt) Query(args []sqldriver.Value) (sqldriver.Rows, error) {
	return s.QueryContext(context.Background(), convertToNamed(args))
}

func (s *stmt) ExecContext(ctx context.Context, args []sqldriver.NamedValue) (sqldriver.Result, error) {
	return s.conn.ExecContext(ctx, s.sql, args)
}

func (s *stmt) QueryContext(ctx context.Context, args []sqldriver.NamedValue) (sqldriver.Rows, error) {
	return s.conn.QueryContext(ctx, s.sql, args)
}

type tx struct {
	conn *conn
}

func (t tx) Commit() error {
	_, err := t.conn.ExecContext(context.Background(), "COMMIT", nil)
	return err
}

func (t tx) Rollback() error {
	_, err := t.conn.ExecContext(context.Background(), "ROLLBACK", nil)
	return err
}

const (
	TYPE_INT int = iota + 1
	TYPE_FLOAT
	TYPE_TEXT
	TYPE_BLOB
	TYPE_NULL
)

func newRows(nativePtr rowsHandle, rawText bool) (*rows, error) {
	if nativePtr == nil {
		return &rows{rawText: rawText}, nil
	}
	columnCount := nColumnCount(nativePtr)
	columns := make([]string, columnCount)
	for i := 0; i < columnCount; i++ {
		name, e := nColumnName(nativePtr, i)
		if e != nil {
			return nil, libsqlError(fmt.Sprint("failed to get column name for index ", i), e)
		}
		columns[i] = name
	}
	return &rows{nativePtr: nativePtr, columnNames: columns, rawText: rawText}, nil
}

type rows struct {
	nativePtr   rowsHandle
	columnNames []string
	// rawText is connOptions.rawText of the conn that ran the query.
	rawText bool
}

func (r *rows) Columns() []string {
	return r.columnNames
}

func (r *rows) Close() error {
	if r.nativePtr != nil {
		nFreeRows(r.nativePtr)
		r.nativePtr = nil
	}
	return nil
}

func (r *rows) Next(dest []sqldriver.Value) error {
	if r.nativePtr == nil {
		return io.EOF
	}
	row, e := nNextRow(r.nativePtr)
	if e != nil {
		return libsqlError("failed to get next row", e)
	}
	if row == nil {
		r.Close()
		return io.EOF
	}
	defer nFreeRow(row)
	count := len(dest)
	if count > len(r.columnNames) {
		count = len(r.columnNames)
	}

Outerloop:
	for i := 0; i < count; i++ {
		columnType, e := nColumnType(r.nativePtr, row, i)
		if e != nil {
			return libsqlError(fmt.Sprint("failed to get column type for index ", i), e)
		}

		switch columnType {
		case TYPE_NULL:
			dest[i] = nil
		case TYPE_INT:
			value, e := nGetInt(row, i)
			if e != nil {
				return libsqlError(fmt.Sprint("failed to get integer for column ", i), e)
			}
			dest[i] = value
		case TYPE_FLOAT:
			value, e := nGetFloat(row, i)
			if e != nil {
				return libsqlError(fmt.Sprint("failed to get float for column ", i), e)
			}
			dest[i] = value
		case TYPE_BLOB:
			value, e := nGetBlob(row, i)
			if e != nil {
				return libsqlError(fmt.Sprint("failed to get blob for column ", i), e)
			}
			dest[i] = value
		case TYPE_TEXT:
			str, e := nGetString(row, i)
			if e != nil {
				return libsqlError(fmt.Sprint("failed to get string for column ", i), e)
			}
			if r.rawText {
				dest[i] = str
				continue
			}
			for _, format := range []string{
				time.RFC3339Nano,
				"2006-01-02 15:04:05.999999999-07:00",
				"2006-01-02T15:04:05.999999999-07:00",
				"2006-01-02 15:04:05.999999999",
				"2006-01-02T15:04:05.999999999",
				"2006-01-02 15:04:05",
				"2006-01-02T15:04:05",
				"2006-01-02 15:04",
				"2006-01-02T15:04",
				"2006-01-02",
			} {
				if t, err := time.ParseInLocation(format, str, time.UTC); err == nil {
					dest[i] = t
					continue Outerloop
				}
			}
			dest[i] = str
		}
	}
	return nil
}

func (c *conn) QueryContext(ctx context.Context, query string, args []sqldriver.NamedValue) (sqldriver.Rows, error) {
	if c.opts.readOnly {
		if err := c.admit(query, len(args)); err != nil {
			return nil, err
		}
	}
	rowsNativePtr, err := c.execute(query, args, false)
	if err != nil {
		return nil, err
	}
	return newRows(rowsNativePtr, c.opts.rawText)
}
