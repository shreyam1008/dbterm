package backup

import (
	"strings"
	"testing"
)

func TestJobValidateRejectsAlgorithmSpecificCompressionLevel(t *testing.T) {
	job := Job{
		Name: "nightly", ConnectionID: "connection", Destination: t.TempDir(),
		FilenameTemplate: DefaultFilenameTemplate,
		Compression:      CompressionGzip, CompressionLevel: 10,
		Encryption: EncryptionNone, Schedule: Schedule{Kind: ScheduleManual}, TimeoutMinutes: 5,
	}
	err := job.Validate()
	if err == nil || !strings.Contains(err.Error(), "gzip compression level") {
		t.Fatalf("Validate() error = %v, want gzip level guidance", err)
	}
}

func TestJobValidateRejectsInvalidAgeRecipient(t *testing.T) {
	job := Job{
		Name: "nightly", ConnectionID: "connection", Destination: t.TempDir(),
		FilenameTemplate: DefaultFilenameTemplate,
		Compression:      CompressionZstd, CompressionLevel: 3,
		Encryption: EncryptionAge, AgeRecipient: "not-an-age-recipient",
		Schedule: Schedule{Kind: ScheduleManual}, TimeoutMinutes: 5,
	}
	err := job.Validate()
	if err == nil || !strings.Contains(err.Error(), "valid X25519") {
		t.Fatalf("Validate() error = %v, want age recipient guidance", err)
	}
}
