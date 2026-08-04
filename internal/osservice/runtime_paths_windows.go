//go:build windows

package osservice

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func validateSystemRuntimePaths(options Options, _ string) error {
	if options.Scope != ScopeSystem {
		return nil
	}
	for _, candidate := range []struct {
		label     string
		path      string
		directory bool
	}{
		{label: "backup executable", path: options.Executable},
		{label: "config directory", path: options.ConfigDir, directory: true},
		{label: "state directory", path: options.StateDir, directory: true},
		{label: "log directory", path: options.LogDir, directory: true},
	} {
		if remote, err := windowsRemotePath(candidate.path); err != nil {
			return fmt.Errorf("validate %s for LocalSystem: %w", candidate.label, err)
		} else if remote {
			return fmt.Errorf("%s %s is on a network or mapped drive; Windows system backup tasks run as LocalSystem and require explicit local paths (user drive mappings are unavailable)", candidate.label, candidate.path)
		}
		info, err := os.Stat(candidate.path)
		if err != nil {
			if os.IsNotExist(err) && candidate.directory {
				return fmt.Errorf("%s %s does not exist; create it before elevated installation and grant LocalSystem access", candidate.label, candidate.path)
			}
			return fmt.Errorf("inspect %s %s for LocalSystem: %w", candidate.label, candidate.path, err)
		}
		if candidate.directory && !info.IsDir() {
			return fmt.Errorf("%s is not a directory: %s", candidate.label, candidate.path)
		}
		if !candidate.directory && !info.Mode().IsRegular() {
			return fmt.Errorf("backup executable is not a regular file: %s", candidate.path)
		}
	}
	return nil
}

func windowsRemotePath(path string) (bool, error) {
	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, `\\`) {
		return true, nil
	}
	volume := filepath.VolumeName(cleaned)
	if volume == "" {
		return false, fmt.Errorf("path does not have a local volume: %s", path)
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return false, fmt.Errorf("encode volume %s: %w", volume, err)
	}
	return windows.GetDriveType(root) == windows.DRIVE_REMOTE, nil
}
