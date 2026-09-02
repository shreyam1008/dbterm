//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package backup

import (
	"fmt"
	"os"
)

func copyVolumeFilesystemIdentity(_ string, _ os.FileInfo) (string, error) {
	return "", fmt.Errorf("destination volume identity verification is unsupported on this operating system")
}
