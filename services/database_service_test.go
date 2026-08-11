package services

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func createSQLiteFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sqlx.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close fixture: %v", err)
		}
	}()
	if _, err := db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			active BOOLEAN NOT NULL DEFAULT 1
		);
		INSERT INTO users (name, active) VALUES
			('Ada', 1), ('Linus', 1), ('Grace', 0), ('Ken', 1), ('Margaret', 1);
	`); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	return path
}

func TestDatabaseServiceSQLiteSchemaAndPagination(t *testing.T) {
	fixturePath := createSQLiteFixture(t)
	service := NewDatabaseService()
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})

	info, err := service.Connect(DatabaseConnectionConfig{
		ID:           "fixture",
		Name:         "Fixture",
		Provider:     "sqlite",
		DatabasePath: fixturePath,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if info.ID != "fixture" || info.Provider != "sqlite" {
		t.Fatalf("connection info = %#v", info)
	}

	schemas, err := service.ListSchemas("fixture")
	if err != nil {
		t.Fatalf("list schemas: %v", err)
	}
	if len(schemas) != 1 || schemas[0].Name != "main" {
		t.Fatalf("schemas = %#v", schemas)
	}

	tables, err := service.ListTables("fixture", "main")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "users" || tables[0].Type != "table" {
		t.Fatalf("tables = %#v", tables)
	}

	columns, err := service.DescribeTable("fixture", "main", "users")
	if err != nil {
		t.Fatalf("describe users: %v", err)
	}
	if len(columns) != 3 || columns[0].Name != "id" || !columns[0].PrimaryKey {
		t.Fatalf("columns = %#v", columns)
	}

	first, err := service.QueryPage(DatabaseQueryRequest{
		RequestID:    "page-1",
		ConnectionID: "fixture",
		SQL:          "SELECT id, name, active FROM users ORDER BY id",
		Page:         0,
		PageSize:     2,
	})
	if err != nil {
		t.Fatalf("query first page: %v", err)
	}
	if len(first.Rows) != 2 || !first.HasMore || first.RequestID != "page-1" {
		t.Fatalf("first page = %#v", first)
	}
	if got := first.Rows[0][1]; got != "Ada" {
		t.Fatalf("first row name = %#v", got)
	}

	last, err := service.QueryPage(DatabaseQueryRequest{
		RequestID:    "page-3",
		ConnectionID: "fixture",
		SQL:          "SELECT id, name FROM users ORDER BY id",
		Page:         2,
		PageSize:     2,
	})
	if err != nil {
		t.Fatalf("query last page: %v", err)
	}
	if len(last.Rows) != 1 || last.HasMore {
		t.Fatalf("last page = %#v", last)
	}
}

func TestDatabaseServiceSQLiteIsReadOnly(t *testing.T) {
	fixturePath := createSQLiteFixture(t)
	service := NewDatabaseService()
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})
	_, err := service.Connect(DatabaseConnectionConfig{
		ID:           "readonly",
		Provider:     "sqlite",
		DatabasePath: fixturePath,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	_, err = service.QueryPage(DatabaseQueryRequest{
		RequestID:    "write",
		ConnectionID: "readonly",
		SQL:          "UPDATE users SET active = 0",
		PageSize:     50,
	})
	if err == nil || !errors.Is(err, ErrDatabaseReadOnlyQuery) {
		t.Fatalf("write query error = %v", err)
	}

	result, err := service.QueryPage(DatabaseQueryRequest{
		RequestID:    "cte-write",
		ConnectionID: "readonly",
		SQL:          "WITH changed AS (UPDATE users SET active = 0 RETURNING id) SELECT * FROM changed",
		PageSize:     50,
	})
	if err == nil {
		t.Fatalf("writable CTE returned %#v", result)
	}

	connection, err := service.connection("readonly")
	if err != nil {
		t.Fatalf("get connection: %v", err)
	}
	session, ok := connection.session.(*sqliteDatabaseSession)
	if !ok {
		t.Fatalf("session type = %T", connection.session)
	}
	if _, err := session.db.Exec("UPDATE users SET active = 0"); err == nil {
		t.Fatal("SQLite connection accepted a write after bypassing query validation")
	}
}

func TestDatabaseServiceValidatesQueryBounds(t *testing.T) {
	service := NewDatabaseService()
	tests := []struct {
		name    string
		request DatabaseQueryRequest
		want    error
	}{
		{
			name: "negative page",
			request: DatabaseQueryRequest{
				RequestID: "negative", ConnectionID: "missing", SQL: "SELECT 1", Page: -1,
			},
		},
		{
			name: "excessive page",
			request: DatabaseQueryRequest{
				RequestID: "large-page", ConnectionID: "missing", SQL: "SELECT 1", Page: maxDatabasePage + 1,
			},
		},
		{
			name: "excessive page size",
			request: DatabaseQueryRequest{
				RequestID: "large-page-size", ConnectionID: "missing", SQL: "SELECT 1", PageSize: maxDatabasePageSize + 1,
			},
		},
		{
			name: "multiple statements",
			request: DatabaseQueryRequest{
				RequestID: "multiple", ConnectionID: "missing", SQL: "SELECT 1; SELECT 2", PageSize: 10,
			},
			want: ErrDatabaseReadOnlyQuery,
		},
		{
			name: "write statement",
			request: DatabaseQueryRequest{
				RequestID: "write", ConnectionID: "missing", SQL: "DELETE FROM users", PageSize: 10,
			},
			want: ErrDatabaseReadOnlyQuery,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.QueryPage(test.request)
			if err == nil {
				t.Fatal("QueryPage returned nil error")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("QueryPage error = %v, want %v", err, test.want)
			}
		})
	}
}

type blockingDatabaseProvider struct {
	started chan struct{}
}

func (p *blockingDatabaseProvider) Open(context.Context, DatabaseConnectionConfig) (DatabaseSession, error) {
	return &blockingDatabaseSession{started: p.started}, nil
}

type blockingDatabaseSession struct {
	started chan struct{}
}

func (s *blockingDatabaseSession) ListSchemas(context.Context) ([]DatabaseSchema, error) {
	return nil, nil
}

func (s *blockingDatabaseSession) ListTables(context.Context, string) ([]DatabaseTable, error) {
	return nil, nil
}

func (s *blockingDatabaseSession) DescribeTable(context.Context, string, string) ([]DatabaseColumn, error) {
	return nil, nil
}

func (s *blockingDatabaseSession) QueryPage(
	ctx context.Context,
	request DatabaseQueryRequest,
) (DatabaseQueryResult, error) {
	close(s.started)
	<-ctx.Done()
	return DatabaseQueryResult{RequestID: request.RequestID}, ctx.Err()
}

func (s *blockingDatabaseSession) Close() error { return nil }

func TestDatabaseServiceCancelQuery(t *testing.T) {
	started := make(chan struct{})
	service := NewDatabaseService()
	if err := service.RegisterProvider("blocking", &blockingDatabaseProvider{started: started}); err != nil {
		t.Fatalf("register blocking provider: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})
	if _, err := service.Connect(DatabaseConnectionConfig{
		ID:       "blocking",
		Provider: "blocking",
	}); err != nil {
		t.Fatalf("connect fake provider: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := service.QueryPage(DatabaseQueryRequest{
			RequestID:    "cancel-me",
			ConnectionID: "blocking",
			SQL:          "SELECT 1",
			PageSize:     10,
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("query did not start")
	}
	if !service.CancelQuery("cancel-me") {
		t.Fatal("CancelQuery returned false for active query")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("query error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("query did not stop after cancellation")
	}
}

func TestDatabaseServiceRejectsDuplicateConnectionID(t *testing.T) {
	fixturePath := createSQLiteFixture(t)
	service := NewDatabaseService()
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})
	config := DatabaseConnectionConfig{
		ID:           "duplicate",
		Provider:     "sqlite",
		DatabasePath: fixturePath,
	}
	if _, err := service.Connect(config); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if _, err := service.Connect(config); !errors.Is(err, ErrDatabaseConnectionExists) {
		t.Fatalf("duplicate connect error = %v", err)
	}
}

type fixedDatabaseSecretResolver struct {
	wantConfigID string
	secret       string
	calls        int
}

func (r *fixedDatabaseSecretResolver) ResolveDatabaseSecret(
	_ context.Context,
	configID string,
) (string, error) {
	r.calls++
	if configID != r.wantConfigID {
		return "", errors.New("unexpected credential config id")
	}
	return r.secret, nil
}

type recordingDatabaseProvider struct {
	opened  chan DatabaseConnectionConfig
	session DatabaseSession
}

func (p *recordingDatabaseProvider) Open(
	_ context.Context,
	config DatabaseConnectionConfig,
) (DatabaseSession, error) {
	p.opened <- config
	return p.session, nil
}

type recordingDatabaseSession struct{}

func (*recordingDatabaseSession) ListSchemas(context.Context) ([]DatabaseSchema, error) {
	return []DatabaseSchema{{Name: "analytics"}, {Name: "public"}}, nil
}

func (*recordingDatabaseSession) ListTables(_ context.Context, schema string) ([]DatabaseTable, error) {
	return []DatabaseTable{{Schema: schema, Name: "events", Type: "table"}}, nil
}

func (*recordingDatabaseSession) DescribeTable(
	_ context.Context,
	schema string,
	table string,
) ([]DatabaseColumn, error) {
	return []DatabaseColumn{{Name: schema + "." + table, DataType: "text"}}, nil
}

func (*recordingDatabaseSession) QueryPage(
	context.Context,
	DatabaseQueryRequest,
) (DatabaseQueryResult, error) {
	return DatabaseQueryResult{}, nil
}

func (*recordingDatabaseSession) Close() error { return nil }

func TestDatabaseServiceResolvesCredentialOnlyInsideBackend(t *testing.T) {
	const secret = "postgres://db-user:top-secret@db.internal/app"
	resolver := &fixedDatabaseSecretResolver{wantConfigID: "database-prod", secret: secret}
	service := NewDatabaseService(resolver)
	provider := &recordingDatabaseProvider{
		opened:  make(chan DatabaseConnectionConfig, 1),
		session: &recordingDatabaseSession{},
	}
	if err := service.RegisterProvider("recording", provider); err != nil {
		t.Fatalf("register provider: %v", err)
	}

	info, err := service.Connect(DatabaseConnectionConfig{
		ID:                 "prod",
		Name:               "Production",
		Provider:           "recording",
		CredentialConfigID: "database-prod",
		DefaultSchema:      "analytics",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	opened := <-provider.opened
	if opened.resolvedDSN != secret {
		t.Fatal("provider did not receive the resolved secret")
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal connection info: %v", err)
	}
	if strings.Contains(string(encoded), "top-secret") || strings.Contains(string(encoded), "db.internal") {
		t.Fatalf("connection info leaked DSN: %s", encoded)
	}
	if info.DefaultSchema != "analytics" {
		t.Fatalf("default schema = %q", info.DefaultSchema)
	}

	schemas, err := service.ListSchemas("prod")
	if err != nil || len(schemas) != 2 {
		t.Fatalf("schemas = %#v, err = %v", schemas, err)
	}
	tables, err := service.ListTables("prod", "analytics")
	if err != nil || len(tables) != 1 || tables[0].Schema != "analytics" {
		t.Fatalf("tables = %#v, err = %v", tables, err)
	}
	columns, err := service.DescribeTable("prod", "analytics", "events")
	if err != nil || len(columns) != 1 || columns[0].Name != "analytics.events" {
		t.Fatalf("columns = %#v, err = %v", columns, err)
	}
}

func TestDatabaseServiceRegistersRelationalProvidersWithoutNetwork(t *testing.T) {
	service := NewDatabaseService()
	for _, provider := range []string{"sqlite", "postgres", "mysql"} {
		if service.providers[provider] == nil {
			t.Errorf("provider %q is not registered", provider)
		}
	}
	for _, provider := range []string{"postgres", "mysql"} {
		_, err := service.Connect(DatabaseConnectionConfig{ID: provider, Provider: provider})
		if err == nil || !strings.Contains(err.Error(), "credential config") {
			t.Errorf("%s missing credential error = %v", provider, err)
		}
	}
}

func TestRelationalProviderParseErrorsDoNotExposeCredentials(t *testing.T) {
	tests := []struct {
		provider string
		secret   string
	}{
		{provider: "postgres", secret: "postgres://db-user:top-secret@%zz/app"},
		{provider: "mysql", secret: "db-user:top-secret@tcp([broken)/app"},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			resolver := &fixedDatabaseSecretResolver{
				wantConfigID: "database-invalid",
				secret:       test.secret,
			}
			service := NewDatabaseService(resolver)
			_, err := service.Connect(DatabaseConnectionConfig{
				ID:                 test.provider,
				Provider:           test.provider,
				CredentialConfigID: "database-invalid",
			})
			if err == nil {
				t.Fatal("Connect returned nil error")
			}
			message := err.Error()
			for _, sensitive := range []string{"top-secret", "db-user", "%zz", "[broken"} {
				if strings.Contains(message, sensitive) {
					t.Fatalf("connection error leaked %q: %s", sensitive, message)
				}
			}
		})
	}
}

func TestValidateDatabaseIdentifier(t *testing.T) {
	for _, identifier := range []string{"main", "public", "tenant-1", "reporting data", "schema_name"} {
		if err := validateDatabaseIdentifier(identifier); err != nil {
			t.Errorf("valid identifier %q rejected: %v", identifier, err)
		}
	}
	for _, identifier := range []string{"", "public\"; DROP TABLE users; --", "a/b", "line\nbreak"} {
		if err := validateDatabaseIdentifier(identifier); err == nil {
			t.Errorf("invalid identifier %q accepted", identifier)
		}
	}
}
