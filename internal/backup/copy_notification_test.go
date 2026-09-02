package backup

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCopyRetentionWarningUsesFailurePolicyWithoutChangingCopyStatus(t *testing.T) {
	notification := EmailNotification{Policy: NotificationFailure}
	run := CopyRun{Status: RunSucceeded, RetentionError: "changed file was preserved"}
	if !notification.ShouldNotifyCopyRun(run) {
		t.Fatal("successful copy with retention warning should use failure notifications")
	}
	if run.Status != RunSucceeded {
		t.Fatalf("notification policy changed copy status to %s", run.Status)
	}
}

func TestSendCopyRunNotificationContainsProtectionFactsButNotCredentials(t *testing.T) {
	server := startSMTPTestServer(t, "")
	notification := EmailNotification{
		Policy: NotificationBoth, SMTPHost: server.host, SMTPPort: server.port, TLSMode: SMTPTLSNone,
		From: "dbterm@example.test", Recipients: []string{"operator@example.test"},
	}
	job := CopyJob{
		Name: "Vrindavan to CT400", Mode: CopyModePull,
		Source:       CopyEndpoint{Kind: CopyEndpointSFTP, Location: "sftp://reader@producer.example/backups", CredentialRef: "C:/secret/id_ed25519", PinnedHostKey: copyModelTestPin()},
		Destination:  CopyEndpoint{Kind: CopyEndpointLocal, Location: "C:/vault"},
		Notification: notification,
	}
	finished := time.Now().UTC()
	run := CopyRun{
		ID: "copyrun_test", JobID: "copy_test", Trigger: CopyTriggerTimed, Status: RunSucceeded,
		StartedAt: finished.Add(-2 * time.Second), FinishedAt: finished,
		RequiredVerification: CopyVerificationSHA256Format,
		Discovered:           2, AlreadyPresent: 1, BytesCopied: 42,
		Artifacts: []CopyArtifactResult{{
			ArtifactID: "artifact_test", Destination: "C:/vault/test.sqlite", SizeBytes: 42,
			SHA256: strings.Repeat("a", 64), PublicationState: ArtifactPublicationComplete,
		}},
	}
	if err := SendCopyRunNotification(context.Background(), job, run); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-server.message:
		for _, expected := range []string{"Copy succeeded", "Mode: pull", "New copies: 1", "Already present: 1", "SHA-256 aaaaaaaaaaaa"} {
			if !strings.Contains(message, expected) {
				t.Fatalf("copy notification is missing %q:\n%s", expected, message)
			}
		}
		for _, forbidden := range []string{job.Source.CredentialRef, job.Source.PinnedHostKey} {
			if strings.Contains(message, forbidden) {
				t.Fatalf("copy notification leaked credential metadata %q", forbidden)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP server did not receive copy message")
	}
}
