//go:build windows

package backup

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// copyVolumeFilesystemIdentity binds a path to both its Windows volume root
// and serial number. GetVolumePathName resolves nested mounted volumes, while
// the serial number prevents a path string alone from being treated as proof.
func copyVolumeFilesystemIdentity(path string, info os.FileInfo) (string, error) {
	if info == nil {
		return "", fmt.Errorf("file information is unavailable")
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("encode path: %w", err)
	}
	volumePath := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(pathPointer, &volumePath[0], uint32(len(volumePath))); err != nil {
		return "", fmt.Errorf("resolve volume root: %w", err)
	}
	root := windows.UTF16ToString(volumePath)
	rootPointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", fmt.Errorf("encode volume root: %w", err)
	}
	var serial uint32
	if err := windows.GetVolumeInformation(rootPointer, nil, 0, &serial, nil, nil, nil, 0); err != nil {
		return "", fmt.Errorf("read volume serial number: %w", err)
	}
	return fmt.Sprintf("windows-volume:%s:%08x", strings.ToLower(root), serial), nil
}
