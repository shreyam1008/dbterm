package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyAgentVolumeReleaseWarningDoesNotInvalidateVerifiedCopy(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	source := t.TempDir()
	mountPoint := filepath.Join(t.TempDir(), "vault")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-agent-identity")
	writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_volume_agent", "producer", "backup-job", time.Now(), false)
	job := localCopyRunnerJob(t, source, destination)
	job.DestinationVolume = &CopyDestinationVolume{
		Mode: CopyVolumeAlreadyMounted, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-agent-identity",
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	owner := "copy-agent-owner"
	claimed, err := store.ClaimCopyJob(context.Background(), job.ID, owner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	deletedLease := false
	progress := func(event ProgressEvent) {
		if event.Phase != "volume" || event.Message != "releasing the configured destination volume" || deletedLease {
			return
		}
		deletedLease = true
		if _, deleteErr := store.db.Exec(`DELETE FROM copy_volume_leases`); deleteErr != nil {
			t.Errorf("delete volume lease: %v", deleteErr)
		}
	}
	run, err := executeClaimedCopyJobWithVolumeOperations(context.Background(), store, claimed, owner, CopyTriggerManual, progress, defaultCopyVolumeOperations())
	if err != nil {
		t.Fatalf("verified copy became an execution error after release warning: %v", err)
	}
	if run.Status != RunSucceeded || len(run.Artifacts) != 1 || run.Artifacts[0].PublicationState != ArtifactPublicationComplete {
		t.Fatalf("copy run = %#v", run)
	}
	if !deletedLease || !strings.Contains(strings.Join(run.Warnings, " "), "lease was lost") {
		t.Fatalf("release warning was not surfaced: %#v", run.Warnings)
	}
	stored, err := store.GetCopyRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != RunSucceeded || !strings.Contains(strings.Join(stored.Warnings, " "), "lease was lost") {
		t.Fatalf("durable copy history lost release warning: %#v", stored)
	}
	if _, err := ReadArtifactManifestForArtifactRequired(filepath.Join(destination, "copy.sqlite")); err != nil {
		t.Fatalf("verified copy was not preserved: %v", err)
	}
}

func TestCopyAgentWrongVolumeFailsBeforePublicationAndReleasesLeases(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	source := t.TempDir()
	mountPoint := filepath.Join(t.TempDir(), "vault")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "unexpected-volume")
	writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_wrong_volume", "producer", "backup-job", time.Now(), false)
	job := localCopyRunnerJob(t, source, destination)
	job.DestinationVolume = &CopyDestinationVolume{
		Mode: CopyVolumeOSManaged, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "expected-volume-id",
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	owner := "copy-agent-owner"
	claimed, err := store.ClaimCopyJob(context.Background(), job.ID, owner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run, err := executeClaimedCopyJobWithVolumeOperations(context.Background(), store, claimed, owner, CopyTriggerManual, nil, defaultCopyVolumeOperations())
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong-volume run error = %v", err)
	}
	if run.Status != RunFailed || len(run.Artifacts) != 0 {
		t.Fatalf("wrong-volume run = %#v", run)
	}
	if _, statErr := os.Lstat(filepath.Join(destination, "copy.sqlite")); !os.IsNotExist(statErr) {
		t.Fatalf("wrong volume received a published artifact: %v", statErr)
	}
	var volumeLeases int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM copy_volume_leases`).Scan(&volumeLeases); err != nil || volumeLeases != 0 {
		t.Fatalf("wrong-volume failure left %d volume leases: %v", volumeLeases, err)
	}
	if _, err := store.ClaimCopyJob(context.Background(), job.ID, "retry-owner", time.Now()); err != nil {
		t.Fatalf("wrong-volume failure left copy-job lease stuck: %v", err)
	}
}

func TestCopyAgentVolumeSwapAfterArtifactPartialPublishesNothing(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	source := t.TempDir()
	mountPoint := filepath.Join(t.TempDir(), "vault")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "expected-volume")
	sentinelPath := filepath.Join(mountPoint, ".vault-id")
	writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_midrun_swap", "producer", "backup-job", time.Now().UTC(), false)
	job := localCopyRunnerJob(t, source, destination)
	job.MaxAttempts = 1
	job.DestinationVolume = &CopyDestinationVolume{
		Mode: CopyVolumeOSManaged, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "expected-volume",
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	owner := "copy-agent-midrun-volume-swap"
	claimed, err := store.ClaimCopyJob(context.Background(), job.ID, owner, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	swapped := false
	operations := copyVolumeOperations{lstat: func(name string) (os.FileInfo, error) {
		if !swapped && strings.HasPrefix(filepath.Base(name), ".dbterm-publish-") {
			if err := os.WriteFile(sentinelPath, []byte("replacement-volume\n"), 0o600); err != nil {
				t.Fatalf("inject destination-volume swap: %v", err)
			}
			swapped = true
		}
		return os.Lstat(name)
	}}
	run, err := executeClaimedCopyJobWithVolumeOperations(context.Background(), store, claimed, owner, CopyTriggerManual, nil, operations)
	if err == nil || (!strings.Contains(err.Error(), "sentinel changed") && !strings.Contains(err.Error(), "does not match")) {
		t.Fatalf("mid-run destination-volume swap error = %v", err)
	}
	if !swapped {
		t.Fatal("test did not swap the volume identity after artifact staging")
	}
	if run.Status != RunFailed || len(run.Artifacts) != 0 {
		t.Fatalf("mid-run volume swap run = %#v, want failed with no published artifacts", run)
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("mid-run volume swap left destination files: %v", entryNames(entries))
	}
	for _, finalPath := range []string{filepath.Join(destination, "copy.sqlite"), filepath.Join(destination, "copy.sqlite") + ArtifactManifestSuffix} {
		if _, statErr := os.Lstat(finalPath); !os.IsNotExist(statErr) {
			t.Fatalf("mid-run volume swap published %s: %v", finalPath, statErr)
		}
	}
}

func TestCopyAgentManagedUnmountFailureIsDurableWarningNotCopyFailure(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	source := t.TempDir()
	mountPoint := filepath.Join(t.TempDir(), "vault")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-managed-agent")
	writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_managed_agent", "producer", "backup-job", time.Now(), false)
	device := filepath.Join(t.TempDir(), "dev", "backup-partition")
	job := localCopyRunnerJob(t, source, destination)
	job.DestinationVolume = &CopyDestinationVolume{
		Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-managed-agent",
		FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4", Spindown: true,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	owner := "copy-agent-owner"
	claimed, err := store.ClaimCopyJob(context.Background(), job.ID, owner, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeLinuxCopyVolume{
		device: device, mountPoint: mountPoint, filesystem: "ext4", uuid: "abcd-1234", failUnmount: true,
	}
	run, err := executeClaimedCopyJobWithVolumeOperations(context.Background(), store, claimed, owner, CopyTriggerManual, nil, fake.operations(time.Now().UTC()))
	if err != nil {
		t.Fatalf("unmount warning invalidated a verified copy: %v", err)
	}
	if run.Status != RunSucceeded || len(run.Artifacts) != 1 || !strings.Contains(strings.Join(run.Warnings, " "), "unmount failed") {
		t.Fatalf("copy run did not preserve success plus warning: %#v", run)
	}
	if !fake.mounted {
		t.Fatal("failed unmount unexpectedly changed simulated mount state")
	}
	if strings.Contains(strings.Join(fake.calls, "\n"), "udisksctl") {
		t.Fatalf("spindown was attempted after failed unmount: %v", fake.calls)
	}
	stored, err := store.GetCopyRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != RunSucceeded || !strings.Contains(strings.Join(stored.Warnings, " "), "unmount failed") {
		t.Fatalf("durable history did not retain managed release warning: %#v", stored)
	}
}

func TestCopyRetentionDeadlinePrecedesJobAndVolumeLeases(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	source := t.TempDir()
	mountPoint := filepath.Join(t.TempDir(), "vault")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-retention-deadline")
	writeCopyRunnerFixture(t, source, "copy.sqlite", "artifact_retention_deadline", "producer", "backup-job", time.Now(), false)
	job := localCopyRunnerJob(t, source, destination)
	job.DestinationVolume = &CopyDestinationVolume{
		Mode: CopyVolumeAlreadyMounted, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-retention-deadline",
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	const owner = "copy-retention-deadline-owner"
	claimed, err := store.ClaimCopyJob(context.Background(), job.ID, owner, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	leaseEvidence := make(chan error, 1)
	retentionStopped := make(chan struct{})
	applyRetention := func(ctx context.Context, _ *Store, _ CopyJob, _ time.Time) ([]string, error) {
		defer close(retentionStopped)
		deadline, ok := ctx.Deadline()
		if !ok {
			leaseEvidence <- fmt.Errorf("copy retention context has no deadline")
			return nil, fmt.Errorf("copy retention context has no deadline")
		}
		var jobLeaseRaw string
		if err := store.db.QueryRow(`SELECT lease_until FROM copy_jobs WHERE id = ?`, job.ID).Scan(&jobLeaseRaw); err != nil {
			leaseEvidence <- err
			return nil, err
		}
		jobLeaseUntil, err := time.Parse(time.RFC3339Nano, jobLeaseRaw)
		if err != nil {
			leaseEvidence <- err
			return nil, err
		}
		var volumeLeaseRaw string
		if err := store.db.QueryRow(`SELECT lease_until FROM copy_volume_leases WHERE volume_key = ?`, job.DestinationVolume.leaseKey()).Scan(&volumeLeaseRaw); err != nil {
			leaseEvidence <- err
			return nil, err
		}
		volumeLeaseUntil, err := time.Parse(time.RFC3339Nano, volumeLeaseRaw)
		if err != nil {
			leaseEvidence <- err
			return nil, err
		}
		if !jobLeaseUntil.After(deadline) || !volumeLeaseUntil.After(deadline) {
			err := fmt.Errorf("retention deadline %s must precede job %s and volume %s lease expiry", deadline, jobLeaseUntil, volumeLeaseUntil)
			leaseEvidence <- err
			return nil, err
		}
		leaseEvidence <- nil
		<-ctx.Done()
		return nil, ctx.Err()
	}
	type copyExecutionResult struct {
		run CopyRun
		err error
	}
	result := make(chan copyExecutionResult, 1)
	go func() {
		run, runErr := executeClaimedCopyJobWithPostProcessing(context.Background(), store, claimed, owner, CopyTriggerManual, nil,
			defaultCopyVolumeOperations(), applyRetention, 500*time.Millisecond)
		result <- copyExecutionResult{run: run, err: runErr}
	}()
	select {
	case evidenceErr := <-leaseEvidence:
		if evidenceErr != nil {
			t.Fatal(evidenceErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not reach bounded retention")
	}
	if _, err := store.ClaimCopyJob(context.Background(), job.ID, "competitor", time.Now()); !errors.Is(err, ErrCopyJobBusy) {
		t.Fatalf("competing copy claim during retention = %v, want ErrCopyJobBusy", err)
	}
	changed := job
	changed.Retention.KeepLast++
	if err := store.UpsertCopyJob(context.Background(), &changed); !errors.Is(err, ErrCopyJobBusy) {
		t.Fatalf("copy policy edit during retention = %v, want ErrCopyJobBusy", err)
	}
	if _, err := store.ClaimCopyVolumeLease(context.Background(), job.DestinationVolume.leaseKey(), "competitor", "other-job", "other-run", time.Now(), time.Now().Add(time.Minute)); !errors.Is(err, ErrCopyVolumeBusy) {
		t.Fatalf("competing volume claim during retention = %v, want ErrCopyVolumeBusy", err)
	}
	select {
	case <-retentionStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("copy retention continued beyond its deadline")
	}
	var completed copyExecutionResult
	select {
	case completed = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not finish after retention deadline")
	}
	if completed.err != nil {
		t.Fatalf("retention deadline invalidated verified copy: %v", completed.err)
	}
	if completed.run.Status != RunSucceeded || !strings.Contains(completed.run.RetentionError, context.DeadlineExceeded.Error()) {
		t.Fatalf("copy retention deadline was not a visible independent warning: %#v", completed.run)
	}
	stored, err := store.GetCopyRun(context.Background(), completed.run.ID)
	if err != nil || stored.Status != RunSucceeded || !strings.Contains(stored.RetentionError, context.DeadlineExceeded.Error()) {
		t.Fatalf("durable copy retention deadline outcome = %#v, %v", stored, err)
	}
	if _, err := ReadArtifactManifestForArtifactRequired(filepath.Join(destination, "copy.sqlite")); err != nil {
		t.Fatalf("retention deadline did not preserve the verified copy: %v", err)
	}
	if _, err := store.ClaimCopyJob(context.Background(), job.ID, "competitor", time.Now()); err != nil {
		t.Fatalf("copy-job lease remained held after retention stopped: %v", err)
	}
	var volumeLeases int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM copy_volume_leases`).Scan(&volumeLeases); err != nil || volumeLeases != 0 {
		t.Fatalf("retention deadline left %d destination-volume leases: %v", volumeLeases, err)
	}
}
