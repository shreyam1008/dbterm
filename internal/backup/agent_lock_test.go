package backup

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentProcessLockIsExclusiveAndRecoverable(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := acquireAgentLock(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireAgentLock(store); err == nil || !strings.Contains(err.Error(), "already running") {
		first.release()
		t.Fatalf("second lock error = %v", err)
	}
	first.release()

	second, err := acquireAgentLock(store)
	if err != nil {
		t.Fatalf("lock was not recoverable after release: %v", err)
	}
	second.release()
}
