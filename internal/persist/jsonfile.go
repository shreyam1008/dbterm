package persist

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	appConfigDirName = "dbterm"
	defaultFileMode  = 0o600
	defaultDirMode   = 0o700
)

// DefaultConfigFile returns ~/.config/dbterm/<name>.
func DefaultConfigFile(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("file name is required")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, ".config", appConfigDirName, name), nil
}

// LoadJSON loads JSON from path into target.
// Missing or empty files are treated as zero-value data.
func LoadJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("unmarshal %s: %w", path, err)
	}

	return nil
}

// SaveJSON stores value as indented JSON.
func SaveJSON(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, defaultDirMode); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	data = append(data, '\n')

	tempFile, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}

	tempPath := tempFile.Name()
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("write temp file %s: %w", tempPath, err)
	}

	if err := tempFile.Chmod(defaultFileMode); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("chmod temp file %s: %w", tempPath, err)
	}

	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file %s: %w", tempPath, err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		// Some platforms cannot rename over an existing file.
		if removeErr := os.Remove(path); removeErr == nil || os.IsNotExist(removeErr) {
			if retryErr := os.Rename(tempPath, path); retryErr == nil {
				cleanupTemp = false
				return nil
			}
		}
		return fmt.Errorf("replace %s: %w", path, err)
	}

	cleanupTemp = false
	return nil
}
