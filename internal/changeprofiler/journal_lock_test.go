package changeprofiler

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalLockSerializesHandlesAndRetainsLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	first := &Store{path: path}
	second := &Store{path: path}
	release, err := first.lockJournalOperation()
	if err != nil {
		t.Fatal(err)
	}
	if unexpected, err := second.lockJournalOperation(); err == nil {
		unexpected()
		release()
		t.Fatal("second catalog handle acquired a held operation lock")
	}
	release()
	release() // Unlock functions may safely run from more than one cleanup path.
	if _, err := os.Stat(path + ".journal.lock"); err != nil {
		t.Fatalf("operation removed its persistent lock file: %v", err)
	}
	release, err = second.lockJournalOperation()
	if err != nil {
		t.Fatalf("released operation still appeared busy: %v", err)
	}
	release()
}

func TestJournalLockResolvesCatalogSymlinks(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "catalog.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(directory, "alias.db")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	release, err := (&Store{path: path}).lockJournalOperation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if other, err := (&Store{path: alias}).lockJournalOperation(); err == nil {
		other()
		t.Fatal("database symlink bypassed catalog operation lock")
	}
}

func TestJournalLockResolvesSymlinkBeforeCatalogCreation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "not-created-yet.db")
	alias := filepath.Join(directory, "alias.db")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	release, err := (&Store{path: path}).lockJournalOperation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if other, err := (&Store{path: alias}).lockJournalOperation(); err == nil {
		other()
		t.Fatal("symlink bypassed catalog initialization lock")
	}
}

func TestJournalLockFailureReleasesMutex(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "not-created-yet")
	store := &Store{path: filepath.Join(directory, "catalog.db")}
	if release, err := store.lockJournalOperation(); err == nil {
		release()
		t.Fatal("missing catalog directory unexpectedly acquired lock")
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := store.lockJournalOperation()
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestJournalLockIsSharedWithProcessesAndReleasedOnExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	store := &Store{path: path}
	release, err := store.lockJournalOperation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	run := func(mode string) {
		t.Helper()
		command := exec.Command(os.Args[0], "-test.run=^TestJournalLockProcessHelper$")
		command.Env = append(os.Environ(), "DBTERM_JOURNAL_LOCK_TEST_PATH="+path, "DBTERM_JOURNAL_LOCK_TEST_MODE="+mode)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("process lock check %s: %v\n%s", mode, err, output)
		}
	}
	run("busy")
	release()
	run("exit-while-held")
	// The child exits without invoking its unlock function, like process death.
	unlock, err := store.lockJournalOperation()
	if err != nil {
		t.Fatalf("child process exit stranded a catalog lock: %v", err)
	}
	unlock()
}

func TestJournalLockProcessHelper(t *testing.T) {
	path := os.Getenv("DBTERM_JOURNAL_LOCK_TEST_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	release, err := (&Store{path: path}).lockJournalOperation()
	if os.Getenv("DBTERM_JOURNAL_LOCK_TEST_MODE") == "busy" {
		if err == nil {
			release()
			t.Fatal("child process bypassed parent's catalog lock")
		}
		if !strings.Contains(err.Error(), "another checkpoint operation") {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
