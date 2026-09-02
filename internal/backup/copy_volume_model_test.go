package backup

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyDestinationVolumeManagedDefaultsAndValidation(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "backup-volume")
	destination := filepath.Join(mountPoint, "registration")
	job := CopyJob{
		Name: "managed vault", Mode: CopyModePull,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		DestinationVolume: &CopyDestinationVolume{
			Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: mountPoint,
			SentinelValue: "vault-ct400-2026", FilesystemUUID: "A1B2-C3D4",
			ExpectedFilesystem: "EXT4", MountOptions: []string{"errors=remount-ro"},
		},
		Trigger: CopyTriggerManual,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	volume := job.DestinationVolume
	if volume == nil || volume.SentinelFile != defaultCopyVolumeSentinelFile || volume.FilesystemUUID != "a1b2-c3d4" || volume.ExpectedFilesystem != "ext4" {
		t.Fatalf("normalized volume = %#v", volume)
	}
	for _, required := range []string{"rw", "nodev", "nosuid", "noexec"} {
		found := false
		for _, option := range volume.MountOptions {
			found = found || option == required
		}
		if !found {
			t.Fatalf("managed mount options %v omit %q", volume.MountOptions, required)
		}
	}
	if err := job.Validate(); err != nil {
		t.Fatalf("normalized volume did not validate: %v", err)
	}
}

func TestCopyDestinationVolumeRejectsUnsafePolicies(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "vault")
	destination := filepath.Join(mountPoint, "copies")
	valid := CopyJob{
		Name: "vault", Mode: CopyModePull,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: t.TempDir()},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		DestinationVolume: &CopyDestinationVolume{
			Mode: CopyVolumeAlreadyMounted, MountPoint: mountPoint,
			SentinelFile: ".vault-id", SentinelValue: "vault-identity-1",
		},
		Trigger: CopyTriggerManual,
	}
	tests := []struct {
		name    string
		mutate  func(*CopyJob)
		message string
	}{
		{"remote destination", func(job *CopyJob) {
			job.Mode = CopyModePush
			job.Destination = CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://vault/backups"}
		}, "only valid for a local destination"},
		{"destination outside mount", func(job *CopyJob) {
			job.Destination.Location = filepath.Join(t.TempDir(), "outside")
		}, "must be at or below"},
		{"sentinel traversal", func(job *CopyJob) { job.DestinationVolume.SentinelFile = "../identity" }, "one file name"},
		{"short sentinel identity", func(job *CopyJob) { job.DestinationVolume.SentinelValue = "short" }, "8-256"},
		{"verify-only lifecycle setting", func(job *CopyJob) { job.DestinationVolume.Spindown = true }, "cannot contain Linux mount"},
		{"managed missing uuid", func(job *CopyJob) {
			job.DestinationVolume.Mode = CopyVolumeManagedLinuxBlockDevice
			job.DestinationVolume.ExpectedFilesystem = "ext4"
		}, "filesystem UUID"},
		{"managed unsafe option", func(job *CopyJob) {
			job.DestinationVolume.Mode = CopyVolumeManagedLinuxBlockDevice
			job.DestinationVolume.FilesystemUUID = "uuid-1234"
			job.DestinationVolume.ExpectedFilesystem = "ext4"
			job.DestinationVolume.MountOptions = []string{"exec"}
		}, "weakens"},
		{"managed secret option", func(job *CopyJob) {
			job.DestinationVolume.Mode = CopyVolumeManagedLinuxBlockDevice
			job.DestinationVolume.FilesystemUUID = "uuid-1234"
			job.DestinationVolume.ExpectedFilesystem = "ext4"
			job.DestinationVolume.MountOptions = []string{"password=do-not-store"}
		}, "must not carry credentials"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := valid
			volume := *valid.DestinationVolume
			job.DestinationVolume = &volume
			test.mutate(&job)
			err := job.ApplyDefaults(time.Now())
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("ApplyDefaults() error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestCopyVolumeLeaseKeyUsesPhysicalOrCanonicalMountIdentity(t *testing.T) {
	firstMount := filepath.Join(t.TempDir(), "one")
	secondMount := filepath.Join(t.TempDir(), "two")
	managedFirst := CopyDestinationVolume{
		Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: firstMount,
		FilesystemUUID: "ABCD-1234", SentinelValue: "vault-identity-1",
	}
	managedSameDisk := CopyDestinationVolume{
		Mode: CopyVolumeManagedLinuxBlockDevice, MountPoint: secondMount,
		FilesystemUUID: "abcd-1234", SentinelValue: "different-expected-token",
	}
	if managedFirst.leaseKey() != managedSameDisk.leaseKey() {
		t.Fatal("same filesystem UUID did not produce one shared lease key")
	}
	verifyFirst := CopyDestinationVolume{Mode: CopyVolumeOSManaged, MountPoint: firstMount, SentinelValue: "vault-identity-1"}
	verifySameMount := CopyDestinationVolume{Mode: CopyVolumeAlreadyMounted, MountPoint: firstMount, SentinelValue: "different-token"}
	if verifyFirst.leaseKey() != verifySameMount.leaseKey() {
		t.Fatal("same canonical verify-only mount did not produce one shared lease key")
	}
	verifyOtherMount := CopyDestinationVolume{Mode: CopyVolumeOSManaged, MountPoint: secondMount, SentinelValue: verifyFirst.SentinelValue}
	if verifyFirst.leaseKey() == verifyOtherMount.leaseKey() {
		t.Fatal("different verify-only mount points produced the same lease key")
	}
}
