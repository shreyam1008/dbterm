package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestRedactConnectionErrorRemovesStoredSecrets(t *testing.T) {
	cfg := &config.ConnectionConfig{Password: "database-password", AuthToken: "cloud-token"}
	err := redactConnectionError(fmt.Errorf("connect failed with %s and %s", cfg.Password, cfg.AuthToken), cfg)
	if err == nil {
		t.Fatal("redactConnectionError returned nil")
	}
	for _, secret := range []string{cfg.Password, cfg.AuthToken} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("redacted error still contains %q: %s", secret, err)
		}
	}
	if count := strings.Count(err.Error(), "[redacted]"); count != 2 {
		t.Fatalf("redacted error contains %d markers, want 2: %s", count, err)
	}
	wrapped := redactConnectionError(fmt.Errorf("%s: %w", cfg.Password, context.Canceled), cfg)
	if !errors.Is(wrapped, context.Canceled) {
		t.Fatalf("redaction lost the cancellation cause: %v", wrapped)
	}
}

func TestVerifyNativeBackupRequiresRecognizableLogicalStructure(t *testing.T) {
	tests := []struct {
		name    string
		dbType  config.DBType
		payload string
		wantErr bool
	}{
		{name: "mysql dump header", dbType: config.MySQL, payload: "-- MySQL dump 10.13\nCREATE TABLE users(id INT);\n"},
		{name: "mariadb sandbox header", dbType: config.MySQL, payload: "/*M!999999\\- enable the sandbox mode */\nCREATE TABLE users(id INT);\n"},
		{name: "turso exporter header", dbType: config.Turso, payload: "PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCOMMIT;\n"},
		{name: "d1 recognizable sqlite sql", dbType: config.CloudflareD1, payload: "CREATE TABLE users(id INTEGER);\nINSERT INTO users VALUES(1);\n"},
		{name: "html is not mysql", dbType: config.MySQL, payload: "<html><body>upstream error</body></html>", wantErr: true},
		{name: "sqlite header is not mysql", dbType: config.MySQL, payload: "PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCOMMIT;\n", wantErr: true},
		{name: "comments are not d1 sql", dbType: config.CloudflareD1, payload: "-- export completed without a payload\n", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "native.sql")
			if err := os.WriteFile(path, []byte(test.payload), 0o600); err != nil {
				t.Fatal(err)
			}
			err := verifyNativeBackup(context.Background(), &config.ConnectionConfig{Type: test.dbType}, path)
			if test.wantErr && err == nil {
				t.Fatal("verifyNativeBackup() accepted a structurally invalid logical artifact")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("verifyNativeBackup() error = %v", err)
			}
		})
	}
}
