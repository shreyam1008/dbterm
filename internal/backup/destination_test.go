package backup

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeBackupDestinationSupportsLocalAndRclone(t *testing.T) {
	local := filepath.Join(t.TempDir(), "nested", "..", "backups")
	got, err := NormalizeBackupDestination(local)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Clean(local))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("local destination = %q, want %q", got, want)
	}

	got, err = NormalizeBackupDestination("rclone://offsite/team//nightly/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "rclone://offsite/team/nightly" {
		t.Fatalf("remote destination = %q", got)
	}
	joined, err := JoinBackupDestination(got, "orders.dump.zst")
	if err != nil {
		t.Fatal(err)
	}
	if joined != "rclone://offsite/team/nightly/orders.dump.zst" {
		t.Fatalf("joined destination = %q", joined)
	}
}

func TestNormalizeBackupDestinationRejectsUnsafeRcloneValues(t *testing.T) {
	for _, value := range []string{
		"rclone://",
		"rclone://user@remote/backups",
		"rclone://remote/path/../outside",
		`rclone://remote/path\backup`,
		"rclone://remote/path?secret=value",
	} {
		t.Run(strings.ReplaceAll(value, "/", "_"), func(t *testing.T) {
			if _, err := NormalizeBackupDestination(value); err == nil {
				t.Fatalf("NormalizeBackupDestination(%q) succeeded", value)
			}
		})
	}
}

func TestJobValidateRejectsRcloneBackupPublication(t *testing.T) {
	job := Job{
		Name: "offsite", ConnectionID: "connection", Destination: "rclone://archive/dbterm",
		FilenameTemplate: DefaultFilenameTemplate,
		Compression:      CompressionZstd, CompressionLevel: 3,
		Encryption: EncryptionNone, Schedule: Schedule{Kind: ScheduleManual}, TimeoutMinutes: 5,
	}
	if err := job.Validate(); err == nil || !strings.Contains(err.Error(), "rclone backup publication is disabled") {
		t.Fatalf("Validate() error = %v, want explicit fail-closed error", err)
	}
}

func TestParseRemoteArtifactWithinRefusesOtherRemoteAndSiblingPrefix(t *testing.T) {
	root, err := parseDestination("rclone://archive/team")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseRemoteArtifactWithin(root, "rclone://archive/team/nightly.dump"); err != nil {
		t.Fatalf("contained artifact rejected: %v", err)
	}
	for _, value := range []string{
		"rclone://other/team/nightly.dump",
		"rclone://archive/team-old/nightly.dump",
		"rclone://archive/nightly.dump",
	} {
		if _, err := parseRemoteArtifactWithin(root, value); err == nil {
			t.Fatalf("outside artifact %q accepted", value)
		}
	}
}
