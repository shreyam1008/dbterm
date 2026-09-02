package backup

import (
	"crypto/sha256"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyJobDefaultsAndSupportedTopologies(t *testing.T) {
	now := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	localSource := filepath.Join(t.TempDir(), "producer")
	localDestination := filepath.Join(t.TempDir(), "vault")
	identity := filepath.Join(t.TempDir(), "id_ed25519")
	tests := []struct {
		name string
		job  CopyJob
	}{
		{
			name: "push local to sftp after success",
			job: CopyJob{
				Name: "producer push", Mode: CopyModePush,
				Source: CopyEndpoint{Kind: CopyEndpointLocal}, SourceBackupJobID: "job_source",
				Destination: CopyEndpoint{
					Kind: CopyEndpointSFTP, Location: "sftp://backup@vault.example:2222/srv/backups/",
					CredentialRef: identity, PinnedHostKey: copyModelTestPin(),
				},
				Trigger: CopyTriggerAfterSuccess,
			},
		},
		{
			name: "pull ssh to local on schedule",
			job: CopyJob{
				Name: "vault pull", Mode: CopyModePull,
				Source: CopyEndpoint{
					Kind: CopyEndpointSSH, Location: "ssh://reader@producer.example/E:/archives",
					CredentialRef: identity, PinnedHostKey: copyModelTestPin(),
				},
				Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: localDestination},
				Trigger:     CopyTriggerTimed, Schedule: Schedule{Kind: ScheduleInterval, EveryMinutes: 30},
			},
		},
		{
			name: "pull rclone to local manually",
			job: CopyJob{
				Name: "object mirror", Mode: CopyModePull,
				Source:      CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://archive/team//nightly/"},
				Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: localDestination},
				Trigger:     CopyTriggerManual, Verification: CopyVerificationSizeOnly,
			},
		},
		{
			name: "push local to local manually",
			job: CopyJob{
				Name: "mounted vault", Mode: CopyModePush,
				Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: localSource},
				Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: localDestination},
				Trigger:     CopyTriggerManual,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.job.ApplyDefaults(now); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(test.job.ID, "copy_") {
				t.Fatalf("copy job ID = %q", test.job.ID)
			}
			if test.job.Verification == "" || test.job.Retention.KeepLast == 0 || test.job.TimeoutMinutes == 0 {
				t.Fatalf("defaults were not applied: %#v", test.job)
			}
			if test.job.CreatedAt != now || test.job.UpdatedAt != now {
				t.Fatalf("timestamps = %s / %s, want %s", test.job.CreatedAt, test.job.UpdatedAt, now)
			}
			if err := test.job.Validate(); err != nil {
				t.Fatalf("normalized job did not validate: %v", err)
			}
		})
	}
}

func TestCopyTransferFingerprintTracksOnlyTransportCriticalConfiguration(t *testing.T) {
	source := filepath.Join(t.TempDir(), "producer")
	vaultRoot := t.TempDir()
	destination := filepath.Join(vaultRoot, "copies")
	job := CopyJob{
		Name: "daily vault", Mode: CopyModePush,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: source},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		DestinationVolume: &CopyDestinationVolume{
			Mode: CopyVolumeAlreadyMounted, MountPoint: vaultRoot,
			SentinelFile: ".vault-id", SentinelValue: "vault-one",
		},
		ArtifactFilter: CopyArtifactFilter{
			ProducerID: "producer", JobID: "job", Formats: []string{"sqlite", "mysql-sql+zstd"},
		},
		Trigger:      CopyTriggerTimed,
		Schedule:     Schedule{Kind: ScheduleDaily, TimesOfDay: []string{"01:00"}, Timezone: "UTC"},
		Verification: CopyVerificationSHA256Format,
		Retention:    Retention{KeepLast: 7},
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := job.TransferConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	operationalEdit := job
	operationalEdit.Name = "renamed vault"
	operationalEdit.Schedule.TimesOfDay = []string{"03:00", "15:00"}
	operationalEdit.Retention.KeepLast = 30
	operationalEdit.TimeoutMinutes++
	operationalEdit.ArtifactFilter.Formats = []string{"MYSQL-SQL+ZSTD", "SQLITE"}
	got, err := operationalEdit.TransferConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if got != fingerprint {
		t.Fatalf("non-transport edit changed fingerprint: %s -> %s", fingerprint, got)
	}

	for name, mutate := range map[string]func(*CopyJob){
		"mode":        func(candidate *CopyJob) { candidate.Mode = CopyModePull },
		"source":      func(candidate *CopyJob) { candidate.Source.Location = filepath.Join(t.TempDir(), "other") },
		"destination": func(candidate *CopyJob) { candidate.Destination.Location = filepath.Join(vaultRoot, "other") },
		"filter":      func(candidate *CopyJob) { candidate.ArtifactFilter.JobID = "other" },
		"volume": func(candidate *CopyJob) {
			changed := *candidate.DestinationVolume
			changed.SentinelValue = "vault-two"
			candidate.DestinationVolume = &changed
		},
		"verification": func(candidate *CopyJob) { candidate.Verification = CopyVerificationSHA256 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := job
			mutate(&candidate)
			changed, err := candidate.TransferConfigurationFingerprint()
			if err != nil && name == "mode" {
				// Mode alone can make an otherwise valid topology invalid, but it
				// still must never be accepted as the original proof.
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if changed == fingerprint {
				t.Fatalf("%s edit did not change transfer fingerprint", name)
			}
		})
	}
}

func TestCopySSHAndSFTPSpellingsShareCanonicalTransportIdentity(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "id_ed25519")
	sshEndpoint := CopyEndpoint{
		Kind: CopyEndpointSSH, Location: "ssh://BackupUser@VAULT.Example:22/Srv/Backups/",
		CredentialRef: identity, PinnedHostKey: copyModelTestPin(),
	}
	sftpEndpoint := CopyEndpoint{
		Kind: CopyEndpointSFTP, Location: "sftp://BackupUser@vault.example/Srv/Backups",
		CredentialRef: identity, PinnedHostKey: copyModelTestPin(),
	}
	normalizedSSH, err := normalizeCopyEndpoint(sshEndpoint, false)
	if err != nil {
		t.Fatal(err)
	}
	normalizedSFTP, err := normalizeCopyEndpoint(sftpEndpoint, false)
	if err != nil {
		t.Fatal(err)
	}
	if normalizedSSH != normalizedSFTP {
		t.Fatalf("SSH and SFTP aliases normalized differently:\nSSH  %+v\nSFTP %+v", normalizedSSH, normalizedSFTP)
	}
	if normalizedSSH.Kind != CopyEndpointSFTP || normalizedSSH.Location != "sftp://BackupUser@vault.example/Srv/Backups" {
		t.Fatalf("canonical SSH/SFTP endpoint = %+v", normalizedSSH)
	}

	destination := CopyEndpoint{Kind: CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "vault")}
	sshJob := CopyJob{
		Name: "SSH spelling", Mode: CopyModePull, Source: sshEndpoint, Destination: destination,
		Trigger: CopyTriggerManual, Verification: CopyVerificationSHA256Format,
	}
	sftpJob := sshJob
	sftpJob.Name = "SFTP spelling"
	sftpJob.Source = sftpEndpoint
	sshFingerprint, err := sshJob.TransferConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	sftpFingerprint, err := sftpJob.TransferConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if sshFingerprint != sftpFingerprint {
		t.Fatalf("SSH/SFTP alias fingerprints differ: %s != %s", sshFingerprint, sftpFingerprint)
	}
	if !copyJobsConflict(sshJob, sftpJob) {
		t.Fatal("SSH/SFTP spelling bypassed duplicate transfer-owner detection")
	}
}

func copyModelTestPin() string {
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, sha256.Size))
}

func TestCopyJobValidationRejectsUnsafeOrAmbiguousPolicies(t *testing.T) {
	localA := filepath.Join(t.TempDir(), "a")
	localB := filepath.Join(t.TempDir(), "b")
	identity := filepath.Join(t.TempDir(), "id_ed25519")
	valid := CopyJob{
		Name: "copy", Mode: CopyModePush,
		Source:      CopyEndpoint{Kind: CopyEndpointLocal, Location: localA},
		Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: localB},
		Trigger:     CopyTriggerManual,
	}
	tests := []struct {
		name    string
		mutate  func(*CopyJob)
		message string
	}{
		{"unknown mode", func(job *CopyJob) { job.Mode = "mirror" }, "unsupported copy mode"},
		{"same path", func(job *CopyJob) { job.Destination.Location = localA }, "must be different"},
		{"push remote source", func(job *CopyJob) {
			job.Source = CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://source/backups"}
		}, "push copy source must be local"},
		{"pull remote destination", func(job *CopyJob) {
			job.Mode = CopyModePull
			job.Source = CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://source/backups"}
			job.Destination = CopyEndpoint{Kind: CopyEndpointRclone, Location: "rclone://vault/backups"}
		}, "pull copy destination must be local"},
		{"after success pull", func(job *CopyJob) {
			job.Mode = CopyModePull
			job.Trigger = CopyTriggerAfterSuccess
			job.SourceBackupJobID = "job_source"
		}, "only valid for push"},
		{"after success without job", func(job *CopyJob) { job.Trigger = CopyTriggerAfterSuccess }, "requires a source backup job ID"},
		{"timed without schedule", func(job *CopyJob) { job.Trigger = CopyTriggerTimed }, "requires an interval"},
		{"manual with schedule", func(job *CopyJob) {
			job.Schedule = Schedule{Kind: ScheduleInterval, EveryMinutes: 15}
		}, "manual copy trigger"},
		{"sftp password", func(job *CopyJob) {
			job.Destination = CopyEndpoint{Kind: CopyEndpointSFTP, Location: "sftp://user:secret@host/backups", CredentialRef: "cred", PinnedHostKey: "pin"}
		}, "passwords must use a credential reference"},
		{"sftp missing pin", func(job *CopyJob) {
			job.Destination = CopyEndpoint{Kind: CopyEndpointSFTP, Location: "sftp://user@host/backups", CredentialRef: "cred"}
		}, "requires a pinned host key"},
		{"sftp traversal", func(job *CopyJob) {
			job.Destination = CopyEndpoint{Kind: CopyEndpointSFTP, Location: "sftp://user@host/backups/%2e%2e/other", CredentialRef: "cred", PinnedHostKey: "pin"}
		}, "must not contain parent traversal"},
		{"sftp missing user", func(job *CopyJob) {
			job.Destination = CopyEndpoint{Kind: CopyEndpointSFTP, Location: "sftp://host/backups", CredentialRef: identity, PinnedHostKey: copyModelTestPin()}
		}, "explicit service username"},
		{"sftp malformed pin", func(job *CopyJob) {
			job.Destination = CopyEndpoint{Kind: CopyEndpointSFTP, Location: "sftp://user@host/backups", CredentialRef: identity, PinnedHostKey: "SHA256:not-a-fingerprint"}
		}, "SHA256 fingerprint"},
		{"sftp relative identity", func(job *CopyJob) {
			job.Destination = CopyEndpoint{Kind: CopyEndpointSFTP, Location: "sftp://user@host/backups", CredentialRef: "id_ed25519", PinnedHostKey: copyModelTestPin()}
		}, "absolute private identity path"},
		{"duplicate format", func(job *CopyJob) {
			job.ArtifactFilter.Formats = []string{"mysql-sql+zstd", "MYSQL-SQL+ZSTD"}
		}, "is duplicated"},
		{"conflicting job filter", func(job *CopyJob) {
			job.SourceBackupJobID = "job_source"
			job.ArtifactFilter.JobID = "job_other"
		}, "conflicts with source backup job"},
		{"bad verification", func(job *CopyJob) { job.Verification = "trust-me" }, "unsupported copy verification strength"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := valid
			test.mutate(&job)
			err := job.ApplyDefaults(time.Now())
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("ApplyDefaults() error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestCopyArtifactResultRequiresConfiguredEvidenceAndCompletion(t *testing.T) {
	now := time.Now().UTC()
	result := CopyArtifactResult{
		ArtifactID: "artifact_test", Source: "source.dump", Destination: "vault.dump", SizeBytes: 42,
		SHA256: strings.Repeat("a", 64), Verification: CopyVerificationSHA256Format, VerifiedAt: now,
		ManifestPath: "vault.dump.dbterm.json", ManifestSize: 100, ManifestSHA256: strings.Repeat("b", 64),
		PublicationState: ArtifactPublicationComplete,
	}
	if err := result.validate(CopyVerificationSHA256Format, true); err != nil {
		t.Fatal(err)
	}
	weaker := result
	weaker.Verification = CopyVerificationSizeOnly
	weaker.SHA256 = ""
	if err := weaker.validate(CopyVerificationSHA256, true); err == nil || !strings.Contains(err.Error(), "weaker") {
		t.Fatalf("weaker verification error = %v", err)
	}
	boundary := result
	boundary.PublicationState = ArtifactPublicationArtifactOnly
	boundary.ManifestPath = ""
	boundary.ManifestSize = 0
	boundary.ManifestSHA256 = ""
	if err := boundary.validate(CopyVerificationSHA256, false); err != nil {
		t.Fatalf("failed run could not retain artifact-only boundary: %v", err)
	}
	if err := boundary.validate(CopyVerificationSHA256, true); err == nil || !strings.Contains(err.Error(), "successful copy run") {
		t.Fatalf("successful artifact-only result error = %v", err)
	}
}

func TestCopyJobCompatibilityDefaultsRetryPolicy(t *testing.T) {
	job := CopyJob{}
	job.applyCompatibilityDefaults()
	if job.MaxAttempts != 3 || job.RetryInitialSeconds != 2 || job.RetryMaxSeconds != 60 {
		t.Fatalf("compatibility retry defaults = %d/%d/%d", job.MaxAttempts, job.RetryInitialSeconds, job.RetryMaxSeconds)
	}
	job = CopyJob{MaxAttempts: 5, RetryInitialSeconds: 7, RetryMaxSeconds: 90}
	job.applyCompatibilityDefaults()
	if job.MaxAttempts != 5 || job.RetryInitialSeconds != 7 || job.RetryMaxSeconds != 90 {
		t.Fatalf("explicit retry policy changed = %d/%d/%d", job.MaxAttempts, job.RetryInitialSeconds, job.RetryMaxSeconds)
	}
}
