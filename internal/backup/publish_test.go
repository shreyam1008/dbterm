package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishNoReplaceFallbackStagesPrivatelyThenPublishes(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	destinationDirectory := filepath.Join(root, "destination")
	if err := os.MkdirAll(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(sourceDirectory, "completed.stage")
	final := filepath.Join(destinationDirectory, "backup.dump")
	want := []byte("complete backup payload")
	if err := os.WriteFile(staged, want, 0o600); err != nil {
		t.Fatal(err)
	}

	err := publishNoReplaceWithOps(context.Background(), staged, final, nil, publicationOps{
		link:   func(string, string) error { return errors.New("hard links unavailable") },
		atomic: atomicPublishNoReplace,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("published content = %q, want %q", got, want)
	}
	if _, err := os.Lstat(staged); !os.IsNotExist(err) {
		t.Fatalf("completed staging file remains after publication: %v", err)
	}
	assertNoPublicationPartials(t, destinationDirectory)
}

func TestPublishNoReplaceCancellationDuringFallbackNeverExposesFinal(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	destinationDirectory := filepath.Join(root, "destination")
	if err := os.MkdirAll(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(sourceDirectory, "completed.stage")
	final := filepath.Join(destinationDirectory, "backup.dump")
	if err := os.WriteFile(staged, make([]byte, 2*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	atomicCalled := false
	err := publishNoReplaceWithOps(ctx, staged, final, func(event ProgressEvent) {
		if event.Phase == "publish" && event.CurrentBytes > 0 {
			cancel()
		}
	}, publicationOps{
		link: func(string, string) error { return errors.New("hard links unavailable") },
		atomic: func(string, string) error {
			atomicCalled = true
			return nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("publishNoReplaceWithOps() error = %v, want cancellation", err)
	}
	if atomicCalled {
		t.Fatal("atomic publication ran after cancellation")
	}
	if _, err := os.Lstat(final); !os.IsNotExist(err) {
		t.Fatalf("canceled publication exposed final path: %v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("canceled publication removed completed source stage: %v", err)
	}
	assertNoPublicationPartials(t, destinationDirectory)
}

func TestPublishNoReplaceNeverOverwritesExistingPath(t *testing.T) {
	directory := t.TempDir()
	staged := filepath.Join(directory, "completed.stage")
	final := filepath.Join(directory, "backup.dump")
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(final, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	atomicCalled := false
	err := publishNoReplaceWithOps(context.Background(), staged, final, nil, publicationOps{
		link: func(string, string) error { return os.ErrExist },
		atomic: func(string, string) error {
			atomicCalled = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("publish error = %v, want existing-file rejection", err)
	}
	if atomicCalled {
		t.Fatal("atomic publication ran despite an existing final path")
	}
	got, readErr := os.ReadFile(final)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old" {
		t.Fatalf("existing final content = %q, want old", got)
	}
}

func TestPublishNoReplaceNeverOverwritesPathCreatedAtAtomicBoundary(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	destinationDirectory := filepath.Join(root, "destination")
	if err := os.MkdirAll(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(sourceDirectory, "completed.stage")
	final := filepath.Join(destinationDirectory, "backup.dump")
	if err := os.WriteFile(staged, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := publishNoReplaceWithOps(context.Background(), staged, final, nil, publicationOps{
		link: func(string, string) error { return errors.New("hard links unavailable") },
		atomic: func(source, destination string) error {
			if err := os.WriteFile(destination, []byte("competitor"), 0o600); err != nil {
				return err
			}
			return atomicPublishNoReplace(source, destination)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("publish error = %v, want atomic existing-file rejection", err)
	}
	got, readErr := os.ReadFile(final)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "competitor" {
		t.Fatalf("racing final content = %q, want competitor", got)
	}
	assertNoPublicationPartials(t, destinationDirectory)
}

func TestPublishNoReplaceCleansPartialWhenAtomicPublicationFails(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	destinationDirectory := filepath.Join(root, "destination")
	if err := os.MkdirAll(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(sourceDirectory, "completed.stage")
	final := filepath.Join(destinationDirectory, "backup.dump")
	if err := os.WriteFile(staged, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("filesystem cannot publish atomically")
	err := publishNoReplaceWithOps(context.Background(), staged, final, nil, publicationOps{
		link:   func(string, string) error { return errors.New("hard links unavailable") },
		atomic: func(string, string) error { return wantErr },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("publish error = %v, want %v", err, wantErr)
	}
	if _, err := os.Lstat(final); !os.IsNotExist(err) {
		t.Fatalf("failed atomic publication exposed final path: %v", err)
	}
	assertNoPublicationPartials(t, destinationDirectory)
}

func TestPublishNoReplacePreservesSuccessWhenCancellationLosesBoundaryRace(t *testing.T) {
	directory := t.TempDir()
	staged := filepath.Join(directory, "completed.stage")
	final := filepath.Join(directory, "backup.dump")
	if err := os.WriteFile(staged, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := publishNoReplaceWithOps(ctx, staged, final, nil, publicationOps{
		link: func(source, destination string) error {
			if err := os.Link(source, destination); err != nil {
				return err
			}
			cancel()
			return nil
		},
		atomic: atomicPublishNoReplace,
	})
	if err != nil {
		t.Fatalf("publication that won the atomic race returned error: %v", err)
	}
	if got, err := os.ReadFile(final); err != nil || string(got) != "complete" {
		t.Fatalf("published backup = %q, %v", got, err)
	}
}

func TestPublishNoReplaceCleansOnlyStalePublicationPartials(t *testing.T) {
	directory := t.TempDir()
	staged := filepath.Join(directory, "completed.stage")
	final := filepath.Join(directory, "backup.dump")
	if err := os.WriteFile(staged, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPartial := filepath.Join(directory, ".dbterm-publish-00112233445566778899aabb.partial")
	freshPartial := filepath.Join(directory, ".dbterm-publish-00112233445566778899aabc.partial")
	malformedPartial := filepath.Join(directory, ".dbterm-publish-crashed.partial")
	unrelated := filepath.Join(directory, ".dbterm-other.partial")
	for _, path := range []string{oldPartial, freshPartial, malformedPartial, unrelated} {
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-49 * time.Hour)
	if err := os.Chtimes(oldPartial, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := publishNoReplace(context.Background(), staged, final, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(oldPartial); !os.IsNotExist(err) {
		t.Fatalf("stale publication partial remains: %v", err)
	}
	for _, path := range []string{freshPartial, malformedPartial, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("publication cleanup removed %s: %v", filepath.Base(path), err)
		}
	}
}

func TestPublishNoReplaceGuardRunsAtFinalSyscallBoundaries(t *testing.T) {
	directory := t.TempDir()
	staged := filepath.Join(directory, "guarded.partial")
	final := filepath.Join(directory, "guarded.dump")
	if err := os.WriteFile(staged, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	guardCalls := 0
	linkCalls := 0
	atomicCalls := 0
	err := publishNoReplaceWithOps(context.Background(), staged, final, nil, publicationOps{
		beforePublish: func() error {
			guardCalls++
			return nil
		},
		link: func(string, string) error {
			linkCalls++
			if guardCalls != 1 {
				t.Fatalf("link attempt observed %d guard calls, want one immediately before it", guardCalls)
			}
			return errors.New("hard links unavailable")
		},
		atomic: func(source, destination string) error {
			atomicCalls++
			if guardCalls != 2 {
				t.Fatalf("atomic attempt observed %d guard calls, want a repeated guard immediately before it", guardCalls)
			}
			return os.Rename(source, destination)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if guardCalls != 2 || linkCalls != 1 || atomicCalls != 1 {
		t.Fatalf("guard/link/atomic calls = %d/%d/%d", guardCalls, linkCalls, atomicCalls)
	}
}

func TestPublishNoReplaceGuardFailurePublishesNothing(t *testing.T) {
	directory := t.TempDir()
	staged := filepath.Join(directory, "guarded.partial")
	final := filepath.Join(directory, "guarded.dump")
	if err := os.WriteFile(staged, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("destination identity changed")
	publicationCalled := false
	err := publishNoReplaceWithOps(context.Background(), staged, final, nil, publicationOps{
		beforePublish: func() error { return wantErr },
		link: func(string, string) error {
			publicationCalled = true
			return nil
		},
		atomic: func(string, string) error {
			publicationCalled = true
			return nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("guard failure = %v, want %v", err, wantErr)
	}
	if publicationCalled {
		t.Fatal("guard failure still invoked a publication syscall")
	}
	if _, err := os.Lstat(final); !os.IsNotExist(err) {
		t.Fatalf("guard failure published a final name: %v", err)
	}
}

func assertNoPublicationPartials(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".dbterm-publish-*.partial"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("private publication partials remain: %v", matches)
	}
}
