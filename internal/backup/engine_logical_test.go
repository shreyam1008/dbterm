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
