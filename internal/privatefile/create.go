package privatefile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Create opens a new, no-clobber file that is private to the current runtime
// identity (plus the operating system's administrator identities on Windows).
func Create(path string) (*os.File, error) {
	return create(path)
}

// Protect applies dbterm's private-file permissions to an existing regular
// file without following a symbolic link.
func Protect(path string) error {
	return protect(path)
}

// EnsurePrivateDirectory creates or tightens a directory owned exclusively by
// dbterm. Callers must not pass a shared directory: existing permissions are
// deliberately replaced so files created below it inherit private access on
// Windows.
func EnsurePrivateDirectory(path string) error {
	path = filepath.Clean(path)
	if path == "." || filepath.Dir(path) == path {
		return fmt.Errorf("private directory must be an application-owned child path: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create private directory parent: %w", err)
	}
	if err := ensurePrivateDirectory(path); err != nil {
		return fmt.Errorf("protect private directory %s: %w", path, err)
	}
	return nil
}

// CreateTemp creates a private, unpredictable file in an existing directory.
// It avoids os.CreateTemp because Unix mode bits alone do not install a
// restrictive DACL when dbterm runs on Windows.
func CreateTemp(directory, prefix, suffix string) (*os.File, error) {
	if directory == "" {
		directory = os.TempDir()
	}
	for attempts := 0; attempts < 100; attempts++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("generate private temporary filename: %w", err)
		}
		path := filepath.Join(directory, prefix+hex.EncodeToString(random[:])+suffix)
		file, err := Create(path)
		if err == nil {
			return file, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("create unique private temporary file in %s", directory)
}

// CreateTempDirectory creates an unpredictable private child directory. The
// parent directory must already exist; use EnsurePrivateDirectory when dbterm
// owns that parent.
func CreateTempDirectory(directory, prefix string) (string, error) {
	if directory == "" {
		directory = "."
	}
	for attempts := 0; attempts < 100; attempts++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("generate private temporary directory name: %w", err)
		}
		path := filepath.Join(directory, prefix+hex.EncodeToString(random[:]))
		err := createPrivateDirectory(path)
		if err == nil {
			return path, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("create unique private temporary directory in %s", directory)
}
