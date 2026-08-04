//go:build !windows

package backup

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %s for sync: %w", path, err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS) {
			return nil
		}
		return fmt.Errorf("sync directory %s: %w", path, err)
	}
	return nil
}
