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

func TestWithPreparedCopyDestinationVolumeHoldsAndReleasesPhysicalLease(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "mounted")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-identity-1")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeOSManaged, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-identity-1",
	})
	actionRan := false
	result, warnings, err := withPreparedCopyDestinationVolume(context.Background(), store, job, "test", copyVolumeOperations{}, func(ctx context.Context) (string, error) {
		actionRan = true
		now := time.Now().UTC()
		_, claimErr := store.ClaimCopyVolumeLease(ctx, job.DestinationVolume.leaseKey(), "competing-owner", "competing-job", "competing-run", now, now.Add(time.Hour))
		if !errors.Is(claimErr, ErrCopyVolumeBusy) {
			return "", errors.New("destination-volume lease was not held during operation")
		}
		return "finished", nil
	})
	if err != nil || result != "finished" || len(warnings) != 0 || !actionRan {
		t.Fatalf("guarded operation = result %q warnings %v ran %t err %v", result, warnings, actionRan, err)
	}
	var leaseCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM copy_volume_leases`).Scan(&leaseCount); err != nil || leaseCount != 0 {
		t.Fatalf("guarded operation left %d volume leases: %v", leaseCount, err)
	}
}

func TestManualVolumeOperationsFailBeforeTouchingWrongDestination(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "mounted")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "actual-volume")
	marker := filepath.Join(destination, "keep-me")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeAlreadyMounted, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "expected-volume",
	})
	if _, warnings, err := PreviewCopyRetentionWithVolume(context.Background(), store, job, time.Now()); err == nil || !strings.Contains(err.Error(), "does not match") || len(warnings) != 0 {
		t.Fatalf("wrong-volume preview = warnings %v err %v", warnings, err)
	}
	if _, warnings, err := ApplyCopyRetentionWithVolume(context.Background(), store, job, time.Now()); err == nil || !strings.Contains(err.Error(), "does not match") || len(warnings) != 0 {
		t.Fatalf("wrong-volume apply = warnings %v err %v", warnings, err)
	}
	if _, warnings, err := StageCopyArtifactForInspectionWithVolume(context.Background(), store, job, CopyArtifactResult{}, CopyInspectionStageOptions{Location: CopyInspectionDestination}); err == nil || !strings.Contains(err.Error(), "does not match") || len(warnings) != 0 {
		t.Fatalf("wrong-volume inspection stage = warnings %v err %v", warnings, err)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "unchanged" {
		t.Fatalf("wrong-volume operation touched destination marker: contents %q err %v", contents, err)
	}
}

func TestApplyCopyRetentionPlanHoldsAndReleasesCopyJobLease(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	job := localCopyRunnerJob(t, t.TempDir(), t.TempDir())
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	removed, warnings, err := ApplyCopyRetentionPlanWithVolume(context.Background(), store, job, time.Now(), nil)
	if err != nil || len(removed) != 0 || len(warnings) != 0 {
		t.Fatalf("empty exact-plan apply = removed %v warnings %v err %v", removed, warnings, err)
	}
	var owner, until any
	if err := store.db.QueryRow(`SELECT lease_owner, lease_until FROM copy_jobs WHERE id = ?`, job.ID).Scan(&owner, &until); err != nil {
		t.Fatal(err)
	}
	if owner != nil || until != nil {
		t.Fatalf("exact-plan apply left copy-job lease owner=%v until=%v", owner, until)
	}
}
