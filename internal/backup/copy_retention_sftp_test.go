package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestSFTPCopyRetentionKeepsNewestVerifiedRecoveryPoints(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := t.TempDir()
	base := time.Now().Add(-3 * time.Hour).UTC()
	writeCopyRunnerFixture(t, source, "old.sqlite", "artifact_old", "producer", "source_job", base, false)
	writeCopyRunnerFixture(t, source, "middle.sqlite", "artifact_middle", "producer", "source_job", base.Add(time.Hour), false)
	writeCopyRunnerFixture(t, source, "new.sqlite", "artifact_new", "producer", "source_job", base.Add(2*time.Hour), false)
	job := sftpPushJob(t, source, endpoint)
	job.Enabled = true
	job.Retention = Retention{KeepLast: 2}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCopyJobNow(sftpTestSyncContext(), store, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	client := server.client(t, endpoint)
	defer client.Close()
	for _, name := range []string{"middle.sqlite", "new.sqlite"} {
		if _, err := client.client.Lstat("/vault/" + name); err != nil {
			t.Fatalf("retained SFTP artifact %s is missing: %v", name, err)
		}
		if _, err := client.client.Lstat("/vault/" + name + ArtifactManifestSuffix); err != nil {
			t.Fatalf("retained SFTP manifest %s is missing: %v", name, err)
		}
	}
	for _, remote := range []string{"/vault/old.sqlite", "/vault/old.sqlite" + ArtifactManifestSuffix} {
		if _, err := client.client.Lstat(remote); !isSFTPNotExist(err) {
			t.Fatalf("expired SFTP component still exists at %s: %v", remote, err)
		}
	}
	latest, ok, err := store.LatestCopyRun(context.Background(), job.ID)
	if err != nil || !ok {
		t.Fatalf("latest copy run: found=%t err=%v", ok, err)
	}
	for _, artifact := range latest.Artifacts {
		if artifact.ArtifactID == "artifact_old" && (artifact.PrunedAt.IsZero() || artifact.PruneReason != "retention") {
			t.Fatalf("SFTP retention result was not persisted: %+v", artifact)
		}
	}
}

func TestSFTPCopyRetentionRefusesChangedRemoteArtifact(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	store, err := OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := t.TempDir()
	base := time.Now().Add(-2 * time.Hour).UTC()
	writeCopyRunnerFixture(t, source, "old.sqlite", "artifact_old", "producer", "source_job", base, false)
	writeCopyRunnerFixture(t, source, "new.sqlite", "artifact_new", "producer", "source_job", base.Add(time.Hour), false)
	job := sftpPushJob(t, source, endpoint)
	job.Enabled = true
	job.Retention = Retention{KeepLast: 10}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	if _, err := RunCopyJobNow(sftpTestSyncContext(), store, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	client := server.client(t, endpoint)
	changed, err := client.client.OpenFile("/vault/old.sqlite", os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	if _, err := changed.Write([]byte("changed remote bytes")); err != nil {
		_ = changed.Close()
		client.Close()
		t.Fatal(err)
	}
	if err := changed.Close(); err != nil {
		client.Close()
		t.Fatal(err)
	}
	client.Close()
	job.Retention = Retention{KeepLast: 1}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	_, err = ApplyCopyRetention(context.Background(), store, job, time.Now())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "changed") {
		t.Fatalf("changed remote artifact retention error = %v", err)
	}
	client = server.client(t, endpoint)
	defer client.Close()
	for _, remote := range []string{"/vault/old.sqlite", "/vault/old.sqlite" + ArtifactManifestSuffix} {
		if _, err := client.client.Lstat(remote); err != nil && !os.IsNotExist(err) {
			t.Fatalf("inspect preserved SFTP component %s: %v", remote, err)
		} else if os.IsNotExist(err) {
			t.Fatalf("changed SFTP component was deleted: %s", remote)
		}
	}
}

func TestSFTPRetentionCaptureRaceNeverOverwritesAndPreservesSource(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	client := server.client(t, endpoint)
	defer client.Close()
	racer := server.client(t, endpoint)
	defer racer.Close()

	sourcePath := "/vault/expired.sqlite"
	payload := []byte("recorded recovery bytes")
	digestBytes := sha256.Sum256(payload)
	digest := fmt.Sprintf("%x", digestBytes)
	if err := writeSFTPTestFile(client.client, sourcePath, payload); err != nil {
		t.Fatal(err)
	}
	quarantine, err := sftpPruneQuarantinePath("/vault", sourcePath, int64(len(payload)), digest)
	if err != nil {
		t.Fatal(err)
	}
	attackerBytes := []byte("unrelated quarantine bytes")
	server.commands.setBeforeLink(func(request *sftp.Request) error {
		if request.Target != quarantine {
			return nil
		}
		server.commands.setBeforeLink(nil)
		return writeSFTPTestFile(racer.client, quarantine, attackerBytes)
	})

	removed, err := captureAndRemoveSFTPPrune(context.Background(), client.client, "/vault", sourcePath, int64(len(payload)), digest, nil)
	if err == nil || removed {
		t.Fatalf("racing quarantine capture = removed %t, err %v; want fail closed", removed, err)
	}
	if got, readErr := readSFTPTestFile(client.client, sourcePath); readErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("owned source was changed during quarantine race: got=%q err=%v", got, readErr)
	}
	if got, readErr := readSFTPTestFile(client.client, quarantine); readErr != nil || !bytes.Equal(got, attackerBytes) {
		t.Fatalf("racing quarantine was overwritten: got=%q err=%v", got, readErr)
	}
}

func TestSFTPRetentionResumesVerifiedHardlinkCaptureAfterInterruptedUnlink(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	client := server.client(t, endpoint)
	defer client.Close()

	sourcePath := "/vault/expired.sqlite"
	payload := []byte("recorded recovery bytes")
	digestBytes := sha256.Sum256(payload)
	digest := fmt.Sprintf("%x", digestBytes)
	if err := writeSFTPTestFile(client.client, sourcePath, payload); err != nil {
		t.Fatal(err)
	}
	quarantine, err := sftpPruneQuarantinePath("/vault", sourcePath, int64(len(payload)), digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.client.Link(sourcePath, quarantine); err != nil {
		t.Fatal(err)
	}
	if err := client.client.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}

	removed, err := captureAndRemoveSFTPPrune(context.Background(), client.client, "/vault", sourcePath, int64(len(payload)), digest, nil)
	if err != nil || !removed {
		t.Fatalf("resume verified capture = removed %t, err %v", removed, err)
	}
	if _, err := client.client.Lstat(quarantine); !isSFTPNotExist(err) {
		t.Fatalf("verified resumed quarantine remains: %v", err)
	}
}
