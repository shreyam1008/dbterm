//go:build linux

package backup

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func atomicPublicationSupported() error { return nil }

func atomicPublishNoReplace(source, destination string) error {
	err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOTSUP) {
		return err
	}
	// Older kernels and some filesystems lack renameat2(RENAME_NOREPLACE).
	// A same-filesystem hard link has the same atomic no-clobber property.
	if linkErr := os.Link(source, destination); linkErr != nil {
		return fmt.Errorf("renameat2 no-replace is unavailable (%v) and atomic hard-link publication failed: %w", err, linkErr)
	}
	return nil
}
