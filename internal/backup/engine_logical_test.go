package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestPlanForCloudflareD1UsesNativeExport(t *testing.T) {
	plan, err := PlanFor(&config.ConnectionConfig{Type: config.CloudflareD1})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Format != "sqlite_sql" || plan.Extension != ".sql" {
		t.Fatalf("D1 plan = %#v", plan)
	}
	if !strings.Contains(plan.ToolLabel, "Cloudflare D1 export API") || strings.Contains(strings.ToLower(plan.ToolLabel), "logical exporter") {
		t.Fatalf("D1 plan does not identify the native export API: %#v", plan)
	}
}

func TestPlanForRejectsServerProfileWithoutSpecificDatabase(t *testing.T) {
	for _, dbType := range []config.DBType{config.PostgreSQL, config.MySQL} {
		if _, err := PlanFor(&config.ConnectionConfig{Type: dbType}); err == nil || !strings.Contains(err.Error(), "specific") {
			t.Fatalf("PlanFor(%s server profile) error = %v, want specific database guidance", dbType, err)
		}
	}
}

func TestCreateNativeBackupCloudflareD1DownloadsNativeExport(t *testing.T) {
	const exportedSQL = "PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCREATE TABLE users(id INTEGER PRIMARY KEY);\nCOMMIT;\n"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/native-export.sql" {
			t.Errorf("download request = %s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/sql")
		_, _ = writer.Write([]byte(exportedSQL))
	}))
	defer server.Close()

	cfg := &config.ConnectionConfig{
		Name: "production-d1", Type: config.CloudflareD1,
		AccountID: "account-id", DatabaseID: "11111111-2222-3333-4444-555555555555", AuthToken: "api-token",
	}
	var exportRequested bool
	options := NativeOptions{
		d1HTTPClient: server.Client(),
		d1ExportURL: func(ctx context.Context, accountID, authToken, databaseID string) (string, error) {
			exportRequested = true
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if accountID != cfg.AccountID || authToken != cfg.AuthToken || databaseID != cfg.DatabaseID {
				t.Fatalf("native export arguments = %q, %q, %q", accountID, authToken, databaseID)
			}
			return server.URL + "/native-export.sql?signature=short-lived", nil
		},
	}
	output := filepath.Join(t.TempDir(), "d1.sql")
	if err := CreateNativeBackup(context.Background(), cfg, output, options); err != nil {
		t.Fatal(err)
	}
	if !exportRequested {
		t.Fatal("Cloudflare native export API was not requested")
	}
	payload, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != exportedSQL {
		t.Fatalf("downloaded D1 export = %q", payload)
	}
}

func TestCreateNativeBackupCloudflareD1FailsClosedOnUnsafeDownload(t *testing.T) {
	cfg := &config.ConnectionConfig{
		Name: "production-d1", Type: config.CloudflareD1,
		AccountID: "account-id", DatabaseID: "11111111-2222-3333-4444-555555555555", AuthToken: "api-token",
	}
	output := filepath.Join(t.TempDir(), "d1.sql")
	err := CreateNativeBackup(context.Background(), cfg, output, NativeOptions{
		d1ExportURL: func(context.Context, string, string, string) (string, error) {
			return "http://downloads.example.test/export.sql?signature=secret", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("unsafe signed URL error = %v", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("failed D1 export left destination behind: %v", statErr)
	}
}

func TestCloudflareD1DownloadErrorRedactsRedirectedSignedURL(t *testing.T) {
	const (
		originalSecret = "original-secret"
		redirectSecret = "redirect-secret"
	)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://redirected.example.test/export.sql?signature=" + redirectSecret}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    request,
			}, nil
		}
		return nil, fmt.Errorf("transport failed while requesting %s", request.URL.String())
	})}
	cfg := &config.ConnectionConfig{
		Name: "production-d1", Type: config.CloudflareD1,
		AccountID: "account-id", DatabaseID: "11111111-2222-3333-4444-555555555555", AuthToken: "api-token",
	}
	output := filepath.Join(t.TempDir(), "d1.sql")
	err := CreateNativeBackup(context.Background(), cfg, output, NativeOptions{
		d1HTTPClient: client,
		d1ExportURL: func(context.Context, string, string, string) (string, error) {
			return "https://original.example.test/export.sql?signature=" + originalSecret, nil
		},
	})
	if err == nil {
		t.Fatal("redirected download failure unexpectedly succeeded")
	}
	for _, secret := range []string{originalSecret, redirectSecret} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("download error leaked signed-URL secret %q: %v", secret, err)
		}
	}
	if requests != 2 {
		t.Fatalf("download requests = %d, want initial request plus redirect", requests)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("failed redirected download left destination behind: %v", statErr)
	}
}

func TestTursoLogicalDumpRejectsVirtualTablesAndRemovesStage(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE documents USING fts5(body); INSERT INTO documents(body) VALUES ('hello');`); err != nil {
		t.Fatalf("create FTS fixture: %v", err)
	}

	output := filepath.Join(t.TempDir(), "turso.sql")
	err = writeSQLiteCompatibleDump(context.Background(), db, config.Turso, output)
	if err == nil || !strings.Contains(err.Error(), `virtual table "documents"`) {
		t.Fatalf("virtual-table dump error = %v", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("rejected virtual-table dump left staging file behind: %v", statErr)
	}
}

func TestLogicalDumpClosesSchemaRowsBeforeColumnQueries(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT); INSERT INTO users(name) VALUES ('Ada');`); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output := filepath.Join(t.TempDir(), "turso.sql")
	if err := writeSQLiteCompatibleDump(ctx, db, config.Turso, output); err != nil {
		t.Fatalf("single-connection logical dump: %v", err)
	}
	payload, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `INSERT INTO "users" ("id", "name") VALUES(1, 'Ada');`) {
		t.Fatalf("logical dump omitted table data:\n%s", payload)
	}
}

func TestLogicalDumpRoundTripsMixedValuesIncludingNULText(t *testing.T) {
	source, restored := logicalDumpRoundTrip(t, `
		CREATE TABLE samples(id INTEGER PRIMARY KEY, value);
		INSERT INTO samples(value) VALUES
			(NULL), (''), ('O''Brien'), ('नमस्ते 🌏'),
			(CAST(X'6265666F7265006166746572' AS TEXT)),
			(CAST(X'00' AS TEXT)), (X'0001FF'), (X''),
			(-9223372036854775808), (9223372036854775807), (1.25);
	`)
	for id := 1; id <= 11; id++ {
		var sourceType, sourceValue, restoredType, restoredValue string
		query := `SELECT typeof(value), hex(value) FROM samples WHERE id = ?`
		if err := source.QueryRow(query, id).Scan(&sourceType, &sourceValue); err != nil {
			t.Fatal(err)
		}
		if err := restored.QueryRow(query, id).Scan(&restoredType, &restoredValue); err != nil {
			t.Fatal(err)
		}
		if sourceType != restoredType || sourceValue != restoredValue {
			t.Errorf("row %d: restored %s %s, want %s %s", id, restoredType, restoredValue, sourceType, sourceValue)
		}
	}
}

func TestLogicalDumpPreservesAUTOINCREMENTAfterDeletedRows(t *testing.T) {
	source, restored := logicalDumpRoundTrip(t, `
		CREATE TABLE events(id INTEGER PRIMARY KEY AUTOINCREMENT, message TEXT);
		INSERT INTO events(id, message) VALUES (1, 'retained'), (100, 'deleted');
		DELETE FROM events WHERE id = 100;
		CREATE TABLE emptied(id INTEGER PRIMARY KEY AUTOINCREMENT);
		INSERT INTO emptied(id) VALUES (200);
		DELETE FROM emptied;
	`)
	for _, table := range []string{"events", "emptied"} {
		var sourceID, restoredID int64
		query := `INSERT INTO ` + table + ` DEFAULT VALUES RETURNING id`
		if err := source.QueryRow(query).Scan(&sourceID); err != nil {
			t.Fatal(err)
		}
		if err := restored.QueryRow(query).Scan(&restoredID); err != nil {
			t.Fatal(err)
		}
		if restoredID != sourceID {
			t.Errorf("%s next ID = %d after restore, want %d", table, restoredID, sourceID)
		}
	}
}

func TestLogicalDumpRoundTripsAfterLastAUTOINCREMENTTableIsDropped(t *testing.T) {
	_, restored := logicalDumpRoundTrip(t, `
		CREATE TABLE removed(id INTEGER PRIMARY KEY AUTOINCREMENT);
		INSERT INTO removed DEFAULT VALUES;
		DROP TABLE removed;
		CREATE TABLE ordinary(id INTEGER PRIMARY KEY, value TEXT);
		INSERT INTO ordinary VALUES (1, 'retained');
	`)
	var value string
	if err := restored.QueryRow(`SELECT value FROM ordinary WHERE id = 1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "retained" {
		t.Fatalf("restored ordinary value = %q, want retained", value)
	}
}

func TestLogicalDumpRoundTripsUnusedAUTOINCREMENTTable(t *testing.T) {
	_, restored := logicalDumpRoundTrip(t, `CREATE TABLE empty(id INTEGER PRIMARY KEY AUTOINCREMENT);`)
	var id int64
	if err := restored.QueryRow(`INSERT INTO empty DEFAULT VALUES RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("unused AUTOINCREMENT table next ID = %d, want 1", id)
	}
}

func TestLogicalDumpPreservesUserObjectsStartingWithSQLite(t *testing.T) {
	_, restored := logicalDumpRoundTrip(t, `
		CREATE TABLE sqliteledger(id INTEGER PRIMARY KEY, amount INTEGER);
		INSERT INTO sqliteledger VALUES (1, 42);
		CREATE INDEX sqliteindex ON sqliteledger(amount);
		CREATE VIEW sqliteview AS SELECT amount FROM sqliteledger;
		CREATE TRIGGER sqlitetrigger AFTER INSERT ON sqliteledger
		BEGIN UPDATE sqliteledger SET amount = amount + 1 WHERE id = NEW.id; END;
	`)
	var amount int
	if err := restored.QueryRow(`SELECT amount FROM sqliteview WHERE amount = 42`).Scan(&amount); err != nil {
		t.Fatalf("restored user table/view: %v", err)
	}
	var count int
	if err := restored.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('sqliteindex', 'sqlitetrigger')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("restored user index/trigger count = %d, want 2", count)
	}
	if _, err := restored.Exec(`INSERT INTO sqliteledger VALUES (2, 50)`); err != nil {
		t.Fatal(err)
	}
	if err := restored.QueryRow(`SELECT amount FROM sqliteledger WHERE id = 2`).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != 51 {
		t.Fatalf("restored trigger produced amount = %d, want 51", amount)
	}
}

func logicalDumpRoundTrip(t *testing.T, fixture string) (*sql.DB, *sql.DB) {
	t.Helper()
	source, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	source.SetMaxOpenConns(1)
	if _, err := source.Exec(fixture); err != nil {
		t.Fatalf("create logical dump fixture: %v", err)
	}
	output := filepath.Join(t.TempDir(), "logical.sql")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := writeSQLiteCompatibleDump(ctx, source, config.Turso, output); err != nil {
		t.Fatalf("export logical dump: %v", err)
	}
	if err := validateSQLiteSQLForRestore(context.Background(), output); err != nil {
		t.Fatalf("logical dump is incompatible with guarded restore: %v", err)
	}
	payload, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	restored.SetMaxOpenConns(1)
	if _, err := restored.Exec(string(payload)); err != nil {
		t.Fatalf("restore logical dump: %v", err)
	}
	return source, restored
}
