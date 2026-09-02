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

type fakeCopyBlockInfo struct {
	name string
}

func (info fakeCopyBlockInfo) Name() string       { return info.name }
func (info fakeCopyBlockInfo) Size() int64        { return 0 }
func (info fakeCopyBlockInfo) Mode() os.FileMode  { return os.ModeDevice }
func (info fakeCopyBlockInfo) ModTime() time.Time { return time.Time{} }
func (info fakeCopyBlockInfo) IsDir() bool        { return false }
func (info fakeCopyBlockInfo) Sys() any           { return nil }

type fakeLinuxCopyVolume struct {
	device              string
	mountPoint          string
	filesystem          string
	uuid                string
	label               string
	mounted             bool
	failMountAfterMount bool
	failSync            bool
	failUnmount         bool
	failSpindown        bool
	calls               []string
	waits               []time.Duration
}

func (fake *fakeLinuxCopyVolume) operations(now time.Time) copyVolumeOperations {
	return copyVolumeOperations{
		platform: "linux",
		now:      func() time.Time { return now },
		lstat: func(name string) (os.FileInfo, error) {
			if filepath.Clean(name) == filepath.Clean(fake.device) {
				return fakeCopyBlockInfo{name: filepath.Base(name)}, nil
			}
			return os.Lstat(name)
		},
		open:         os.Open,
		evalSymlinks: filepath.EvalSymlinks,
		run:          fake.run,
		wait: func(ctx context.Context, duration time.Duration) error {
			fake.waits = append(fake.waits, duration)
			return ctx.Err()
		},
	}
}

func (fake *fakeLinuxCopyVolume) run(_ context.Context, name string, arguments ...string) (copyVolumeCommandResult, error) {
	fake.calls = append(fake.calls, name+" "+strings.Join(arguments, " "))
	switch name {
	case "blkid":
		if len(arguments) == 2 && arguments[0] == "-U" {
			return copyVolumeCommandResult{Stdout: fake.device + "\n"}, nil
		}
		if len(arguments) == 5 && arguments[0] == "-o" && arguments[1] == "value" && arguments[2] == "-s" {
			values := map[string]string{"UUID": fake.uuid, "TYPE": fake.filesystem, "LABEL": fake.label}
			return copyVolumeCommandResult{Stdout: values[arguments[3]] + "\n"}, nil
		}
	case "findmnt":
		if !fake.mounted {
			return copyVolumeCommandResult{ExitCode: 1}, errors.New("exit status 1")
		}
		payload := fmt.Sprintf(`{"filesystems":[{"target":%q,"fstype":%q}]}`, fake.mountPoint, fake.filesystem)
		return copyVolumeCommandResult{Stdout: payload}, nil
	case "mount":
		fake.mounted = true
		if fake.failMountAfterMount {
			return copyVolumeCommandResult{Stderr: "helper disconnected", ExitCode: 1}, errors.New("exit status 1")
		}
		return copyVolumeCommandResult{}, nil
	case "sync":
		if fake.failSync {
			return copyVolumeCommandResult{Stderr: "simulated sync failure", ExitCode: 1}, errors.New("exit status 1")
		}
		return copyVolumeCommandResult{}, nil
	case "umount":
		if fake.failUnmount {
			return copyVolumeCommandResult{Stderr: "simulated unmount failure", ExitCode: 1}, errors.New("exit status 1")
		}
		fake.mounted = false
		return copyVolumeCommandResult{}, nil
	case "udisksctl":
		if fake.failSpindown {
			return copyVolumeCommandResult{Stderr: "simulated spindown failure", ExitCode: 1}, errors.New("exit status 1")
		}
		return copyVolumeCommandResult{}, nil
	}
	return copyVolumeCommandResult{Stderr: "unexpected command", ExitCode: -1}, fmt.Errorf("unexpected command %s %v", name, arguments)
}

func newCopyVolumeTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func writeCopyVolumeSentinel(t *testing.T, mountPoint, file, value string) string {
	t.Helper()
	destination := filepath.Join(mountPoint, "copies")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mountPoint, file), []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return destination
}

func normalizedCopyVolumeJob(t *testing.T, destination string, volume CopyDestinationVolume) CopyJob {
	t.Helper()
	job := CopyJob{
		ID: "copy-volume", Name: "volume copy", Mode: CopyModePull,
		Source:            CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Destination:       CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		DestinationVolume: &volume,
		Trigger:           CopyTriggerManual,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestCopyVolumeVerifyOnlyRequiresExactSentinelAndNeverRunsCommands(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "mounted")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-identity-1")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeOSManaged, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-identity-1",
	})
	now := time.Now().UTC()
	commandCalled := false
	operations := copyVolumeOperations{
		platform: "windows", now: func() time.Time { return now },
		run: func(context.Context, string, ...string) (copyVolumeCommandResult, error) {
			commandCalled = true
			return copyVolumeCommandResult{}, errors.New("unexpected command")
		},
	}
	session, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-one", operations)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.verifyAfterTransfer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if warnings := session.release(context.Background(), true); len(warnings) != 0 {
		t.Fatalf("release warnings = %v", warnings)
	}
	if commandCalled {
		t.Fatal("verify-only volume policy invoked an operating-system command")
	}

	if err := os.WriteFile(filepath.Join(mountPoint, ".vault-id"), []byte("wrong-volume\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-two", operations); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong-sentinel preparation error = %v", err)
	}
	var leaseCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM copy_volume_leases`).Scan(&leaseCount); err != nil || leaseCount != 0 {
		t.Fatalf("failed preparation left %d leases: %v", leaseCount, err)
	}
}

func TestVerifyCopyDestinationVolumeIdentityIsReadOnlyAndFailClosed(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "mounted")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-identity-1")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeAlreadyMounted, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-identity-1",
	})
	if err := VerifyCopyDestinationVolumeIdentity(job); err != nil {
		t.Fatalf("verify matching identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mountPoint, ".vault-id"), []byte("wrong-volume\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCopyDestinationVolumeIdentity(job); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("verify wrong identity error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(destination, ".partial")); !os.IsNotExist(err) {
		t.Fatalf("read-only identity check changed the destination: %v", err)
	}
}

func TestCopyVolumeRejectsNestedDestinationFilesystem(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "mounted")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-identity-1")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeAlreadyMounted, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-identity-1",
	})
	operations := copyVolumeOperations{
		filesystemIdentity: func(path string, _ os.FileInfo) (string, error) {
			if filepath.Clean(path) == filepath.Clean(destination) {
				return "nested-filesystem", nil
			}
			return "vault-filesystem", nil
		},
	}
	_, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-nested-device", operations)
	if err == nil || !strings.Contains(err.Error(), "same filesystem or volume") {
		t.Fatalf("nested-filesystem preparation error = %v", err)
	}
	var leaseCount int
	if queryErr := store.db.QueryRow(`SELECT COUNT(*) FROM copy_volume_leases`).Scan(&leaseCount); queryErr != nil || leaseCount != 0 {
		t.Fatalf("nested-filesystem refusal left %d leases: %v", leaseCount, queryErr)
	}
}

func TestCopyVolumeRejectsSentinelOnDifferentFilesystem(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "mounted")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-identity-1")
	sentinelPath := filepath.Join(mountPoint, ".vault-id")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeOSManaged, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-identity-1",
	})
	operations := copyVolumeOperations{
		filesystemIdentity: func(path string, _ os.FileInfo) (string, error) {
			if filepath.Clean(path) == filepath.Clean(sentinelPath) {
				return "sentinel-filesystem", nil
			}
			return "vault-filesystem", nil
		},
	}
	_, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-sentinel-device", operations)
	if err == nil || !strings.Contains(err.Error(), "same filesystem or volume") {
		t.Fatalf("cross-filesystem sentinel preparation error = %v", err)
	}
}

func TestCopyVolumeRejectsWindowsReparsePointInAnyPathComponent(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	ancestor := filepath.Join(t.TempDir(), "junction")
	mountPoint := filepath.Join(ancestor, "mounted")
	destination := filepath.Join(mountPoint, "nested", "copies")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mountPoint, ".vault-id"), []byte("vault-identity-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeAlreadyMounted, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-identity-1",
	})
	var inspected []string
	operations := copyVolumeOperations{
		platform: "windows",
		filesystemIdentity: func(string, os.FileInfo) (string, error) {
			return "vault-filesystem", nil
		},
		isReparsePoint: func(path string, _ os.FileInfo) bool {
			inspected = append(inspected, filepath.Clean(path))
			return filepath.Clean(path) == filepath.Clean(ancestor)
		},
	}
	_, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-reparse", operations)
	if err == nil || !strings.Contains(err.Error(), "reparse point") || !strings.Contains(err.Error(), ancestor) {
		t.Fatalf("ancestor-reparse preparation error = %v", err)
	}
	found := false
	for _, path := range inspected {
		if path == filepath.Clean(ancestor) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ancestor path was not inspected for Windows reparse metadata: %v", inspected)
	}
}

func TestCopyVolumeRejectsFilesystemSwapDuringVerification(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "mounted")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-identity-1")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeAlreadyMounted, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-identity-1",
	})
	identityCalls := make(map[string]int)
	operations := copyVolumeOperations{
		filesystemIdentity: func(path string, _ os.FileInfo) (string, error) {
			path = filepath.Clean(path)
			identityCalls[path]++
			if path == filepath.Clean(destination) && identityCalls[path] > 1 {
				return "replacement-filesystem", nil
			}
			return "vault-filesystem", nil
		},
	}
	_, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-device-swap", operations)
	if err == nil || !strings.Contains(err.Error(), "changed filesystem or volume") {
		t.Fatalf("filesystem-swap preparation error = %v", err)
	}
}

func TestManagedLinuxCopyVolumeMountsVerifiesSyncsUnmountsAndSpinsDown(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "managed")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-managed-1")
	device := filepath.Join(t.TempDir(), "dev", "backup-partition")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-managed-1",
		FilesystemUUID: "ABCD-1234", ExpectedFilesystem: "ext4", ExpectedVolumeLabel: "BACKUP",
		MountOptions: []string{"errors=remount-ro"}, WarmupSeconds: 2, CooldownSeconds: 3, Spindown: true,
	})
	now := time.Now().UTC()
	fake := &fakeLinuxCopyVolume{
		device: device, mountPoint: mountPoint, filesystem: "ext4", uuid: "abcd-1234", label: "BACKUP",
	}
	session, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-managed", fake.operations(now))
	if err != nil {
		t.Fatal(err)
	}
	if !session.mountedByUs || !fake.mounted {
		t.Fatalf("managed mount state = session %t, fake %t", session.mountedByUs, fake.mounted)
	}
	if err := session.verifyAfterTransfer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if warnings := session.release(context.Background(), true); len(warnings) != 0 {
		t.Fatalf("release warnings = %v", warnings)
	}
	if fake.mounted {
		t.Fatal("volume remained mounted after successful owned release")
	}
	joined := strings.Join(fake.calls, "\n")
	for _, command := range []string{"blkid -U", "mount -t ext4 -o", "sync -f", "umount ", "udisksctl power-off"} {
		if !strings.Contains(joined, command) {
			t.Fatalf("command trace omitted %q:\n%s", command, joined)
		}
	}
	if len(fake.waits) != 2 || fake.waits[0] != 2*time.Second || fake.waits[1] != 3*time.Second {
		t.Fatalf("warmup/cooldown waits = %v", fake.waits)
	}
	var leaseCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM copy_volume_leases`).Scan(&leaseCount); err != nil || leaseCount != 0 {
		t.Fatalf("successful release left %d leases: %v", leaseCount, err)
	}
}

func TestManagedLinuxCopyVolumeDoesNotUnmountPreexistingMount(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "managed")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-managed-2")
	device := filepath.Join(t.TempDir(), "dev", "backup-partition")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-managed-2",
		FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4", Spindown: true,
	})
	fake := &fakeLinuxCopyVolume{
		device: device, mountPoint: mountPoint, filesystem: "ext4", uuid: "abcd-1234", mounted: true,
	}
	session, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-mounted", fake.operations(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if session.mountedByUs {
		t.Fatal("preexisting mount was marked as owned by dbterm")
	}
	if warnings := session.release(context.Background(), true); len(warnings) != 0 {
		t.Fatalf("release warnings = %v", warnings)
	}
	joined := strings.Join(fake.calls, "\n")
	for _, forbidden := range []string{"mount -t", "sync -f", "umount ", "udisksctl"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("preexisting mount invoked %q:\n%s", forbidden, joined)
		}
	}
	if !fake.mounted {
		t.Fatal("preexisting mount was changed")
	}
}

func TestManagedLinuxCopyVolumeSyncFailurePreservesMountAndBecomesWarning(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "managed")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-managed-3")
	device := filepath.Join(t.TempDir(), "dev", "backup-partition")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-managed-3",
		FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4",
	})
	fake := &fakeLinuxCopyVolume{
		device: device, mountPoint: mountPoint, filesystem: "ext4", uuid: "abcd-1234", failSync: true,
	}
	session, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-sync", fake.operations(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	warnings := session.release(context.Background(), true)
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "sync failed") {
		t.Fatalf("sync-failure warnings = %v", warnings)
	}
	if !fake.mounted {
		t.Fatal("sync failure should leave the volume mounted")
	}
	if strings.Contains(strings.Join(fake.calls, "\n"), "umount ") {
		t.Fatalf("sync failure attempted unmount: %v", fake.calls)
	}
}

func TestManagedLinuxCopyVolumeLostLeasePreventsUnmount(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "managed")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-managed-4")
	device := filepath.Join(t.TempDir(), "dev", "backup-partition")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-managed-4",
		FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4",
	})
	fake := &fakeLinuxCopyVolume{device: device, mountPoint: mountPoint, filesystem: "ext4", uuid: "abcd-1234"}
	session, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-lost", fake.operations(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM copy_volume_leases WHERE volume_key = ?`, session.lease.VolumeKey); err != nil {
		t.Fatal(err)
	}
	before := len(fake.calls)
	warnings := session.release(context.Background(), true)
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "lease was lost") {
		t.Fatalf("lease-loss warnings = %v", warnings)
	}
	for _, call := range fake.calls[before:] {
		if strings.HasPrefix(call, "sync ") || strings.HasPrefix(call, "umount ") || strings.HasPrefix(call, "udisksctl ") {
			t.Fatalf("lease loss permitted destructive command %q", call)
		}
	}
	if !fake.mounted {
		t.Fatal("lease loss should leave the volume mounted")
	}
}

func TestManagedLinuxCopyVolumeLeavesAmbiguousMountAfterHelperError(t *testing.T) {
	store := newCopyVolumeTestStore(t)
	mountPoint := filepath.Join(t.TempDir(), "managed")
	destination := writeCopyVolumeSentinel(t, mountPoint, ".vault-id", "vault-managed-5")
	device := filepath.Join(t.TempDir(), "dev", "backup-partition")
	job := normalizedCopyVolumeJob(t, destination, CopyDestinationVolume{
		Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: mountPoint,
		SentinelFile: ".vault-id", SentinelValue: "vault-managed-5",
		FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4",
	})
	fake := &fakeLinuxCopyVolume{
		device: device, mountPoint: mountPoint, filesystem: "ext4", uuid: "abcd-1234", failMountAfterMount: true,
	}
	_, err := prepareCopyDestinationVolume(context.Background(), store, job, "run-boundary", fake.operations(time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "mount configured") {
		t.Fatalf("mount-helper boundary error = %v", err)
	}
	if !fake.mounted {
		t.Fatal("ambiguous mount ownership must be left mounted")
	}
	joined := strings.Join(fake.calls, "\n")
	if strings.Contains(joined, "sync -f") || strings.Contains(joined, "umount ") || strings.Contains(joined, "udisksctl ") {
		t.Fatalf("ambiguous mount ownership triggered a destructive cleanup command: %s", joined)
	}
	var leaseCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM copy_volume_leases`).Scan(&leaseCount); err != nil || leaseCount != 0 {
		t.Fatalf("mount-helper error left %d leases: %v", leaseCount, err)
	}
}
