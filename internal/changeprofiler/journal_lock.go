package changeprofiler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// lockJournalOperation serializes source-object ownership transitions for every
// handle/process using this local catalog. The file remains in place forever:
// unlinking an advisory lock lets another process lock a different inode while
// an existing operation still holds the old one. Kernel locks disappear on exit.
func (s *Store) lockJournalOperation() (func(), error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil, fmt.Errorf("checkpoint catalog path is unavailable for the operation lock")
	}
	s.journalMu.Lock()
	releaseMutex := true
	defer func() {
		if releaseMutex {
			s.journalMu.Unlock()
		}
	}()
	canonical, err := canonicalJournalStorePath(s.path)
	if err != nil {
		return nil, err
	}
	path := canonical + ".journal.lock"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open checkpoint operation lock: %w", err)
	}
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("checkpoint operation lock must be a regular file")
	}
	locked, err := tryLockJournalFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock checkpoint catalog: %w", err)
	}
	if !locked {
		_ = file.Close()
		return nil, fmt.Errorf("another checkpoint operation is using this catalog; wait for it to finish and retry")
	}
	var once sync.Once
	releaseMutex = false
	return func() {
		once.Do(func() {
			_ = unlockJournalFile(file)
			_ = file.Close()
			s.journalMu.Unlock()
		})
	}, nil
}

// Resolve existing database files as well as a missing file in an existing
// directory. The latter is needed while OpenStore holds the lock for creation
// and interrupted-operation recovery before SQLite has created the catalog.
func canonicalJournalStorePath(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve checkpoint catalog path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return canonical, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve checkpoint catalog symlinks: %w", err)
	}
	// An existing symlink may point to a catalog that is about to be created.
	// Resolve its target as well, so opening through the alias cannot race an
	// initializer using the target's ordinary path.
	if info, statErr := os.Lstat(absolute); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(absolute)
		if readErr != nil {
			return "", fmt.Errorf("read checkpoint catalog symlink: %w", readErr)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(absolute), target)
		}
		return canonicalJournalStorePath(target)
	}
	directory, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("resolve checkpoint catalog directory: %w", err)
	}
	return filepath.Join(directory, filepath.Base(absolute)), nil
}
