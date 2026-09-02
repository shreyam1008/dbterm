package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProducerIDFileIsPrivate(t *testing.T) {
	state := t.TempDir()
	t.Setenv("DBTERM_STATE_DIR", state)

	if _, err := resolveProducerID(""); err != nil {
		t.Fatal(err)
	}
	assertBackupPrivateFile(t, filepath.Join(state, "backup", producerIDFilename))
}

func TestOpenDefaultStoreProtectsCatalogDirectoryAndSidecars(t *testing.T) {
	state := t.TempDir()
	t.Setenv("DBTERM_STATE_DIR", state)
	directory := filepath.Join(state, "backup")
	if err := os.MkdirAll(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "backups.db")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	makeBackupPathBroad(t, path, false)
	makeBackupPathBroad(t, directory, true)

	store, err := OpenDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetMeta(context.Background(), "privacy-test", "secret"); err != nil {
		t.Fatal(err)
	}

	assertBackupPrivateDirectory(t, directory)
	assertBackupPrivateFile(t, path)
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := path + suffix
		if _, err := os.Stat(sidecar); err != nil {
			t.Fatalf("SQLite %s sidecar was not present for its privacy check: %v", suffix, err)
		}
		assertBackupPrivateFile(t, sidecar)
	}
}

func TestPrivateNativeStageDirectoriesArePrivate(t *testing.T) {
	state := t.TempDir()
	t.Setenv("DBTERM_STATE_DIR", state)

	root, err := privateNativeStageRoot()
	if err != nil {
		t.Fatal(err)
	}
	assertBackupPrivateDirectory(t, root)
	directory, err := newPrivateNativeStage(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	assertBackupPrivateDirectory(t, directory)
}
