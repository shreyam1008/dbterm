package backup

import (
	"context"
	"errors"
	"fmt"
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
