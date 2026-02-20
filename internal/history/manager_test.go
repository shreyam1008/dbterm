package history

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestManagerAppendCapAndPersistence(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.json")

	m, err := NewManagerAt(path, 2)
	if err != nil {
		t.Fatalf("NewManagerAt() error = %v", err)
	}

	if err := m.Append("conn-a", "SELECT 1"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := m.Append("conn-a", "SELECT 2"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := m.Append("conn-a", "SELECT 3"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	got := m.Entries("conn-a")
	if len(got) != 2 {
		t.Fatalf("Entries() len = %d, want 2", len(got))
	}
	if got[0].SQL != "SELECT 2" || got[1].SQL != "SELECT 3" {
		t.Fatalf("Entries() SQL = %#v, want [SELECT 2 SELECT 3]", []string{got[0].SQL, got[1].SQL})
	}
	if got[0].Timestamp.IsZero() || got[1].Timestamp.IsZero() {
		t.Fatalf("Entries() timestamp should be set")
	}

	reloaded, err := NewManagerAt(path, 2)
	if err != nil {
		t.Fatalf("NewManagerAt(reload) error = %v", err)
	}

	got = reloaded.Entries("conn-a")
	if len(got) != 2 {
		t.Fatalf("reloaded Entries() len = %d, want 2", len(got))
	}
	if got[0].SQL != "SELECT 2" || got[1].SQL != "SELECT 3" {
		t.Fatalf("reloaded Entries() SQL = %#v, want [SELECT 2 SELECT 3]", []string{got[0].SQL, got[1].SQL})
	}
}

func TestManagerConnectionIsolation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.json")

	m, err := NewManagerAt(path, 10)
	if err != nil {
		t.Fatalf("NewManagerAt() error = %v", err)
	}

	if err := m.Append("conn-a", "SELECT 1"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := m.Append("conn-b", "SELECT 2"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	gotA := m.Entries("conn-a")
	gotB := m.Entries("conn-b")

	if len(gotA) != 1 || gotA[0].SQL != "SELECT 1" {
		t.Fatalf("Entries(conn-a) = %#v, want one query SELECT 1", gotA)
	}
	if len(gotB) != 1 || gotB[0].SQL != "SELECT 2" {
		t.Fatalf("Entries(conn-b) = %#v, want one query SELECT 2", gotB)
	}
}

func TestManagerConcurrentAppend(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.json")

	m, err := NewManagerAt(path, 500)
	if err != nil {
		t.Fatalf("NewManagerAt() error = %v", err)
	}

	const workers = 8
	const perWorker = 20

	var wg sync.WaitGroup
	errCh := make(chan error, workers*perWorker)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				query := fmt.Sprintf("SELECT %d, %d", worker, j)
				if err := m.Append("shared", query); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("Append() concurrent error = %v", err)
		}
	}

	want := workers * perWorker
	got := m.Entries("shared")
	if len(got) != want {
		t.Fatalf("Entries(shared) len = %d, want %d", len(got), want)
	}
}

func TestManagerInputValidation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.json")

	m, err := NewManagerAt(path, 10)
	if err != nil {
		t.Fatalf("NewManagerAt() error = %v", err)
	}

	if err := m.Append("", "SELECT 1"); err == nil {
		t.Fatalf("Append() with empty connection key expected error")
	}

	if err := m.Append("conn-a", "   "); err != nil {
		t.Fatalf("Append() with empty query should not error: %v", err)
	}

	if got := m.Entries("conn-a"); len(got) != 0 {
		t.Fatalf("Entries(conn-a) len = %d, want 0", len(got))
	}
}
