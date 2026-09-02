//go:build !windows

package privatefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateDirectoryAndExistingFileModesOnUnix(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "owned")
	if err := os.Mkdir(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	assertUnixMode(t, directory, 0o700)

	temporary, err := CreateTempDirectory(directory, "stage-")
	if err != nil {
		t.Fatal(err)
	}
	assertUnixMode(t, temporary, 0o700)

	filePath := filepath.Join(directory, "existing-secret")
	if err := os.WriteFile(filePath, []byte("secret"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filePath, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := Protect(filePath); err != nil {
		t.Fatal(err)
	}
	assertUnixMode(t, filePath, 0o600)
}

func assertUnixMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
