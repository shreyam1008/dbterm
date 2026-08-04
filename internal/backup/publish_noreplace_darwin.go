//go:build darwin

package backup

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func atomicPublicationSupported() error { return nil }

func atomicPublishNoReplace(source, destination string) error {
	err := unix.RenamexNp(source, destination, unix.RENAME_EXCL)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOTSUP) {
		return err
	}
	if linkErr := os.Link(source, destination); linkErr != nil {
		return fmt.Errorf("renamex_np exclusive publication is unavailable (%v) and atomic hard-link publication failed: %w", err, linkErr)
	}
	return nil
}
