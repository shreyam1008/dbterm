package mcpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shreyam1008/dbterm/internal/config"
	_ "modernc.org/sqlite"
)

func TestSQLiteInspectionQueryAndFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE visits (id INTEGER PRIMARY KEY, person_id INTEGER NOT NULL, note TEXT, FOREIGN KEY(person_id) REFERENCES people(id));
INSERT INTO people(id,name) VALUES (1,'Shreyam');
INSERT INTO visits(id,person_id,note) VALUES (10,1,'first'),(11,1,'second');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := config.ConnectionConfig{ID: "local", Name: "Local", Type: config.SQLite, FilePath: path, ReadOnly: true, Active: true}
	var audit bytes.Buffer
	service := newService(Options{
		ConnectionScope: "active", AuditWriter: &audit,
		StoreLoader: func() (*config.Store, error) { return &config.Store{Connections: []config.ConnectionConfig{cfg}}, nil },
	})

	metadata, err := service.inspectTable(context.Background(), inspectTableInput{Table: "visits"})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.Columns) != 3 || len(metadata.ForeignKeys) != 1 {
		t.Fatalf("metadata = %#v", metadata)
	}

	query, err := service.queryReadOnly(context.Background(), readQueryInput{SQL: "SELECT id, name FROM people", MaxRows: 5})
	if err != nil {
		t.Fatal(err)
	}
	if query.RowCount != 1 || query.Rows[0]["name"] != "Shreyam" {
		t.Fatalf("query = %#v", query)
	}

	followed, err := service.followRecord(context.Background(), followRecordInput{Table: "people", Key: map[string]any{"id": 1}, RowsPerLink: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(followed.Related) != 1 || followed.Related[0].Direction != "incoming" || len(followed.Related[0].Rows) != 2 {
		t.Fatalf("followed = %#v", followed)
	}
	if !bytes.Contains(audit.Bytes(), []byte(`"tool":"query_read_only"`)) {
		t.Fatalf("missing query audit: %s", audit.String())
	}
}

func TestSQLiteDriverReadOnlyTransactionBlocksWrites(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "readonly.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE guarded(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	service := newService(Options{AuditWriter: &bytes.Buffer{}})
	readonlyDB, queryCtx, closeFn, err := service.connect(context.Background(), config.ConnectionConfig{ID: "sqlite", Type: config.SQLite, FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	tx, err := readonlyDB.BeginTx(queryCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO guarded(id) VALUES (1)`); err == nil {
		t.Fatal("MCP SQLite connection accepted a write")
	}
}

func TestListConnectionsNeverReturnsSecrets(t *testing.T) {
	t.Parallel()
	secret := "do-not-return-password"
	token := "do-not-return-token"
	service := newService(Options{
		ConnectionScope: "all", AuditWriter: &bytes.Buffer{},
		StoreLoader: func() (*config.Store, error) {
			return &config.Store{Connections: []config.ConnectionConfig{{
				ID: "one", Name: "prod", Type: config.PostgreSQL, Host: "db.example", Port: "5432",
				User: "agent", Password: secret, AuthToken: token,
			}}}, nil
		},
	})
	result, err := service.listConnections()
	if err != nil {
		t.Fatal(err)
	}
	text := result.Connections[0].Endpoint + result.Connections[0].Name + result.Connections[0].Database
	if bytes.Contains([]byte(text), []byte(secret)) || bytes.Contains([]byte(text), []byte(token)) {
		t.Fatalf("secret leaked in %#v", result)
	}
	if result.Connections[0].Endpoint != "db.example:5432" {
		t.Fatalf("unexpected summary: %#v", result)
	}
}

func TestListConnectionsSanitizesURLCredentials(t *testing.T) {
	t.Parallel()
	service := newService(Options{
		ConnectionScope: "all", AuditWriter: &bytes.Buffer{},
		StoreLoader: func() (*config.Store, error) {
			return &config.Store{Connections: []config.ConnectionConfig{{
				ID: "remote", Name: "remote", Type: config.Turso,
				Host: "libsql://user:password@db.example/private?authToken=secret#fragment",
			}}}, nil
		},
	})
	result, err := service.listConnections()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := result.Connections[0].Endpoint
	if endpoint != "libsql://db.example" || strings.Contains(endpoint, "password") || strings.Contains(endpoint, "secret") {
		t.Fatalf("unsafe endpoint %q", endpoint)
	}
}

func TestCollectRowsEnforcesTotalOutputBudget(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE large_rows(value TEXT); INSERT INTO large_rows VALUES (printf('%0500d', 1)), (printf('%0500d', 2));`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT value FROM large_rows`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := collectRows(rows, 10, limits{maxColumns: 10, maxCellBytes: 4096, maxOutputBytes: 200})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Rows) != 0 {
		t.Fatalf("budget was not enforced: %#v", result)
	}
}

func TestBoundedRelationshipEnforcesAggregateBudget(t *testing.T) {
	t.Parallel()
	used := 0
	relation, ok := boundedRelationship(relatedRows{Direction: "incoming", Constraint: "fk", Table: "child", Rows: []map[string]any{{"value": strings.Repeat("x", 200)}}}, &used, 100)
	if !ok || !relation.Truncated || len(relation.Rows) != 0 {
		t.Fatalf("relation = %#v ok=%t used=%d", relation, ok, used)
	}
}

func TestMetadataOutputsEnforceAggregateBudget(t *testing.T) {
	t.Parallel()
	databaseOutput := boundInspectDatabaseOutput(inspectDatabaseOutput{
		Schemas: []string{strings.Repeat("s", 400)},
		Tables:  []string{strings.Repeat("t", 400)},
	}, 256)
	if !databaseOutput.Truncated || encodedSize(databaseOutput) > 256 {
		t.Fatalf("database metadata was not bounded: %#v", databaseOutput)
	}

	tableOutput := boundInspectTableOutput(inspectTableOutput{
		Table:       "people",
		Columns:     []columnInfo{{Name: strings.Repeat("c", 400)}},
		ForeignKeys: []foreignKeyInfo{{Name: strings.Repeat("f", 400)}},
	}, 256)
	if !tableOutput.Truncated || encodedSize(tableOutput) > 256 {
		t.Fatalf("table metadata was not bounded: %#v", tableOutput)
	}
}

func TestSameTablePreservesSchemaIdentity(t *testing.T) {
	t.Parallel()
	postgres := config.ConnectionConfig{Type: config.PostgreSQL}
	if !sameTable(postgres, "public.people", "people") {
		t.Fatal("unqualified PostgreSQL table should resolve to public")
	}
	if sameTable(postgres, "other.people", "people") {
		t.Fatal("different PostgreSQL schemas must not be treated as the same table")
	}

	mysql := config.ConnectionConfig{Type: config.MySQL, Database: "app"}
	if !sameTable(mysql, "app.people", "people") {
		t.Fatal("unqualified MySQL table should resolve to the saved database")
	}
	if sameTable(mysql, "archive.people", "people") {
		t.Fatal("different MySQL schemas must not be treated as the same table")
	}
}

func TestToolDiscoveryHidesProfileWriterByDefault(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		allow      bool
		wantWriter bool
	}{
		{name: "default", allow: false, wantWriter: false},
		{name: "explicit", allow: true, wantWriter: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			clientTransport, serverTransport := mcp.NewInMemoryTransports()
			server := New(Options{AllowProfileWrites: test.allow, AuditWriter: &bytes.Buffer{}})
			serverSession, err := server.Connect(ctx, serverTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer serverSession.Close()
			client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			clientSession, err := client.Connect(ctx, clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer clientSession.Close()
			listed, err := clientSession.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			foundWriter := false
			for _, tool := range listed.Tools {
				if tool.Name == "save_connection_profile" {
					foundWriter = true
					if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
						t.Fatalf("profile replacement tool must be marked destructive: %#v", tool.Annotations)
					}
					if tool.Annotations.IdempotentHint {
						t.Fatalf("profile create without an ID is not idempotent: %#v", tool.Annotations)
					}
				}
			}
			if foundWriter != test.wantWriter {
				t.Fatalf("writer present = %t, want %t", foundWriter, test.wantWriter)
			}
		})
	}
}

func TestNormalizeRunErrorTreatsClientCloseAsClean(t *testing.T) {
	t.Parallel()
	for _, err := range []error{nil, io.EOF, fmt.Errorf("server is closing: %w", io.EOF), context.Canceled} {
		if got := normalizeRunError(err); got != nil {
			t.Fatalf("normalizeRunError(%v) = %v", err, got)
		}
	}
	want := errors.New("transport failed")
	if got := normalizeRunError(want); !errors.Is(got, want) {
		t.Fatalf("unexpected non-close normalization: %v", got)
	}
}

func TestSaveProfileDefaultsReadOnlyAndNeverEchoesSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := &config.Store{}
	service := newService(Options{
		AllowProfileWrites: true, ConnectionScope: "all", AuditWriter: &bytes.Buffer{},
		StoreLoader: func() (*config.Store, error) { return store, nil },
		Connector:   func(*config.ConnectionConfig) (*sql.DB, error) { return sql.Open("sqlite", ":memory:") },
	})
	output, err := service.saveProfile(context.Background(), saveProfileInput{
		Name: "agent-created", Type: config.SQLite, FilePath: filepath.Join(t.TempDir(), "db.sqlite"),
		Password: "password-secret", AuthToken: "token-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Created || !output.Connection.ReadOnly || len(store.Connections) != 1 {
		t.Fatalf("output=%#v store=%#v", output, store.Connections)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("password-secret")) || bytes.Contains(encoded, []byte("token-secret")) {
		t.Fatalf("secret leaked: %s", encoded)
	}
	if store.Connections[0].Password != "" || store.Connections[0].AuthToken != "" {
		t.Fatalf("irrelevant secrets were persisted: %#v", store.Connections[0])
	}
}

func TestSaveD1ProfileDefaultsReadOnlyAndKeepsTokenWriteOnly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := &config.Store{}
	service := newService(Options{
		AllowProfileWrites: true, ConnectionScope: "all", AuditWriter: &bytes.Buffer{},
		StoreLoader: func() (*config.Store, error) { return store, nil },
		Connector:   func(*config.ConnectionConfig) (*sql.DB, error) { return sql.Open("sqlite", ":memory:") },
	})
	token := "d1-write-only-token"
	output, err := service.saveProfile(context.Background(), saveProfileInput{
		Name: "agent-d1", Type: config.CloudflareD1,
		AccountID: "account-id", DatabaseID: "database-id", AuthToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Created || !output.Connection.ReadOnly || len(store.Connections) != 1 || !store.Connections[0].ReadOnly {
		t.Fatalf("output=%#v store=%#v", output, store.Connections)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(token)) {
		t.Fatalf("D1 token leaked in MCP output: %s", encoded)
	}
}
