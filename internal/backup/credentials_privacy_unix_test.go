//go:build !windows

package backup

import (
	"os"
	"testing"
)

func assertBackupPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("private file permissions are %04o, want 0600", got)
	}
}

func assertBackupPrivateDirectory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("private backup state path is not a real directory: %s", path)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("private directory permissions are %04o, want 0700", got)
	}
}

func makeBackupPathBroad(t *testing.T, path string, directory bool) {
	t.Helper()
	mode := os.FileMode(0o666)
	if directory {
		mode = 0o777
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
