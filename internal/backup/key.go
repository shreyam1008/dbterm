package backup

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/shreyam1008/dbterm/internal/privatefile"
)

// GenerateAgeIdentity creates a private age X25519 identity with no-clobber
// semantics. Jobs store only the returned public recipient.
func GenerateAgeIdentity(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("age identity output path is required")
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return "", fmt.Errorf("generate age identity: %w", err)
	}
	path = filepath.Clean(path)
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create age key directory: %w", err)
	}
	file, err := privatefile.Create(path)
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
	if err := syncDirectory(directory); err != nil {
		return "", fmt.Errorf("sync age identity directory: %w", err)
	}
	cleanup = false
	return recipient, nil
}

// VerifyAgeIdentityRecipient proves that the private identity file can decrypt
// bytes encrypted to recipient. It is intentionally separate from backup
// generation: unattended producer jobs need only the public recipient, while
// operators should keep and test the private identity on independent storage.
func VerifyAgeIdentityRecipient(path, recipient string) error {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return fmt.Errorf("age public recipient is required")
	}
	parsedRecipient, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return fmt.Errorf("age public recipient is invalid")
	}
	identities, err := readAgeIdentities(path)
	if err != nil {
		return err
	}

	const challenge = "dbterm age recovery check v1"
	var encrypted bytes.Buffer
	writer, err := age.Encrypt(&encrypted, parsedRecipient)
	if err != nil {
		return fmt.Errorf("start age recovery check: %w", err)
	}
	if _, err := io.WriteString(writer, challenge); err != nil {
		return fmt.Errorf("write age recovery check: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finalize age recovery check: %w", err)
	}
	reader, err := age.Decrypt(bytes.NewReader(encrypted.Bytes()), identities...)
	if err != nil {
		return fmt.Errorf("age identity does not match the public recipient")
	}
	decoded, err := io.ReadAll(io.LimitReader(reader, int64(len(challenge)+1)))
	if err != nil {
		return fmt.Errorf("read age recovery check: %w", err)
	}
	if string(decoded) != challenge {
		return fmt.Errorf("age identity recovery check produced unexpected data")
	}
	return nil
}
