package osservice

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWritePrivateFileAtomicallyReplacesDefinition(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "service.definition")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed definition: %v", err)
	}
	if err := writePrivateFile(path, []byte("new\n")); err != nil {
		t.Fatalf("writePrivateFile() error = %v", err)
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read definition: %v", err)
	}
	if string(payload) != "new\n" {
		t.Fatalf("definition contents = %q, want new", payload)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat definition: %v", err)
		}
		if got := info.Mode().Perm(); got != privateFileMode {
			t.Fatalf("definition mode = %o, want %o", got, privateFileMode)
		}
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".service.definition.tmp-*"))
	if err != nil {
		t.Fatalf("glob temporary definitions: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary definitions remain: %#v", matches)
	}
}

func TestWritePrivateTempFileUsesUniquePrivateFile(t *testing.T) {
	directory := t.TempDir()
	path, err := writePrivateTempFile(directory, ".task-*.xml", []byte("definition"))
	if err != nil {
		t.Fatalf("writePrivateTempFile() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if filepath.Dir(path) != directory {
		t.Fatalf("temporary file directory = %q, want %q", filepath.Dir(path), directory)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat temporary definition: %v", err)
		}
		if got := info.Mode().Perm(); got != privateFileMode {
			t.Fatalf("temporary definition mode = %o, want %o", got, privateFileMode)
		}
	}
}

func TestWritePrivateFileInExistingDirectoryNeverCreatesManagerDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing-manager-directory")
	path := filepath.Join(parent, "service.definition")
	err := writePrivateFileInExistingDirectory(path, []byte("definition"))
	if err == nil {
		t.Fatal("writePrivateFileInExistingDirectory() expected an error")
	}
	if _, statErr := os.Stat(parent); !os.IsNotExist(statErr) {
		t.Fatalf("manager directory was unexpectedly created: %v", statErr)
	}
}
