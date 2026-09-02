package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyCatalogMigrationCreatesDurableTablesAndIndexes(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state", "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	want := map[string]string{
		"copy_jobs":                     "table",
		"copy_runs":                     "table",
		"copy_jobs_name_nocase_idx":     "index",
		"copy_jobs_due_idx":             "index",
		"copy_jobs_after_success_idx":   "index",
		"copy_runs_job_idx":             "index",
		"copy_volume_leases":            "table",
		"copy_volume_leases_expiry_idx": "index",
	}
	for name, kind := range want {
		var got string
		if err := store.db.QueryRow(`SELECT type FROM sqlite_master WHERE name = ?`, name).Scan(&got); err != nil {
			t.Fatalf("find migrated %s %s: %v", kind, name, err)
		}
		if got != kind {
			t.Fatalf("migrated object %s type = %q, want %q", name, got, kind)
		}
	}
	version, ok, err := store.GetMeta(context.Background(), "schema_version")
	if err != nil || !ok || version != "3" {
		t.Fatalf("schema version = %q, %t, %v; want 3", version, ok, err)
	}
	// Opening the same catalog again exercises the idempotent migration path.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(store.path)
	if err != nil {
		t.Fatalf("reopen migrated catalog: %v", err)
	}
	defer reopened.Close()
}

func TestCopyStoreTimedRunLifecycleAndLease(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := CopyJob{
		Name: "Vrindavan to CT400", Enabled: true, Mode: CopyModePull,
		Source: CopyEndpoint{
			Kind: CopyEndpointSFTP, Location: "sftp://backup@vrindavan.example/E:/archives",
			CredentialRef: filepath.Join(t.TempDir(), "id_ed25519"), PinnedHostKey: copyModelTestPin(),
		},
		Destination:  CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Trigger:      CopyTriggerTimed,
		Schedule:     Schedule{Kind: ScheduleInterval, EveryMinutes: 30, RunMissedOnWake: true},
		Verification: CopyVerificationSHA256Format,
		Retention:    Retention{KeepLast: 7}, TimeoutMinutes: 5,
	}
	if err := job.ApplyDefaults(now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	job.Enabled = false
	recordStoredCopyTransferProofForTest(t, store, &job, now.Add(-30*time.Minute))
	job.Enabled = true
	job.NextRunAt = now.Add(-time.Minute)
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}

	byName, err := store.GetCopyJob(ctx, strings.ToUpper(job.Name))
	if err != nil || byName.ID != job.ID {
		t.Fatalf("GetCopyJob(name) = %#v, %v", byName, err)
	}
	jobs, err := store.ListCopyJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("ListCopyJobs() = %#v, %v", jobs, err)
	}
	claimed, err := store.ClaimDueCopyJobs(ctx, now, "vault-agent", 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != job.ID {
		t.Fatalf("ClaimDueCopyJobs() = %#v, %v", claimed, err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "other-agent", now); !errors.Is(err, ErrCopyJobBusy) {
		t.Fatalf("competing ClaimCopyJob() error = %v, want ErrCopyJobBusy", err)
	}
	if err := store.DeleteCopyJob(ctx, job.ID); !errors.Is(err, ErrCopyJobBusy) {
		t.Fatalf("leased DeleteCopyJob() error = %v, want ErrCopyJobBusy", err)
	}
	job.Name = "editing while active"
	if err := store.UpsertCopyJob(ctx, &job); !errors.Is(err, ErrCopyJobBusy) {
		t.Fatalf("leased UpsertCopyJob() error = %v, want ErrCopyJobBusy", err)
	}

	run, err := store.StartCopyRun(ctx, job.ID, CopyTriggerTimed, now)
	if err != nil {
		t.Fatal(err)
	}
	if run.RequiredVerification != CopyVerificationSHA256Format || run.Status != RunRunning {
		t.Fatalf("started copy run = %#v", run)
	}
	run.Status = RunSucceeded
	run.FinishedAt = now.Add(time.Minute)
	run.Discovered = 1
	run.BytesCopied = 7
	run.Artifacts = []CopyArtifactResult{{
		ArtifactID: "artifact_123", Source: "sftp://backup@vrindavan.example/E:/archives/a.dump", Destination: "a.dump",
		SizeBytes: 7, SHA256: strings.Repeat("a", 64), Verification: CopyVerificationSHA256Format,
		VerifiedAt: run.FinishedAt, ManifestPath: "a.dump.dbterm.json", ManifestSize: 101,
		ManifestSHA256: strings.Repeat("b", 64), PublicationState: ArtifactPublicationComplete,
	}}
	if err := store.FinishCopyRun(ctx, &run, "vault-agent"); err != nil {
		t.Fatal(err)
	}

	stored, err := store.GetCopyJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LastRunAt.Equal(run.FinishedAt) || !stored.NextRunAt.After(run.FinishedAt) {
		t.Fatalf("copy job timestamps = last %s next %s", stored.LastRunAt, stored.NextRunAt)
	}
	runs, err := store.ListCopyRuns(ctx, job.ID, 10)
	if err != nil || len(runs) != 2 || runs[0].Status != RunSucceeded || len(runs[0].Artifacts) != 1 {
		t.Fatalf("ListCopyRuns() = %#v, %v", runs, err)
	}
	latest, ok, err := store.LatestCopyRun(ctx, job.ID)
	if err != nil || !ok || latest.ID != run.ID {
		t.Fatalf("LatestCopyRun() = %#v, %t, %v", latest, ok, err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "manual-owner", run.FinishedAt); err != nil {
		t.Fatalf("completed run did not release lease: %v", err)
	}
	if err := store.ReleaseCopyJob(ctx, job.ID, "wrong-owner"); !errors.Is(err, ErrCopyJobLeaseLost) {
		t.Fatalf("wrong-owner release error = %v, want ErrCopyJobLeaseLost", err)
	}
	if err := store.ReleaseCopyJob(ctx, job.ID, "manual-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCopyJobEnabled(ctx, job.ID, false); err != nil {
		t.Fatal(err)
	}
	disabled, err := store.GetCopyJob(ctx, job.ID)
	if err != nil || disabled.Enabled || !disabled.NextRunAt.IsZero() {
		t.Fatalf("disabled copy job = %#v, %v", disabled, err)
	}
	if err := store.SetCopyJobEnabled(ctx, job.ID, true); err != nil {
		t.Fatal(err)
	}
	reenabled, err := store.GetCopyJob(ctx, job.ID)
	if err != nil || !reenabled.Enabled || reenabled.NextRunAt.IsZero() {
		t.Fatalf("re-enabled timed copy job = %#v, %v", reenabled, err)
	}
}

func TestCopyStoreAfterSuccessReferenceAndUniqueName(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	backupJob := Job{
		Name: "producer", ConnectionID: "conn", Destination: t.TempDir(),
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2},
	}
	if err := store.UpsertJob(ctx, &backupJob); err != nil {
		t.Fatal(err)
	}
	copyJob := CopyJob{
		Name: "Immediate vault push", Enabled: false, Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal}, SourceBackupJobID: backupJob.ID,
		Destination: CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://vault/dbterm"},
		Trigger:     CopyTriggerAfterSuccess,
	}
	recordStoredCopyTransferProofForTest(t, store, &copyJob, time.Now().Add(-time.Minute))
	copyJob.Enabled = true
	if err := store.UpsertCopyJob(ctx, &copyJob); err != nil {
		t.Fatal(err)
	}
	if !copyJob.NextRunAt.IsZero() {
		t.Fatalf("after-success copy unexpectedly received scheduled time %s", copyJob.NextRunAt)
	}
	afterSuccess, err := store.ListEnabledAfterSuccessCopyJobs(ctx, backupJob.ID)
	if err != nil || len(afterSuccess) != 1 || afterSuccess[0].ID != copyJob.ID {
		t.Fatalf("ListEnabledAfterSuccessCopyJobs() = %#v, %v", afterSuccess, err)
	}
	missing := copyJob
	missing.ID = ""
	missing.Name = "missing source"
	missing.SourceBackupJobID = "job_missing"
	missing.Enabled = false
	if err := store.UpsertCopyJob(ctx, &missing); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing source backup error = %v", err)
	}
	duplicate := copyJob
	duplicate.ID = ""
	duplicate.Name = strings.ToLower(copyJob.Name)
	duplicate.Enabled = false
	if err := store.UpsertCopyJob(ctx, &duplicate); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("duplicate copy job error = %v", err)
	}

	if _, err := store.ClaimCopyJob(ctx, copyJob.ID, "push-agent", time.Now()); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(ctx, copyJob.ID, CopyTriggerAfterSuccess, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunFailed
	run.Error = "manifest publication failed"
	run.Artifacts = []CopyArtifactResult{{
		ArtifactID: "artifact_boundary", Source: "producer.dump", Destination: "rclone://vault/dbterm/producer.dump",
		SizeBytes: 8, SHA256: strings.Repeat("c", 64), Verification: CopyVerificationSHA256Format,
		VerifiedAt: time.Now(), PublicationState: ArtifactPublicationArtifactOnly,
	}}
	if err := store.FinishCopyRun(ctx, &run, "push-agent"); err != nil {
		t.Fatalf("persist artifact-only failure boundary: %v", err)
	}
	got, ok, err := store.LatestCopyRun(ctx, copyJob.ID)
	if err != nil || !ok || got.Status != RunFailed || len(got.Artifacts) != 1 || got.Artifacts[0].PublicationState != ArtifactPublicationArtifactOnly {
		t.Fatalf("stored boundary run = %#v, %t, %v", got, ok, err)
	}
	byID, err := store.GetCopyRun(ctx, run.ID)
	if err != nil || byID.ID != run.ID || byID.Status != RunFailed {
		t.Fatalf("GetCopyRun() = %#v, %v", byID, err)
	}
}

func TestCopySourceBackupDestinationIsMaterializedAndDoesNotRedirect(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	firstSource := t.TempDir()
	secondSource := t.TempDir()
	explicitSource := t.TempDir()
	backupJob := Job{
		Name: "bound producer", ConnectionID: "conn", Destination: firstSource,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2},
	}
	if err := store.UpsertJob(ctx, &backupJob); err != nil {
		t.Fatal(err)
	}
	copyJob := CopyJob{
		Name: "bound producer copy", Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal}, SourceBackupJobID: backupJob.ID,
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Trigger:     CopyTriggerTimed,
		Schedule: Schedule{
			Kind: ScheduleInterval, EveryMinutes: 30,
		},
	}
	if err := store.UpsertCopyJob(ctx, &copyJob); err != nil {
		t.Fatal(err)
	}
	if copyJob.Source.Location != firstSource {
		t.Fatalf("materialized copy source = %q, want %q", copyJob.Source.Location, firstSource)
	}
	stored, err := store.GetCopyJob(ctx, copyJob.ID)
	if err != nil || stored.Source.Location != firstSource {
		t.Fatalf("stored materialized copy source = %q, %v; want %q", stored.Source.Location, err, firstSource)
	}

	backupJob.Destination = secondSource
	if err := store.UpsertJob(ctx, &backupJob); err != nil {
		t.Fatal(err)
	}
	stored, err = store.GetCopyJob(ctx, copyJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _, err := (CopyRunner{Store: store}).resolveLocalSource(ctx, stored)
	if err != nil || resolved.Location != firstSource {
		t.Fatalf("copy source after backup destination edit = %q, %v; want immutable binding %q", resolved.Location, err, firstSource)
	}

	recordStoredCopyTransferProofForTest(t, store, &stored, time.Now().Add(-time.Minute))
	if !stored.HasCurrentTransferProof() {
		t.Fatal("real transfer did not establish proof for materialized source")
	}
	stored.Enabled = true
	if err := store.UpsertCopyJob(ctx, &stored); err != nil {
		t.Fatalf("enable copy proved against materialized source: %v", err)
	}
	changedSource := stored
	changedSource.Source.Location = explicitSource
	if err := store.UpsertCopyJob(ctx, &changedSource); !errors.Is(err, ErrCopyJobTransferProofRequired) {
		t.Fatalf("enabled copy source update error = %v, want ErrCopyJobTransferProofRequired", err)
	}
	stored.Enabled = false
	stored.Source.Location = explicitSource
	if err := store.UpsertCopyJob(ctx, &stored); err != nil {
		t.Fatalf("save intentional source update: %v", err)
	}
	updated, err := store.GetCopyJob(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Source.Location != explicitSource {
		t.Fatalf("explicit copy source = %q, want preserved %q", updated.Source.Location, explicitSource)
	}
	if updated.HasCurrentTransferProof() {
		t.Fatal("intentional source update retained proof for the old source")
	}
}

func TestBackupDestinationChangeRequiresDependentCopyDisable(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	firstSource := t.TempDir()
	secondSource := t.TempDir()
	backupJob := Job{
		Name: "moving producer", ConnectionID: "conn", Destination: firstSource,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2},
	}
	if err := store.UpsertJob(ctx, &backupJob); err != nil {
		t.Fatal(err)
	}
	copyJob := CopyJob{
		Name: "proved dependent", Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal}, SourceBackupJobID: backupJob.ID,
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Trigger:     CopyTriggerAfterSuccess,
	}
	recordStoredCopyTransferProofForTest(t, store, &copyJob, time.Now().Add(-time.Minute))
	if err := store.SetCopyJobEnabled(ctx, copyJob.ID, true); err != nil {
		t.Fatal(err)
	}

	backupJob.Destination = secondSource
	if err := store.UpsertJob(ctx, &backupJob); err == nil || !strings.Contains(err.Error(), "disable it") || !strings.Contains(err.Error(), "re-prove") {
		t.Fatalf("enabled dependent destination-change error = %v", err)
	}
	storedBackup, err := store.GetJob(ctx, backupJob.ID)
	if err != nil || storedBackup.Destination != firstSource {
		t.Fatalf("rejected destination change stored %#v, %v; want %q", storedBackup, err, firstSource)
	}
	if err := store.SetCopyJobEnabled(ctx, copyJob.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertJob(ctx, &backupJob); err != nil {
		t.Fatalf("destination change after dependent disable: %v", err)
	}
	storedCopy, err := store.GetCopyJob(ctx, copyJob.ID)
	if err != nil || storedCopy.Source.Location != firstSource {
		t.Fatalf("disabled dependent source changed to %q, %v; want immutable binding %q", storedCopy.Source.Location, err, firstSource)
	}
}

func TestCopySourceBackupDestinationRejectsRemoteLegacyJob(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	backupJob := Job{
		Name: "legacy remote producer", ConnectionID: "conn", Destination: t.TempDir(),
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2},
	}
	if err := store.UpsertJob(ctx, &backupJob); err != nil {
		t.Fatal(err)
	}
	backupJob.Destination = "rclone://legacy-vault/backups"
	payload, err := json.Marshal(backupJob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE backup_jobs SET job_json = ? WHERE id = ?`, payload, backupJob.ID); err != nil {
		t.Fatal(err)
	}
	copyJob := CopyJob{
		Name: "unsafe dynamic source", Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal}, SourceBackupJobID: backupJob.ID,
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Trigger:     CopyTriggerManual,
	}
	if err := store.UpsertCopyJob(ctx, &copyJob); err == nil || !strings.Contains(err.Error(), "does not publish to a local directory") {
		t.Fatalf("remote source backup destination error = %v", err)
	}
}

func TestCopyAutomaticEnableRequiresRealTransferForCurrentConfiguration(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := CopyJob{
		Name: "proved scheduled vault", Enabled: true, Mode: CopyModePush,
		Source:       CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Destination:  CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Trigger:      CopyTriggerTimed,
		Schedule:     Schedule{Kind: ScheduleInterval, EveryMinutes: 30},
		Verification: CopyVerificationSHA256Format,
	}
	if err := job.ApplyDefaults(now); err != nil {
		t.Fatal(err)
	}
	forgedFingerprint, err := job.TransferConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	job.TransferProofAt = now
	job.TransferProofFingerprint = forgedFingerprint
	if err := store.UpsertCopyJob(ctx, &job); !errors.Is(err, ErrCopyJobTransferProofRequired) {
		t.Fatalf("new enabled automatic copy error = %v, want ErrCopyJobTransferProofRequired", err)
	}
	if _, err := store.GetCopyJob(ctx, job.ID); err == nil {
		t.Fatal("rejected enabled copy job was unexpectedly saved")
	}

	job.Enabled = false
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCopyJobEnabled(ctx, job.ID, true); !errors.Is(err, ErrCopyJobTransferProofRequired) || !strings.Contains(err.Error(), "read-only endpoint test does not count") {
		t.Fatalf("unproved enable error = %v", err)
	}

	// A successful no-op scan is useful health evidence, but it neither wrote
	// bytes nor proved publication to this destination.
	if _, err := store.ClaimCopyJob(ctx, job.ID, "noop-owner", now); err != nil {
		t.Fatal(err)
	}
	noOp, err := store.StartCopyRun(ctx, job.ID, CopyTriggerManual, now)
	if err != nil {
		t.Fatal(err)
	}
	noOp.Status = RunSucceeded
	noOp.FinishedAt = now.Add(time.Minute)
	if err := store.FinishCopyRun(ctx, &noOp, "noop-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCopyJobEnabled(ctx, job.ID, true); !errors.Is(err, ErrCopyJobTransferProofRequired) {
		t.Fatalf("no-op scan unlocked automation: %v", err)
	}

	if _, err := store.ClaimCopyJob(ctx, job.ID, "transfer-owner", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(ctx, job.ID, CopyTriggerManual, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.FinishedAt = now.Add(3 * time.Minute)
	run.Discovered = 1
	run.BytesCopied = 4096
	run.Artifacts = []CopyArtifactResult{provedCopyArtifactForTest(run.FinishedAt, job.Destination.Location)}
	if err := store.FinishCopyRun(ctx, &run, "transfer-owner"); err != nil {
		t.Fatal(err)
	}
	proved, err := store.GetCopyJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !proved.HasCurrentTransferProof() || !proved.TransferProofAt.Equal(run.FinishedAt) {
		t.Fatalf("real transfer proof was not persisted: %+v", proved)
	}
	if err := store.SetCopyJobEnabled(ctx, job.ID, true); err != nil {
		t.Fatalf("proved automatic copy could not be enabled: %v", err)
	}

	proved, err = store.GetCopyJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	proved.Retention.KeepLast = 90
	proved.Schedule.EveryMinutes = 60
	if err := store.UpsertCopyJob(ctx, &proved); err != nil {
		t.Fatalf("non-transfer policy edit invalidated proof: %v", err)
	}
	changedDestination := proved
	changedDestination.Destination.Location = t.TempDir()
	if err := store.UpsertCopyJob(ctx, &changedDestination); !errors.Is(err, ErrCopyJobTransferProofRequired) {
		t.Fatalf("transport edit retained automatic proof: %v", err)
	}
	unchanged, err := store.GetCopyJob(ctx, job.ID)
	if err != nil || unchanged.Destination.Location != proved.Destination.Location {
		t.Fatalf("rejected transport edit changed stored job: %+v, %v", unchanged, err)
	}
}

func TestLegacyEnabledCopyWithoutProofStillLoadsButCannotBeReenabled(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job := CopyJob{
		Name: "legacy timed copy", Mode: CopyModePush,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Trigger:     CopyTriggerTimed,
		Schedule:    Schedule{Kind: ScheduleInterval, EveryMinutes: 30},
	}
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(payload, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "transfer_proof_at")
	delete(legacy, "transfer_proof_fingerprint")
	legacy["enabled"] = true
	payload, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE copy_jobs SET enabled = 1, job_json = ? WHERE id = ?`, payload, job.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetCopyJob(ctx, job.ID)
	if err != nil || !loaded.Enabled || loaded.HasCurrentTransferProof() {
		t.Fatalf("legacy enabled copy did not load compatibly: %+v, %v", loaded, err)
	}
	if err := store.SetCopyJobEnabled(ctx, job.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCopyJobEnabled(ctx, job.ID, true); !errors.Is(err, ErrCopyJobTransferProofRequired) {
		t.Fatalf("legacy unproved copy was re-enabled: %v", err)
	}
}

func TestCopyRunTransferProofRejectsFailedExistingAndReconciledResults(t *testing.T) {
	now := time.Now().UTC()
	fresh := provedCopyArtifactForTest(now, "vault/artifact")
	for name, run := range map[string]CopyRun{
		"successful transfer": {Status: RunSucceeded, BytesCopied: 1, Artifacts: []CopyArtifactResult{fresh}},
		"failed batch":        {Status: RunFailed, BytesCopied: 1, Artifacts: []CopyArtifactResult{fresh}},
		"zero bytes":          {Status: RunSucceeded, BytesCopied: 0, Artifacts: []CopyArtifactResult{fresh}},
		"already present": func() CopyRun {
			artifact := fresh
			artifact.AlreadyPresent = true
			return CopyRun{Status: RunSucceeded, BytesCopied: 1, Artifacts: []CopyArtifactResult{artifact}}
		}(),
		"reconciled": func() CopyRun {
			artifact := fresh
			artifact.Reconciled = true
			return CopyRun{Status: RunSucceeded, BytesCopied: 1, Artifacts: []CopyArtifactResult{artifact}}
		}(),
	} {
		want := name == "successful transfer"
		if got := copyRunProvesTransfer(run); got != want {
			t.Errorf("%s proof = %t, want %t", name, got, want)
		}
	}
}

func recordStoredCopyTransferProofForTest(t *testing.T, store *Store, job *CopyJob, at time.Time) {
	t.Helper()
	job.Enabled = false
	if err := store.UpsertCopyJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	owner := "proof-owner-" + job.ID
	if _, err := store.ClaimCopyJob(context.Background(), job.ID, owner, at); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(context.Background(), job.ID, CopyTriggerManual, at)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.FinishedAt = at.Add(time.Second)
	run.Discovered = 1
	run.BytesCopied = 4096
	run.Artifacts = []CopyArtifactResult{provedCopyArtifactForTest(run.FinishedAt, job.Destination.Location)}
	if err := store.FinishCopyRun(context.Background(), &run, owner); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetCopyJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	*job = stored
}

func provedCopyArtifactForTest(verifiedAt time.Time, destination string) CopyArtifactResult {
	return CopyArtifactResult{
		ArtifactID: "artifact_proof", Source: "source/artifact", Destination: destination,
		SizeBytes: 4096, SHA256: strings.Repeat("a", 64), Verification: CopyVerificationSHA256Format,
		VerifiedAt: verifiedAt, ManifestPath: destination + ArtifactManifestSuffix, ManifestSize: 512,
		ManifestSHA256: strings.Repeat("b", 64), PublicationState: ArtifactPublicationComplete,
	}
}

func TestFinishCopyRunLeaseLossRollsBackThenReconciles(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := CopyJob{
		Name: "manual local copy", Mode: CopyModePush,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Trigger:     CopyTriggerManual,
	}
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "original", now); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(ctx, job.ID, CopyTriggerManual, now)
	if err != nil {
		t.Fatal(err)
	}
	leaseExpiry := now.Add(time.Hour)
	if _, err := store.db.ExecContext(ctx, `UPDATE copy_jobs SET lease_owner = ?, lease_until = ? WHERE id = ?`,
		"replacement", formatTime(leaseExpiry), job.ID); err != nil {
		t.Fatal(err)
	}
	run.Status = RunFailed
	run.Error = "network unavailable"
	run.FinishedAt = now.Add(time.Minute)
	if err := store.FinishCopyRun(ctx, &run, "original"); !errors.Is(err, ErrCopyJobLeaseLost) {
		t.Fatalf("FinishCopyRun() error = %v, want ErrCopyJobLeaseLost", err)
	}
	stored, ok, err := store.LatestCopyRun(ctx, job.ID)
	if err != nil || !ok || stored.Status != RunRunning || !stored.FinishedAt.IsZero() {
		t.Fatalf("lease-loss transaction did not roll back: %#v, %t, %v", stored, ok, err)
	}
	if count, err := store.ReconcileStaleCopyRuns(ctx, now.Add(30*time.Minute)); err != nil || count != 0 {
		t.Fatalf("active-lease reconciliation = %d, %v", count, err)
	}
	if count, err := store.ReconcileStaleCopyRuns(ctx, leaseExpiry.Add(time.Second)); err != nil || count != 1 {
		t.Fatalf("expired-lease reconciliation = %d, %v", count, err)
	}
	stored, ok, err = store.LatestCopyRun(ctx, job.ID)
	if err != nil || !ok || stored.Status != RunFailed || stored.Error != staleCopyRunRecoveryError {
		t.Fatalf("reconciled copy run = %#v, %t, %v", stored, ok, err)
	}
	if err := store.DeleteCopyJob(ctx, job.Name); err != nil {
		t.Fatalf("delete recovered copy job: %v", err)
	}
	if history, err := store.ListCopyRuns(ctx, job.ID, 10); err != nil || len(history) != 1 {
		t.Fatalf("copy history was not retained after policy deletion: %#v, %v", history, err)
	}
}

func TestRecordCopyRunTerminalKeepsJobLeaseThroughPostProcessing(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := CopyJob{
		Name: "post-processing lease", Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()}, Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Trigger: CopyTriggerManual,
	}
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "owner", now); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(ctx, job.ID, CopyTriggerManual, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.FinishedAt = now.Add(time.Minute)
	if err := store.recordCopyRunTerminal(ctx, &run, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "competitor", now.Add(2*time.Minute)); !errors.Is(err, ErrCopyJobBusy) {
		t.Fatalf("competing post-processing claim error = %v, want ErrCopyJobBusy", err)
	}
	if err := store.ReleaseCopyJob(ctx, job.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "competitor", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("claim after post-processing release: %v", err)
	}
}

func TestRenewCopyJobLeaseRequiresSameOwnerAndUnexpiredLease(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := localCopyRunnerJob(t, t.TempDir(), t.TempDir())
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "owner", now); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewCopyJobLease(ctx, job.ID, "other", now.Add(time.Minute), now.Add(3*time.Hour)); !errors.Is(err, ErrCopyJobLeaseLost) {
		t.Fatalf("wrong-owner renewal error = %v, want ErrCopyJobLeaseLost", err)
	}
	renewedUntil := now.Add(3 * time.Hour)
	if err := store.RenewCopyJobLease(ctx, job.ID, "owner", now.Add(time.Minute), renewedUntil); err != nil {
		t.Fatalf("live owner renewal: %v", err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "competitor", now.Add(2*time.Hour)); !errors.Is(err, ErrCopyJobBusy) {
		t.Fatalf("renewed lease allowed early competitor: %v", err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "competitor", renewedUntil.Add(time.Second)); err != nil {
		t.Fatalf("expired renewed lease blocked competitor: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE copy_jobs SET lease_until = ? WHERE id = ?`, formatTime(now.Add(-time.Second)), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewCopyJobLease(ctx, job.ID, "competitor", now, now.Add(time.Hour)); !errors.Is(err, ErrCopyJobLeaseLost) {
		t.Fatalf("expired lease renewal error = %v, want ErrCopyJobLeaseLost", err)
	}
	if err := store.RenewCopyJobLease(ctx, job.ID, "competitor", now, now); err == nil || !strings.Contains(err.Error(), "expire after") {
		t.Fatalf("invalid renewal interval error = %v", err)
	}
}

func TestLostPostProcessingLeaseDoesNotUndoTerminalCopyValidity(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	job := localCopyRunnerJob(t, t.TempDir(), t.TempDir())
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "owner", now); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(ctx, job.ID, CopyTriggerManual, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.FinishedAt = now.Add(time.Minute)
	if err := store.recordCopyRunTerminal(ctx, &run, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE copy_jobs SET lease_owner = ?, lease_until = ? WHERE id = ?`, "replacement", formatTime(now.Add(time.Hour)), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewCopyJobLease(ctx, job.ID, "owner", now.Add(2*time.Minute), now.Add(2*time.Hour)); !errors.Is(err, ErrCopyJobLeaseLost) {
		t.Fatalf("lost post-processing lease renewal error = %v", err)
	}
	stored, err := store.GetCopyRun(ctx, run.ID)
	if err != nil || stored.Status != RunSucceeded || !stored.FinishedAt.Equal(run.FinishedAt) {
		t.Fatalf("terminal copy validity changed after lease loss: %+v, %v", stored, err)
	}
}

func TestCopyStoreRejectsTerminalResultWeakerThanPolicy(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	job := CopyJob{
		Name: "strong verification", Mode: CopyModePush,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Trigger:     CopyTriggerManual, Verification: CopyVerificationSHA256,
	}
	if err := store.UpsertCopyJob(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCopyJob(ctx, job.ID, "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(ctx, job.ID, CopyTriggerManual, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run.Status = RunSucceeded
	run.Artifacts = []CopyArtifactResult{{
		ArtifactID: "artifact", Source: "source", Destination: "destination", SizeBytes: 1,
		Verification: CopyVerificationSizeOnly, VerifiedAt: time.Now(),
		ManifestPath: "destination.dbterm.json", ManifestSize: 1, ManifestSHA256: strings.Repeat("a", 64),
		PublicationState: ArtifactPublicationComplete,
	}}
	if err := store.FinishCopyRun(ctx, &run, "owner"); err == nil || !strings.Contains(err.Error(), "weaker") {
		t.Fatalf("FinishCopyRun() weak verification error = %v", err)
	}
	stored, ok, err := store.LatestCopyRun(ctx, job.ID)
	if err != nil || !ok || stored.Status != RunRunning {
		t.Fatalf("rejected terminal result mutated history: %#v, %t, %v", stored, ok, err)
	}
}

func TestCopyStoreRejectsDuplicateEnabledTransferOwner(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := t.TempDir()
	destination := t.TempDir()
	first := CopyJob{
		Name: "first owner", Enabled: true, Mode: CopyModePush,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: source},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		Trigger:     CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual},
		ArtifactFilter: CopyArtifactFilter{ProducerID: "producer_one"},
	}
	if err := store.UpsertCopyJob(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	duplicate := first
	duplicate.ID = ""
	duplicate.Name = "second owner"
	duplicate.CreatedAt = time.Time{}
	duplicate.UpdatedAt = time.Time{}
	if err := store.UpsertCopyJob(context.Background(), &duplicate); err == nil || !strings.Contains(err.Error(), "exactly one transfer owner") {
		t.Fatalf("duplicate enabled topology error = %v", err)
	}

	// Non-overlapping artifact streams may intentionally share one transport.
	disjoint := duplicate
	disjoint.ID = ""
	disjoint.Name = "different producer"
	disjoint.ArtifactFilter.ProducerID = "producer_two"
	if err := store.UpsertCopyJob(context.Background(), &disjoint); err != nil {
		t.Fatalf("disjoint filtered topology was rejected: %v", err)
	}
}

func TestCopyStoreRejectsBackupReferenceAndPhysicalLocalAliases(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	source := t.TempDir()
	destination := t.TempDir()
	backupJob := Job{
		Name: "topology producer", ConnectionID: "conn", Destination: source,
		Compression: CompressionNone, Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 2},
	}
	if err := store.UpsertJob(ctx, &backupJob); err != nil {
		t.Fatal(err)
	}
	referenced := CopyJob{
		Name: "reference owner", Enabled: true, Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal}, SourceBackupJobID: backupJob.ID,
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		Trigger:     CopyTriggerManual,
	}
	if err := store.UpsertCopyJob(ctx, &referenced); err != nil {
		t.Fatal(err)
	}

	explicit := CopyJob{
		Name: "explicit owner", Enabled: true, Mode: CopyModePush,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: source},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		Trigger:     CopyTriggerManual,
	}
	if err := store.UpsertCopyJob(ctx, &explicit); err == nil || !strings.Contains(err.Error(), "exactly one transfer owner") {
		t.Fatalf("backup-reference versus explicit-path topology error = %v", err)
	}

	t.Run("filesystem alias", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "source-alias")
		if err := os.Symlink(source, alias); err != nil {
			t.Skipf("filesystem symlinks are unavailable: %v", err)
		}
		aliasJob := explicit
		aliasJob.ID = ""
		aliasJob.Name = "alias owner"
		aliasJob.Source.Location = alias
		if err := store.UpsertCopyJob(ctx, &aliasJob); err == nil || !strings.Contains(err.Error(), "exactly one transfer owner") {
			t.Fatalf("physical-alias topology error = %v", err)
		}
	})

	t.Run("managed volume UUID and subpath", func(t *testing.T) {
		mountOne := filepath.Join(t.TempDir(), "offline-vault-one")
		mountTwo := filepath.Join(t.TempDir(), "offline-vault-two")
		managed := CopyJob{
			Name: "managed disk owner", Enabled: true, Mode: CopyModePush,
			Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
			Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: filepath.Join(mountOne, "registration", "backups")},
			DestinationVolume: &CopyDestinationVolume{
				Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: mountOne,
				SentinelValue: "vault-one-identity", FilesystemUUID: "ABCD-1234", ExpectedFilesystem: "ext4",
			},
			Trigger: CopyTriggerManual,
		}
		if err := store.UpsertCopyJob(ctx, &managed); err != nil {
			t.Fatal(err)
		}
		alias := managed
		alias.ID = ""
		alias.Name = "same managed disk route"
		alias.Destination.Location = filepath.Join(mountTwo, "registration", "backups")
		alias.DestinationVolume = &CopyDestinationVolume{
			Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: mountTwo,
			SentinelValue: "vault-two-identity", FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4",
		}
		if err := store.UpsertCopyJob(ctx, &alias); err == nil || !strings.Contains(err.Error(), "exactly one transfer owner") {
			t.Fatalf("same managed-volume route error = %v", err)
		}
	})
}
