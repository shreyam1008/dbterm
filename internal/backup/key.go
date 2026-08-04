package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
)

// GenerateAgeIdentity creates a private age X25519 identity with no-clobber
// semantics. Jobs store only the returned public recipient.
func GenerateAgeIdentity(path string) (string, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return "", fmt.Errorf("generate age identity: %w", err)
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create age key directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("age identity already exists: %s", path)
		}
		return "", fmt.Errorf("create age identity: %w", err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	recipient := identity.Recipient().String()
	contents := fmt.Sprintf("# dbterm age identity created %s\n# public key: %s\n%s\n", time.Now().UTC().Format(time.RFC3339), recipient, identity.String())
	if _, err := file.WriteString(contents); err != nil {
		return "", fmt.Errorf("write age identity: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync age identity: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close age identity: %w", err)
	}
	cleanup = false
	return recipient, nil
}
