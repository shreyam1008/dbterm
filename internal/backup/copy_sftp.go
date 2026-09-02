package backup

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/shreyam1008/dbterm/internal/privatefile"
	"golang.org/x/crypto/ssh"
)

const (
	maxSFTPIdentityBytes          = 1 << 20
	sftpHardlinkExtension         = "hardlink@openssh.com"
	sftpFSyncExtension            = "fsync@openssh.com"
	sftpPrivatePartialPrefix      = ".dbterm-upload-"
	sftpPrivatePartialSuffix      = ".partial"
	sftpPrivatePartialAttempts    = 8
	sftpPrivatePartialRandomBytes = 16
)

// SFTPTransportMeasurement is a read-only endpoint test. It deliberately does
// not upload and remove a probe object: some SFTP servers cannot make that
// capability test race-free, and copy jobs must never depend on broad cleanup.
type SFTPTransportMeasurement struct {
	EndpointKind       CopyEndpointKind
	HostKeyFingerprint string
	ConnectDuration    time.Duration
	ListDuration       time.Duration
	Entries            int
	Readable           bool
	CreateOnlyPublish  bool
	StableStorageSync  bool
}

type sftpCopyEndpoint struct {
	address     string
	user        string
	root        string
	fingerprint string
}

type sftpCopyConnection struct {
	client    *sftp.Client
	sshClient *ssh.Client
	stopWatch chan struct{}
	closeOnce sync.Once
}

type sftpCopyCandidate struct {
	manifest     ArtifactManifest
	artifactName string
	artifactPath string
	manifestPath string
}

// MeasureSFTPTransport verifies the pinned host key, authenticates only with
// the endpoint's dedicated identity file, starts the SFTP subsystem, and lists
// the configured root. It performs no remote writes.
func MeasureSFTPTransport(ctx context.Context, endpoint CopyEndpoint) (SFTPTransportMeasurement, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	parsed, err := parseSFTPCopyEndpoint(endpoint)
	if err != nil {
		return SFTPTransportMeasurement{}, err
	}
	started := time.Now()
	connection, err := dialSFTPCopyEndpoint(ctx, endpoint)
	if err != nil {
		return SFTPTransportMeasurement{}, err
	}
	connectDuration := time.Since(started)
	defer connection.Close()
	if err := requireSFTPDirectory(connection.client, parsed.root); err != nil {
		return SFTPTransportMeasurement{}, fmt.Errorf("inspect SFTP root: %w", err)
	}
	listStarted := time.Now()
	entries, err := connection.client.ReadDirContext(ctx, parsed.root)
	if err != nil {
		return SFTPTransportMeasurement{}, preferSFTPContextError(ctx, fmt.Errorf("list SFTP root: %w", err))
	}
	return SFTPTransportMeasurement{
		EndpointKind: endpoint.Kind, HostKeyFingerprint: parsed.fingerprint,
		ConnectDuration: connectDuration, ListDuration: time.Since(listStarted),
		Entries: len(entries), Readable: true,
		CreateOnlyPublish: supportsSFTPExtension(connection.client, sftpHardlinkExtension),
		StableStorageSync: supportsSFTPExtension(connection.client, sftpFSyncExtension),
	}, nil
}

// runLocalToSFTP pushes every manifest-published local artifact that is not
// already present by immutable artifact identity. Upload bytes are written to
// cryptographically random private partial names. The hardlink@openssh.com
// extension then supplies the atomic create-only publication boundary; the
// completion manifest crosses that boundary only after artifact verification.
func (runner CopyRunner) runLocalToSFTP(ctx context.Context, job CopyJob, sourceRoot string, endpoint CopyEndpoint, filter CopyArtifactFilter) (CopyOutcome, error) {
	if err := ensureCopyDirectory(sourceRoot, false); err != nil {
		return CopyOutcome{}, fmt.Errorf("copy source: %w", err)
	}
	parsed, err := parseSFTPCopyEndpoint(endpoint)
	if err != nil {
		return CopyOutcome{}, err
	}
	connection, err := dialSFTPCopyEndpoint(ctx, endpoint)
	if err != nil {
		return CopyOutcome{}, err
	}
	defer connection.Close()
	if err := requireSFTPDirectory(connection.client, parsed.root); err != nil {
		return CopyOutcome{}, fmt.Errorf("copy destination: %w", err)
	}
	if err := requireSFTPCreateOnlyPublication(connection.client); err != nil {
		return CopyOutcome{}, err
	}
	if err := requireSFTPStableStorageSync(ctx, connection.client); err != nil {
		return CopyOutcome{}, err
	}

	candidates, err := scanLocalCopyCandidates(sourceRoot, filter)
	if err != nil {
		return CopyOutcome{}, err
	}
	outcome := newSFTPCopyOutcome(len(candidates), candidates)
	if len(candidates) == 0 {
		runner.report(ProgressEvent{Phase: "scan", Message: "no completed artifact manifests matched the copy job"})
		return outcome, nil
	}

	destinationCandidates, err := scanSFTPCopyCandidates(ctx, connection.client, parsed.root, CopyArtifactFilter{})
	if err != nil {
		return CopyOutcome{}, fmt.Errorf("scan SFTP copy destination: %w", err)
	}
	known, err := indexSFTPCandidates(destinationCandidates, "copy destination")
	if err != nil {
		return CopyOutcome{}, err
	}
	for index, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		if present, exists := known[candidate.manifest.ArtifactID]; exists {
			if err := requireSameCopyIdentity(candidate.manifest, present.manifest, "SFTP copy destination"); err != nil {
				return outcome, err
			}
			result, err := runner.reverifyAlreadyPresentSFTPCopy(ctx, connection.client, candidate.manifest, candidate.artifactPath, present, endpoint, job.Verification)
			if err != nil {
				return outcome, fmt.Errorf("verify already-present SFTP copy of artifact %q: %w", candidate.manifest.ArtifactID, err)
			}
			outcome.AlreadyPresent++
			outcome.Artifacts = append(outcome.Artifacts, result)
			runner.report(ProgressEvent{Phase: "scan", Message: fmt.Sprintf("already present by artifact identity: %s", candidate.artifactName), CurrentBytes: int64(index + 1), TotalBytes: int64(len(candidates))})
			continue
		}

		artifactPath, err := safeSFTPChild(parsed.root, candidate.artifactName)
		if err != nil {
			return outcome, err
		}
		manifestPath, err := safeSFTPChild(parsed.root, candidate.artifactName+ArtifactManifestSuffix)
		if err != nil {
			return outcome, err
		}
		if reconciled, recovered, reconcileWarnings, recoverErr := runner.reconcileSFTPArtifactOnly(ctx, connection.client, job, endpoint, candidate, artifactPath, manifestPath); recoverErr != nil {
			return outcome, recoverErr
		} else if recovered {
			outcome.Warnings = append(outcome.Warnings, reconcileWarnings...)
			outcome.Artifacts = append(outcome.Artifacts, reconciled)
			known[reconciled.ArtifactID] = sftpCopyCandidate{manifest: candidate.manifest, artifactName: candidate.artifactName, artifactPath: artifactPath, manifestPath: manifestPath}
			continue
		}
		if err := requireSFTPTargetsAbsent(connection.client, artifactPath, manifestPath); err != nil {
			return outcome, err
		}

		result, copyWarnings, copyErr := runner.pushSFTPArtifact(ctx, job, connection.client, endpoint, candidate, artifactPath, manifestPath)
		outcome.Warnings = append(outcome.Warnings, copyWarnings...)
		if strings.TrimSpace(result.ArtifactID) != "" {
			outcome.Artifacts = append(outcome.Artifacts, result)
			if result.PublicationState == ArtifactPublicationComplete {
				outcome.BytesCopied += result.SizeBytes
				known[result.ArtifactID] = sftpCopyCandidate{manifest: candidate.manifest, artifactName: candidate.artifactName, artifactPath: artifactPath, manifestPath: manifestPath}
			} else {
				outcome.Warnings = append(outcome.Warnings, fmt.Sprintf("remote orphan may remain at %s; no cleanup was attempted", artifactPath))
			}
		}
		if copyErr != nil {
			return outcome, copyErr
		}
	}
	return outcome, nil
}

// runSFTPToLocal pulls all missing, manifest-published remote artifacts. A
// remote artifact is copied into a private local stage and fully verified
// before publishVerifiedLocalCopy crosses the local immutable boundary.
func (runner CopyRunner) runSFTPToLocal(ctx context.Context, job CopyJob, endpoint CopyEndpoint, destinationRoot string, filter CopyArtifactFilter) (CopyOutcome, error) {
	if err := runner.ensureLocalCopyDestination(ctx, destinationRoot); err != nil {
		return CopyOutcome{}, fmt.Errorf("copy destination: %w", err)
	}
	parsed, err := parseSFTPCopyEndpoint(endpoint)
	if err != nil {
		return CopyOutcome{}, err
	}
	connection, err := dialSFTPCopyEndpoint(ctx, endpoint)
	if err != nil {
		return CopyOutcome{}, err
	}
	defer connection.Close()
	if err := requireSFTPDirectory(connection.client, parsed.root); err != nil {
		return CopyOutcome{}, fmt.Errorf("copy source: %w", err)
	}

	candidates, err := scanSFTPCopyCandidates(ctx, connection.client, parsed.root, filter)
	if err != nil {
		return CopyOutcome{}, err
	}
	outcome := newRemoteSFTPCopyOutcome(candidates)
	if len(candidates) == 0 {
		runner.report(ProgressEvent{Phase: "scan", Message: "no completed artifact manifests matched the copy job"})
		return outcome, nil
	}

	destinationCandidates, err := scanLocalCopyCandidates(destinationRoot, CopyArtifactFilter{})
	if err != nil {
		return CopyOutcome{}, fmt.Errorf("scan copy destination: %w", err)
	}
	known := make(map[string]localCopyCandidate, len(destinationCandidates))
	for _, candidate := range destinationCandidates {
		if previous, exists := known[candidate.manifest.ArtifactID]; exists {
			return CopyOutcome{}, fmt.Errorf("copy destination contains duplicate artifact ID %q at %s and %s", candidate.manifest.ArtifactID, previous.artifactPath, candidate.artifactPath)
		}
		known[candidate.manifest.ArtifactID] = candidate
	}

	for index, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		if present, exists := known[candidate.manifest.ArtifactID]; exists {
			if err := requireSameCopyIdentity(candidate.manifest, present.manifest, "copy destination"); err != nil {
				return outcome, err
			}
			result, err := runner.reverifyAlreadyPresentLocalCopy(ctx, candidate.manifest, sftpDisplayPath(endpoint, candidate.artifactName), present)
			if err != nil {
				return outcome, fmt.Errorf("verify already-present copy of artifact %q: %w", candidate.manifest.ArtifactID, err)
			}
			outcome.AlreadyPresent++
			outcome.Artifacts = append(outcome.Artifacts, result)
			runner.report(ProgressEvent{Phase: "scan", Message: fmt.Sprintf("already present by artifact identity: %s", candidate.artifactName), CurrentBytes: int64(index + 1), TotalBytes: int64(len(candidates))})
			continue
		}

		finalArtifact := filepath.Join(destinationRoot, candidate.artifactName)
		finalManifest := artifactManifestPath(finalArtifact)
		orphanCandidate := localCopyCandidate{
			manifest: candidate.manifest, artifactName: candidate.artifactName,
			artifactPath: sftpDisplayPath(endpoint, candidate.artifactName),
		}
		if reconciled, recovered, recoverErr := runner.reconcileLocalCopyOrphan(ctx, orphanCandidate, finalArtifact, finalManifest); recoverErr != nil {
			return outcome, recoverErr
		} else if recovered {
			outcome.Artifacts = append(outcome.Artifacts, reconciled)
			known[reconciled.ArtifactID] = localCopyCandidate{manifest: candidate.manifest, artifactName: candidate.artifactName, artifactPath: finalArtifact, manifestPath: finalManifest}
			continue
		}
		if err := requireCopyTargetAbsent(finalArtifact, finalManifest); err != nil {
			return outcome, err
		}
		if err := ensureCopyCapacity(destinationRoot, candidate.manifest.SizeBytes); err != nil {
			return outcome, err
		}
		result, copyErr := runner.pullSFTPArtifact(ctx, connection.client, endpoint, candidate, finalArtifact, finalManifest)
		if strings.TrimSpace(result.ArtifactID) != "" {
			outcome.Artifacts = append(outcome.Artifacts, result)
			if result.PublicationState == ArtifactPublicationComplete {
				outcome.BytesCopied += result.SizeBytes
				known[result.ArtifactID] = localCopyCandidate{manifest: candidate.manifest, artifactName: candidate.artifactName, artifactPath: finalArtifact, manifestPath: finalManifest}
			}
		}
		if copyErr != nil {
			return outcome, copyErr
		}
	}
	return outcome, nil
}

func (runner CopyRunner) reverifyAlreadyPresentSFTPCopy(
	ctx context.Context,
	client *sftp.Client,
	expected ArtifactManifest,
	sourceDisplay string,
	present sftpCopyCandidate,
	endpoint CopyEndpoint,
	strength CopyVerificationStrength,
) (CopyArtifactResult, error) {
	first, firstSize, firstDigest, err := readSFTPArtifactManifest(ctx, client, present.manifestPath)
	if err != nil {
		return CopyArtifactResult{}, fmt.Errorf("read completion manifest: %w", err)
	}
	if !sameArtifactManifestForOwnership(*first, expected) {
		return CopyArtifactResult{}, fmt.Errorf("destination completion manifest does not exactly match the producer manifest")
	}
	if err := verifySFTPArtifact(ctx, client, present.artifactPath, *first, strength); err != nil {
		return CopyArtifactResult{}, err
	}
	second, secondSize, secondDigest, err := readSFTPArtifactManifest(ctx, client, present.manifestPath)
	if err != nil || firstSize != secondSize || !strings.EqualFold(firstDigest, secondDigest) || !sameArtifactManifestForOwnership(*first, *second) {
		if err == nil {
			err = fmt.Errorf("completion manifest bytes changed")
		}
		return CopyArtifactResult{}, fmt.Errorf("destination completion manifest changed during verification: %w", err)
	}
	result := runner.copyResult(*first, sourceDisplay,
		sftpDisplayPath(endpoint, present.artifactName), sftpDisplayPath(endpoint, present.artifactName+ArtifactManifestSuffix))
	result.ManifestSize = secondSize
	result.ManifestSHA256 = secondDigest
	result.PublicationState = ArtifactPublicationComplete
	result.AlreadyPresent = true
	return result, nil
}

func (runner CopyRunner) reconcileSFTPArtifactOnly(ctx context.Context, client *sftp.Client, job CopyJob, endpoint CopyEndpoint, candidate localCopyCandidate, artifactPath, manifestPath string) (CopyArtifactResult, bool, []string, error) {
	artifactInfo, artifactErr := client.Lstat(artifactPath)
	_, manifestErr := client.Lstat(manifestPath)
	artifactExists := artifactErr == nil
	manifestExists := manifestErr == nil
	if artifactErr != nil && !isSFTPNotExist(artifactErr) {
		return CopyArtifactResult{}, false, nil, fmt.Errorf("inspect possible SFTP orphan artifact %s: %w", artifactPath, artifactErr)
	}
	if manifestErr != nil && !isSFTPNotExist(manifestErr) {
		return CopyArtifactResult{}, false, nil, fmt.Errorf("inspect possible SFTP orphan manifest %s: %w", manifestPath, manifestErr)
	}
	if !artifactExists && !manifestExists {
		return CopyArtifactResult{}, false, nil, nil
	}
	if manifestExists {
		return CopyArtifactResult{}, false, nil, fmt.Errorf("SFTP completion manifest already exists without a matching indexed artifact identity: %s", manifestPath)
	}
	if err := requireSFTPRegular(artifactInfo, artifactPath, candidate.manifest.SizeBytes); err != nil {
		return CopyArtifactResult{}, false, nil, fmt.Errorf("SFTP artifact-only collision was preserved for manual review: %w", err)
	}
	if err := verifySFTPArtifact(ctx, client, artifactPath, candidate.manifest, CopyVerificationSHA256Format); err != nil {
		return CopyArtifactResult{}, false, nil, fmt.Errorf("SFTP artifact-only collision does not match producer artifact %q; preserved for manual review: %w", candidate.manifest.ArtifactID, err)
	}
	result := runner.copyResult(candidate.manifest, candidate.artifactPath,
		sftpDisplayPath(endpoint, candidate.artifactName), sftpDisplayPath(endpoint, candidate.artifactName+ArtifactManifestSuffix))
	result.Reconciled = true
	result.PublicationState = ArtifactPublicationArtifactOnly
	manifestBytes, manifestSize, manifestSHA256, err := encodedCopyManifest(candidate.manifest)
	if err != nil {
		return result, false, nil, err
	}
	result.ManifestSize = manifestSize
	result.ManifestSHA256 = manifestSHA256
	partial, err := writeSFTPPrivatePartial(ctx, client, path.Dir(manifestPath), "manifest", bytes.NewReader(manifestBytes), manifestSize, manifestSHA256, nil)
	if err != nil {
		return result, false, nil, fmt.Errorf("stage reconciled SFTP completion manifest privately: %w", err)
	}
	warnings, err := publishSFTPPartialCreateOnly(ctx, client, partial, manifestPath)
	if err != nil {
		return result, false, warnings, fmt.Errorf("publish reconciled SFTP completion manifest atomically: %w", err)
	}
	published, actualSize, actualDigest, readErr := readSFTPArtifactManifest(ctx, client, manifestPath)
	if readErr != nil || actualSize != manifestSize || !strings.EqualFold(actualDigest, manifestSHA256) || !sameCopyManifest(*published, candidate.manifest) {
		result.PublicationState = ArtifactPublicationUncertain
		if readErr == nil {
			readErr = fmt.Errorf("published manifest bytes do not match the producer manifest")
		}
		return result, false, warnings, fmt.Errorf("reconciled SFTP manifest publication could not be confirmed: %w", readErr)
	}
	result.PublicationState = ArtifactPublicationComplete
	result.Verification = CopyVerificationSHA256Format
	runner.report(ProgressEvent{Phase: "copy", Message: fmt.Sprintf("reconciled verified artifact-only copy %s", candidate.artifactName), CurrentBytes: candidate.manifest.SizeBytes, TotalBytes: candidate.manifest.SizeBytes})
	return result, true, warnings, nil
}

func (runner CopyRunner) pushSFTPArtifact(ctx context.Context, job CopyJob, client *sftp.Client, endpoint CopyEndpoint, candidate localCopyCandidate, artifactPath, manifestPath string) (CopyArtifactResult, []string, error) {
	now := time.Now().UTC()
	if runner.Now != nil {
		now = runner.Now().UTC()
	}
	result := CopyArtifactResult{
		ArtifactID: candidate.manifest.ArtifactID, Source: candidate.artifactPath,
		Destination:     sftpDisplayPath(endpoint, candidate.artifactName),
		SourceCreatedAt: candidate.manifest.CreatedAt,
		ManifestPath:    sftpDisplayPath(endpoint, candidate.artifactName+ArtifactManifestSuffix),
		SizeBytes:       candidate.manifest.SizeBytes, SHA256: strings.ToLower(candidate.manifest.SHA256),
		Verification: job.Verification, VerifiedAt: now,
		PublicationState: ArtifactPublicationUncertain,
	}

	// Fail before the remote create-only boundary if the local published source
	// is already corrupt or has the wrong envelope.
	if err := verifyLocalCopyCandidate(ctx, candidate); err != nil {
		return CopyArtifactResult{}, nil, fmt.Errorf("verify copy source artifact %q: %w", candidate.manifest.ArtifactID, err)
	}
	initial, err := os.Lstat(candidate.artifactPath)
	if err != nil {
		return CopyArtifactResult{}, nil, fmt.Errorf("inspect copy source artifact: %w", err)
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() || initial.Size() != candidate.manifest.SizeBytes {
		return CopyArtifactResult{}, nil, fmt.Errorf("copy source artifact changed before upload: %s", candidate.artifactPath)
	}
	source, err := os.Open(candidate.artifactPath)
	if err != nil {
		return CopyArtifactResult{}, nil, fmt.Errorf("open copy source artifact: %w", err)
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(initial, opened) {
		return CopyArtifactResult{}, nil, fmt.Errorf("copy source artifact changed while it was being opened: %s", candidate.artifactPath)
	}

	artifactPartial, err := writeSFTPPrivatePartial(ctx, client, path.Dir(artifactPath), "artifact", source, candidate.manifest.SizeBytes, candidate.manifest.SHA256, runner.Progress)
	if err != nil {
		return CopyArtifactResult{}, nil, fmt.Errorf("upload artifact to private SFTP partial: %w", err)
	}
	if err := verifyOpenedFileUnchanged(source, initial, candidate.artifactPath); err != nil {
		return CopyArtifactResult{}, nil, failAndCleanSFTPPartial(ctx, client, artifactPartial, err)
	}
	if err := verifySFTPArtifact(ctx, client, artifactPartial.path, candidate.manifest, job.Verification); err != nil {
		return CopyArtifactResult{}, nil, failAndCleanSFTPPartial(ctx, client, artifactPartial, fmt.Errorf("verify private SFTP artifact partial: %w", err))
	}
	warnings, err := publishSFTPPartialCreateOnly(ctx, client, artifactPartial, artifactPath)
	if err != nil {
		return CopyArtifactResult{}, warnings, fmt.Errorf("publish SFTP artifact atomically without replacement: %w", err)
	}
	result.PublicationState = ArtifactPublicationArtifactOnly
	if err := verifySFTPArtifact(ctx, client, artifactPath, candidate.manifest, job.Verification); err != nil {
		result.PublicationState = ArtifactPublicationUncertain
		return result, warnings, fmt.Errorf("published SFTP artifact at %s could not be reconfirmed; completion manifest was not published: %w", artifactPath, err)
	}

	manifestBytes, manifestSize, manifestSHA256, err := encodedCopyManifest(candidate.manifest)
	if err != nil {
		return result, warnings, fmt.Errorf("encode copy completion manifest: %w", err)
	}
	result.ManifestSize = manifestSize
	result.ManifestSHA256 = manifestSHA256
	manifestPartial, err := writeSFTPPrivatePartial(ctx, client, path.Dir(manifestPath), "manifest", bytes.NewReader(manifestBytes), manifestSize, manifestSHA256, nil)
	if err != nil {
		return result, warnings, fmt.Errorf("stage SFTP completion manifest privately; verified artifact remains unpublished by manifest at %s: %w", artifactPath, err)
	}
	manifestWarnings, err := publishSFTPPartialCreateOnly(ctx, client, manifestPartial, manifestPath)
	warnings = append(warnings, manifestWarnings...)
	if err != nil {
		return result, warnings, fmt.Errorf("publish SFTP completion manifest atomically; verified artifact remains at %s without a completion signal: %w", artifactPath, err)
	}

	published, actualSize, actualDigest, err := readSFTPArtifactManifest(ctx, client, manifestPath)
	if err != nil || actualSize != manifestSize || !strings.EqualFold(actualDigest, manifestSHA256) || !sameCopyManifest(*published, candidate.manifest) {
		result.PublicationState = ArtifactPublicationUncertain
		if err == nil {
			err = fmt.Errorf("remote completion manifest did not match the bytes that were published")
		}
		return result, warnings, preferSFTPContextError(ctx, fmt.Errorf("completion manifest publication at %s could not be confirmed; the remote artifact and manifest were not deleted: %w", manifestPath, err))
	}
	result.PublicationState = ArtifactPublicationComplete
	runner.report(ProgressEvent{Phase: "copy", Message: fmt.Sprintf("copied and verified %s", candidate.artifactName), CurrentBytes: candidate.manifest.SizeBytes, TotalBytes: candidate.manifest.SizeBytes})
	return result, warnings, nil
}

type sftpPrivatePartial struct {
	root     string
	path     string
	size     int64
	digest   string
	complete bool
}

func supportsSFTPExtension(client *sftp.Client, name string) bool {
	if client == nil {
		return false
	}
	version, ok := client.HasExtension(name)
	return ok && version == "1"
}

func requireSFTPCreateOnlyPublication(client *sftp.Client) error {
	if supportsSFTPExtension(client, sftpHardlinkExtension) {
		return nil
	}
	return fmt.Errorf("SFTP server does not advertise %s version 1; dbterm refuses push/retention because baseline SFTP rename cannot guarantee atomic create-only publication on every server", sftpHardlinkExtension)
}

type sftpFileSyncFunc func(*sftp.File) error
type sftpFileSyncContextKey struct{}

func sftpFileSyncForContext(ctx context.Context, client *sftp.Client) (sftpFileSyncFunc, bool) {
	// The context override is deliberately package-private. It lets the in-memory
	// protocol test server (which cannot implement fsync packets) exercise the
	// rest of the publication state machine without exposing an unsafe runtime
	// option to callers.
	if ctx != nil {
		if syncFile, ok := ctx.Value(sftpFileSyncContextKey{}).(sftpFileSyncFunc); ok && syncFile != nil {
			return syncFile, true
		}
	}
	if !supportsSFTPExtension(client, sftpFSyncExtension) {
		return nil, false
	}
	return func(file *sftp.File) error { return file.Sync() }, true
}

func requireSFTPStableStorageSync(ctx context.Context, client *sftp.Client) error {
	if _, ok := sftpFileSyncForContext(ctx, client); ok {
		return nil
	}
	return fmt.Errorf("SFTP server does not advertise %s version 1; dbterm refuses push because it cannot prove private artifact and manifest bytes reached stable storage before publication", sftpFSyncExtension)
}

func newSFTPPrivatePartialPath(root, purpose string) (string, error) {
	if purpose != "artifact" && purpose != "manifest" {
		return "", fmt.Errorf("unsupported SFTP partial purpose %q", purpose)
	}
	random := make([]byte, sftpPrivatePartialRandomBytes)
	if _, err := io.ReadFull(cryptorand.Reader, random); err != nil {
		return "", fmt.Errorf("generate private SFTP partial name: %w", err)
	}
	return safeSFTPChild(root, sftpPrivatePartialPrefix+purpose+"-"+hex.EncodeToString(random)+sftpPrivatePartialSuffix)
}

func writeSFTPPrivatePartial(
	ctx context.Context,
	client *sftp.Client,
	root string,
	purpose string,
	reader io.Reader,
	expectedSize int64,
	expectedDigest string,
	progress ProgressFunc,
) (partial *sftpPrivatePartial, returnErr error) {
	if client == nil || reader == nil {
		return nil, fmt.Errorf("SFTP private partial writer requires a client and source")
	}
	if expectedSize <= 0 || len(expectedDigest) != sha256.Size*2 {
		return nil, fmt.Errorf("SFTP private partial requires a positive size and SHA-256")
	}
	for attempt := 0; attempt < sftpPrivatePartialAttempts; attempt++ {
		partialPath, err := newSFTPPrivatePartialPath(root, purpose)
		if err != nil {
			return nil, err
		}
		file, err := client.OpenFile(partialPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			if isSFTPExist(err) {
				continue
			}
			return nil, preferSFTPContextError(ctx, fmt.Errorf("create private SFTP partial: %w", err))
		}
		partial = &sftpPrivatePartial{root: path.Clean(root), path: partialPath, size: expectedSize, digest: strings.ToLower(expectedDigest)}
		owned := partial
		closed := false
		defer func() {
			if !closed {
				_ = file.Close()
			}
			if returnErr != nil && owned.path != "" {
				partialPath := owned.path
				if cleanupErr := owned.remove(ctx, client); cleanupErr != nil {
					returnErr = fmt.Errorf("%w; private SFTP partial was preserved at %s because cleanup could not be proven safe: %v", returnErr, partialPath, cleanupErr)
				}
			}
		}()
		if err := file.Chmod(0o600); err != nil {
			return nil, preferSFTPContextError(ctx, fmt.Errorf("protect private SFTP partial: %w", err))
		}

		hash := sha256.New()
		writer := &publicationProgressWriter{
			writer: io.MultiWriter(file, hash), total: expectedSize,
			message: "uploading verified artifact to private SFTP partial", progress: progress,
		}
		written, err := io.CopyBuffer(writer, &contextReader{ctx: ctx, reader: reader}, make([]byte, 256*1024))
		if err == nil {
			writer.finish()
			err = ctx.Err()
		}
		if err == nil && written != expectedSize {
			err = fmt.Errorf("uploaded %d bytes; manifest records %d", written, expectedSize)
		}
		if err == nil && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedDigest) {
			err = fmt.Errorf("source SHA-256 changed during private SFTP upload")
		}
		if err == nil {
			partial.complete = true
		}
		if err == nil {
			syncFile, ok := sftpFileSyncForContext(ctx, client)
			if !ok {
				err = fmt.Errorf("SFTP server lost required %s capability", sftpFSyncExtension)
			} else {
				err = syncFile(file)
			}
			if err != nil {
				err = fmt.Errorf("sync private SFTP partial to stable storage: %w", err)
			}
		}
		closeErr := file.Close()
		closed = true
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, preferSFTPContextError(ctx, err)
		}
		if err := verifySFTPPathDigest(ctx, client, partialPath, expectedSize, expectedDigest); err != nil {
			return nil, fmt.Errorf("verify private SFTP partial: %w", err)
		}
		return partial, nil
	}
	return nil, fmt.Errorf("could not allocate a collision-free private SFTP partial after %d attempts", sftpPrivatePartialAttempts)
}

func (partial *sftpPrivatePartial) remove(ctx context.Context, client *sftp.Client) error {
	if partial == nil || partial.path == "" {
		return nil
	}
	base := path.Base(partial.path)
	if path.Dir(partial.path) != path.Clean(partial.root) || !strings.HasPrefix(base, sftpPrivatePartialPrefix) || !strings.HasSuffix(base, sftpPrivatePartialSuffix) {
		return fmt.Errorf("refuse cleanup outside dbterm's private SFTP partial namespace: %s", partial.path)
	}
	info, err := client.Lstat(partial.path)
	if err != nil {
		if isSFTPNotExist(err) {
			partial.path = ""
			return nil
		}
		return preferSFTPContextError(ctx, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("private SFTP partial changed type before cleanup: %s", partial.path)
	}
	if partial.complete {
		if err := verifySFTPPathDigest(ctx, client, partial.path, partial.size, partial.digest); err != nil {
			return err
		}
	}
	if err := client.Remove(partial.path); err != nil {
		if isSFTPNotExist(err) {
			partial.path = ""
			return nil
		}
		return preferSFTPContextError(ctx, err)
	}
	partial.path = ""
	return nil
}

func failAndCleanSFTPPartial(ctx context.Context, client *sftp.Client, partial *sftpPrivatePartial, cause error) error {
	if partial == nil {
		return cause
	}
	partialPath := partial.path
	if err := partial.remove(ctx, client); err != nil {
		return fmt.Errorf("%w; private SFTP partial was preserved at %s because cleanup could not be proven safe: %v", cause, partialPath, err)
	}
	return cause
}

// publishSFTPPartialCreateOnly uses an OpenSSH hard link rather than rename.
// Hard-link creation is atomic and fails if finalPath already exists, while
// both names are in the same configured directory/filesystem. The private name
// is removed only after the final path has been re-read and verified.
func publishSFTPPartialCreateOnly(ctx context.Context, client *sftp.Client, partial *sftpPrivatePartial, finalPath string) ([]string, error) {
	if partial == nil || partial.path == "" {
		return nil, fmt.Errorf("private SFTP partial is unavailable")
	}
	if err := requireSFTPCreateOnlyPublication(client); err != nil {
		return nil, failAndCleanSFTPPartial(ctx, client, partial, err)
	}
	if path.Dir(finalPath) != path.Clean(partial.root) {
		return nil, failAndCleanSFTPPartial(ctx, client, partial, fmt.Errorf("SFTP final path must share the private partial directory"))
	}
	if err := client.Link(partial.path, finalPath); err != nil {
		if isSFTPExist(err) {
			err = fmt.Errorf("create-only SFTP publication refused existing destination %s: %w", finalPath, err)
		} else {
			err = preferSFTPContextError(ctx, fmt.Errorf("atomic create-only SFTP publication failed for %s: %w", finalPath, err))
		}
		return nil, failAndCleanSFTPPartial(ctx, client, partial, err)
	}
	if err := verifySFTPPathDigest(ctx, client, finalPath, partial.size, partial.digest); err != nil {
		return nil, fmt.Errorf("atomic SFTP publication created %s, but its bytes could not be confirmed; private partial remains for manual recovery: %w", finalPath, err)
	}
	partialPath := partial.path
	if err := partial.remove(ctx, client); err != nil {
		return []string{fmt.Sprintf("published %s safely, but its private upload link remains at %s: %v", finalPath, partialPath, err)}, nil
	}
	return nil, nil
}

func (runner CopyRunner) pullSFTPArtifact(ctx context.Context, client *sftp.Client, endpoint CopyEndpoint, candidate sftpCopyCandidate, finalArtifact, finalManifest string) (CopyArtifactResult, error) {
	initial, err := client.Lstat(candidate.artifactPath)
	if err != nil {
		return CopyArtifactResult{}, fmt.Errorf("inspect SFTP source artifact: %w", err)
	}
	if err := requireSFTPRegular(initial, candidate.artifactPath, candidate.manifest.SizeBytes); err != nil {
		return CopyArtifactResult{}, err
	}
	source, err := client.Open(candidate.artifactPath)
	if err != nil {
		return CopyArtifactResult{}, preferSFTPContextError(ctx, fmt.Errorf("open SFTP source artifact: %w", err))
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !sameSFTPFileState(initial, opened) {
		return CopyArtifactResult{}, fmt.Errorf("SFTP source artifact changed while it was being opened: %s", candidate.artifactPath)
	}

	stage, err := privatefile.CreateTemp(filepath.Dir(finalArtifact), ".dbterm-publish-", ".partial")
	if err != nil {
		return CopyArtifactResult{}, fmt.Errorf("create private copy staging file: %w", err)
	}
	stagePath := stage.Name()
	stageInfo, err := stage.Stat()
	if err != nil {
		_ = stage.Close()
		_ = os.Remove(stagePath)
		return CopyArtifactResult{}, fmt.Errorf("capture private copy staging file: %w", err)
	}
	stageClosed := false
	defer func() {
		if !stageClosed {
			_ = stage.Close()
		}
		removeExactOwnedLocalPartial(stagePath, stageInfo)
	}()
	if err := runner.guardLocalPublication(ctx, stagePath, CopyLocalStageCreated); err != nil {
		return CopyArtifactResult{}, err
	}
	if err := stage.Chmod(0o600); err != nil {
		return CopyArtifactResult{}, fmt.Errorf("protect private copy staging file: %w", err)
	}

	hash := sha256.New()
	reporter := &publicationProgressWriter{writer: io.MultiWriter(stage, hash), total: initial.Size(), message: "downloading verified artifact", progress: runner.Progress}
	written, err := io.CopyBuffer(reporter, &contextReader{ctx: ctx, reader: source}, make([]byte, 256*1024))
	if err != nil {
		return CopyArtifactResult{}, preferSFTPContextError(ctx, fmt.Errorf("download artifact %s: %w", candidate.artifactName, err))
	}
	reporter.finish()
	if err := ctx.Err(); err != nil {
		return CopyArtifactResult{}, err
	}
	if written != candidate.manifest.SizeBytes {
		return CopyArtifactResult{}, fmt.Errorf("download artifact %s wrote %d bytes; manifest records %d", candidate.artifactName, written, candidate.manifest.SizeBytes)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), candidate.manifest.SHA256) {
		return CopyArtifactResult{}, fmt.Errorf("download artifact %s failed SHA-256 verification", candidate.artifactName)
	}
	if err := verifySFTPHandleAndPathUnchanged(client, source, candidate.artifactPath, initial); err != nil {
		return CopyArtifactResult{}, err
	}
	if err := stage.Sync(); err != nil {
		return CopyArtifactResult{}, fmt.Errorf("sync downloaded artifact staging file: %w", err)
	}
	if err := stage.Close(); err != nil {
		return CopyArtifactResult{}, fmt.Errorf("close downloaded artifact staging file: %w", err)
	}
	stageClosed = true
	if err := verifyCopiedArtifactEnvelopeContext(ctx, stagePath, candidate.manifest); err != nil {
		return CopyArtifactResult{}, err
	}
	return runner.publishVerifiedLocalCopy(ctx, candidate.manifest, sftpDisplayPath(endpoint, candidate.artifactName), candidate.artifactName, stagePath, finalArtifact, finalManifest)
}

func dialSFTPCopyEndpoint(ctx context.Context, endpoint CopyEndpoint) (*sftpCopyConnection, error) {
	parsed, err := parseSFTPCopyEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	signer, err := loadSFTPIdentity(endpoint.CredentialRef)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User: parsed.user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: pinnedSFTPHostKey(parsed.fingerprint),
		ClientVersion:   "SSH-2.0-dbterm",
	}
	raw, err := (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", parsed.address)
	if err != nil {
		return nil, preferSFTPContextError(ctx, fmt.Errorf("connect to pinned SSH/SFTP endpoint %s: %w", parsed.address, err))
	}
	handshakeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = raw.Close()
		case <-handshakeDone:
		}
	}()
	clientConn, channels, requests, err := ssh.NewClientConn(raw, parsed.address, config)
	close(handshakeDone)
	if err != nil {
		_ = raw.Close()
		return nil, preferSFTPContextError(ctx, fmt.Errorf("establish pinned SSH connection to %s: %w", parsed.address, err))
	}
	sshClient := ssh.NewClient(clientConn, channels, requests)
	stopWatch := make(chan struct{})
	connection := &sftpCopyConnection{sshClient: sshClient, stopWatch: stopWatch}
	go func() {
		select {
		case <-ctx.Done():
			_ = sshClient.Close()
		case <-stopWatch:
		}
	}()
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		connection.Close()
		return nil, preferSFTPContextError(ctx, fmt.Errorf("start SFTP subsystem at %s: %w", parsed.address, err))
	}
	connection.client = sftpClient
	return connection, nil
}

func (connection *sftpCopyConnection) Close() error {
	if connection == nil {
		return nil
	}
	var closeErr error
	connection.closeOnce.Do(func() {
		close(connection.stopWatch)
		if connection.client != nil {
			closeErr = connection.client.Close()
		}
		if connection.sshClient != nil {
			if err := connection.sshClient.Close(); closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

func parseSFTPCopyEndpoint(endpoint CopyEndpoint) (sftpCopyEndpoint, error) {
	if endpoint.Kind != CopyEndpointSFTP && endpoint.Kind != CopyEndpointSSH {
		return sftpCopyEndpoint{}, fmt.Errorf("SFTP transport requires an ssh or sftp endpoint, not %q", endpoint.Kind)
	}
	parsed, err := url.Parse(endpoint.Location)
	if err != nil || parsed.Scheme != string(endpoint.Kind) || parsed.Hostname() == "" || !strings.HasPrefix(parsed.Path, "/") {
		return sftpCopyEndpoint{}, fmt.Errorf("%s location must use %s://user@host/absolute/path", endpoint.Kind, endpoint.Kind)
	}
	if parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return sftpCopyEndpoint{}, fmt.Errorf("SSH/SFTP endpoint user is required")
	}
	if hasUnsafeCopyText(parsed.User.Username()) {
		return sftpCopyEndpoint{}, fmt.Errorf("SSH/SFTP endpoint user contains an unsupported control character")
	}
	if _, present := parsed.User.Password(); present {
		return sftpCopyEndpoint{}, fmt.Errorf("SSH/SFTP password authentication is not supported; use a dedicated private identity file")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return sftpCopyEndpoint{}, fmt.Errorf("SSH/SFTP location cannot contain a query or fragment")
	}
	fingerprint, err := canonicalSFTPFingerprint(endpoint.PinnedHostKey)
	if err != nil {
		return sftpCopyEndpoint{}, err
	}
	if strings.TrimSpace(endpoint.CredentialRef) == "" {
		return sftpCopyEndpoint{}, fmt.Errorf("SSH/SFTP endpoint requires a dedicated private identity file in credential_ref")
	}
	remoteRoot, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || !strings.HasPrefix(remoteRoot, "/") || hasUnsafeCopyText(remoteRoot) || copyPathTraverses(remoteRoot) {
		return sftpCopyEndpoint{}, fmt.Errorf("SSH/SFTP path must be absolute and contain no parent traversal")
	}
	remoteRoot = path.Clean(remoteRoot)
	if remoteRoot == "/" || remoteRoot == "." {
		return sftpCopyEndpoint{}, fmt.Errorf("SSH/SFTP path must identify a directory below the remote root")
	}
	port := parsed.Port()
	if port == "" {
		port = "22"
	}
	return sftpCopyEndpoint{
		address: net.JoinHostPort(parsed.Hostname(), port), user: parsed.User.Username(),
		root: remoteRoot, fingerprint: fingerprint,
	}, nil
}

func canonicalSFTPFingerprint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "SHA256:") {
		return "", fmt.Errorf("SSH/SFTP pinned host key must be an SHA256 fingerprint in SHA256:<base64> form")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "SHA256:"))
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("SSH/SFTP pinned host key must be an SHA256 fingerprint in SHA256:<base64> form")
	}
	canonical := "SHA256:" + base64.RawStdEncoding.EncodeToString(raw)
	if subtle.ConstantTimeCompare([]byte(value), []byte(canonical)) != 1 {
		return "", fmt.Errorf("SSH/SFTP pinned host key must use canonical unpadded SHA256:<base64> form")
	}
	return canonical, nil
}

func pinnedSFTPHostKey(expected string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if len(actual) != len(expected) || subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
			return fmt.Errorf("SSH/SFTP host key mismatch: expected %s, received %s", expected, actual)
		}
		return nil
	}
}

func loadSFTPIdentity(reference string) (ssh.Signer, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, fmt.Errorf("SSH/SFTP credential reference must name a dedicated private identity file")
	}
	if !filepath.IsAbs(reference) {
		return nil, fmt.Errorf("SSH/SFTP credential reference must be an absolute path to a dedicated private identity file")
	}
	identityPath := filepath.Clean(reference)
	initial, err := os.Lstat(identityPath)
	if err != nil {
		return nil, fmt.Errorf("inspect SSH private identity file: %w", err)
	}
	if initial.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("SSH private identity file must not be a symbolic link: %s", identityPath)
	}
	if !initial.Mode().IsRegular() {
		return nil, fmt.Errorf("SSH private identity must be a regular file: %s", identityPath)
	}
	if initial.Size() <= 0 || initial.Size() > maxSFTPIdentityBytes {
		return nil, fmt.Errorf("SSH private identity file size must be between 1 and %d bytes: %s", maxSFTPIdentityBytes, identityPath)
	}
	if runtime.GOOS != "windows" && initial.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("SSH private identity file permissions are too broad; use mode 0600: %s", identityPath)
	}
	file, err := os.Open(identityPath)
	if err != nil {
		return nil, fmt.Errorf("open SSH private identity file: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(initial, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("SSH private identity file changed while it was being opened: %s", identityPath)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSFTPIdentityBytes+1))
	defer clear(data)
	after, statErr := file.Stat()
	closeErr := file.Close()
	current, pathErr := os.Lstat(identityPath)
	if readErr != nil {
		return nil, fmt.Errorf("read SSH private identity file: %w", readErr)
	}
	if len(data) > maxSFTPIdentityBytes {
		return nil, fmt.Errorf("SSH private identity file exceeds %d bytes: %s", maxSFTPIdentityBytes, identityPath)
	}
	if statErr != nil || pathErr != nil || !sameLocalFileState(opened, after) || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) || !sameLocalFileState(opened, current) {
		return nil, fmt.Errorf("SSH private identity file changed while it was being read: %s", identityPath)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close SSH private identity file: %w", closeErr)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		var passphraseMissing *ssh.PassphraseMissingError
		parseMessage := strings.ToLower(err.Error())
		if errors.As(err, &passphraseMissing) || strings.Contains(parseMessage, "passphrase") || strings.Contains(parseMessage, "encrypted") {
			return nil, fmt.Errorf("encrypted SSH private identities are not supported; use a dedicated unencrypted identity file protected by OS permissions")
		}
		return nil, fmt.Errorf("parse SSH private identity file: %w", err)
	}
	return signer, nil
}

func scanSFTPCopyCandidates(ctx context.Context, client *sftp.Client, root string, filter CopyArtifactFilter) ([]sftpCopyCandidate, error) {
	entries, err := client.ReadDirContext(ctx, root)
	if err != nil {
		return nil, preferSFTPContextError(ctx, fmt.Errorf("scan completed SFTP backup manifests in %s: %w", root, err))
	}
	seenIDs := make(map[string]string)
	candidates := make([]sftpCopyCandidate, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ArtifactManifestSuffix) {
			continue
		}
		if path.Base(name) != name || strings.ContainsAny(name, `/\\`) {
			return nil, fmt.Errorf("SFTP directory returned an unsafe manifest name %q", name)
		}
		manifestPath, err := safeSFTPChild(root, name)
		if err != nil {
			return nil, err
		}
		info, err := client.Lstat(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("inspect SFTP completion manifest %s: %w", manifestPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("SFTP completion manifest must be a regular file, not a symlink: %s", manifestPath)
		}
		manifest, _, _, err := readSFTPArtifactManifest(ctx, client, manifestPath)
		if err != nil {
			return nil, fmt.Errorf("read SFTP completion manifest %s: %w", manifestPath, err)
		}
		if !copyManifestMatches(*manifest, filter) {
			continue
		}
		artifactName := strings.TrimSuffix(name, ArtifactManifestSuffix)
		if err := validateExactArtifactFilename(artifactName); err != nil {
			return nil, fmt.Errorf("SFTP copy manifest %s identifies an unsafe artifact filename: %w", manifestPath, err)
		}
		artifactPath, err := safeSFTPChild(root, artifactName)
		if err != nil {
			return nil, err
		}
		artifactInfo, err := client.Lstat(artifactPath)
		if err != nil {
			return nil, fmt.Errorf("SFTP completion manifest %s has no readable artifact: %w", manifestPath, err)
		}
		if err := requireSFTPRegular(artifactInfo, artifactPath, manifest.SizeBytes); err != nil {
			return nil, err
		}
		if previous, exists := seenIDs[manifest.ArtifactID]; exists {
			return nil, fmt.Errorf("SFTP copy source contains duplicate artifact ID %q at %s and %s", manifest.ArtifactID, previous, artifactPath)
		}
		seenIDs[manifest.ArtifactID] = artifactPath
		candidates = append(candidates, sftpCopyCandidate{manifest: *manifest, artifactName: artifactName, artifactPath: artifactPath, manifestPath: manifestPath})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].manifest.CreatedAt.Equal(candidates[j].manifest.CreatedAt) {
			return candidates[i].manifest.ArtifactID < candidates[j].manifest.ArtifactID
		}
		return candidates[i].manifest.CreatedAt.Before(candidates[j].manifest.CreatedAt)
	})
	return candidates, nil
}

func readSFTPArtifactManifest(ctx context.Context, client *sftp.Client, manifestPath string) (*ArtifactManifest, int64, string, error) {
	initial, err := client.Lstat(manifestPath)
	if err != nil {
		return nil, 0, "", err
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() {
		return nil, 0, "", fmt.Errorf("SFTP artifact manifest must be a regular file, not a symlink: %s", manifestPath)
	}
	if initial.Size() <= 0 || initial.Size() > maxArtifactManifestBytes {
		return nil, 0, "", fmt.Errorf("SFTP artifact manifest size must be between 1 and %d bytes: %s", maxArtifactManifestBytes, manifestPath)
	}
	file, err := client.Open(manifestPath)
	if err != nil {
		return nil, 0, "", preferSFTPContextError(ctx, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameSFTPFileState(initial, opened) {
		return nil, 0, "", fmt.Errorf("SFTP artifact manifest changed while it was being opened: %s", manifestPath)
	}
	data, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, maxArtifactManifestBytes+1))
	if err != nil {
		return nil, 0, "", preferSFTPContextError(ctx, err)
	}
	if len(data) > maxArtifactManifestBytes {
		return nil, 0, "", fmt.Errorf("SFTP artifact manifest exceeds %d bytes: %s", maxArtifactManifestBytes, manifestPath)
	}
	if err := verifySFTPHandleAndPathUnchanged(client, file, manifestPath, initial); err != nil {
		return nil, 0, "", err
	}
	manifest, err := DecodeArtifactManifest(bytes.NewReader(data))
	if err != nil {
		return nil, 0, "", err
	}
	digest := sha256.Sum256(data)
	return manifest, int64(len(data)), hex.EncodeToString(digest[:]), nil
}

func verifySFTPArtifact(ctx context.Context, client *sftp.Client, artifactPath string, manifest ArtifactManifest, strength CopyVerificationStrength) error {
	initial, err := client.Lstat(artifactPath)
	if err != nil {
		return err
	}
	if err := requireSFTPRegular(initial, artifactPath, manifest.SizeBytes); err != nil {
		return err
	}
	rank, ok := verifyCopyStrengthRank(strength)
	if !ok {
		return fmt.Errorf("unsupported SFTP verification strength %q", strength)
	}
	if rank == 1 {
		return nil
	}
	file, err := client.Open(artifactPath)
	if err != nil {
		return preferSFTPContextError(ctx, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameSFTPFileState(initial, opened) {
		return fmt.Errorf("SFTP artifact changed while it was being opened: %s", artifactPath)
	}
	hash := sha256.New()
	prefix := &boundedPrefixWriter{remaining: payloadPeekBytes}
	written, err := io.CopyBuffer(io.MultiWriter(hash, prefix), &contextReader{ctx: ctx, reader: file}, make([]byte, 256*1024))
	if err != nil {
		return preferSFTPContextError(ctx, err)
	}
	if written != manifest.SizeBytes {
		return fmt.Errorf("SFTP artifact size changed while it was being verified: got %d, expected %d", written, manifest.SizeBytes)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), manifest.SHA256) {
		return fmt.Errorf("SFTP artifact SHA-256 does not match its completion manifest")
	}
	if err := verifySFTPHandleAndPathUnchanged(client, file, artifactPath, initial); err != nil {
		return err
	}
	if rank >= 3 {
		if !manifest.Encrypted && manifest.Compression == CompressionNone && manifest.Format == string(FormatDBTermBundle) {
			if err := VerifyDBTermBundleEnvelopeContext(ctx, file, manifest.SizeBytes); err != nil {
				return fmt.Errorf("SFTP artifact is not the dbterm bundle recorded by its manifest: %w", err)
			}
			return nil
		}
		return verifyCopyEnvelopePrefix(prefix.data, manifest)
	}
	return nil
}

func verifyCopyEnvelopePrefix(prefix []byte, manifest ArtifactManifest) error {
	if manifest.Encrypted {
		if !bytes.HasPrefix(prefix, []byte("age-encryption.org/v1\n")) {
			return fmt.Errorf("copied artifact does not contain the age v1 envelope recorded by its manifest")
		}
		return nil
	}
	switch manifest.Compression {
	case CompressionGzip:
		if len(prefix) < 3 || prefix[0] != 0x1f || prefix[1] != 0x8b || prefix[2] != 0x08 {
			return fmt.Errorf("copied artifact does not contain the gzip envelope recorded by its manifest")
		}
	case CompressionZip:
		if !isZipPrefix(prefix) {
			return fmt.Errorf("copied artifact does not contain the ZIP envelope recorded by its manifest")
		}
	case CompressionZstd:
		if !isZstdPrefix(prefix) {
			return fmt.Errorf("copied artifact does not contain the zstd envelope recorded by its manifest")
		}
	case CompressionNone:
		return verifyUnwrappedCopyFormat(prefix, manifest)
	default:
		return fmt.Errorf("copied artifact manifest uses unsupported compression %q", manifest.Compression)
	}
	return nil
}

func encodedCopyManifest(manifest ArtifactManifest) ([]byte, int64, string, error) {
	var encoded bytes.Buffer
	if err := EncodeArtifactManifest(&encoded, manifest); err != nil {
		return nil, 0, "", err
	}
	if encoded.Len() > maxArtifactManifestBytes {
		return nil, 0, "", fmt.Errorf("artifact manifest exceeds %d bytes", maxArtifactManifestBytes)
	}
	digest := sha256.Sum256(encoded.Bytes())
	data := append([]byte(nil), encoded.Bytes()...)
	return data, int64(len(data)), hex.EncodeToString(digest[:]), nil
}

func requireSFTPDirectory(client *sftp.Client, root string) error {
	info, err := client.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("SFTP copy path must be a real directory, not a symlink: %s", root)
	}
	return nil
}

func requireSFTPRegular(info os.FileInfo, filePath string, expectedSize int64) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("SFTP copy artifact must be a regular file, not a symlink: %s", filePath)
	}
	if info.Size() != expectedSize {
		return fmt.Errorf("SFTP copy artifact %s has size %d; completion manifest records %d", filePath, info.Size(), expectedSize)
	}
	return nil
}

func requireSFTPTargetsAbsent(client *sftp.Client, artifactPath, manifestPath string) error {
	for label, target := range map[string]string{"artifact": artifactPath, "completion manifest": manifestPath} {
		if _, err := client.Lstat(target); err == nil {
			return fmt.Errorf("copy destination %s already exists without a matching recorded artifact identity: %s", label, target)
		} else if !isSFTPNotExist(err) {
			return fmt.Errorf("inspect SFTP copy destination %s %s: %w", label, target, err)
		}
	}
	return nil
}

func requireSameCopyIdentity(source, destination ArtifactManifest, label string) error {
	if destination.SizeBytes != source.SizeBytes || !strings.EqualFold(destination.SHA256, source.SHA256) {
		return fmt.Errorf("%s artifact ID %q conflicts with the producer checksum or size", label, source.ArtifactID)
	}
	return nil
}

func indexSFTPCandidates(candidates []sftpCopyCandidate, label string) (map[string]sftpCopyCandidate, error) {
	known := make(map[string]sftpCopyCandidate, len(candidates))
	for _, candidate := range candidates {
		if previous, exists := known[candidate.manifest.ArtifactID]; exists {
			return nil, fmt.Errorf("%s contains duplicate artifact ID %q at %s and %s", label, candidate.manifest.ArtifactID, previous.artifactPath, candidate.artifactPath)
		}
		known[candidate.manifest.ArtifactID] = candidate
	}
	return known, nil
}

func safeSFTPChild(root, name string) (string, error) {
	if name == "" || name == "." || name == ".." || path.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("unsafe SFTP child name %q", name)
	}
	root = path.Clean(root)
	child := path.Join(root, name)
	if root == "/" || !strings.HasPrefix(child, root+"/") {
		return "", fmt.Errorf("SFTP child path escaped configured root: %q", name)
	}
	return child, nil
}

func verifySFTPHandleAndPathUnchanged(client *sftp.Client, file *sftp.File, filePath string, expected os.FileInfo) error {
	after, handleErr := file.Stat()
	current, pathErr := client.Lstat(filePath)
	if handleErr != nil || pathErr != nil || !sameSFTPFileState(expected, after) || !sameSFTPFileState(expected, current) || current.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("SFTP file changed while it was being read: %s", filePath)
	}
	return nil
}

func sameSFTPFileState(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Name() == right.Name() && left.Size() == right.Size() && left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func sameLocalFileState(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Size() == right.Size() && left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func sameCopyManifest(left, right ArtifactManifest) bool {
	return left.ArtifactID == right.ArtifactID && left.RunID == right.RunID && left.JobID == right.JobID &&
		left.ProducerID == right.ProducerID && left.CreatedAt.Equal(right.CreatedAt) &&
		left.SizeBytes == right.SizeBytes && strings.EqualFold(left.SHA256, right.SHA256) &&
		left.Engine == right.Engine && left.Format == right.Format && left.Compression == right.Compression &&
		left.Encryption == right.Encryption && left.Encrypted == right.Encrypted
}

func sftpDisplayPath(endpoint CopyEndpoint, name string) string {
	parsed, err := url.Parse(endpoint.Location)
	if err != nil {
		return endpoint.Location + "/" + name
	}
	parsed.Path = path.Join(parsed.Path, name)
	parsed.RawPath = ""
	return parsed.String()
}

func isSFTPNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

func isSFTPExist(err error) bool {
	return errors.Is(err, os.ErrExist)
}

func preferSFTPContextError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func newSFTPCopyOutcome(discovered int, candidates []localCopyCandidate) CopyOutcome {
	outcome := CopyOutcome{Discovered: discovered, Artifacts: []CopyArtifactResult{}, Warnings: []string{}}
	for _, candidate := range candidates {
		if candidate.manifest.CreatedAt.After(outcome.NewestSourceAt) {
			outcome.NewestSourceAt = candidate.manifest.CreatedAt
		}
	}
	return outcome
}

func newRemoteSFTPCopyOutcome(candidates []sftpCopyCandidate) CopyOutcome {
	outcome := CopyOutcome{Discovered: len(candidates), Artifacts: []CopyArtifactResult{}, Warnings: []string{}}
	for _, candidate := range candidates {
		if candidate.manifest.CreatedAt.After(outcome.NewestSourceAt) {
			outcome.NewestSourceAt = candidate.manifest.CreatedAt
		}
	}
	return outcome
}

type boundedPrefixWriter struct {
	data      []byte
	remaining int
}

func (writer *boundedPrefixWriter) Write(data []byte) (int, error) {
	original := len(data)
	if writer.remaining > 0 {
		keep := len(data)
		if keep > writer.remaining {
			keep = writer.remaining
		}
		writer.data = append(writer.data, data[:keep]...)
		writer.remaining -= keep
	}
	return original, nil
}
