package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/shreyam1008/dbterm/internal/privatefile"
)

type rcloneCopyVersion struct {
	size    int64
	modTime time.Time
}

type rcloneCopyCandidate struct {
	manifest        ArtifactManifest
	artifactName    string
	artifact        destinationSpec
	manifestObject  destinationSpec
	artifactVersion rcloneCopyVersion
	manifestVersion rcloneCopyVersion
}

func (runner CopyRunner) runRcloneToLocal(ctx context.Context, sourceLocation, destinationRoot string, filter CopyArtifactFilter) (CopyOutcome, error) {
	sourceRoot, err := parseDestination(sourceLocation)
	if err != nil {
		return CopyOutcome{}, fmt.Errorf("parse rclone copy source: %w", err)
	}
	if sourceRoot.kind != destinationRclone {
		return CopyOutcome{}, fmt.Errorf("rclone copy source is required")
	}
	if err := runner.ensureLocalCopyDestination(ctx, destinationRoot); err != nil {
		return CopyOutcome{}, fmt.Errorf("copy destination: %w", err)
	}

	candidates, err := scanRcloneCopyCandidates(ctx, sourceRoot, filter)
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

	// Artifact identity, not a source filename or timestamp, determines whether
	// an immutable copy is already present in this vault.
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
			if present.manifest.SizeBytes != candidate.manifest.SizeBytes || !strings.EqualFold(present.manifest.SHA256, candidate.manifest.SHA256) {
				return outcome, fmt.Errorf("copy destination artifact ID %q conflicts with the producer checksum or size", candidate.manifest.ArtifactID)
			}
			result, err := runner.reverifyAlreadyPresentLocalCopy(ctx, candidate.manifest, candidate.artifact.String(), present)
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
			artifactPath: candidate.artifact.String(),
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

		result, copyErr := runner.copyRcloneArtifact(ctx, candidate, finalArtifact, finalManifest)
		if strings.TrimSpace(result.ArtifactID) != "" {
			outcome.Artifacts = append(outcome.Artifacts, result)
			if result.PublicationState == ArtifactPublicationComplete {
				outcome.BytesCopied += result.SizeBytes
				known[result.ArtifactID] = localCopyCandidate{
					manifest: candidate.manifest, artifactName: candidate.artifactName,
					artifactPath: finalArtifact, manifestPath: finalManifest,
				}
			}
		}
		if copyErr != nil {
			return outcome, copyErr
		}
	}
	return outcome, nil
}

func scanRcloneCopyCandidates(ctx context.Context, root destinationSpec, filter CopyArtifactFilter) ([]rcloneCopyCandidate, error) {
	items, err := listRcloneCopyObjects(ctx, root)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]rcloneObject, len(items))
	for _, item := range items {
		if item.IsDir {
			continue
		}
		name, nameErr := rcloneCopyObjectName(item)
		if nameErr != nil {
			if strings.HasSuffix(item.Name, ArtifactManifestSuffix) || strings.HasSuffix(item.Path, ArtifactManifestSuffix) {
				return nil, nameErr
			}
			continue
		}
		if _, duplicate := byName[name]; duplicate {
			return nil, fmt.Errorf("rclone copy source returned duplicate object name %q", name)
		}
		byName[name] = item
	}

	seenIDs := make(map[string]string)
	candidates := make([]rcloneCopyCandidate, 0)
	for name, item := range byName {
		if !strings.HasSuffix(name, ArtifactManifestSuffix) {
			continue
		}
		artifactName := strings.TrimSuffix(name, ArtifactManifestSuffix)
		if err := validateExactArtifactFilename(artifactName); err != nil || hasUnsafeRcloneCopyName(artifactName) {
			if err == nil {
				err = fmt.Errorf("backup output filename contains an unsupported control character")
			}
			return nil, fmt.Errorf("rclone completion manifest %q identifies an unsafe artifact filename: %w", name, err)
		}
		manifestObject, err := joinRcloneCopyObject(root, name)
		if err != nil {
			return nil, err
		}
		manifestVersion, err := rcloneCopyObjectVersion(item, manifestObject.String())
		if err != nil {
			return nil, err
		}
		if manifestVersion.size < 1 || manifestVersion.size > maxArtifactManifestBytes {
			return nil, fmt.Errorf("rclone artifact manifest size must be between 1 and %d bytes: %s", maxArtifactManifestBytes, manifestObject.String())
		}
		manifest, manifestSize, _, err := readRcloneArtifactManifest(ctx, manifestObject)
		if err != nil {
			return nil, fmt.Errorf("read rclone artifact manifest %s: %w", manifestObject.String(), err)
		}
		if manifestSize != manifestVersion.size {
			return nil, fmt.Errorf("rclone artifact manifest %s returned %d bytes; listing recorded %d", manifestObject.String(), manifestSize, manifestVersion.size)
		}
		if err := verifyRcloneCopyObjectUnchanged(ctx, manifestObject, manifestVersion, "while it was being read"); err != nil {
			return nil, err
		}
		if !copyManifestMatches(*manifest, filter) {
			continue
		}

		artifactItem, exists := byName[artifactName]
		if !exists {
			return nil, fmt.Errorf("rclone completion manifest %s has no readable artifact %q", manifestObject.String(), artifactName)
		}
		artifactObject, err := joinRcloneCopyObject(root, artifactName)
		if err != nil {
			return nil, err
		}
		artifactVersion, err := rcloneCopyObjectVersion(artifactItem, artifactObject.String())
		if err != nil {
			return nil, err
		}
		if artifactVersion.size != manifest.SizeBytes {
			return nil, fmt.Errorf("rclone completion manifest artifact %s has size %d; expected %d", artifactObject.String(), artifactVersion.size, manifest.SizeBytes)
		}
		if previous, exists := seenIDs[manifest.ArtifactID]; exists {
			return nil, fmt.Errorf("rclone copy source contains duplicate artifact ID %q at %s and %s", manifest.ArtifactID, previous, artifactObject.String())
		}
		seenIDs[manifest.ArtifactID] = artifactObject.String()
		candidates = append(candidates, rcloneCopyCandidate{
			manifest: *manifest, artifactName: artifactName, artifact: artifactObject,
			manifestObject: manifestObject, artifactVersion: artifactVersion, manifestVersion: manifestVersion,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].manifest.CreatedAt.Equal(candidates[j].manifest.CreatedAt) {
			return candidates[i].manifest.ArtifactID < candidates[j].manifest.ArtifactID
		}
		return candidates[i].manifest.CreatedAt.Before(candidates[j].manifest.CreatedAt)
	})
	return candidates, nil
}

func listRcloneCopyObjects(ctx context.Context, root destinationSpec) ([]rcloneObject, error) {
	if root.kind != destinationRclone {
		return nil, fmt.Errorf("rclone copy source is required")
	}
	var output bytes.Buffer
	if err := runRclone(ctx, &output, "lsjson", root.rclonePath(), "--max-depth", "1", "--files-only", "--no-mimetype"); err != nil {
		return nil, fmt.Errorf("scan completed rclone backup manifests in %s: %w", root.String(), err)
	}
	var items []rcloneObject
	decoder := json.NewDecoder(&output)
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf("decode rclone copy source listing: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode rclone copy source listing: trailing JSON data")
	}
	if items == nil {
		items = []rcloneObject{}
	}
	return items, nil
}

func rcloneCopyObjectName(item rcloneObject) (string, error) {
	name := item.Name
	if name == "" {
		name = item.Path
	}
	if item.Name != "" && item.Path != "" && item.Name != item.Path {
		return "", fmt.Errorf("rclone copy source returned inconsistent object names %q and %q", item.Name, item.Path)
	}
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) || hasUnsafeRcloneCopyName(name) {
		return "", fmt.Errorf("rclone copy source returned unsafe object name %q", name)
	}
	return name, nil
}

func hasUnsafeRcloneCopyName(name string) bool {
	if strings.TrimSpace(name) != name {
		return true
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func joinRcloneCopyObject(root destinationSpec, name string) (destinationSpec, error) {
	raw, err := root.join(name)
	if err != nil {
		return destinationSpec{}, err
	}
	object, err := parseDestination(raw)
	if err != nil || object.kind != destinationRclone {
		if err == nil {
			err = fmt.Errorf("joined object is not an rclone path")
		}
		return destinationSpec{}, fmt.Errorf("resolve rclone copy object %q: %w", name, err)
	}
	return object, nil
}

func rcloneCopyObjectVersion(item rcloneObject, display string) (rcloneCopyVersion, error) {
	if item.IsDir || item.Size < 0 {
		return rcloneCopyVersion{}, fmt.Errorf("rclone copy source object is not a regular file: %s", display)
	}
	modified, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.ModTime))
	if err != nil {
		return rcloneCopyVersion{}, fmt.Errorf("rclone copy source object has invalid modification time at %s", display)
	}
	return rcloneCopyVersion{size: item.Size, modTime: modified}, nil
}

func verifyRcloneCopyObjectUnchanged(ctx context.Context, object destinationSpec, expected rcloneCopyVersion, action string) error {
	item, exists, err := inspectRcloneObject(ctx, object)
	if err != nil {
		return fmt.Errorf("inspect rclone copy source object %s: %w", object.String(), err)
	}
	if !exists {
		return fmt.Errorf("rclone copy source object disappeared %s: %s", action, object.String())
	}
	current, err := rcloneCopyObjectVersion(item, object.String())
	if err != nil {
		return err
	}
	if current.size != expected.size || !current.modTime.Equal(expected.modTime) {
		return fmt.Errorf("rclone copy source object changed %s: %s", action, object.String())
	}
	return nil
}

func (runner CopyRunner) copyRcloneArtifact(ctx context.Context, candidate rcloneCopyCandidate, finalArtifact, finalManifest string) (CopyArtifactResult, error) {
	if err := verifyRcloneCopyObjectUnchanged(ctx, candidate.artifact, candidate.artifactVersion, "after discovery"); err != nil {
		return CopyArtifactResult{}, err
	}
	stage, err := privatefile.CreateTemp(filepath.Dir(finalArtifact), ".dbterm-publish-", ".partial")
	if err != nil {
		return CopyArtifactResult{}, fmt.Errorf("create private rclone copy staging file: %w", err)
	}
	stagePath := stage.Name()
	stageInfo, err := stage.Stat()
	if err != nil {
		_ = stage.Close()
		_ = os.Remove(stagePath)
		return CopyArtifactResult{}, fmt.Errorf("capture private rclone copy staging file: %w", err)
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
		return CopyArtifactResult{}, fmt.Errorf("protect private rclone copy staging file: %w", err)
	}

	hash := sha256.New()
	sink := &rcloneExactSizeWriter{writer: io.MultiWriter(stage, hash), maximum: candidate.manifest.SizeBytes}
	reporter := &publicationProgressWriter{
		writer: sink, total: candidate.manifest.SizeBytes,
		message: "pulling verified artifact from rclone", progress: runner.Progress,
	}
	if err := runRclone(ctx, reporter, "cat", candidate.artifact.rclonePath()); err != nil {
		return CopyArtifactResult{}, fmt.Errorf("pull rclone artifact %s: %w", candidate.artifactName, err)
	}
	reporter.finish()
	if err := ctx.Err(); err != nil {
		return CopyArtifactResult{}, err
	}
	if err := verifyRcloneCopyObjectUnchanged(ctx, candidate.artifact, candidate.artifactVersion, "during transfer"); err != nil {
		return CopyArtifactResult{}, err
	}
	if err := verifyRcloneCopyObjectUnchanged(ctx, candidate.manifestObject, candidate.manifestVersion, "during artifact transfer"); err != nil {
		return CopyArtifactResult{}, err
	}
	if sink.written != candidate.manifest.SizeBytes {
		return CopyArtifactResult{}, fmt.Errorf("pull rclone artifact %s wrote %d bytes; manifest records %d", candidate.artifactName, sink.written, candidate.manifest.SizeBytes)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(digest, candidate.manifest.SHA256) {
		return CopyArtifactResult{}, fmt.Errorf("pull rclone artifact %s failed SHA-256 verification", candidate.artifactName)
	}
	if err := stage.Sync(); err != nil {
		return CopyArtifactResult{}, fmt.Errorf("sync rclone copy staging file: %w", err)
	}
	if err := stage.Close(); err != nil {
		return CopyArtifactResult{}, fmt.Errorf("close rclone copy staging file: %w", err)
	}
	stageClosed = true
	if err := verifyCopiedArtifactEnvelopeContext(ctx, stagePath, candidate.manifest); err != nil {
		return CopyArtifactResult{}, err
	}
	return runner.publishVerifiedLocalCopy(
		ctx, candidate.manifest, candidate.artifact.String(), candidate.artifactName,
		stagePath, finalArtifact, finalManifest,
	)
}

type rcloneExactSizeWriter struct {
	writer  io.Writer
	maximum int64
	written int64
}

func (writer *rcloneExactSizeWriter) Write(data []byte) (int, error) {
	remaining := writer.maximum - writer.written
	if remaining <= 0 && len(data) > 0 {
		return 0, fmt.Errorf("rclone artifact exceeds the size recorded by its completion manifest")
	}
	allowed := len(data)
	if int64(allowed) > remaining {
		allowed = int(remaining)
	}
	written, err := writer.writer.Write(data[:allowed])
	writer.written += int64(written)
	if err != nil {
		return written, err
	}
	if written != allowed {
		return written, io.ErrShortWrite
	}
	if allowed != len(data) {
		return written, fmt.Errorf("rclone artifact exceeds the size recorded by its completion manifest")
	}
	return written, nil
}
