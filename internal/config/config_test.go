package config

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

func TestBuildMySQLConnStringPreservesTemporalLexemes(t *testing.T) {
	cfg := ConnectionConfig{
		Type: MySQL, Host: "localhost", Port: "3306", User: "dbterm", Database: "app",
	}
	parsed, err := mysql.ParseDSN(cfg.BuildConnString())
	if err != nil {
		t.Fatalf("parse MySQL DSN: %v", err)
	}
	if parsed.ParseTime {
		t.Fatal("MySQL DSN enables parseTime; zero dates and fractional precision would be lost")
	}
}

func TestBuildConnStringCloudflareD1UsesDriverDSN(t *testing.T) {
	cfg := ConnectionConfig{
		Type:       CloudflareD1,
		AccountID:  "account-id",
		DatabaseID: "01234567-89ab-cdef-0123-456789abcdef",
		AuthToken:  "token:/with?reserved#characters",
	}

	dsn := cfg.BuildConnString()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse D1 DSN %q: %v", dsn, err)
	}
	if parsed.Scheme != "d1" {
		t.Fatalf("D1 DSN scheme = %q, want d1", parsed.Scheme)
	}
	if parsed.Host != cfg.DatabaseID {
		t.Fatalf("D1 DSN host = %q, want database ID %q", parsed.Host, cfg.DatabaseID)
	}
	if parsed.User == nil || parsed.User.Username() != cfg.AccountID {
		t.Fatalf("D1 DSN account = %v, want %q", parsed.User, cfg.AccountID)
	}
	password, ok := parsed.User.Password()
	if !ok || password != cfg.AuthToken {
		t.Fatalf("D1 DSN token round trip = %q, %v; want %q, true", password, ok, cfg.AuthToken)
	}
	if got := cfg.DriverName(); got != "dbterm-d1" {
		t.Fatalf("D1 driver = %q, want dbterm's ordered raw adapter", got)
	}
}

func TestPostgreSQLServerProfileUsesMaintenanceDatabase(t *testing.T) {
	cfg := ConnectionConfig{Type: PostgreSQL, Host: "localhost", Port: "5432", User: "alice"}
	parsed, err := url.Parse(cfg.BuildConnString())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/postgres" {
		t.Fatalf("server profile path = %q, want /postgres", parsed.Path)
	}
	if cfg.Database != "" {
		t.Fatalf("BuildConnString mutated saved default database to %q", cfg.Database)
	}
}

func useTestConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DBTERM_CONFIG_DIR", dir)
	return dir
}

func TestLoadStoreGeneratesAndPersistsMissingConnectionIDs(t *testing.T) {
	dir := useTestConfigDir(t)
	path := filepath.Join(dir, "connections.json")
	original := []ConnectionConfig{
		{ID: "existing-id", Name: "existing", Type: PostgreSQL},
		{Name: "legacy-one", Type: MySQL},
		{Name: "legacy-two", Type: SQLite, FilePath: "local.db"},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal legacy connections: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write legacy connections: %v", err)
	}

	store, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error = %v", err)
	}
	if len(store.Connections) != 3 {
		t.Fatalf("connection count = %d, want 3", len(store.Connections))
	}
	if store.Connections[0].ID != "existing-id" {
		t.Fatalf("existing ID changed to %q", store.Connections[0].ID)
	}
	firstGenerated := store.Connections[1].ID
	secondGenerated := store.Connections[2].ID
	if firstGenerated == "" || secondGenerated == "" || firstGenerated == secondGenerated {
		t.Fatalf("generated IDs are not non-empty and unique: %q, %q", firstGenerated, secondGenerated)
	}

	reloaded, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore(reload) error = %v", err)
	}
	if reloaded.Connections[0].ID != "existing-id" || reloaded.Connections[1].ID != firstGenerated || reloaded.Connections[2].ID != secondGenerated {
		t.Fatalf("connection IDs were not stable after reload: %#v", reloaded.Connections)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat connections file: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("connections file mode = %o, want 600", got)
		}
	}
}

func TestStoreUpdatePreservesConnectionID(t *testing.T) {
	dir := useTestConfigDir(t)
	store := &Store{configPath: filepath.Join(dir, "connections.json")}
	if err := store.Add(ConnectionConfig{Name: "before", Type: PostgreSQL}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	originalID := store.Connections[0].ID
	if originalID == "" {
		t.Fatal("Add() did not generate an ID")
	}

	updated := ConnectionConfig{ID: "caller-supplied-replacement", Name: "after", Type: MySQL}
	if err := store.Update(0, updated); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got := store.Connections[0].ID; got != originalID {
		t.Fatalf("Update() changed ID from %q to %q", originalID, got)
	}
	if store.Connections[0].Name != "after" {
		t.Fatalf("Update() did not update editable fields: %#v", store.Connections[0])
	}

	reloaded, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error = %v", err)
	}
	if got := reloaded.Connections[0].ID; got != originalID {
		t.Fatalf("persisted ID = %q, want %q", got, originalID)
	}
}

func TestStoreAddRejectsDuplicateConnectionID(t *testing.T) {
	dir := useTestConfigDir(t)
	store := &Store{configPath: filepath.Join(dir, "connections.json")}
	if err := store.Add(ConnectionConfig{ID: "stable-id", Name: "one", Type: PostgreSQL}); err != nil {
		t.Fatalf("first Add() error = %v", err)
	}
	if err := store.Add(ConnectionConfig{ID: "stable-id", Name: "two", Type: MySQL}); err == nil {
		t.Fatal("second Add() accepted a duplicate ID")
	}
	if len(store.Connections) != 1 {
		t.Fatalf("duplicate Add() changed store length to %d", len(store.Connections))
	}
}

func TestLoadStoreRejectsDuplicatePersistedConnectionIDs(t *testing.T) {
	dir := useTestConfigDir(t)
	path := filepath.Join(dir, "connections.json")
	data := []byte(`[
  {"id":"duplicate","name":"one","type":"postgresql"},
  {"id":"duplicate","name":"two","type":"mysql"}
]`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write duplicate connections: %v", err)
	}

	store, err := LoadStore()
	if err == nil {
		t.Fatal("LoadStore() accepted duplicate persisted IDs")
	}
	if store == nil || len(store.Connections) != 2 {
		t.Fatalf("LoadStore() should retain decoded connections for diagnosis: %#v", store)
	}
}
