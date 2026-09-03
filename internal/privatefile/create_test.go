package privatefile

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreateAndCreateTempAreNoClobberAndPrivateOnUnix(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "secret")
	file, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("secret"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path); !os.IsExist(err) {
		t.Fatalf("second Create() error = %v, want no-clobber refusal", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("private file mode = %04o, want 0600", got)
		}
	}
	temporary, err := CreateTemp(directory, ".private-", ".partial")
	if err != nil {
		t.Fatal(err)
	}
	temporaryPath := temporary.Name()
	if _, err := temporary.WriteString("round trip"); err != nil {
		t.Fatal(err)
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(temporary)
	if err != nil {
		t.Fatalf("read private temporary file after writing: %v", err)
	}
	if string(contents) != "round trip" {
		t.Fatalf("private temporary contents = %q, want round trip", contents)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(temporaryPath) != directory {
		t.Fatalf("private temporary path = %q, want directory %q", temporaryPath, directory)
	}
}

func TestEnsurePrivateDirectoryAndCreateTempDirectory(t *testing.T) {
	parent := t.TempDir()
	owned := filepath.Join(parent, "owned")
	if err := EnsurePrivateDirectory(owned); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(owned)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("private directory is not a real directory: %s", info.Mode())
	}

	temporary, err := CreateTempDirectory(owned, "stage-")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(temporary) != owned || !strings.HasPrefix(filepath.Base(temporary), "stage-") {
		t.Fatalf("private temporary directory = %q, want a stage-* child of %q", temporary, owned)
	}
	info, err = os.Lstat(temporary)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("private temporary path is not a real directory: %s", info.Mode())
	}
}
