package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

var (
	ErrDatabaseConnectionExists   = errors.New("database connection id already exists")
	ErrDatabaseConnectionNotFound = errors.New("database connection not found")
	ErrDatabaseReadOnlyQuery      = errors.New("database tool only permits read-only queries")
)

// databaseOperationError preserves the underlying cause for errors.Is/errors.As
// without exposing driver errors, DSNs, credentials, or query parameters.
type databaseOperationError struct {
	message string
	cause   error
}

func (e *databaseOperationError) Error() string { return e.message }

func (e *databaseOperationError) Unwrap() error { return e.cause }

func wrapDatabaseOperationError(message string, cause error) error {
	if cause == nil {
		return nil
	}
	return &databaseOperationError{message: message, cause: cause}
}

const (
	defaultDatabasePageSize = 100
	maxDatabasePageSize     = 500
	maxDatabasePage         = 1_000_000
	databaseOpenTimeout     = 10 * time.Second
)

var (
	databaseIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	databaseObjectPattern     = regexp.MustCompile(`^[\pL\pN_$ .-]{1,128}$`)
)

type DatabaseConnectionConfig struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Provider           string `json:"provider"`
	DatabasePath       string `json:"databasePath,omitempty"`
	CredentialConfigID string `json:"credentialConfigId,omitempty"`
	DefaultSchema      string `json:"defaultSchema,omitempty"`
	resolvedDSN        string
}

type DatabaseConnectionInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	DefaultSchema string `json:"defaultSchema,omitempty"`
}

type DatabaseSchema struct {
	Name string `json:"name" db:"name"`
}

type DatabaseTable struct {
	Schema string `json:"schema,omitempty" db:"schema"`
	Name   string `json:"name" db:"name"`
	Type   string `json:"type" db:"type"`
}

type DatabaseColumn struct {
	Name         string `json:"name" db:"name"`
	DataType     string `json:"dataType" db:"data_type"`
	Nullable     bool   `json:"nullable" db:"nullable"`
	DefaultValue string `json:"defaultValue,omitempty" db:"default_value"`
	PrimaryKey   bool   `json:"primaryKey" db:"primary_key"`
	Ordinal      int    `json:"ordinal" db:"ordinal"`
}

type DatabaseQueryRequest struct {
	RequestID    string `json:"requestId"`
	ConnectionID string `json:"connectionId"`
	SQL          string `json:"sql"`
	Parameters   []any  `json:"parameters,omitempty"`
	Page         int    `json:"page"`
	PageSize     int    `json:"pageSize"`
}

type DatabaseQueryColumn struct {
	Name         string `json:"name"`
	DatabaseType string `json:"databaseType"`
	Nullable     bool   `json:"nullable"`
}

type DatabaseQueryResult struct {
	RequestID  string                `json:"requestId"`
	Columns    []DatabaseQueryColumn `json:"columns"`
	Rows       [][]any               `json:"rows"`
	Page       int                   `json:"page"`
	PageSize   int                   `json:"pageSize"`
	HasMore    bool                  `json:"hasMore"`
	DurationMs int64                 `json:"durationMs"`
}

type DatabaseSecretResolver interface {
	ResolveDatabaseSecret(context.Context, string) (string, error)
}

type DatabaseProvider interface {
	Open(context.Context, DatabaseConnectionConfig) (DatabaseSession, error)
}

type DatabaseSession interface {
	ListSchemas(context.Context) ([]DatabaseSchema, error)
	ListTables(context.Context, string) ([]DatabaseTable, error)
	DescribeTable(context.Context, string, string) ([]DatabaseColumn, error)
	QueryPage(context.Context, DatabaseQueryRequest) (DatabaseQueryResult, error)
	Close() error
}

type databaseConnection struct {
	info    DatabaseConnectionInfo
	session DatabaseSession
}

type activeDatabaseQuery struct {
	connectionID string
	runID        uint64
	cancel       context.CancelFunc
}

type DatabaseService struct {
	mu             sync.RWMutex
	providers      map[string]DatabaseProvider
	secretResolver DatabaseSecretResolver
	connections    map[string]*databaseConnection
	activeQueries  map[string]activeDatabaseQuery
	nextRunID      atomic.Uint64
}

func NewDatabaseService(resolvers ...DatabaseSecretResolver) *DatabaseService {
	service := &DatabaseService{
		providers:     make(map[string]DatabaseProvider),
		connections:   make(map[string]*databaseConnection),
		activeQueries: make(map[string]activeDatabaseQuery),
	}
	if len(resolvers) > 0 {
		service.secretResolver = resolvers[0]
	}
	service.providers["sqlite"] = &SQLiteDatabaseProvider{}
	service.providers["postgres"] = &PostgresDatabaseProvider{}
	service.providers["mysql"] = &MySQLDatabaseProvider{}
	return service
}

func (s *DatabaseService) RegisterProvider(name string, provider DatabaseProvider) error {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" || provider == nil {
		return errors.New("database provider name and implementation are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.providers[key]; exists {
		return fmt.Errorf("database provider %q is already registered", key)
	}
	s.providers[key] = provider
	return nil
}

func (s *DatabaseService) Connect(config DatabaseConnectionConfig) (DatabaseConnectionInfo, error) {
	config.ID = strings.TrimSpace(config.ID)
	config.Name = strings.TrimSpace(config.Name)
	config.Provider = strings.ToLower(strings.TrimSpace(config.Provider))
	config.CredentialConfigID = strings.TrimSpace(config.CredentialConfigID)
	config.DefaultSchema = strings.TrimSpace(config.DefaultSchema)
	if !databaseIdentifierPattern.MatchString(config.ID) {
		return DatabaseConnectionInfo{}, errors.New("database connection id must use 1-64 letters, numbers, dots, underscores, or hyphens")
	}
	if config.Provider == "" {
		return DatabaseConnectionInfo{}, errors.New("database provider is required")
	}
	if len(config.Name) > 128 {
		return DatabaseConnectionInfo{}, errors.New("database connection name must not exceed 128 characters")
	}
	if config.DefaultSchema != "" {
		if err := validateDatabaseIdentifier(config.DefaultSchema); err != nil {
			return DatabaseConnectionInfo{}, fmt.Errorf("invalid default database schema: %w", err)
		}
	}
	if config.Provider == "sqlite" && config.DefaultSchema == "" {
		config.DefaultSchema = "main"
	}
	if config.Provider == "postgres" && config.DefaultSchema == "" {
		config.DefaultSchema = "public"
	}

	s.mu.RLock()
	provider := s.providers[config.Provider]
	_, exists := s.connections[config.ID]
	s.mu.RUnlock()
	if exists {
		return DatabaseConnectionInfo{}, ErrDatabaseConnectionExists
	}
	if provider == nil {
		return DatabaseConnectionInfo{}, fmt.Errorf("unsupported database provider %q", config.Provider)
	}
	requiresCredential := config.Provider == "postgres" || config.Provider == "mysql"
	if requiresCredential || config.CredentialConfigID != "" {
		if !databaseIdentifierPattern.MatchString(config.CredentialConfigID) {
			return DatabaseConnectionInfo{}, errors.New("database credential config id is required and must use 1-64 letters, numbers, dots, underscores, or hyphens")
		}
		if s.secretResolver == nil {
			return DatabaseConnectionInfo{}, errors.New("database credential config cannot be resolved")
		}
		ctx, cancel := context.WithTimeout(context.Background(), databaseOpenTimeout)
		secret, err := s.secretResolver.ResolveDatabaseSecret(ctx, config.CredentialConfigID)
		cancel()
		if err != nil {
			return DatabaseConnectionInfo{}, wrapDatabaseOperationError("database credential config is unavailable", err)
		}
		if strings.TrimSpace(secret) == "" {
			return DatabaseConnectionInfo{}, errors.New("database credential config is unavailable")
		}
		config.resolvedDSN = secret
	}

	ctx, cancel := context.WithTimeout(context.Background(), databaseOpenTimeout)
	session, err := provider.Open(ctx, config)
	cancel()
	config.resolvedDSN = ""
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return DatabaseConnectionInfo{}, wrapDatabaseOperationError(
				fmt.Sprintf("open %s database connection timed out", config.Provider),
				err,
			)
		}
		return DatabaseConnectionInfo{}, wrapDatabaseOperationError(
			fmt.Sprintf("open %s database connection failed", config.Provider),
			err,
		)
	}
	name := config.Name
	if name == "" {
		name = config.ID
	}
	info := DatabaseConnectionInfo{
		ID:            config.ID,
		Name:          name,
		Provider:      config.Provider,
		DefaultSchema: config.DefaultSchema,
	}

	s.mu.Lock()
	if _, exists := s.connections[config.ID]; exists {
		s.mu.Unlock()
		return DatabaseConnectionInfo{}, errors.Join(
			ErrDatabaseConnectionExists,
			wrapDatabaseOperationError("close duplicate database connection", session.Close()),
		)
	}
	s.connections[config.ID] = &databaseConnection{info: info, session: session}
	s.mu.Unlock()
	return info, nil
}

func (s *DatabaseService) ListConnections() []DatabaseConnectionInfo {
	s.mu.RLock()
	result := make([]DatabaseConnectionInfo, 0, len(s.connections))
	for _, connection := range s.connections {
		result = append(result, connection.info)
	}
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (s *DatabaseService) Disconnect(connectionID string) error {
	s.mu.Lock()
	connection := s.connections[connectionID]
	if connection == nil {
		s.mu.Unlock()
		return ErrDatabaseConnectionNotFound
	}
	delete(s.connections, connectionID)
	for requestID, query := range s.activeQueries {
		if query.connectionID == connectionID {
			query.cancel()
			delete(s.activeQueries, requestID)
		}
	}
	s.mu.Unlock()
	return wrapDatabaseOperationError("close database connection", connection.session.Close())
}

func (s *DatabaseService) ListSchemas(connectionID string) ([]DatabaseSchema, error) {
	connection, err := s.connection(connectionID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), databaseOpenTimeout)
	defer cancel()
	schemas, err := connection.session.ListSchemas(ctx)
	if err != nil {
		return nil, wrapDatabaseOperationError("list database schemas", err)
	}
	return schemas, nil
}

func (s *DatabaseService) ListTables(connectionID, schema string) ([]DatabaseTable, error) {
	connection, err := s.connection(connectionID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(schema) == "" {
		schema = connection.info.DefaultSchema
	}
	if err := validateDatabaseIdentifier(schema); err != nil {
		return nil, fmt.Errorf("invalid database schema: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), databaseOpenTimeout)
	defer cancel()
	tables, err := connection.session.ListTables(ctx, schema)
	if err != nil {
		return nil, wrapDatabaseOperationError("list database tables", err)
	}
	return tables, nil
}

func (s *DatabaseService) DescribeTable(connectionID, schema, table string) ([]DatabaseColumn, error) {
	connection, err := s.connection(connectionID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(schema) == "" {
		schema = connection.info.DefaultSchema
	}
	if err := validateDatabaseIdentifier(schema); err != nil {
		return nil, fmt.Errorf("invalid database schema: %w", err)
	}
	if err := validateDatabaseIdentifier(table); err != nil {
		return nil, fmt.Errorf("invalid database table: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), databaseOpenTimeout)
	defer cancel()
	columns, err := connection.session.DescribeTable(ctx, schema, table)
	if err != nil {
		return nil, wrapDatabaseOperationError("describe database table", err)
	}
	return columns, nil
}

func (s *DatabaseService) QueryPage(request DatabaseQueryRequest) (DatabaseQueryResult, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.ConnectionID = strings.TrimSpace(request.ConnectionID)
	if request.RequestID == "" || len(request.RequestID) > 128 {
		return DatabaseQueryResult{}, errors.New("database query request id is required and must not exceed 128 characters")
	}
	if request.Page < 0 || request.Page > maxDatabasePage {
		return DatabaseQueryResult{}, fmt.Errorf("database query page must be between 0 and %d", maxDatabasePage)
	}
	if request.PageSize == 0 {
		request.PageSize = defaultDatabasePageSize
	}
	if request.PageSize < 1 || request.PageSize > maxDatabasePageSize {
		return DatabaseQueryResult{}, fmt.Errorf("database query page size must be between 1 and %d", maxDatabasePageSize)
	}
	if _, err := normalizeReadOnlySQL(request.SQL); err != nil {
		return DatabaseQueryResult{}, err
	}
	connection, err := s.connection(request.ConnectionID)
	if err != nil {
		return DatabaseQueryResult{}, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	runID := s.nextRunID.Add(1)
	s.mu.Lock()
	if previous, exists := s.activeQueries[request.RequestID]; exists {
		previous.cancel()
	}
	s.activeQueries[request.RequestID] = activeDatabaseQuery{
		connectionID: request.ConnectionID,
		runID:        runID,
		cancel:       cancel,
	}
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		if current, exists := s.activeQueries[request.RequestID]; exists && current.runID == runID {
			delete(s.activeQueries, request.RequestID)
		}
		s.mu.Unlock()
	}()

	started := time.Now()
	result, err := connection.session.QueryPage(ctx, request)
	if err != nil {
		return DatabaseQueryResult{}, wrapDatabaseOperationError("query database", err)
	}
	result.RequestID = request.RequestID
	result.Page = request.Page
	result.PageSize = request.PageSize
	result.DurationMs = time.Since(started).Milliseconds()
	return result, nil
}

func (s *DatabaseService) CancelQuery(requestID string) bool {
	s.mu.RLock()
	query, exists := s.activeQueries[requestID]
	s.mu.RUnlock()
	if !exists {
		return false
	}
	query.cancel()
	return true
}

func (s *DatabaseService) Close() error {
	s.mu.Lock()
	connections := make([]*databaseConnection, 0, len(s.connections))
	for _, connection := range s.connections {
		connections = append(connections, connection)
	}
	for _, query := range s.activeQueries {
		query.cancel()
	}
	s.connections = make(map[string]*databaseConnection)
	s.activeQueries = make(map[string]activeDatabaseQuery)
	s.mu.Unlock()

	var closeErr error
	for _, connection := range connections {
		closeErr = errors.Join(
			closeErr,
			wrapDatabaseOperationError("close database connection", connection.session.Close()),
		)
	}
	return closeErr
}

func (s *DatabaseService) connection(id string) (*databaseConnection, error) {
	s.mu.RLock()
	connection := s.connections[id]
	s.mu.RUnlock()
	if connection == nil {
		return nil, ErrDatabaseConnectionNotFound
	}
	return connection, nil
}

type SQLiteDatabaseProvider struct{}

func (p *SQLiteDatabaseProvider) Open(
	ctx context.Context,
	config DatabaseConnectionConfig,
) (DatabaseSession, error) {
	path := strings.TrimSpace(config.DatabasePath)
	if path == "" {
		return nil, errors.New("sqlite database path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, wrapDatabaseOperationError("resolve sqlite database path", err)
	}
	absPath, err = filepath.EvalSymlinks(absPath)
	if err != nil {
		return nil, wrapDatabaseOperationError("resolve sqlite database file", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, wrapDatabaseOperationError("inspect sqlite database file", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("sqlite database path must be a regular file")
	}

	slashPath := filepath.ToSlash(absPath)
	if runtime.GOOS == "windows" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	databaseURL := &url.URL{Scheme: "file", Path: slashPath}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	databaseURL.RawQuery = query.Encode()

	db, err := sqlx.Open("sqlite", databaseURL.String())
	if err != nil {
		return nil, wrapDatabaseOperationError("initialize sqlite driver", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, wrapDatabaseOperationError(
			"open sqlite database in read-only mode",
			errors.Join(err, db.Close()),
		)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return nil, wrapDatabaseOperationError(
			"enable sqlite query-only mode",
			errors.Join(err, db.Close()),
		)
	}
	return &sqliteDatabaseSession{db: db}, nil
}

type sqliteDatabaseSession struct {
	db *sqlx.DB
}

func (s *sqliteDatabaseSession) ListSchemas(context.Context) ([]DatabaseSchema, error) {
	return []DatabaseSchema{{Name: "main"}}, nil
}

func (s *sqliteDatabaseSession) ListTables(ctx context.Context, schema string) ([]DatabaseTable, error) {
	if schema != "main" {
		return nil, fmt.Errorf("sqlite schema %q is not available", schema)
	}
	var tables []DatabaseTable
	err := s.db.SelectContext(ctx, &tables, `
		SELECT 'main' AS schema, name, type
		FROM sqlite_schema
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list sqlite tables: %w", err)
	}
	return tables, nil
}

func (s *sqliteDatabaseSession) DescribeTable(
	ctx context.Context,
	schema string,
	table string,
) (result []DatabaseColumn, resultErr error) {
	if schema != "main" {
		return nil, fmt.Errorf("sqlite schema %q is not available", schema)
	}
	table = strings.TrimSpace(table)
	if err := validateDatabaseIdentifier(table); err != nil {
		return nil, err
	}
	var exists int
	if err := s.db.GetContext(
		ctx,
		&exists,
		"SELECT COUNT(*) FROM sqlite_schema WHERE name = ? AND type IN ('table', 'view')",
		table,
	); err != nil {
		return nil, fmt.Errorf("find sqlite table: %w", err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("database table %q not found", table)
	}

	quoted := quoteANSIIdentifier(table)
	rows, err := s.db.QueryxContext(ctx, "PRAGMA table_info("+quoted+")")
	if err != nil {
		return nil, fmt.Errorf("describe sqlite table: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				wrapDatabaseOperationError("close sqlite schema rows", closeErr),
			)
		}
	}()
	columns := make([]DatabaseColumn, 0)
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan sqlite column: %w", err)
		}
		column := DatabaseColumn{
			Name:       name,
			DataType:   dataType,
			Nullable:   notNull == 0,
			PrimaryKey: primaryKey != 0,
			Ordinal:    ordinal,
		}
		if defaultValue.Valid {
			column.DefaultValue = defaultValue.String
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sqlite columns: %w", err)
	}
	return columns, nil
}

func (s *sqliteDatabaseSession) QueryPage(
	ctx context.Context,
	request DatabaseQueryRequest,
) (DatabaseQueryResult, error) {
	return querySQLPage(ctx, s.db, sqlx.QUESTION, request)
}

func (s *sqliteDatabaseSession) Close() error {
	return s.db.Close()
}

type PostgresDatabaseProvider struct{}

func (p *PostgresDatabaseProvider) Open(
	ctx context.Context,
	config DatabaseConnectionConfig,
) (DatabaseSession, error) {
	dsn := strings.TrimSpace(config.resolvedDSN)
	if dsn == "" {
		return nil, errors.New("postgresql credential is unavailable")
	}
	if _, err := pgx.ParseConfig(dsn); err != nil {
		return nil, wrapDatabaseOperationError("postgresql credential is invalid", err)
	}
	return openRelationalDatabase(
		ctx,
		"pgx",
		dsn,
		"postgres",
		sqlx.DOLLAR,
		"SET default_transaction_read_only = on",
	)
}

type MySQLDatabaseProvider struct{}

func (p *MySQLDatabaseProvider) Open(
	ctx context.Context,
	config DatabaseConnectionConfig,
) (DatabaseSession, error) {
	dsn := strings.TrimSpace(config.resolvedDSN)
	if dsn == "" {
		return nil, errors.New("mysql credential is unavailable")
	}
	if _, err := mysqlDriver.ParseDSN(dsn); err != nil {
		return nil, wrapDatabaseOperationError("mysql credential is invalid", err)
	}
	return openRelationalDatabase(
		ctx,
		"mysql",
		dsn,
		"mysql",
		sqlx.QUESTION,
		"SET SESSION TRANSACTION READ ONLY",
	)
}

func openRelationalDatabase(
	ctx context.Context,
	driverName string,
	dsn string,
	provider string,
	bindType int,
	readOnlyStatement string,
) (DatabaseSession, error) {
	db, err := sqlx.Open(driverName, dsn)
	if err != nil {
		return nil, wrapDatabaseOperationError("initialize database driver", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, wrapDatabaseOperationError(
			"database connection failed",
			errors.Join(err, db.Close()),
		)
	}
	if _, err := db.ExecContext(ctx, readOnlyStatement); err != nil {
		return nil, wrapDatabaseOperationError(
			"enable database read-only mode",
			errors.Join(err, db.Close()),
		)
	}
	return &relationalDatabaseSession{
		db:       db,
		provider: provider,
		bindType: bindType,
	}, nil
}

type relationalDatabaseSession struct {
	db       *sqlx.DB
	provider string
	bindType int
}

func (s *relationalDatabaseSession) ListSchemas(ctx context.Context) ([]DatabaseSchema, error) {
	query := `
		SELECT schema_name AS name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('information_schema', 'pg_catalog', 'mysql', 'performance_schema', 'sys')
		  AND schema_name NOT LIKE 'pg_toast%'
		ORDER BY schema_name
	`
	var schemas []DatabaseSchema
	if err := s.db.SelectContext(ctx, &schemas, query); err != nil {
		return nil, wrapDatabaseOperationError(fmt.Sprintf("list %s schemas", s.provider), err)
	}
	return schemas, nil
}

func (s *relationalDatabaseSession) ListTables(
	ctx context.Context,
	schema string,
) ([]DatabaseTable, error) {
	if err := validateDatabaseIdentifier(schema); err != nil {
		return nil, err
	}
	query := s.db.Rebind(`
		SELECT table_schema AS schema,
		       table_name AS name,
		       LOWER(REPLACE(table_type, 'BASE ', '')) AS type
		FROM information_schema.tables
		WHERE table_schema = ? AND table_type IN ('BASE TABLE', 'VIEW')
		ORDER BY table_name
	`)
	var tables []DatabaseTable
	if err := s.db.SelectContext(ctx, &tables, query, schema); err != nil {
		return nil, wrapDatabaseOperationError(fmt.Sprintf("list %s tables", s.provider), err)
	}
	return tables, nil
}

func (s *relationalDatabaseSession) DescribeTable(
	ctx context.Context,
	schema string,
	table string,
) ([]DatabaseColumn, error) {
	if err := validateDatabaseIdentifier(schema); err != nil {
		return nil, err
	}
	if err := validateDatabaseIdentifier(table); err != nil {
		return nil, err
	}
	var query string
	if s.provider == "postgres" {
		query = `
			SELECT c.column_name AS name,
			       c.data_type AS data_type,
			       c.is_nullable = 'YES' AS nullable,
			       COALESCE(c.column_default, '') AS default_value,
			       EXISTS (
			         SELECT 1
			         FROM information_schema.table_constraints tc
			         JOIN information_schema.key_column_usage kcu
			           ON tc.constraint_name = kcu.constraint_name
			          AND tc.table_schema = kcu.table_schema
			         WHERE tc.constraint_type = 'PRIMARY KEY'
			           AND tc.table_schema = c.table_schema
			           AND tc.table_name = c.table_name
			           AND kcu.column_name = c.column_name
			       ) AS primary_key,
			       c.ordinal_position - 1 AS ordinal
			FROM information_schema.columns c
			WHERE c.table_schema = ? AND c.table_name = ?
			ORDER BY c.ordinal_position
		`
	} else {
		query = `
			SELECT column_name AS name,
			       data_type AS data_type,
			       is_nullable = 'YES' AS nullable,
			       COALESCE(column_default, '') AS default_value,
			       column_key = 'PRI' AS primary_key,
			       ordinal_position - 1 AS ordinal
			FROM information_schema.columns
			WHERE table_schema = ? AND table_name = ?
			ORDER BY ordinal_position
		`
	}
	query = s.db.Rebind(query)
	var columns []DatabaseColumn
	if err := s.db.SelectContext(ctx, &columns, query, schema, table); err != nil {
		return nil, wrapDatabaseOperationError(fmt.Sprintf("describe %s table", s.provider), err)
	}
	return columns, nil
}

func (s *relationalDatabaseSession) QueryPage(
	ctx context.Context,
	request DatabaseQueryRequest,
) (result DatabaseQueryResult, resultErr error) {
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DatabaseQueryResult{}, wrapDatabaseOperationError("begin read-only database query", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			resultErr = errors.Join(
				resultErr,
				wrapDatabaseOperationError("rollback read-only database query", rollbackErr),
			)
		}
	}()
	result, err = querySQLPage(ctx, tx, s.bindType, request)
	if err != nil {
		return DatabaseQueryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DatabaseQueryResult{}, wrapDatabaseOperationError("commit read-only database query", err)
	}
	return result, nil
}

func (s *relationalDatabaseSession) Close() error {
	return s.db.Close()
}

func querySQLPage(
	ctx context.Context,
	db sqlx.QueryerContext,
	bindType int,
	request DatabaseQueryRequest,
) (result DatabaseQueryResult, resultErr error) {
	query, err := normalizeReadOnlySQL(request.SQL)
	if err != nil {
		return DatabaseQueryResult{}, err
	}
	offset := request.Page * request.PageSize
	wrapped := "SELECT * FROM (" + query + ") AS _koyori_query LIMIT ? OFFSET ?"
	wrapped = sqlx.Rebind(bindType, wrapped)
	args := append(append([]any{}, request.Parameters...), request.PageSize+1, offset)
	rows, err := db.QueryxContext(ctx, wrapped, args...)
	if err != nil {
		return DatabaseQueryResult{}, wrapDatabaseOperationError("execute database query", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				wrapDatabaseOperationError("close database result rows", closeErr),
			)
		}
	}()

	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return DatabaseQueryResult{}, wrapDatabaseOperationError("read database result columns", err)
	}
	columns := make([]DatabaseQueryColumn, 0, len(columnTypes))
	for _, columnType := range columnTypes {
		nullable, _ := columnType.Nullable()
		columns = append(columns, DatabaseQueryColumn{
			Name:         columnType.Name(),
			DatabaseType: columnType.DatabaseTypeName(),
			Nullable:     nullable,
		})
	}

	resultRows := make([][]any, 0, request.PageSize)
	hasMore := false
	for rows.Next() {
		values, err := rows.SliceScan()
		if err != nil {
			return DatabaseQueryResult{}, wrapDatabaseOperationError("scan database result row", err)
		}
		if len(resultRows) == request.PageSize {
			hasMore = true
			break
		}
		for index, value := range values {
			values[index] = normalizeDatabaseValue(value)
		}
		resultRows = append(resultRows, values)
	}
	if err := rows.Err(); err != nil {
		return DatabaseQueryResult{}, wrapDatabaseOperationError("read database result rows", err)
	}
	return DatabaseQueryResult{Columns: columns, Rows: resultRows, HasMore: hasMore}, nil
}

func validateDatabaseIdentifier(identifier string) error {
	identifier = strings.TrimSpace(identifier)
	if !databaseObjectPattern.MatchString(identifier) {
		return errors.New("database identifier must use 1-128 letters, numbers, spaces, underscores, dots, dollars, or hyphens")
	}
	return nil
}

func quoteANSIIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func normalizeReadOnlySQL(query string) (string, error) {
	query = strings.TrimSpace(query)
	query = strings.TrimSpace(strings.TrimSuffix(query, ";"))
	if query == "" {
		return "", errors.New("database query is required")
	}
	if strings.Contains(query, ";") {
		return "", fmt.Errorf("%w: multiple statements are not allowed", ErrDatabaseReadOnlyQuery)
	}
	withoutComments := strings.TrimSpace(stripLeadingSQLComments(query))
	fields := strings.Fields(withoutComments)
	if len(fields) == 0 {
		return "", errors.New("database query is required")
	}
	keyword := strings.ToUpper(fields[0])
	if keyword != "SELECT" && keyword != "WITH" {
		return "", fmt.Errorf("%w: statement begins with %s", ErrDatabaseReadOnlyQuery, keyword)
	}
	return query, nil
}

func stripLeadingSQLComments(query string) string {
	for {
		query = strings.TrimSpace(query)
		switch {
		case strings.HasPrefix(query, "--"):
			newline := strings.IndexByte(query, '\n')
			if newline < 0 {
				return ""
			}
			query = query[newline+1:]
		case strings.HasPrefix(query, "/*"):
			end := strings.Index(query[2:], "*/")
			if end < 0 {
				return ""
			}
			query = query[end+4:]
		default:
			return query
		}
	}
}

func normalizeDatabaseValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		return typed
	}
}
