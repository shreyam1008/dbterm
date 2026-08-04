package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestGenerateAgeIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "identity.txt")
	recipient, err := GenerateAgeIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := age.ParseX25519Recipient(recipient); err != nil {
		t.Fatalf("recipient is invalid: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "AGE-SECRET-KEY-") {
		t.Fatal("identity file does not contain a secret key")
	}
	if _, err := GenerateAgeIdentity(path); err == nil {
		t.Fatal("expected no-clobber error")
	}
}
