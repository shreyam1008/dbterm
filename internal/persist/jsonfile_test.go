package persist

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveJSONAtomicallyReplacesPrivateFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.json")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed destination: %v", err)
	}

	value := map[string]any{"enabled": true, "count": 2}
	if err := SaveJSON(path, value); err != nil {
		t.Fatalf("SaveJSON() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !strings.Contains(string(data), `"enabled": true`) || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("unexpected JSON contents: %q", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat destination: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("destination mode = %o, want 600", got)
		}
	}

	matches, err := filepath.Glob(filepath.Join(directory, ".settings.json.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %#v", matches)
	}
}

func TestSaveJSONMarshalFailurePreservesDestination(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "connections.json")
	if err := os.WriteFile(path, []byte("keep-me\n"), 0o600); err != nil {
		t.Fatalf("seed destination: %v", err)
	}

	if err := SaveJSON(path, map[string]any{"unsupported": func() {}}); err == nil {
		t.Fatal("SaveJSON() expected marshal error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preserved destination: %v", err)
	}
	if string(data) != "keep-me\n" {
		t.Fatalf("destination changed after marshal failure: %q", data)
	}
}
