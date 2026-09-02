//go:build !windows

package privatefile

import (
	"fmt"
	"os"
)

func create(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func protect(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("private file must be a regular file, not a symbolic link: %s", path)
	}
	return os.Chmod(path, 0o600)
}

func createPrivateDirectory(path string) error {
	return os.Mkdir(path, 0o700)
}

func ensurePrivateDirectory(path string) error {
	err := createPrivateDirectory(path)
	if err != nil && !os.IsExist(err) {
		return err
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return statErr
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("private directory must be a real directory, not a symbolic link: %s", path)
	}
	return os.Chmod(path, 0o700)
}
