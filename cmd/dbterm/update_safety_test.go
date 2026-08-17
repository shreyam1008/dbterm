//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSudoUpdateDoesNotManagePerUserBackupAgent(t *testing.T) {
	if shouldManageBackupAgentDuringUpdate("alice") {
		t.Fatal("sudo update must not inspect or mutate the invoking user's service from the root process")
	}
	if !shouldManageBackupAgentDuringUpdate("") {
		t.Fatal("ordinary update should preserve the normal backup-agent lifecycle")
	}
}

func TestUnixBinaryReplacementLeavesPerUserDataUntouched(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	configDir := filepath.Join(root, "home", ".config", "dbterm")
	backupDir := filepath.Join(root, "home", ".local", "state", "dbterm", "backup")
	profilerDir := filepath.Join(root, "home", ".local", "state", "dbterm", "change-profiler")
	for _, dir := range []string{binDir, configDir, backupDir, profilerDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	dataFiles := map[string]string{
		filepath.Join(configDir, "connections.json"):              `{"connections":[{"name":"production"}]}`,
		filepath.Join(backupDir, "backups.db"):                    "backup plans and history",
		filepath.Join(profilerDir, "change-profiler.db"):          "saved anchors",
		filepath.Join(root, "chosen-destination", "snapshot.sql"): "completed backup artifact",
	}
	for path, contents := range dataFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	executable := filepath.Join(binDir, "dbterm")
	download := filepath.Join(root, "downloaded-dbterm")
	if err := os.WriteFile(executable, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBinary := "#!/bin/sh\nprintf 'dbterm v0.9.0 \\\"Chenab\\\"\\nBuild test\\nGo test/unix\\n'\n"
	if err := os.WriteFile(download, []byte(newBinary), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceUnixBinary(executable, download, "0.8.0", "v0.9.0"); err != nil {
		t.Fatalf("replaceUnixBinary() error = %v", err)
	}
	if got, err := os.ReadFile(executable); err != nil || string(got) != newBinary {
		t.Fatalf("installed binary = %q, %v", got, err)
	}
	for path, want := range dataFiles {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read preserved data %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("data %s changed during update: got %q, want %q", path, got, want)
		}
	}
}
