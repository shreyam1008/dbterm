package backup

import (
	"os"
	"path/filepath"
	"runtime"
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
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("identity mode = %04o, want 0600", got)
		}
		directoryInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if got := directoryInfo.Mode().Perm(); got != 0o700 {
			t.Fatalf("identity directory mode = %04o, want 0700", got)
		}
	}
}

func TestGenerateAgeIdentityRejectsEmptyPath(t *testing.T) {
	if _, err := GenerateAgeIdentity(" \t\r\n"); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("GenerateAgeIdentity() error = %v, want required-path error", err)
	}
}

func TestVerifyAgeIdentityRecipient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.txt")
	recipient, err := GenerateAgeIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgeIdentityRecipient(path, recipient); err != nil {
		t.Fatalf("VerifyAgeIdentityRecipient() error = %v", err)
	}
}

func TestVerifyAgeIdentityRecipientRejectsDifferentIdentityWithoutLeakingSecret(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first.txt")
	firstRecipient, err := GenerateAgeIdentity(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(t.TempDir(), "second.txt")
	if _, err := GenerateAgeIdentity(secondPath); err != nil {
		t.Fatal(err)
	}
	secret, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	err = VerifyAgeIdentityRecipient(secondPath, firstRecipient)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("VerifyAgeIdentityRecipient() error = %v, want mismatch", err)
	}
	for _, line := range strings.Split(string(secret), "\n") {
		if strings.HasPrefix(line, "AGE-SECRET-KEY-") && strings.Contains(err.Error(), line) {
			t.Fatalf("VerifyAgeIdentityRecipient() exposed private identity: %v", err)
		}
	}
}

func TestVerifyAgeIdentityRecipientRejectsInvalidRecipientWithoutEchoingIt(t *testing.T) {
	const invalid = "not-an-age-recipient-secret-marker"
	err := VerifyAgeIdentityRecipient("unused", invalid)
	if err == nil || strings.Contains(err.Error(), invalid) {
		t.Fatalf("VerifyAgeIdentityRecipient() error = %v, want redacted invalid-recipient error", err)
	}
}
