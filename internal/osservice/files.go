package osservice

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	privateDirectoryMode = 0o700
	privateFileMode      = 0o600
)

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, privateDirectoryMode); err != nil {
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	return nil
}

func definitionExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect service definition %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("service definition is not a regular file: %s", path)
	}
	return true, nil
}

func writePrivateFile(path string, data []byte) error {
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return writePrivateFileInExistingDirectory(path, data)
}

// writePrivateFileInExistingDirectory atomically replaces path without ever
// creating its parent. System manager directories are owned by the OS and
// must not be synthesized with dbterm's private runtime-directory mode.
func writePrivateFileInExistingDirectory(path string, data []byte) error {
	parent := filepath.Dir(path)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("inspect service manager directory %s: %w", parent, err)
	}
	if !parentInfo.IsDir() {
		return fmt.Errorf("service manager path is not a directory: %s", parent)
	}

	temporary, err := os.CreateTemp(parent, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary service definition beside %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary service definition %s: %w", temporaryPath, err)
	}
	if err := temporary.Chmod(privateFileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set private permissions on %s: %w", temporaryPath, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary service definition %s: %w", temporaryPath, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary service definition %s: %w", temporaryPath, err)
	}
	if err := replaceDefinition(temporaryPath, path); err != nil {
		return fmt.Errorf("replace service definition %s: %w", path, err)
	}

	keepTemporary = false
	return nil
}

func writePrivateTempFile(directory, pattern string, data []byte) (string, error) {
	if err := ensurePrivateDirectory(directory); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", fmt.Errorf("create temporary service definition in %s: %w", directory, err)
	}
	temporaryPath := temporary.Name()
	ok := false
	defer func() {
		_ = temporary.Close()
		if !ok {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		return "", fmt.Errorf("write temporary service definition %s: %w", temporaryPath, err)
	}
	if err := temporary.Chmod(privateFileMode); err != nil {
		return "", fmt.Errorf("set private permissions on %s: %w", temporaryPath, err)
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary service definition %s: %w", temporaryPath, err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close temporary service definition %s: %w", temporaryPath, err)
	}
	ok = true
	return temporaryPath, nil
}
