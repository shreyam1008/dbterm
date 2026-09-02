//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package backup

import (
	"fmt"
	"os"
	"syscall"
)

// copyVolumeFilesystemIdentity returns the kernel filesystem/device identity
// captured by lstat. Unlike lexical containment, st_dev changes across a
// nested mount, so a destination cannot silently land on another filesystem.
func copyVolumeFilesystemIdentity(_ string, info os.FileInfo) (string, error) {
	if info == nil {
		return "", fmt.Errorf("file information is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return "", fmt.Errorf("operating system did not expose a filesystem device identity")
	}
	return fmt.Sprintf("unix-device:%d", uint64(stat.Dev)), nil
}
