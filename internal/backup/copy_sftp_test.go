package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestSFTPCopyPushPullOldestFirstAndManifestLast(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")

	source := t.TempDir()
	created := time.Date(2026, 9, 3, 3, 0, 0, 0, time.UTC)
	writeCopyRunnerFixture(t, source, "new.sqlite", "artifact_new", "producer_one", "job_one", created.Add(time.Hour), false)
	writeCopyRunnerFixture(t, source, "old.sqlite", "artifact_old", "producer_one", "job_one", created, false)
	job := sftpPushJob(t, source, endpoint)

	var copied []string
	runner := CopyRunner{Now: func() time.Time { return created.Add(2 * time.Hour) }, Progress: func(event ProgressEvent) {
		if event.Phase == "copy" && strings.HasPrefix(event.Message, "copied and verified ") {
			copied = append(copied, strings.TrimPrefix(event.Message, "copied and verified "))
		}
	}}
	outcome, err := runner.runLocalToSFTP(sftpTestSyncContext(), job, source, endpoint, job.ArtifactFilter)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Discovered != 2 || outcome.AlreadyPresent != 0 || outcome.BytesCopied == 0 || len(outcome.Artifacts) != 2 {
		t.Fatalf("unexpected push outcome: %+v", outcome)
	}
	if strings.Join(copied, ",") != "old.sqlite,new.sqlite" {
		t.Fatalf("push order = %v, want oldest first", copied)
	}
	for _, name := range []string{"old.sqlite", "new.sqlite"} {
		if !server.wasLinkedBefore("/vault/"+name, "/vault/"+name+ArtifactManifestSuffix) {
			t.Fatalf("remote publication did not atomically link artifact before manifest for %s; links=%v", name, server.linkTargets())
		}
		if _, ok := server.openFlags("/vault/" + name); ok {
			t.Fatalf("artifact %s was written directly under its final name", name)
		}
		if _, ok := server.openFlags("/vault/" + name + ArtifactManifestSuffix); ok {
			t.Fatalf("manifest %s was written directly under its final name", name)
		}
	}
	for _, writtenPath := range server.writePaths() {
		base := path.Base(writtenPath)
		if !strings.HasPrefix(base, sftpPrivatePartialPrefix) || !strings.HasSuffix(base, sftpPrivatePartialSuffix) {
			t.Fatalf("SFTP upload wrote outside the private partial namespace: %s", writtenPath)
		}
		flags, ok := server.openFlags(writtenPath)
		if !ok || !flags.Creat || !flags.Excl || !flags.Write {
			t.Fatalf("private partial %s flags = %+v, present=%v; want write|create|exclusive", writtenPath, flags, ok)
		}
	}
	assertNoSFTPPrivatePartials(t, server, endpoint, "/vault")

	second, err := (CopyRunner{}).runLocalToSFTP(sftpTestSyncContext(), job, source, endpoint, job.ArtifactFilter)
	if err != nil {
		t.Fatal(err)
	}
	if second.Discovered != 2 || second.AlreadyPresent != 2 || second.BytesCopied != 0 || len(second.Artifacts) != 2 {
		t.Fatalf("second push should be a verified no-op: %+v", second)
	}
	for _, artifact := range second.Artifacts {
		if !artifact.AlreadyPresent || artifact.PublicationState != ArtifactPublicationComplete || artifact.ManifestSize <= 0 || artifact.ManifestSHA256 == "" {
			t.Fatalf("already-present SFTP artifact was not durably ownable: %+v", artifact)
		}
	}

	destination := t.TempDir()
	pullJob := sftpPullJob(t, endpoint, destination)
	pulled, err := (CopyRunner{}).runSFTPToLocal(context.Background(), pullJob, endpoint, destination, pullJob.ArtifactFilter)
	if err != nil {
		t.Fatal(err)
	}
	if pulled.Discovered != 2 || pulled.AlreadyPresent != 0 || len(pulled.Artifacts) != 2 {
		t.Fatalf("unexpected pull outcome: %+v", pulled)
	}
	for _, name := range []string{"old.sqlite", "new.sqlite"} {
		manifest, err := ReadArtifactManifestForArtifactRequired(filepath.Join(destination, name))
		if err != nil {
			t.Fatalf("read pulled %s: %v", name, err)
		}
		if manifest.ArtifactID != "artifact_"+strings.TrimSuffix(name, ".sqlite") {
			t.Fatalf("pulled manifest for %s = %+v", name, manifest)
		}
	}
	pulledAgain, err := (CopyRunner{}).runSFTPToLocal(context.Background(), pullJob, endpoint, destination, pullJob.ArtifactFilter)
	if err != nil {
		t.Fatal(err)
	}
	if pulledAgain.AlreadyPresent != 2 || pulledAgain.BytesCopied != 0 || len(pulledAgain.Artifacts) != 2 {
		t.Fatalf("second pull should durably record two verified no-op artifacts: %+v", pulledAgain)
	}
	for _, artifact := range pulledAgain.Artifacts {
		if !artifact.AlreadyPresent || artifact.PublicationState != ArtifactPublicationComplete || artifact.ManifestSize <= 0 || artifact.ManifestSHA256 == "" {
			t.Fatalf("already-present pulled artifact was not durably ownable: %+v", artifact)
		}
	}
}

func TestSFTPPullInvokesLocalPublicationGuardForArtifactStage(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	source := t.TempDir()
	writeCopyRunnerFixture(t, source, "guarded.sqlite", "artifact_sftp_guard", "producer", "source_job", time.Now().UTC(), false)
	pushJob := sftpPushJob(t, source, endpoint)
	if _, err := (CopyRunner{}).runLocalToSFTP(sftpTestSyncContext(), pushJob, source, endpoint, pushJob.ArtifactFilter); err != nil {
		t.Fatal(err)
	}

	destination := t.TempDir()
	pullJob := sftpPullJob(t, endpoint, destination)
	guardedArtifactStage := false
	runner := CopyRunner{LocalPublicationGuard: func(_ context.Context, localPath string, phase CopyLocalPublicationPhase) error {
		if phase == CopyLocalStageCreated && strings.HasPrefix(filepath.Base(localPath), ".dbterm-publish-") {
			guardedArtifactStage = true
			return errors.New("injected SFTP local-publication guard refusal")
		}
		return nil
	}}
	_, err := runner.runSFTPToLocal(context.Background(), pullJob, endpoint, destination, pullJob.ArtifactFilter)
	if err == nil || !strings.Contains(err.Error(), "injected SFTP") {
		t.Fatalf("SFTP local-publication guard error = %v", err)
	}
	if !guardedArtifactStage {
		t.Fatal("SFTP pull did not guard its local artifact stage")
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("guarded SFTP pull left destination files %v: %v", entryNames(entries), readErr)
	}
}

func TestSFTPCopyLeavesExclusiveArtifactOrphanWhenManifestFails(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSSH)
	server.makeDirectory(t, endpoint, "/vault")
	source := t.TempDir()
	writeCopyRunnerFixture(t, source, "orphan.sqlite", "artifact_orphan", "producer_one", "job_one", time.Now(), false)
	job := sftpPushJob(t, source, endpoint)
	server.failManifest.Store(true)

	outcome, err := (CopyRunner{}).runLocalToSFTP(sftpTestSyncContext(), job, source, endpoint, job.ArtifactFilter)
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("manifest failure error = %v", err)
	}
	if len(outcome.Artifacts) != 1 || outcome.Artifacts[0].PublicationState == ArtifactPublicationComplete || len(outcome.Warnings) == 0 {
		t.Fatalf("orphan boundary was not reported: %+v", outcome)
	}
	client := server.client(t, endpoint)
	defer client.Close()
	info, statErr := client.client.Lstat("/vault/orphan.sqlite")
	if statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("exclusive artifact orphan was not preserved: info=%v err=%v", info, statErr)
	}
	if _, manifestErr := client.client.Lstat("/vault/orphan.sqlite" + ArtifactManifestSuffix); !errors.Is(manifestErr, os.ErrNotExist) {
		t.Fatalf("failed manifest unexpectedly became discoverable: %v", manifestErr)
	}
	assertNoSFTPPrivatePartials(t, server, endpoint, "/vault")

	server.failManifest.Store(false)
	retry, retryErr := (CopyRunner{}).runLocalToSFTP(sftpTestSyncContext(), job, source, endpoint, job.ArtifactFilter)
	if retryErr != nil {
		t.Fatalf("retry did not reconcile verified orphan: %v", retryErr)
	}
	if retry.BytesCopied != 0 || len(retry.Artifacts) != 1 || !retry.Artifacts[0].Reconciled || retry.Artifacts[0].PublicationState != ArtifactPublicationComplete {
		t.Fatalf("retry reconciliation outcome = %+v", retry)
	}
	if _, manifestErr := client.client.Lstat("/vault/orphan.sqlite" + ArtifactManifestSuffix); manifestErr != nil {
		t.Fatalf("reconciled SFTP manifest is missing: %v", manifestErr)
	}
}

func TestSFTPPublishCreateOnlyRacePreservesExistingTargetAndCleansOwnPartial(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	client := server.client(t, endpoint)
	defer client.Close()
	racer := server.client(t, endpoint)
	defer racer.Close()

	payload := []byte("verified dbterm partial")
	digest := sha256.Sum256(payload)
	partial, err := writeSFTPPrivatePartial(sftpTestSyncContext(), client.client, "/vault", "artifact", bytes.NewReader(payload), int64(len(payload)), fmt.Sprintf("%x", digest), nil)
	if err != nil {
		t.Fatal(err)
	}
	partialPath := partial.path
	finalPath := "/vault/raced.sqlite"
	attackerBytes := []byte("pre-existing destination")
	server.commands.setBeforeLink(func(request *sftp.Request) error {
		if request.Target != finalPath {
			return nil
		}
		server.commands.setBeforeLink(nil)
		return writeSFTPTestFile(racer.client, finalPath, attackerBytes)
	})

	_, err = publishSFTPPartialCreateOnly(context.Background(), client.client, partial, finalPath)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "create-only") {
		t.Fatalf("create-only race error = %v", err)
	}
	if got, readErr := readSFTPTestFile(client.client, finalPath); readErr != nil || !bytes.Equal(got, attackerBytes) {
		t.Fatalf("racing destination was overwritten: got=%q err=%v", got, readErr)
	}
	if _, statErr := client.client.Lstat(partialPath); !isSFTPNotExist(statErr) {
		t.Fatalf("owned private partial was not cleaned after collision: %v", statErr)
	}
}

func TestSFTPCopyPushFailsClosedWithoutStableStorageSync(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	source := t.TempDir()
	writeCopyRunnerFixture(t, source, "durable.sqlite", "artifact_durable", "producer_one", "job_one", time.Now(), false)
	job := sftpPushJob(t, source, endpoint)

	_, err := (CopyRunner{}).runLocalToSFTP(context.Background(), job, source, endpoint, job.ArtifactFilter)
	if err == nil || !strings.Contains(err.Error(), sftpFSyncExtension) {
		t.Fatalf("missing-fsync error = %v", err)
	}
	if writes := server.writePaths(); len(writes) != 0 {
		t.Fatalf("push wrote remote bytes before rejecting missing fsync capability: %v", writes)
	}
}

func TestSFTPPrivatePartialCleanupRefusesNonPrivatePath(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	client := server.client(t, endpoint)
	defer client.Close()

	finalPath := "/vault/recovery.sqlite"
	payload := []byte("must not be removed")
	if err := writeSFTPTestFile(client.client, finalPath, payload); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	forged := &sftpPrivatePartial{root: "/vault", path: finalPath, size: int64(len(payload)), digest: fmt.Sprintf("%x", digest), complete: true}
	if err := forged.remove(context.Background(), client.client); err == nil || !strings.Contains(err.Error(), "refuse cleanup") {
		t.Fatalf("non-private cleanup error = %v", err)
	}
	if got, err := readSFTPTestFile(client.client, finalPath); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("non-private path was changed by cleanup guard: got=%q err=%v", got, err)
	}
}

func TestSFTPCopyRejectsWrongHostKeyBeforeAuthentication(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	_, otherPrivate, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := ssh.NewSignerFromKey(otherPrivate)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.PinnedHostKey = ssh.FingerprintSHA256(otherSigner.PublicKey())
	server.authAttempts.Store(0)

	_, err = dialSFTPCopyEndpoint(context.Background(), endpoint)
	if err == nil || !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("wrong-pin error = %v", err)
	}
	if got := server.authAttempts.Load(); got != 0 {
		t.Fatalf("server authentication was attempted %d times before rejecting its host key", got)
	}
}

func TestSFTPIdentityFileSafety(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid := writeCopySFTPIdentity(t, privateKey, nil)
	if _, err := loadSFTPIdentity(valid); err != nil {
		t.Fatalf("valid identity: %v", err)
	}

	t.Run("encrypted", func(t *testing.T) {
		path := writeCopySFTPIdentity(t, privateKey, []byte("not-used-by-dbterm"))
		_, err := loadSFTPIdentity(path)
		if err == nil || !strings.Contains(err.Error(), "encrypted SSH private identities are not supported") {
			t.Fatalf("encrypted identity error = %v", err)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "identity")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxSFTPIdentityBytes + 1); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = loadSFTPIdentity(path)
		if err == nil || !strings.Contains(err.Error(), "size must be between") {
			t.Fatalf("oversize identity error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "identity-link")
		if err := os.Symlink(valid, link); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		_, err := loadSFTPIdentity(link)
		if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
			t.Fatalf("symlink identity error = %v", err)
		}
	})
	t.Run("relative reference", func(t *testing.T) {
		_, err := loadSFTPIdentity("id_ed25519")
		if err == nil || !strings.Contains(err.Error(), "absolute path") {
			t.Fatalf("relative identity error = %v", err)
		}
	})
}

func TestSFTPScannerRejectsSymlinkAndOversizeManifest(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	client := server.client(t, endpoint)
	if err := writeSFTPTestFile(client.client, "/vault/real.dbterm.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := client.client.Symlink("/vault/real.dbterm.json", "/vault/link.sqlite"+ArtifactManifestSuffix); err != nil {
		client.Close()
		t.Skipf("SFTP test handler does not support symbolic links: %v", err)
	}
	_, err := scanSFTPCopyCandidates(context.Background(), client.client, "/vault", CopyArtifactFilter{})
	client.Close()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("remote manifest symlink error = %v", err)
	}

	server = newCopySFTPTestServer(t)
	endpoint = server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	client = server.client(t, endpoint)
	huge := bytes.Repeat([]byte{'x'}, maxArtifactManifestBytes+1)
	if err := writeSFTPTestFile(client.client, "/vault/huge.sqlite"+ArtifactManifestSuffix, huge); err != nil {
		t.Fatal(err)
	}
	_, err = scanSFTPCopyCandidates(context.Background(), client.client, "/vault", CopyArtifactFilter{})
	client.Close()
	if err == nil || !strings.Contains(err.Error(), "size must be between") {
		t.Fatalf("oversize remote manifest error = %v", err)
	}
}

func TestMeasureSFTPTransportIsReadOnly(t *testing.T) {
	server := newCopySFTPTestServer(t)
	endpoint := server.endpoint(CopyEndpointSFTP)
	server.makeDirectory(t, endpoint, "/vault")
	before := server.writePaths()
	measurement, err := MeasureSFTPTransport(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !measurement.Readable || !measurement.CreateOnlyPublish || measurement.HostKeyFingerprint != endpoint.PinnedHostKey {
		t.Fatalf("measurement = %+v", measurement)
	}
	if measurement.StableStorageSync {
		t.Fatalf("in-memory SFTP server unexpectedly advertised stable-storage sync: %+v", measurement)
	}
	if after := server.writePaths(); len(after) != len(before) {
		t.Fatalf("read-only measurement wrote remote paths: before=%v after=%v", before, after)
	}
}

func sftpPushJob(t *testing.T, source string, endpoint CopyEndpoint) CopyJob {
	t.Helper()
	job := CopyJob{
		ID: "copy_sftp_push", Name: "SFTP push", Mode: CopyModePush,
		Source: CopyEndpoint{Kind: CopyEndpointLocal, Location: source}, Destination: endpoint,
		Trigger: CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual},
		Verification: CopyVerificationSHA256Format, Retention: Retention{KeepLast: 14},
		TimeoutMinutes: 5, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	return job
}

func sftpPullJob(t *testing.T, endpoint CopyEndpoint, destination string) CopyJob {
	t.Helper()
	job := CopyJob{
		ID: "copy_sftp_pull", Name: "SFTP pull", Mode: CopyModePull,
		Source: endpoint, Destination: CopyEndpoint{Kind: CopyEndpointLocal, Location: destination},
		Trigger: CopyTriggerManual, Schedule: Schedule{Kind: ScheduleManual},
		Verification: CopyVerificationSHA256Format, Retention: Retention{KeepLast: 14},
		TimeoutMinutes: 5, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatal(err)
	}
	return job
}

type recordingSFTPFileWriter struct {
	delegate     sftp.FileWriter
	mu           sync.Mutex
	paths        []string
	flags        map[string]sftp.FileOpenFlags
	failManifest *atomic.Bool
}

func (writer *recordingSFTPFileWriter) Filewrite(request *sftp.Request) (io.WriterAt, error) {
	writer.mu.Lock()
	writer.paths = append(writer.paths, request.Filepath)
	writer.flags[request.Filepath] = request.Pflags()
	writer.mu.Unlock()
	base := path.Base(request.Filepath)
	if writer.failManifest.Load() && strings.HasPrefix(base, sftpPrivatePartialPrefix+"manifest-") && strings.HasSuffix(base, sftpPrivatePartialSuffix) {
		return nil, errors.New("injected completion manifest failure")
	}
	return writer.delegate.Filewrite(request)
}

type recordingSFTPFileCmder struct {
	delegate   sftp.FileCmder
	mu         sync.Mutex
	links      [][2]string
	beforeLink func(*sftp.Request) error
}

func (commands *recordingSFTPFileCmder) Filecmd(request *sftp.Request) error {
	if request.Method == "Link" {
		commands.mu.Lock()
		commands.links = append(commands.links, [2]string{request.Filepath, request.Target})
		hook := commands.beforeLink
		commands.mu.Unlock()
		if hook != nil {
			if err := hook(request); err != nil {
				return err
			}
		}
	}
	return commands.delegate.Filecmd(request)
}

func (commands *recordingSFTPFileCmder) setBeforeLink(hook func(*sftp.Request) error) {
	commands.mu.Lock()
	defer commands.mu.Unlock()
	commands.beforeLink = hook
}

type copySFTPTestServer struct {
	t             *testing.T
	listener      net.Listener
	hostSigner    ssh.Signer
	clientPrivate ed25519.PrivateKey
	identityPath  string
	handlers      sftp.Handlers
	writes        *recordingSFTPFileWriter
	commands      *recordingSFTPFileCmder
	failManifest  atomic.Bool
	authAttempts  atomic.Int64
	mu            sync.Mutex
	connections   map[net.Conn]struct{}
}

func newCopySFTPTestServer(t *testing.T) *copySFTPTestServer {
	t.Helper()
	_, hostPrivate, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, clientPrivate, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := ssh.NewPublicKey(clientPublic)
	if err != nil {
		t.Fatal(err)
	}
	server := &copySFTPTestServer{t: t, hostSigner: hostSigner, clientPrivate: clientPrivate, connections: make(map[net.Conn]struct{})}
	server.identityPath = writeCopySFTPIdentity(t, clientPrivate, nil)
	server.handlers = sftp.InMemHandler()
	server.writes = &recordingSFTPFileWriter{delegate: server.handlers.FilePut, flags: make(map[string]sftp.FileOpenFlags), failManifest: &server.failManifest}
	server.commands = &recordingSFTPFileCmder{delegate: server.handlers.FileCmd}
	server.handlers.FilePut = server.writes
	server.handlers.FileCmd = server.commands
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.listener = listener
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		server.authAttempts.Add(1)
		if !bytes.Equal(key.Marshal(), allowed.Marshal()) {
			return nil, errors.New("unknown test public key")
		}
		return nil, nil
	}}
	config.AddHostKey(hostSigner)
	go server.accept(config)
	t.Cleanup(server.close)
	return server
}

func (server *copySFTPTestServer) endpoint(kind CopyEndpointKind) CopyEndpoint {
	return CopyEndpoint{
		Kind: kind, Location: fmt.Sprintf("%s://backup@%s/vault", kind, server.listener.Addr().String()),
		CredentialRef: server.identityPath, PinnedHostKey: ssh.FingerprintSHA256(server.hostSigner.PublicKey()),
	}
}

func (server *copySFTPTestServer) accept(config *ssh.ServerConfig) {
	for {
		raw, err := server.listener.Accept()
		if err != nil {
			return
		}
		server.mu.Lock()
		server.connections[raw] = struct{}{}
		server.mu.Unlock()
		go server.serveConnection(raw, config)
	}
}

func (server *copySFTPTestServer) serveConnection(raw net.Conn, config *ssh.ServerConfig) {
	defer func() {
		_ = raw.Close()
		server.mu.Lock()
		delete(server.connections, raw)
		server.mu.Unlock()
	}()
	_, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)
	for channelRequest := range channels {
		if channelRequest.ChannelType() != "session" {
			_ = channelRequest.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		channel, channelRequests, err := channelRequest.Accept()
		if err != nil {
			return
		}
		for request := range channelRequests {
			var subsystem struct{ Name string }
			if request.Type != "subsystem" || ssh.Unmarshal(request.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
				_ = request.Reply(false, nil)
				continue
			}
			_ = request.Reply(true, nil)
			requestServer := sftp.NewRequestServer(channel, server.handlers)
			_ = requestServer.Serve()
			_ = requestServer.Close()
			return
		}
	}
}

func (server *copySFTPTestServer) close() {
	_ = server.listener.Close()
	server.mu.Lock()
	defer server.mu.Unlock()
	for connection := range server.connections {
		_ = connection.Close()
	}
}

func (server *copySFTPTestServer) client(t *testing.T, endpoint CopyEndpoint) *sftpCopyConnection {
	t.Helper()
	client, err := dialSFTPCopyEndpoint(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func (server *copySFTPTestServer) makeDirectory(t *testing.T, endpoint CopyEndpoint, path string) {
	t.Helper()
	client := server.client(t, endpoint)
	defer client.Close()
	if err := client.client.Mkdir(path); err != nil && !isSFTPExist(err) {
		t.Fatal(err)
	}
}

func (server *copySFTPTestServer) writePaths() []string {
	server.writes.mu.Lock()
	defer server.writes.mu.Unlock()
	return append([]string(nil), server.writes.paths...)
}

func (server *copySFTPTestServer) wasCreatedBefore(first, second string) bool {
	paths := server.writePaths()
	firstIndex, secondIndex := -1, -1
	for index, value := range paths {
		if value == first && firstIndex < 0 {
			firstIndex = index
		}
		if value == second && secondIndex < 0 {
			secondIndex = index
		}
	}
	return firstIndex >= 0 && secondIndex > firstIndex
}

func (server *copySFTPTestServer) linkTargets() []string {
	server.commands.mu.Lock()
	defer server.commands.mu.Unlock()
	targets := make([]string, 0, len(server.commands.links))
	for _, link := range server.commands.links {
		targets = append(targets, link[1])
	}
	return targets
}

func (server *copySFTPTestServer) wasLinkedBefore(first, second string) bool {
	targets := server.linkTargets()
	firstIndex, secondIndex := -1, -1
	for index, target := range targets {
		if target == first && firstIndex < 0 {
			firstIndex = index
		}
		if target == second && secondIndex < 0 {
			secondIndex = index
		}
	}
	return firstIndex >= 0 && secondIndex > firstIndex
}

func (server *copySFTPTestServer) openFlags(path string) (sftp.FileOpenFlags, bool) {
	server.writes.mu.Lock()
	defer server.writes.mu.Unlock()
	flags, ok := server.writes.flags[path]
	return flags, ok
}

func writeCopySFTPIdentity(t *testing.T, privateKey ed25519.PrivateKey, passphrase []byte) string {
	t.Helper()
	var block *pem.Block
	var err error
	if len(passphrase) == 0 {
		block, err = ssh.MarshalPrivateKey(privateKey, "dbterm-sftp-test")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(privateKey, "dbterm-sftp-test", passphrase)
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSFTPTestFile(client *sftp.Client, path string, data []byte) error {
	file, err := client.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func readSFTPTestFile(client *sftp.Client, filePath string) ([]byte, error) {
	file, err := client.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func assertNoSFTPPrivatePartials(t *testing.T, server *copySFTPTestServer, endpoint CopyEndpoint, root string) {
	t.Helper()
	client := server.client(t, endpoint)
	defer client.Close()
	entries, err := client.client.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), sftpPrivatePartialPrefix) && strings.HasSuffix(entry.Name(), sftpPrivatePartialSuffix) {
			t.Fatalf("private SFTP partial leaked after completed operation: %s", entry.Name())
		}
	}
}

func sftpTestSyncContext() context.Context {
	return context.WithValue(context.Background(), sftpFileSyncContextKey{}, sftpFileSyncFunc(func(*sftp.File) error { return nil }))
}
