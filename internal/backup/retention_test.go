package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestApplyRetentionDeletesOwnedManifestBeforeArtifact(t *testing.T) {
	store, job, destination, now := retentionPairFixture(t)
	old := addRetentionPairRun(t, store, job, destination, now.Add(-2*time.Hour), "old")
	newest := addRetentionPairRun(t, store, job, destination, now.Add(-time.Hour), "new")

	removed, err := ApplyRetention(context.Background(), store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != old.Artifact.Path {
		t.Fatalf("removed = %#v, want %s", removed, old.Artifact.Path)
	}
	for _, path := range []string{old.Artifact.ManifestPath, old.Artifact.Path} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired pair member remains at %s: %v", path, err)
		}
	}
	for _, path := range []string{newest.Artifact.Path, newest.Artifact.ManifestPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("newest pair member was removed at %s: %v", path, err)
		}
	}
}

func TestApplyRetentionRefusesTamperedArtifactWithoutRemovingManifest(t *testing.T) {
	store, job, destination, now := retentionPairFixture(t)
	old := addRetentionPairRun(t, store, job, destination, now.Add(-2*time.Hour), "old")
	_ = addRetentionPairRun(t, store, job, destination, now.Add(-time.Hour), "new")
	if err := os.WriteFile(old.Artifact.Path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ApplyRetention(context.Background(), store, job, now)
	if err == nil || !strings.Contains(err.Error(), "changed artifact") {
		t.Fatalf("ApplyRetention() error = %v, want changed-artifact refusal", err)
	}
	for _, path := range []string{old.Artifact.Path, old.Artifact.ManifestPath} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("retention changed pair member %s after refusal: %v", path, statErr)
		}
	}
}

func TestApplyRetentionResumesAfterManifestWasAlreadyRemoved(t *testing.T) {
	store, job, destination, now := retentionPairFixture(t)
	old := addRetentionPairRun(t, store, job, destination, now.Add(-2*time.Hour), "old")
	_ = addRetentionPairRun(t, store, job, destination, now.Add(-time.Hour), "new")
	if err := os.Remove(old.Artifact.ManifestPath); err != nil {
		t.Fatal(err)
	}

	removed, err := ApplyRetention(context.Background(), store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != old.Artifact.Path {
		t.Fatalf("resumed retention removed %#v, want %s", removed, old.Artifact.Path)
	}
}

func TestApplyRetentionRejectsRcloneBeforeToolLookupOrDeletion(t *testing.T) {
	store, job, destination, now := retentionPairFixture(t)
	old := addRetentionPairRun(t, store, job, destination, now.Add(-2*time.Hour), "old")
	newest := addRetentionPairRun(t, store, job, destination, now.Add(-time.Hour), "new")
	job.Destination = "rclone://archive/team/backups"

	toolLookedUp := false
	originalFinder := findRcloneTool
	findRcloneTool = func(string) (string, error) {
		toolLookedUp = true
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { findRcloneTool = originalFinder })

	removed, err := ApplyRetention(context.Background(), store, job, now)
	if err == nil || !strings.Contains(err.Error(), "rclone backup retention is disabled") {
		t.Fatalf("ApplyRetention() = %#v, %v; want explicit fail-closed error", removed, err)
	}
	if len(removed) != 0 {
		t.Fatalf("disabled rclone retention reported removals: %#v", removed)
	}
	if toolLookedUp {
		t.Fatal("disabled rclone retention looked up or invoked rclone")
	}
	for _, path := range []string{
		old.Artifact.Path, old.Artifact.ManifestPath,
		newest.Artifact.Path, newest.Artifact.ManifestPath,
	} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("disabled rclone retention touched recorded artifact %s: %v", path, statErr)
		}
	}
}

func retentionPairFixture(t *testing.T) (*Store, Job, string, time.Time) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	destination := t.TempDir()
	now := time.Date(2026, 9, 3, 6, 30, 0, 0, time.UTC)
	job := Job{
		Name: "manifest retention", ConnectionID: "conn", Destination: destination,
		Compression: CompressionNone, Encryption: EncryptionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{KeepLast: 1}, TimeoutMinutes: 5,
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	return store, job, destination, now
}

func addRetentionPairRun(t *testing.T, store *Store, job Job, destination string, startedAt time.Time, suffix string) Run {
	t.Helper()
	ctx := context.Background()
	owner := "pair-retention-" + suffix
	if _, err := store.ClaimJob(ctx, job.ID, owner, startedAt); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, job.ID, TriggerManual, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("backup-" + suffix)
	digest := sha256.Sum256(payload)
	artifactPath := filepath.Join(destination, "backup-"+suffix+".dump")
	if err := os.WriteFile(artifactPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{
		ID: "artifact_" + suffix, Path: artifactPath, Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:]),
		Format: "postgres_custom", Verified: true, CreatedAt: startedAt,
		ManifestPath: artifactPath + ArtifactManifestSuffix,
	}
	manifest := ArtifactManifest{
		SchemaVersion: ArtifactManifestSchemaVersion,
		ArtifactID:    artifact.ID, RunID: run.ID, JobID: job.ID, CreatedAt: startedAt,
		ProducerID: "producer_retention", DBTermVersion: "test", Engine: config.PostgreSQL,
		Format: artifact.Format, Compression: CompressionNone, Encryption: EncryptionSchemeNone,
		SizeBytes: artifact.Size, SHA256: artifact.SHA256, Verification: ArtifactVerificationPassed, VerificationLevel: ArtifactVerificationBasic,
		FileSets: []ManifestFileSet{}, Warnings: []string{},
	}
	var manifestData bytes.Buffer
	if err := EncodeArtifactManifest(&manifestData, manifest); err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestData.Bytes())
	artifact.ManifestSize = int64(manifestData.Len())
	artifact.ManifestSHA256 = hex.EncodeToString(manifestDigest[:])
	if err := os.WriteFile(artifact.ManifestPath, manifestData.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.FinishedAt = startedAt.Add(time.Minute)
	run.Artifact = artifact
	if err := store.FinishRun(ctx, &run, owner); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestApplyRetentionDeletesArtifactsAndMarksRunsPruned(t *testing.T) {
	stateDir := t.TempDir()
	destination := t.TempDir()
	store, err := OpenStore(filepath.Join(stateDir, "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	job := Job{
		Name: "retention", ConnectionID: "conn", Enabled: true, Destination: destination,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{KeepLast: 2},
	}
	if err := job.ApplyDefaults(now.Add(-5 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}

	paths := make([]string, 4)
	for index := range paths {
		paths[index] = filepath.Join(destination, "backup-"+string(rune('1'+index))+".sql")
		if err := os.WriteFile(paths[index], []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		startedAt := now.Add(time.Duration(index-4) * 24 * time.Hour)
		if _, err := store.ClaimJob(ctx, job.ID, "retention-test", startedAt); err != nil {
			t.Fatal(err)
		}
		run, err := store.StartRun(ctx, job.ID, TriggerManual, startedAt)
		if err != nil {
			t.Fatal(err)
		}
		run.Status = RunSucceeded
		run.FinishedAt = startedAt.Add(time.Minute)
		run.Artifact = Artifact{Path: paths[index], Size: 6, Verified: true, CreatedAt: run.FinishedAt}
		if err := store.FinishRun(ctx, &run, "retention-test"); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := ApplyRetention(ctx, store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %d artifacts, want 2: %#v", len(removed), removed)
	}
	for index, path := range paths {
		_, statErr := os.Stat(path)
		if index < 2 && !os.IsNotExist(statErr) {
			t.Fatalf("expired artifact %s still exists (stat error %v)", path, statErr)
		}
		if index >= 2 && statErr != nil {
			t.Fatalf("retained artifact %s: %v", path, statErr)
		}
	}

	runs, err := store.ListRuns(ctx, job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 {
		t.Fatalf("history has %d runs, want 4", len(runs))
	}
	pruned := make(map[string]Artifact, 2)
	for _, run := range runs {
		if !run.Artifact.PrunedAt.IsZero() {
			pruned[run.Artifact.Path] = run.Artifact
		}
	}
	for _, path := range paths[:2] {
		artifact, ok := pruned[path]
		if !ok {
			t.Errorf("run for removed artifact %s was not marked pruned", path)
			continue
		}
		if artifact.PruneReason != "retention" || !artifact.PrunedAt.Equal(now) {
			t.Errorf("prune metadata for %s = %#v", path, artifact)
		}
	}
	for _, path := range paths[2:] {
		if _, ok := pruned[path]; ok {
			t.Errorf("retained artifact %s was marked pruned", path)
		}
	}

	removed, err = ApplyRetention(ctx, store, job, now.Add(time.Hour))
	if err != nil || len(removed) != 0 {
		t.Fatalf("second ApplyRetention() = %#v, %v; want idempotent no-op", removed, err)
	}
}

func TestVerifyRecordedArtifactForPruneRefusesChangedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.dump")
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err := verifyRecordedArtifactForPrune(context.Background(), path, Artifact{
		Path: path, Size: 6, SHA256: strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("verification = (%t, %v), want checksum refusal", exists, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("changed artifact was modified: %v", statErr)
	}
}

func TestLocalRetentionQuarantineRefusesPathSwapBeforeCapture(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "artifact.dump")
	payload := []byte("recorded backup")
	digest := sha256.Sum256(payload)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	expected := Artifact{Path: path, Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	replacement := []byte("unrelated replacement")
	attackerPath := filepath.Join(directory, "attacker-kept-original.dump")

	removed, err := removeVerifiedLocalForPruneWithRename(context.Background(), path, expected, nil, func(source, quarantine string) error {
		if err := os.Rename(source, attackerPath); err != nil {
			return err
		}
		if err := os.WriteFile(source, replacement, 0o600); err != nil {
			return err
		}
		return os.Rename(source, quarantine)
	})
	if err == nil || removed || !strings.Contains(err.Error(), "preserved a changed capture") {
		t.Fatalf("removeVerifiedLocalForPruneWithRename() = %t, %v; want preserved swap refusal", removed, err)
	}
	if got, readErr := os.ReadFile(attackerPath); readErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("recorded bytes = %q, %v; want preserved original", got, readErr)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatal(readErr)
	}
	preservedReplacement := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".dbterm-prune_") || !strings.HasSuffix(entry.Name(), ".quarantine") {
			continue
		}
		got, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr == nil && bytes.Equal(got, replacement) {
			preservedReplacement = true
		}
	}
	if !preservedReplacement {
		t.Fatalf("swapped replacement was deleted instead of preserved: %v", entries)
	}
}

func TestApplyRetentionResumesDurableQuarantineAfterCrash(t *testing.T) {
	for _, capturedMember := range []string{"artifact", "manifest"} {
		t.Run(capturedMember, func(t *testing.T) {
			store, job, destination, now := retentionPairFixture(t)
			old := addRetentionPairRun(t, store, job, destination, now.Add(-2*time.Hour), "old-"+capturedMember)
			_ = addRetentionPairRun(t, store, job, destination, now.Add(-time.Hour), "new-"+capturedMember)

			capturedPath := old.Artifact.Path
			capturedArtifact := old.Artifact
			if capturedMember == "manifest" {
				capturedPath = old.Artifact.ManifestPath
				capturedArtifact = Artifact{Size: old.Artifact.ManifestSize, SHA256: old.Artifact.ManifestSHA256}
			}
			quarantinePath := localPruneQuarantinePath(capturedPath, capturedArtifact)
			if err := os.Rename(capturedPath, quarantinePath); err != nil {
				t.Fatal(err)
			}

			removed, err := ApplyRetention(context.Background(), store, job, now)
			if err != nil {
				t.Fatalf("resume retention after captured %s: %v", capturedMember, err)
			}
			if len(removed) != 1 || removed[0] != old.Artifact.Path {
				t.Fatalf("removed = %#v, want %s", removed, old.Artifact.Path)
			}
			for _, path := range []string{old.Artifact.Path, old.Artifact.ManifestPath, quarantinePath} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("resumed retention left %s: %v", path, err)
				}
			}
		})
	}
}

func TestApplyRetentionConsidersArtifactsBeyondHistoryViewLimit(t *testing.T) {
	stateDir := t.TempDir()
	destination := t.TempDir()
	store, err := OpenStore(filepath.Join(stateDir, "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	job := Job{
		Name: "large retention history", ConnectionID: "conn", Enabled: true, Destination: destination,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{KeepLast: 1500},
	}
	if err := store.UpsertJob(ctx, &job); err != nil {
		t.Fatal(err)
	}

	const runCount = 1505
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < runCount; index++ {
		path := filepath.Join(destination, "artifact-"+strconv.Itoa(index)+".dump")
		if index < 5 {
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
		}
		finishedAt := now.Add(time.Duration(index-runCount) * time.Minute)
		run := Run{
			ID: "run_bulk_" + strconv.Itoa(index), JobID: job.ID, Trigger: TriggerScheduled,
			Status: RunSucceeded, StartedAt: finishedAt.Add(-time.Second), FinishedAt: finishedAt,
			Artifact: Artifact{Path: path, Size: 1, Verified: true, CreatedAt: finishedAt},
		}
		payload, err := json.Marshal(run)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO backup_runs
			(id, job_id, trigger_kind, status, started_at, finished_at, run_json)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, run.ID, run.JobID, run.Trigger, run.Status,
			formatTime(run.StartedAt), formatTime(run.FinishedAt), payload); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	removed, err := ApplyRetention(ctx, store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 5 {
		t.Fatalf("removed %d artifacts, want 5 beyond the bounded history view", len(removed))
	}
	for index := 0; index < 5; index++ {
		path := filepath.Join(destination, "artifact-"+strconv.Itoa(index)+".dump")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired artifact %s still exists (stat error %v)", path, err)
		}
		var payload []byte
		if err := store.db.QueryRowContext(ctx, `SELECT run_json FROM backup_runs WHERE id = ?`, "run_bulk_"+strconv.Itoa(index)).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var run Run
		if err := json.Unmarshal(payload, &run); err != nil {
			t.Fatal(err)
		}
		if run.Artifact.PrunedAt.IsZero() || run.Artifact.PruneReason != "retention" {
			t.Fatalf("run %d prune metadata = %#v", index, run.Artifact)
		}
	}
}

func TestApplyRetentionMaxTotalBytesKeepsNewestWithinCeiling(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	destination := t.TempDir()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	job := Job{
		Name: "size only", ConnectionID: "conn", Enabled: true, Destination: destination,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{MaxTotalBytes: 8}, TimeoutMinutes: 5,
	}
	if err := job.ApplyDefaults(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if job.Retention.KeepLast != 0 {
		t.Fatalf("size-only ApplyDefaults set KeepLast=%d, want 0", job.Retention.KeepLast)
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}

	paths := make([]string, 4)
	for index := range paths {
		paths[index] = addRetentionRun(t, store, job, destination, now.Add(time.Duration(index-4)*time.Hour), 4, index)
	}
	removed, err := ApplyRetention(context.Background(), store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0] != paths[0] || removed[1] != paths[1] {
		t.Fatalf("removed = %#v, want oldest-first %#v", removed, paths[:2])
	}
	for index, path := range paths {
		_, statErr := os.Stat(path)
		if index < 2 && !os.IsNotExist(statErr) {
			t.Fatalf("old artifact %s remains: %v", path, statErr)
		}
		if index >= 2 && statErr != nil {
			t.Fatalf("new artifact %s was not retained: %v", path, statErr)
		}
	}
}

func TestApplyRetentionAlwaysKeepsOversizedNewestArtifact(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	destination := t.TempDir()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	job := Job{
		Name: "oversized newest", ConnectionID: "conn", Enabled: true, Destination: destination,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual},
		Retention: Retention{MaxTotalBytes: 2}, TimeoutMinutes: 5,
	}
	if err := job.ApplyDefaults(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	oldPath := addRetentionRun(t, store, job, destination, now.Add(-2*time.Hour), 1, 1)
	newPath := addRetentionRun(t, store, job, destination, now.Add(-time.Hour), 5, 2)
	removed, err := ApplyRetention(context.Background(), store, job, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != oldPath {
		t.Fatalf("removed = %#v, want only %s", removed, oldPath)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("oversized newest artifact was removed: %v", err)
	}
}

func addRetentionRun(t *testing.T, store *Store, job Job, destination string, startedAt time.Time, size int, suffix int) string {
	t.Helper()
	path := filepath.Join(destination, "size-artifact-"+strconv.Itoa(suffix)+".dump")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner := "size-retention-test"
	if _, err := store.ClaimJob(ctx, job.ID, owner, startedAt); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, job.ID, TriggerManual, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.FinishedAt = startedAt.Add(time.Minute)
	run.Artifact = Artifact{Path: path, Size: int64(size), Verified: true, CreatedAt: run.FinishedAt}
	if err := store.FinishRun(ctx, &run, owner); err != nil {
		t.Fatal(err)
	}
	return path
}
