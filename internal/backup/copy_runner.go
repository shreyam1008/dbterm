package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/privatefile"
)

const copyFreeSpaceSafetyMargin = 64 << 20

// CopyOutcome is the transfer-only result. The durable CopyRun lifecycle is
// owned by the store/agent layer so a valid local backup never changes status
// merely because an independent copy fails.
type CopyOutcome struct {
	Discovered     int
	AlreadyPresent int
	BytesCopied    int64
	Artifacts      []CopyArtifactResult
	Warnings       []string
	NewestSourceAt time.Time
}

// CopyRunner moves already-published artifacts. It never creates a database
// dump and only discovers artifacts through strict portable sidecar manifests.
type CopyRunner struct {
	Store                 *Store
	Now                   func() time.Time
	Progress              ProgressFunc
	LocalPublicationGuard CopyLocalPublicationGuard
}

// CopyLocalPublicationPhase identifies the local filesystem boundary being
// checked. A configured destination volume supplies the guard; transports call
// it after creating each private partial and immediately before each immutable
// final-name publication.
type CopyLocalPublicationPhase string

const (
	CopyLocalStageCreated          CopyLocalPublicationPhase = "stage-created"
	CopyLocalBeforeArtifactPublish CopyLocalPublicationPhase = "before-artifact-publish"
	CopyLocalBeforeManifestPublish CopyLocalPublicationPhase = "before-manifest-publish"
)

type CopyLocalPublicationGuard func(context.Context, string, CopyLocalPublicationPhase) error

type localCopyCandidate struct {
	manifest     ArtifactManifest
	artifactName string
	artifactPath string
	manifestPath string
}

func (runner CopyRunner) Run(ctx context.Context, job CopyJob) (CopyOutcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := job.Validate(); err != nil {
		return CopyOutcome{}, err
	}
	if job.Destination.Kind == CopyEndpointLocal && job.DestinationVolume != nil && runner.LocalPublicationGuard == nil {
		return CopyOutcome{}, fmt.Errorf("configured destination volume requires an active local publication guard")
	}
	source, filter, err := runner.resolveLocalSource(ctx, job)
	if err != nil {
		return CopyOutcome{}, err
	}
	if source.Kind == CopyEndpointRclone && job.Destination.Kind == CopyEndpointLocal {
		return runner.runRcloneToLocal(ctx, source.Location, job.Destination.Location, filter)
	}
	if source.Kind == CopyEndpointLocal && (job.Destination.Kind == CopyEndpointSFTP || job.Destination.Kind == CopyEndpointSSH) {
		return runner.runLocalToSFTP(ctx, job, source.Location, job.Destination, filter)
	}
	if (source.Kind == CopyEndpointSFTP || source.Kind == CopyEndpointSSH) && job.Destination.Kind == CopyEndpointLocal {
		return runner.runSFTPToLocal(ctx, job, source, job.Destination.Location, filter)
	}
	if source.Kind != CopyEndpointLocal || job.Destination.Kind != CopyEndpointLocal {
		return CopyOutcome{}, fmt.Errorf("copy transport %s to %s is not implemented by the copy runner", source.Kind, job.Destination.Kind)
	}
	return runner.runLocalToLocal(ctx, job, source.Location, job.Destination.Location, filter)
}

func (runner CopyRunner) resolveLocalSource(ctx context.Context, job CopyJob) (CopyEndpoint, CopyArtifactFilter, error) {
	source := job.Source
	filter := job.ArtifactFilter
	if strings.TrimSpace(job.SourceBackupJobID) == "" {
		return source, filter, nil
	}
	if filter.JobID != "" && filter.JobID != job.SourceBackupJobID {
		return CopyEndpoint{}, CopyArtifactFilter{}, fmt.Errorf("copy artifact job filter %q conflicts with source backup job %q", filter.JobID, job.SourceBackupJobID)
	}
	filter.JobID = job.SourceBackupJobID
	if strings.TrimSpace(source.Location) != "" {
		return source, filter, nil
	}
	if runner.Store == nil {
		return CopyEndpoint{}, CopyArtifactFilter{}, fmt.Errorf("copy source backup job %q requires an open backup store", job.SourceBackupJobID)
	}
	backupJob, err := runner.Store.GetJob(ctx, job.SourceBackupJobID)
	if err != nil {
		return CopyEndpoint{}, CopyArtifactFilter{}, fmt.Errorf("resolve copy source backup job: %w", err)
	}
	if IsRemoteBackupDestination(backupJob.Destination) {
		return CopyEndpoint{}, CopyArtifactFilter{}, fmt.Errorf("copy source backup job %q does not publish to a local directory", job.SourceBackupJobID)
	}
	source.Location = backupJob.Destination
	return source, filter, nil
}

func (runner CopyRunner) runLocalToLocal(ctx context.Context, job CopyJob, sourceRoot, destinationRoot string, filter CopyArtifactFilter) (CopyOutcome, error) {
	if err := ensureCopyDirectory(sourceRoot, false); err != nil {
		return CopyOutcome{}, fmt.Errorf("copy source: %w", err)
	}
	if err := ensureCopyDirectory(destinationRoot, false); err != nil {
		return CopyOutcome{}, fmt.Errorf("copy destination: %w", err)
	}
	if err := ensureDistinctLocalCopyDirectories(sourceRoot, destinationRoot); err != nil {
		return CopyOutcome{}, err
	}
	if err := runner.ensureLocalCopyDestination(ctx, destinationRoot); err != nil {
		return CopyOutcome{}, fmt.Errorf("copy destination: %w", err)
	}

	candidates, err := scanLocalCopyCandidates(sourceRoot, filter)
	if err != nil {
		return CopyOutcome{}, err
	}
	outcome := CopyOutcome{Discovered: len(candidates), Artifacts: []CopyArtifactResult{}, Warnings: []string{}}
	for _, candidate := range candidates {
		if candidate.manifest.CreatedAt.After(outcome.NewestSourceAt) {
			outcome.NewestSourceAt = candidate.manifest.CreatedAt
		}
	}
	if len(candidates) == 0 {
		runner.report(ProgressEvent{Phase: "scan", Message: "no completed artifact manifests matched the copy job"})
		return outcome, nil
	}

	destinationByID, err := scanLocalCopyCandidates(destinationRoot, filter)
	if err != nil {
		return CopyOutcome{}, fmt.Errorf("scan copy destination: %w", err)
	}
	known := make(map[string]localCopyCandidate, len(destinationByID))
	for _, candidate := range destinationByID {
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
			if present.manifest.SizeBytes != candidate.manifest.SizeBytes || !strings.EqualFold(present.manifest.SHA256, candidate.manifest.SHA256) {
				return outcome, fmt.Errorf("copy destination artifact ID %q conflicts with the producer checksum or size", candidate.manifest.ArtifactID)
			}
			result, err := runner.reverifyAlreadyPresentLocalCopy(ctx, candidate.manifest, candidate.artifactPath, present)
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
		if reconciled, recovered, recoverErr := runner.reconcileLocalCopyOrphan(ctx, candidate, finalArtifact, finalManifest); recoverErr != nil {
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

		result, copyErr := runner.copyLocalArtifact(ctx, candidate, finalArtifact, finalManifest)
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

// reverifyAlreadyPresentLocalCopy turns an immutable destination artifact into
// a complete catalog ownership result only after re-reading its sidecar around
// full checksum and envelope verification. This is intentionally driven by a
// matching producer manifest: merely finding files in a destination directory
// is never sufficient to adopt them.
func (runner CopyRunner) reverifyAlreadyPresentLocalCopy(ctx context.Context, expected ArtifactManifest, sourceDisplay string, present localCopyCandidate) (CopyArtifactResult, error) {
	first, err := readLocalCopyInspectionManifest(ctx, present.manifestPath)
	if err != nil {
		return CopyArtifactResult{}, fmt.Errorf("read completion manifest: %w", err)
	}
	if !sameArtifactManifestForOwnership(first.manifest, expected) {
		return CopyArtifactResult{}, fmt.Errorf("destination completion manifest does not exactly match the producer manifest")
	}
	verified := present
	verified.manifest = first.manifest
	if err := verifyLocalCopyCandidate(ctx, verified); err != nil {
		return CopyArtifactResult{}, err
	}
	second, err := readLocalCopyInspectionManifest(ctx, present.manifestPath)
	if err != nil || first.size != second.size || !strings.EqualFold(first.digest, second.digest) || !sameArtifactManifestForOwnership(first.manifest, second.manifest) {
		if err == nil {
			err = fmt.Errorf("completion manifest bytes changed")
		}
		return CopyArtifactResult{}, fmt.Errorf("destination completion manifest changed during verification: %w", err)
	}
	result := runner.copyResult(first.manifest, sourceDisplay, present.artifactPath, present.manifestPath)
	result.ManifestSize = second.size
	result.ManifestSHA256 = second.digest
	result.PublicationState = ArtifactPublicationComplete
	result.AlreadyPresent = true
	return result, nil
}

func sameArtifactManifestForOwnership(left, right ArtifactManifest) bool {
	var leftBytes, rightBytes bytes.Buffer
	if err := EncodeArtifactManifest(&leftBytes, left); err != nil {
		return false
	}
	if err := EncodeArtifactManifest(&rightBytes, right); err != nil {
		return false
	}
	return bytes.Equal(leftBytes.Bytes(), rightBytes.Bytes())
}

func (runner CopyRunner) reconcileLocalCopyOrphan(ctx context.Context, candidate localCopyCandidate, finalArtifact, finalManifest string) (CopyArtifactResult, bool, error) {
	artifactInfo, artifactErr := os.Lstat(finalArtifact)
	_, manifestErr := os.Lstat(finalManifest)
	artifactExists := artifactErr == nil
	manifestExists := manifestErr == nil
	if artifactErr != nil && !errors.Is(artifactErr, os.ErrNotExist) {
		return CopyArtifactResult{}, false, fmt.Errorf("inspect possible orphan copy artifact %s: %w", finalArtifact, artifactErr)
	}
	if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
		return CopyArtifactResult{}, false, fmt.Errorf("inspect possible orphan copy manifest %s: %w", finalManifest, manifestErr)
	}
	if !artifactExists && !manifestExists {
		return CopyArtifactResult{}, false, nil
	}
	if manifestExists {
		return CopyArtifactResult{}, false, fmt.Errorf("copy destination completion manifest already exists without a matching indexed artifact identity: %s", finalManifest)
	}
	if artifactInfo.Mode()&os.ModeSymlink != 0 || !artifactInfo.Mode().IsRegular() {
		return CopyArtifactResult{}, false, fmt.Errorf("copy destination orphan is not a regular file: %s", finalArtifact)
	}
	orphan := localCopyCandidate{manifest: candidate.manifest, artifactName: candidate.artifactName, artifactPath: finalArtifact, manifestPath: finalManifest}
	if err := verifyLocalCopyCandidate(ctx, orphan); err != nil {
		return CopyArtifactResult{}, false, fmt.Errorf("copy destination artifact-only collision does not match producer artifact %q; preserved for manual review: %w", candidate.manifest.ArtifactID, err)
	}
	result := runner.copyResult(candidate.manifest, candidate.artifactPath, finalArtifact, finalManifest)
	result.Reconciled = true
	result.PublicationState = ArtifactPublicationArtifactOnly
	manifestStage, manifestSize, manifestSHA256, err := writeArtifactManifestStage(filepath.Dir(finalManifest), candidate.manifest)
	if err != nil {
		return result, false, err
	}
	manifestStageInfo, err := os.Lstat(manifestStage)
	if err != nil {
		_ = os.Remove(manifestStage)
		return result, false, fmt.Errorf("capture private manifest staging file: %w", err)
	}
	defer removeExactOwnedLocalPartial(manifestStage, manifestStageInfo)
	if err := runner.guardLocalPublication(ctx, manifestStage, CopyLocalStageCreated); err != nil {
		return result, false, err
	}
	result.ManifestSize = manifestSize
	result.ManifestSHA256 = manifestSHA256
	completionCtx, cancel := publicationCompletionContext(ctx)
	err = runner.publishLocalNoReplace(completionCtx, manifestStage, finalManifest, nil, finalArtifact, CopyLocalBeforeManifestPublish)
	cancel()
	if err != nil {
		if publicationCrossed(err) {
			result.PublicationState = ArtifactPublicationUncertain
		}
		return result, false, fmt.Errorf("verified orphan artifact remains at %s, but completion manifest publication failed: %w", finalArtifact, err)
	}
	result.PublicationState = ArtifactPublicationComplete
	runner.report(ProgressEvent{Phase: "copy", Message: fmt.Sprintf("reconciled verified artifact-only copy %s", candidate.artifactName), CurrentBytes: candidate.manifest.SizeBytes, TotalBytes: candidate.manifest.SizeBytes})
	return result, true, nil
}

func verifyLocalCopyCandidate(ctx context.Context, candidate localCopyCandidate) error {
	initial, err := os.Lstat(candidate.artifactPath)
	if err != nil {
		return err
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() {
		return fmt.Errorf("copied artifact is not a regular file: %s", candidate.artifactPath)
	}
	if initial.Size() != candidate.manifest.SizeBytes {
		return fmt.Errorf("copied artifact size is %d; manifest records %d", initial.Size(), candidate.manifest.SizeBytes)
	}
	file, err := os.Open(candidate.artifactPath)
	if err != nil {
		return err
	}
	digest, hashErr := hashOpenedFile(ctx, file)
	unchangedErr := verifyOpenedFileUnchanged(file, initial, candidate.artifactPath)
	closeErr := file.Close()
	if hashErr != nil {
		return hashErr
	}
	if unchangedErr != nil {
		return unchangedErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !strings.EqualFold(digest, candidate.manifest.SHA256) {
		return fmt.Errorf("copied artifact SHA-256 does not match its completion manifest")
	}
	return verifyCopiedArtifactEnvelopeContext(ctx, candidate.artifactPath, candidate.manifest)
}

func (runner CopyRunner) copyLocalArtifact(ctx context.Context, candidate localCopyCandidate, finalArtifact, finalManifest string) (CopyArtifactResult, error) {
	initial, err := os.Lstat(candidate.artifactPath)
	if err != nil {
		return CopyArtifactResult{}, fmt.Errorf("inspect copy source artifact %s: %w", candidate.artifactPath, err)
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() {
		return CopyArtifactResult{}, fmt.Errorf("copy source artifact must be a regular file, not a symlink: %s", candidate.artifactPath)
	}
	if initial.Size() != candidate.manifest.SizeBytes {
		return CopyArtifactResult{}, fmt.Errorf("copy source artifact %s has size %d; manifest records %d", candidate.artifactPath, initial.Size(), candidate.manifest.SizeBytes)
	}

	source, err := os.Open(candidate.artifactPath)
	if err != nil {
		return CopyArtifactResult{}, fmt.Errorf("open copy source artifact %s: %w", candidate.artifactPath, err)
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(initial, opened) {
		return CopyArtifactResult{}, fmt.Errorf("copy source artifact changed while it was being opened: %s", candidate.artifactPath)
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
	reporter := &publicationProgressWriter{writer: io.MultiWriter(stage, hash), total: initial.Size(), message: "copying verified artifact", progress: runner.Progress}
	written, err := io.CopyBuffer(reporter, &contextReader{ctx: ctx, reader: source}, make([]byte, 256*1024))
	if err != nil {
		return CopyArtifactResult{}, fmt.Errorf("copy artifact %s: %w", candidate.artifactName, err)
	}
	reporter.finish()
	if err := ctx.Err(); err != nil {
		return CopyArtifactResult{}, err
	}
	if written != candidate.manifest.SizeBytes {
		return CopyArtifactResult{}, fmt.Errorf("copy artifact %s wrote %d bytes; manifest records %d", candidate.artifactName, written, candidate.manifest.SizeBytes)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(digest, candidate.manifest.SHA256) {
		return CopyArtifactResult{}, fmt.Errorf("copy artifact %s failed SHA-256 verification", candidate.artifactName)
	}
	if err := verifyOpenedFileUnchanged(source, initial, candidate.artifactPath); err != nil {
		return CopyArtifactResult{}, err
	}
	if err := stage.Sync(); err != nil {
		return CopyArtifactResult{}, fmt.Errorf("sync copied artifact staging file: %w", err)
	}
	if err := stage.Close(); err != nil {
		return CopyArtifactResult{}, fmt.Errorf("close copied artifact staging file: %w", err)
	}
	stageClosed = true
	if err := verifyCopiedArtifactEnvelopeContext(ctx, stagePath, candidate.manifest); err != nil {
		return CopyArtifactResult{}, err
	}
	return runner.publishVerifiedLocalCopy(ctx, candidate.manifest, candidate.artifactPath, candidate.artifactName, stagePath, finalArtifact, finalManifest)
}

// publishVerifiedLocalCopy performs the irreversible artifact-then-manifest
// boundary shared by local, SFTP-pull, and rclone-pull transports. stagePath
// must already be closed, synced, checksum-verified, and format-validated.
func (runner CopyRunner) publishVerifiedLocalCopy(ctx context.Context, manifest ArtifactManifest, sourceDisplay, artifactName, stagePath, finalArtifact, finalManifest string) (CopyArtifactResult, error) {
	result := runner.copyResult(manifest, sourceDisplay, finalArtifact, finalManifest)
	result.PublicationState = ArtifactPublicationUncertain
	manifestStage, manifestSize, manifestSHA256, err := writeArtifactManifestStage(filepath.Dir(finalManifest), manifest)
	if err != nil {
		return CopyArtifactResult{}, err
	}
	manifestStageInfo, err := os.Lstat(manifestStage)
	if err != nil {
		_ = os.Remove(manifestStage)
		return CopyArtifactResult{}, fmt.Errorf("capture private manifest staging file: %w", err)
	}
	defer removeExactOwnedLocalPartial(manifestStage, manifestStageInfo)
	if err := runner.guardLocalPublication(ctx, manifestStage, CopyLocalStageCreated); err != nil {
		return CopyArtifactResult{}, err
	}
	result.ManifestSize = manifestSize
	result.ManifestSHA256 = manifestSHA256

	if err := runner.publishLocalNoReplace(ctx, stagePath, finalArtifact, runner.Progress, stagePath, CopyLocalBeforeArtifactPublish); err != nil {
		if publicationCrossed(err) {
			return result, fmt.Errorf("copy artifact reached %s but durability could not be confirmed; its completion manifest was not published: %w", finalArtifact, err)
		}
		return CopyArtifactResult{}, err
	}
	result.PublicationState = ArtifactPublicationArtifactOnly

	completionCtx, cancel := publicationCompletionContext(ctx)
	err = runner.publishLocalNoReplace(completionCtx, manifestStage, finalManifest, nil, finalArtifact, CopyLocalBeforeManifestPublish)
	cancel()
	if err != nil {
		if publicationCrossed(err) {
			result.PublicationState = ArtifactPublicationUncertain
			return result, fmt.Errorf("copy artifact is complete at %s and its manifest reached the final name, but manifest durability could not be confirmed: %w", finalArtifact, err)
		}
		return result, fmt.Errorf("copy artifact is complete at %s, but its completion manifest could not be published; scanners will ignore the orphan artifact: %w", finalArtifact, err)
	}
	result.PublicationState = ArtifactPublicationComplete
	runner.report(ProgressEvent{Phase: "copy", Message: fmt.Sprintf("copied and verified %s", artifactName), CurrentBytes: manifest.SizeBytes, TotalBytes: manifest.SizeBytes})
	return result, nil
}

func (runner CopyRunner) guardLocalPublication(ctx context.Context, localPath string, phase CopyLocalPublicationPhase) error {
	if runner.LocalPublicationGuard == nil {
		return nil
	}
	if err := runner.LocalPublicationGuard(ctx, localPath, phase); err != nil {
		return fmt.Errorf("local copy publication guard %s: %w", phase, err)
	}
	return nil
}

func (runner CopyRunner) publishLocalNoReplace(ctx context.Context, stagedPath, finalPath string, progress ProgressFunc, identityPath string, phase CopyLocalPublicationPhase) error {
	if runner.LocalPublicationGuard == nil {
		return publishNoReplace(ctx, stagedPath, finalPath, progress)
	}
	return publishNoReplaceGuarded(ctx, stagedPath, finalPath, progress, func() error {
		return runner.guardLocalPublication(ctx, identityPath, phase)
	})
}

// removeExactOwnedLocalPartial refuses to unlink a pathname that no longer
// identifies the regular file dbterm created. This is the strongest portable
// cleanup available without directory-handle-relative unlink APIs.
func removeExactOwnedLocalPartial(path string, owned os.FileInfo) {
	if owned == nil || owned.Mode()&os.ModeSymlink != 0 || !owned.Mode().IsRegular() {
		return
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(owned, current) {
		return
	}
	_ = os.Remove(path)
}

func (runner CopyRunner) copyResult(manifest ArtifactManifest, sourceDisplay, finalArtifact, finalManifest string) CopyArtifactResult {
	now := time.Now().UTC()
	if runner.Now != nil {
		now = runner.Now().UTC()
	}
	return CopyArtifactResult{
		ArtifactID:       manifest.ArtifactID,
		Source:           sourceDisplay,
		Destination:      finalArtifact,
		SourceCreatedAt:  manifest.CreatedAt,
		ManifestPath:     finalManifest,
		SizeBytes:        manifest.SizeBytes,
		SHA256:           strings.ToLower(manifest.SHA256),
		Verification:     CopyVerificationSHA256Format,
		VerifiedAt:       now,
		PublicationState: ArtifactPublicationUncertain,
	}
}

func scanLocalCopyCandidates(root string, filter CopyArtifactFilter) ([]localCopyCandidate, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("scan completed backup manifests in %s: %w", root, err)
	}
	seenIDs := make(map[string]string)
	candidates := make([]localCopyCandidate, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ArtifactManifestSuffix) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("copy scanner refused symbolic-link manifest: %s", filepath.Join(root, name))
		}
		manifestPath := filepath.Join(root, name)
		manifest, err := ReadArtifactManifest(manifestPath)
		if err != nil {
			return nil, err
		}
		if !copyManifestMatches(*manifest, filter) {
			continue
		}
		artifactName := strings.TrimSuffix(name, ArtifactManifestSuffix)
		if err := validateExactArtifactFilename(artifactName); err != nil {
			return nil, fmt.Errorf("copy manifest %s identifies an unsafe artifact filename: %w", manifestPath, err)
		}
		artifactPath := filepath.Join(root, artifactName)
		if !pathWithin(root, artifactPath) {
			return nil, fmt.Errorf("copy manifest resolved outside source directory: %s", manifestPath)
		}
		info, err := os.Lstat(artifactPath)
		if err != nil {
			return nil, fmt.Errorf("completion manifest %s has no readable artifact: %w", manifestPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("completion manifest artifact must be a regular file, not a symlink: %s", artifactPath)
		}
		if info.Size() != manifest.SizeBytes {
			return nil, fmt.Errorf("completion manifest artifact %s has size %d; expected %d", artifactPath, info.Size(), manifest.SizeBytes)
		}
		if previous, exists := seenIDs[manifest.ArtifactID]; exists {
			return nil, fmt.Errorf("copy source contains duplicate artifact ID %q at %s and %s", manifest.ArtifactID, previous, artifactPath)
		}
		seenIDs[manifest.ArtifactID] = artifactPath
		candidates = append(candidates, localCopyCandidate{manifest: *manifest, artifactName: artifactName, artifactPath: artifactPath, manifestPath: manifestPath})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].manifest.CreatedAt.Equal(candidates[j].manifest.CreatedAt) {
			return candidates[i].manifest.ArtifactID < candidates[j].manifest.ArtifactID
		}
		return candidates[i].manifest.CreatedAt.Before(candidates[j].manifest.CreatedAt)
	})
	return candidates, nil
}

func copyManifestMatches(manifest ArtifactManifest, filter CopyArtifactFilter) bool {
	if filter.ProducerID != "" && manifest.ProducerID != filter.ProducerID {
		return false
	}
	if filter.JobID != "" && manifest.JobID != filter.JobID {
		return false
	}
	if len(filter.Formats) == 0 {
		return true
	}
	for _, format := range filter.Formats {
		if strings.EqualFold(format, manifest.Format) {
			return true
		}
	}
	return false
}

func ensureCopyDirectory(root string, writable bool) error {
	return ensureCopyDirectoryWithGuard(root, writable, nil)
}

func (runner CopyRunner) ensureLocalCopyDestination(ctx context.Context, root string) error {
	return ensureCopyDirectoryWithGuard(root, true, func(path string) error {
		return runner.guardLocalPublication(ctx, path, CopyLocalStageCreated)
	})
}

func ensureCopyDirectoryWithGuard(root string, writable bool, afterCreate func(string) error) error {
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("copy path must be a real directory, not a symlink: %s", root)
	}
	if !writable {
		return nil
	}
	probe, err := privatefile.CreateTemp(root, ".dbterm-copy-probe-", ".partial")
	if err != nil {
		return fmt.Errorf("copy directory is not writable: %w", err)
	}
	probePath := probe.Name()
	probeInfo, err := probe.Stat()
	if err != nil {
		_ = probe.Close()
		_ = os.Remove(probePath)
		return fmt.Errorf("capture copy destination probe: %w", err)
	}
	if afterCreate != nil {
		if guardErr := afterCreate(probePath); guardErr != nil {
			_ = probe.Close()
			removeExactOwnedLocalPartial(probePath, probeInfo)
			return guardErr
		}
	}
	if closeErr := probe.Close(); closeErr != nil {
		removeExactOwnedLocalPartial(probePath, probeInfo)
		return fmt.Errorf("close copy destination probe: %w", closeErr)
	}
	current, statErr := os.Lstat(probePath)
	if statErr != nil || !os.SameFile(probeInfo, current) {
		return fmt.Errorf("copy destination probe changed before cleanup")
	}
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("remove copy destination probe: %w", err)
	}
	return nil
}

func ensureDistinctLocalCopyDirectories(sourceRoot, destinationRoot string) error {
	sourceInfo, err := os.Stat(sourceRoot)
	if err != nil {
		return fmt.Errorf("inspect local copy source identity: %w", err)
	}
	destinationInfo, err := os.Stat(destinationRoot)
	if err != nil {
		return fmt.Errorf("inspect local copy destination identity: %w", err)
	}
	if os.SameFile(sourceInfo, destinationInfo) {
		return fmt.Errorf("copy source and destination resolve to the same physical directory; use an independent recovery-copy location")
	}
	return nil
}

func requireCopyTargetAbsent(artifactPath, manifestPath string) error {
	for label, target := range map[string]string{"artifact": artifactPath, "completion manifest": manifestPath} {
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("copy destination %s already exists without a matching recorded artifact identity: %s", label, target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect copy destination %s %s: %w", label, target, err)
		}
	}
	return nil
}

func ensureCopyCapacity(destination string, artifactBytes int64) error {
	if artifactBytes < 1 {
		return fmt.Errorf("copy artifact size must be positive")
	}
	usage, err := DestinationDiskUsage(destination)
	if err != nil {
		return err
	}
	required := uint64(artifactBytes)
	if required > math.MaxUint64-copyFreeSpaceSafetyMargin {
		required = math.MaxUint64
	} else {
		required += copyFreeSpaceSafetyMargin
	}
	if usage.AvailableBytes < required {
		return fmt.Errorf("copy destination has %s available; %s is required for the artifact and safety margin", FormatByteSize(usage.AvailableBytes), FormatByteSize(required))
	}
	return nil
}

func verifyCopiedArtifactEnvelope(path string, manifest ArtifactManifest) error {
	return verifyCopiedArtifactEnvelopeContext(context.Background(), path, manifest)
}

func verifyCopiedArtifactEnvelopeContext(ctx context.Context, path string, manifest ArtifactManifest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open copied artifact for format verification: %w", err)
	}
	defer file.Close()
	prefix := make([]byte, payloadPeekBytes)
	count, readErr := io.ReadFull(file, prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return fmt.Errorf("read copied artifact format: %w", readErr)
	}
	prefix = prefix[:count]
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
		return nil
	case CompressionZip:
		if !isZipPrefix(prefix) {
			return fmt.Errorf("copied artifact does not contain the ZIP envelope recorded by its manifest")
		}
		return nil
	case CompressionZstd:
		if !isZstdPrefix(prefix) {
			return fmt.Errorf("copied artifact does not contain the zstd envelope recorded by its manifest")
		}
		return nil
	case CompressionNone:
		if manifest.Format == string(FormatDBTermBundle) {
			if err := VerifyDBTermBundleEnvelopeContext(ctx, file, manifest.SizeBytes); err != nil {
				return fmt.Errorf("copied artifact is not the dbterm bundle recorded by its manifest: %w", err)
			}
			return nil
		}
		return verifyUnwrappedCopyFormat(prefix, manifest)
	default:
		return fmt.Errorf("copied artifact manifest uses unsupported compression %q", manifest.Compression)
	}
}

func verifyUnwrappedCopyFormat(prefix []byte, manifest ArtifactManifest) error {
	switch manifest.Engine {
	case config.PostgreSQL:
		if !bytes.HasPrefix(prefix, []byte("PGDMP")) {
			return fmt.Errorf("copied artifact is not the PostgreSQL custom archive recorded by its manifest")
		}
	case config.SQLite:
		if err := validateSQLiteHeader(prefix, manifest.SizeBytes); err != nil {
			return fmt.Errorf("copied artifact is not the SQLite database recorded by its manifest: %w", err)
		}
	case config.MySQL:
		format, engine, _, _, _ := detectSQL(prefix)
		if format != FormatMySQLSQL || engine != config.MySQL {
			return fmt.Errorf("copied artifact is not the MySQL SQL dump recorded by its manifest")
		}
	case config.Turso:
		format, engine, _, _, _ := detectSQL(prefix)
		if format != FormatSQLiteSQL || engine != config.SQLite {
			return fmt.Errorf("copied artifact is not the SQLite-compatible SQL dump recorded by its manifest")
		}
	case config.CloudflareD1:
		format, _, _, _, _ := detectSQL(prefix)
		if format != FormatSQLiteSQL && format != FormatGenericSQL {
			return fmt.Errorf("copied artifact is not the D1 SQL export recorded by its manifest")
		}
	default:
		return fmt.Errorf("copied artifact manifest uses unsupported engine %q", manifest.Engine)
	}
	return nil
}

func (runner CopyRunner) report(event ProgressEvent) {
	if runner.Progress != nil {
		runner.Progress(event)
	}
}
