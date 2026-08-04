//go:build !windows

package backup

import (
	"path/filepath"
	"testing"
)

func TestSyncDirectoryReportsOpenFailure(t *testing.T) {
	if err := syncDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("syncDirectory accepted a missing directory")
	}
}
