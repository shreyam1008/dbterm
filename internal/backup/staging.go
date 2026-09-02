package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/appdirs"
	"github.com/shreyam1008/dbterm/internal/privatefile"
)

const (
	privateStagePrefix = "stage-"
	privateStageMaxAge = 48 * time.Hour
)

// newPrivateNativeStage creates a per-run directory below dbterm's private,
// OS-native state directory. Native dumps and credential files must never be
// staged in the user-selected backup destination: that destination may be a
// shared mount or a synchronised folder, and an interrupted encrypted backup
// must not leave plaintext there.
func newPrivateNativeStage(now time.Time) (string, error) {
	root, err := privateNativeStageRoot()
	if err != nil {
		return "", err
	}
	if now.IsZero() {
		now = time.Now()
	}
	if _, err := cleanupStalePrivateStages(root, now.Add(-privateStageMaxAge)); err != nil {
		return "", fmt.Errorf("clean stale private backup staging directories: %w", err)
	}
	directory, err := privatefile.CreateTempDirectory(root, privateStagePrefix)
	if err != nil {
		return "", fmt.Errorf("create private backup staging directory: %w", err)
	}
	return directory, nil
}

func privateNativeStageRoot() (string, error) {
	root, err := DefaultStagingPath()
	if err != nil {
		return "", err
	}
	if err := privatefile.EnsurePrivateDirectory(root); err != nil {
		return "", fmt.Errorf("create private backup staging root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect private backup staging root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("private backup staging root must be a real directory: %s", root)
	}
	return root, nil
}

// DefaultStagingPath returns the private state path used for raw native dumps.
// It does not create the staging directory or a backup artifact.
func DefaultStagingPath() (string, error) {
	state, err := appdirs.StateDir()
	if err != nil {
		return "", fmt.Errorf("resolve private backup state directory: %w", err)
	}
	return filepath.Join(state, "backup", "staging"), nil
}

// cleanupStalePrivateStages only removes directories created by
// newPrivateNativeStage. Symlinks and directories containing unexpected
// entries are left untouched, making cleanup fail closed if the state tree was
// tampered with.
func cleanupStalePrivateStages(root string, olderThan time.Time) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), privateStagePrefix) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.IsDir() {
			continue
		}
		stagePath := filepath.Join(root, entry.Name())
		stale, err := privateStageIsStale(stagePath, info.ModTime(), olderThan)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, err
		}
		if !stale {
			continue
		}
		current, err := os.Lstat(stagePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, err
		}
		if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(info, current) {
			continue
		}
		if err := os.RemoveAll(stagePath); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func privateStageIsStale(path string, newest time.Time, olderThan time.Time) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return false, nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest.Before(olderThan), nil
}
