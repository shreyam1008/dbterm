// Package appdirs resolves dbterm's per-user configuration, state, and log
// directories on every supported operating system.
package appdirs

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	appDirName       = "dbterm"
	configDirEnvName = "DBTERM_CONFIG_DIR"
	stateDirEnvName  = "DBTERM_STATE_DIR"
	logDirEnvName    = "DBTERM_LOG_DIR"
	ownershipMarker  = ".dbterm-owned"
	ownershipValue   = "dbterm:data-directory:v1\n"
)

// ConfigDir returns dbterm's per-user configuration directory.
//
// DBTERM_CONFIG_DIR takes precedence when set. Without an override, the path
// follows the host OS convention. If the native directory does not yet exist
// and the legacy ~/.config/dbterm directory does, dbterm attempts an atomic
// directory rename. It never merges or overwrites two config trees; when an
// atomic migration is unavailable, the legacy directory remains in use.
func ConfigDir() (string, error) {
	if override, ok, err := environmentDir(configDirEnvName); ok || err != nil {
		return override, err
	}

	native, err := nativeConfigDir()
	if err != nil {
		return "", err
	}
	legacy, err := legacyConfigDir()
	if err != nil {
		// A native directory can still be valid on platforms that resolved it
		// without a home directory. Legacy discovery is best-effort.
		return native, nil
	}
	return selectConfigDir(native, legacy)
}

// StateDir returns dbterm's per-user mutable state directory.
func StateDir() (string, error) {
	if override, ok, err := environmentDir(stateDirEnvName); ok || err != nil {
		return override, err
	}

	switch runtime.GOOS {
	case "linux":
		if xdgState := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdgState != "" {
			base, err := absoluteDir("XDG_STATE_HOME", xdgState)
			if err != nil {
				return "", err
			}
			return filepath.Join(base, appDirName), nil
		}
		home, err := userHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state", appDirName), nil
	case "darwin":
		home, err := userHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", appDirName), nil
	case "windows":
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve local application data directory: %w", err)
		}
		return filepath.Join(base, appDirName), nil
	default:
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user state directory: %w", err)
		}
		return filepath.Join(base, appDirName), nil
	}
}

// LogDir returns dbterm's per-user log directory.
func LogDir() (string, error) {
	if override, ok, err := environmentDir(logDirEnvName); ok || err != nil {
		return override, err
	}

	if runtime.GOOS == "darwin" {
		home, err := userHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Logs", appDirName), nil
	}

	state, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "logs"), nil
}

func nativeConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(base, appDirName), nil
}

func legacyConfigDir() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", appDirName), nil
}

func userHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Clean(home), nil
}

func environmentDir(name string) (string, bool, error) {
	value, present := os.LookupEnv(name)
	value = strings.TrimSpace(value)
	if !present || value == "" {
		return "", false, nil
	}
	dir, err := absoluteDir(name, value)
	if err != nil {
		return "", true, err
	}
	if err := initializeOverrideOwnership(dir); err != nil {
		return "", true, fmt.Errorf("prepare %s directory ownership: %w", name, err)
	}
	return dir, true, nil
}

func absoluteDir(name, value string) (string, error) {
	dir, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve %s directory %q: %w", name, value, err)
	}
	return dir, nil
}

// IsOwnedDirectory reports whether dir has the exact marker dbterm writes when
// it creates a DBTERM_* override directory. Native OS directories do not need
// this marker; it exists solely to put a hard ownership boundary around purge
// of arbitrary environment-provided paths.
func IsOwnedDirectory(dir string) (bool, error) {
	markerPath := filepath.Join(filepath.Clean(dir), ownershipMarker)
	info, err := os.Lstat(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect ownership marker %s: %w", markerPath, err)
	}
	if !info.Mode().IsRegular() || info.Size() > 256 {
		return false, nil
	}
	file, err := os.Open(markerPath)
	if err != nil {
		return false, fmt.Errorf("open ownership marker %s: %w", markerPath, err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, 257))
	closeErr := file.Close()
	if readErr != nil {
		return false, fmt.Errorf("read ownership marker %s: %w", markerPath, readErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close ownership marker %s: %w", markerPath, closeErr)
	}
	return bytes.Equal(payload, []byte(ownershipValue)), nil
}

func initializeOverrideOwnership(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect override directory %s: %w", dir, err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create override directory %s: %w", dir, err)
		}
	} else if !info.IsDir() {
		return fmt.Errorf("override path is not a directory: %s", dir)
	}

	owned, err := IsOwnedDirectory(dir)
	if err != nil || owned {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("inspect override directory contents %s: %w", dir, err)
	}
	if len(entries) != 0 {
		// Never claim an existing non-empty directory. It remains usable, but a
		// later --purge must refuse it rather than deleting unrelated contents.
		return nil
	}
	return writeOwnershipMarker(dir)
}

func writeOwnershipMarker(dir string) error {
	markerPath := filepath.Join(dir, ownershipMarker)
	file, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			owned, inspectErr := IsOwnedDirectory(dir)
			if inspectErr != nil {
				return inspectErr
			}
			if owned {
				return nil
			}
		}
		return fmt.Errorf("create ownership marker %s: %w", markerPath, err)
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(markerPath)
		}
	}()
	if _, err := io.WriteString(file, ownershipValue); err != nil {
		return fmt.Errorf("write ownership marker %s: %w", markerPath, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync ownership marker %s: %w", markerPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close ownership marker %s: %w", markerPath, err)
	}
	ok = true
	return nil
}

func selectConfigDir(native, legacy string) (string, error) {
	native = filepath.Clean(native)
	legacy = filepath.Clean(legacy)
	if native == legacy {
		return native, nil
	}

	if info, err := os.Stat(native); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("native config path is not a directory: %s", native)
		}
		return native, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect native config directory %s: %w", native, err)
	}

	legacyEntry, err := os.Lstat(legacy)
	if err != nil {
		if os.IsNotExist(err) {
			return native, nil
		}
		return "", fmt.Errorf("inspect legacy config directory %s: %w", legacy, err)
	}
	if legacyEntry.Mode()&os.ModeSymlink != 0 {
		info, statErr := os.Stat(legacy)
		if statErr != nil {
			return "", fmt.Errorf("inspect legacy config symlink %s: %w", legacy, statErr)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("legacy config path is not a directory: %s", legacy)
		}
		// Respect an intentional legacy symlink, but do not move its target.
		return legacy, nil
	}
	if !legacyEntry.IsDir() {
		return "", fmt.Errorf("legacy config path is not a directory: %s", legacy)
	}

	// Renaming the whole directory is atomic on a filesystem and avoids a
	// partially copied or merged configuration. Failure is non-destructive:
	// continue using the legacy tree instead.
	if err := os.MkdirAll(filepath.Dir(native), 0o700); err != nil {
		return legacy, nil
	}
	if err := os.Rename(legacy, native); err == nil {
		_ = os.Chmod(native, 0o700)
		return native, nil
	}

	// Another dbterm process may have completed the rename first.
	if info, err := os.Stat(native); err == nil && info.IsDir() {
		return native, nil
	}
	return legacy, nil
}
