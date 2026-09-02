package backup

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCopyVolumeLeaseIsExclusiveRenewableAndRecoverable(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	first, err := store.ClaimCopyVolumeLease(ctx, "volume-one", "run-one", "job-one", "run-one", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimCopyVolumeLease(ctx, "volume-one", "run-two", "job-two", "run-two", now.Add(time.Minute), now.Add(2*time.Hour)); !errors.Is(err, ErrCopyVolumeBusy) {
		t.Fatalf("competing volume claim error = %v, want ErrCopyVolumeBusy", err)
	}
	if _, err := store.ClaimCopyVolumeLease(ctx, "volume-one", "run-one", "job-other", "run-other", now.Add(time.Minute), now.Add(2*time.Hour)); !errors.Is(err, ErrCopyVolumeBusy) {
		t.Fatalf("reused-owner competing claim error = %v, want ErrCopyVolumeBusy", err)
	}
	if err := store.RenewCopyVolumeLease(ctx, &first, now.Add(2*time.Minute), now.Add(3*time.Hour)); err != nil {
		t.Fatalf("renew owned lease: %v", err)
	}
	if first.Until != now.Add(3*time.Hour) {
		t.Fatalf("renewed expiry = %s", first.Until)
	}
	wrong := first
	wrong.Owner = "other"
	if err := store.ReleaseCopyVolumeLease(ctx, wrong); !errors.Is(err, ErrCopyVolumeLeaseLost) {
		t.Fatalf("wrong-owner release error = %v, want ErrCopyVolumeLeaseLost", err)
	}
	if err := store.ReleaseCopyVolumeLease(ctx, first); err != nil {
		t.Fatal(err)
	}

	expired, err := store.ClaimCopyVolumeLease(ctx, "volume-one", "run-old", "job-old", "run-old", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	takeover, err := store.ClaimCopyVolumeLease(ctx, "volume-one", "run-new", "job-new", "run-new", expired.Until.Add(time.Second), expired.Until.Add(time.Hour))
	if err != nil {
		t.Fatalf("take over expired lease: %v", err)
	}
	if err := store.ReleaseCopyVolumeLease(ctx, expired); !errors.Is(err, ErrCopyVolumeLeaseLost) {
		t.Fatalf("stale owner release error = %v, want ErrCopyVolumeLeaseLost", err)
	}
	if err := store.ReleaseCopyVolumeLease(ctx, takeover); err != nil {
		t.Fatal(err)
	}
}
