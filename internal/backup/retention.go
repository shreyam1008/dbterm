package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ApplyRetention removes only successful artifacts recorded for this job and
// still contained by its configured destination. The newest success is always
// retained, even when an age policy would otherwise remove it.
func ApplyRetention(ctx context.Context, store *Store, job Job, now time.Time) ([]string, error) {
	if store == nil {
		return nil, fmt.Errorf("backup store is required")
	}
	successes, err := store.listSuccessfulUnprunedRuns(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if len(successes) <= 1 {
		return nil, nil
	}
	sort.SliceStable(successes, func(i, j int) bool {
		if successes[i].FinishedAt.Equal(successes[j].FinishedAt) {
			if successes[i].StartedAt.Equal(successes[j].StartedAt) {
				return successes[i].ID > successes[j].ID
			}
			return successes[i].StartedAt.After(successes[j].StartedAt)
		}
		return successes[i].FinishedAt.After(successes[j].FinishedAt)
	})
	root, err := parseDestination(job.Destination)
	if err != nil {
		return nil, err
	}
	if root.kind == destinationRclone {
		return nil, ErrRcloneRetentionDisabled
	}
	cutoff := time.Time{}
	if job.Retention.MaxAgeDays > 0 {
		cutoff = now.AddDate(0, 0, -job.Retention.MaxAgeDays)
	}
	prune := make([]bool, len(successes))
	for index := 1; index < len(successes); index++ {
		run := successes[index]
		tooMany := job.Retention.KeepLast > 0 && index >= job.Retention.KeepLast
		tooOld := !cutoff.IsZero() && run.FinishedAt.Before(cutoff)
		prune[index] = tooMany || tooOld
	}
	if job.Retention.MaxTotalBytes > 0 {
		var retainedBytes int64
		for index, run := range successes {
			if prune[index] || run.Artifact.Size <= 0 {
				continue
			}
			overLimit := retainedBytes > job.Retention.MaxTotalBytes || run.Artifact.Size > job.Retention.MaxTotalBytes-retainedBytes
			if index > 0 && overLimit {
				prune[index] = true
				continue
			}
			// The newest artifact is never removed, even when it alone exceeds
			// the byte ceiling. Clamp the accumulator so later additions are
			// rejected without overflowing an int64.
			if overLimit {
				retainedBytes = job.Retention.MaxTotalBytes
			} else {
				retainedBytes += run.Artifact.Size
			}
		}
	}

	var removed []string
	// Execute every policy oldest-first. If a later deletion fails, the newest
	// possible recovery points are still left untouched.
	for index := len(successes) - 1; index >= 1; index-- {
		if !prune[index] {
			continue
		}
		run := successes[index]
		artifactPath := run.Artifact.Path
		localPath, pathErr := filepath.Abs(filepath.Clean(artifactPath))
		if pathErr != nil || !pathWithin(root.localPath, localPath) {
			return removed, fmt.Errorf("retention refused path outside destination: %s", run.Artifact.Path)
		}
		exists, verifyErr := verifyLocalPruneCandidate(ctx, localPath, run.Artifact)
		if verifyErr != nil {
			return removed, verifyErr
		}
		if manifestErr := removeLocalManifestForPrune(ctx, root.localPath, run); manifestErr != nil {
			return removed, manifestErr
		}
		if exists {
			exists, verifyErr = removeVerifiedLocalForPruneWithRename(ctx, localPath, run.Artifact, nil, os.Rename)
			if verifyErr != nil {
				return removed, verifyErr
			}
		}
		if !exists {
			if err := store.MarkArtifactPruned(ctx, run.ID, "missing", now); err != nil {
				return removed, err
			}
			continue
		}
		artifactPath = localPath
		if err := store.MarkArtifactPruned(ctx, run.ID, "retention", now); err != nil {
			return removed, err
		}
		removed = append(removed, artifactPath)
	}
	return removed, nil
}

// removeLocalManifestForPrune removes the portable publication signal before
// its artifact. A missing signal is tolerated so a sidecar-first interrupted
// prune can safely resume after the artifact is re-verified.
func removeLocalManifestForPrune(ctx context.Context, root string, run Run) error {
	if strings.TrimSpace(run.Artifact.ManifestPath) == "" {
		return nil // Legacy catalog row: no sidecar is owned by this run.
	}
	manifestPath, err := filepath.Abs(filepath.Clean(run.Artifact.ManifestPath))
	if err != nil || !pathWithin(root, manifestPath) {
		return fmt.Errorf("retention refused manifest path outside destination: %s", run.Artifact.ManifestPath)
	}
	_, err = removeVerifiedLocalForPruneWithRename(ctx, manifestPath, Artifact{
		Size: run.Artifact.ManifestSize, SHA256: run.Artifact.ManifestSHA256,
	}, func(capturedPath string) error {
		manifest, readErr := ReadArtifactManifest(capturedPath)
		if readErr != nil {
			return readErr
		}
		if verifyErr := verifyManifestRun(manifest, run); verifyErr != nil {
			return fmt.Errorf("retention refused mismatched artifact manifest %s: %w", manifestPath, verifyErr)
		}
		return nil
	}, os.Rename)
	return err
}

// removeVerifiedLocalForPruneWithRename captures the directory entry under a
// deterministic same-directory quarantine name, then verifies the captured
// entry again before deletion. The deterministic name lets a later run safely
// reconcile a crash after capture. A writer that swaps the original pathname
// during the retention window causes a refusal instead of deleting unchecked
// bytes.
func removeVerifiedLocalForPruneWithRename(
	ctx context.Context,
	artifactPath string,
	artifact Artifact,
	validate func(string) error,
	rename func(string, string) error,
) (bool, error) {
	exists, err := verifyLocalPruneCandidate(ctx, artifactPath, artifact)
	if err != nil || !exists {
		return exists, err
	}
	quarantinePath := localPruneQuarantinePath(artifactPath, artifact)
	if _, err := os.Lstat(quarantinePath); err == nil {
		if _, sourceErr := os.Lstat(artifactPath); sourceErr == nil {
			return false, fmt.Errorf("retention found both the original and captured backup; preserved both for manual review: %s and %s", artifactPath, quarantinePath)
		} else if !os.IsNotExist(sourceErr) {
			return false, fmt.Errorf("recheck expired backup before captured deletion: %w", sourceErr)
		}
		return verifyAndRemoveLocalPruneCapture(ctx, quarantinePath, artifact, validate)
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect retention quarantine %s: %w", quarantinePath, err)
	}
	return captureAndRemoveLocalForPrune(ctx, artifactPath, quarantinePath, artifact, validate, rename)
}

// captureAndRemoveLocalForPrune assumes the caller just verified artifactPath.
// The captured name is synced and re-hashed afterward, which detects changes
// during the gap while keeping normal retention to two artifact reads.
func captureAndRemoveLocalForPrune(
	ctx context.Context,
	artifactPath string,
	quarantinePath string,
	artifact Artifact,
	validate func(string) error,
	rename func(string, string) error,
) (bool, error) {
	if rename == nil {
		return false, fmt.Errorf("retention quarantine rename is unavailable")
	}
	if err := rename(artifactPath, quarantinePath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("capture expired backup %s for verified deletion: %w", artifactPath, err)
	}
	if err := syncDirectory(filepath.Dir(quarantinePath)); err != nil {
		return false, fmt.Errorf("retention preserved a captured backup for retry at %s because its rename could not be synced: %w", quarantinePath, err)
	}
	return verifyAndRemoveLocalPruneCapture(ctx, quarantinePath, artifact, validate)
}

func verifyAndRemoveLocalPruneCapture(ctx context.Context, quarantinePath string, artifact Artifact, validate func(string) error) (bool, error) {
	preserve := func(cause error) (bool, error) {
		return false, fmt.Errorf("retention preserved a changed capture for manual review at %s: %w", quarantinePath, cause)
	}
	captured, err := verifyRecordedArtifactForPrune(ctx, quarantinePath, artifact)
	if err != nil {
		return preserve(err)
	}
	if !captured {
		return preserve(fmt.Errorf("captured entry disappeared before verification"))
	}
	if validate != nil {
		if err := validate(quarantinePath); err != nil {
			return preserve(err)
		}
	}
	if err := os.Remove(quarantinePath); err != nil {
		return false, fmt.Errorf("remove verified expired backup capture %s: %w", quarantinePath, err)
	}
	if err := syncDirectory(filepath.Dir(quarantinePath)); err != nil {
		return false, fmt.Errorf("sync backup directory after retention deletion: %w", err)
	}
	return true, nil
}

func verifyLocalPruneCandidate(ctx context.Context, artifactPath string, artifact Artifact) (bool, error) {
	sourceExists, sourceErr := verifyRecordedArtifactForPrune(ctx, artifactPath, artifact)
	if sourceErr != nil {
		return false, sourceErr
	}
	quarantinePath := localPruneQuarantinePath(artifactPath, artifact)
	_, quarantineErr := os.Lstat(quarantinePath)
	quarantineExists := quarantineErr == nil
	if quarantineErr != nil && !os.IsNotExist(quarantineErr) {
		return false, fmt.Errorf("inspect retention quarantine %s: %w", quarantinePath, quarantineErr)
	}
	if sourceExists && quarantineExists {
		return false, fmt.Errorf("retention found both the original and captured backup; preserved both for manual review: %s and %s", artifactPath, quarantinePath)
	}
	if sourceExists {
		return true, nil
	}
	if !quarantineExists {
		return false, nil
	}
	return verifyRecordedArtifactForPrune(ctx, quarantinePath, artifact)
}

func localPruneQuarantinePath(artifactPath string, artifact Artifact) string {
	identity := fmt.Sprintf("%s\x00%d\x00%s", filepath.Base(artifactPath), artifact.Size, strings.ToLower(strings.TrimSpace(artifact.SHA256)))
	digest := sha256.Sum256([]byte(identity))
	return filepath.Join(filepath.Dir(artifactPath), ".dbterm-prune_"+hex.EncodeToString(digest[:12])+".quarantine")
}

func removeRemoteManifestForPrune(ctx context.Context, root destinationSpec, run Run) error {
	if strings.TrimSpace(run.Artifact.ManifestPath) == "" {
		return nil
	}
	object, err := parseRemoteArtifactWithin(root, run.Artifact.ManifestPath)
	if err != nil {
		return err
	}
	initial, exists, err := inspectRcloneObject(ctx, object)
	if err != nil {
		return fmt.Errorf("inspect expired remote backup manifest %s: %w", object.String(), err)
	}
	if !exists {
		return nil
	}
	if run.Artifact.ManifestSize > 0 && initial.Size != run.Artifact.ManifestSize {
		return fmt.Errorf("retention refused changed remote manifest %s: size is %d, catalog recorded %d", object.String(), initial.Size, run.Artifact.ManifestSize)
	}
	manifest, size, digest, err := readRcloneArtifactManifest(ctx, object)
	if err != nil {
		return fmt.Errorf("verify remote artifact manifest %s before deletion: %w", object.String(), err)
	}
	if run.Artifact.ManifestSize > 0 && size != run.Artifact.ManifestSize {
		return fmt.Errorf("retention refused changed remote manifest %s: downloaded size is %d, catalog recorded %d", object.String(), size, run.Artifact.ManifestSize)
	}
	if run.Artifact.ManifestSHA256 != "" && !strings.EqualFold(digest, run.Artifact.ManifestSHA256) {
		return fmt.Errorf("retention refused changed remote manifest %s: SHA-256 no longer matches the catalog", object.String())
	}
	if err := verifyManifestRun(manifest, run); err != nil {
		return fmt.Errorf("retention refused mismatched remote artifact manifest %s: %w", object.String(), err)
	}
	after, stillExists, err := inspectRcloneObject(ctx, object)
	if err != nil {
		return err
	}
	if !stillExists || after.Size != initial.Size || (initial.ModTime != "" && after.ModTime != initial.ModTime) {
		return fmt.Errorf("retention refused remote manifest that changed during verification: %s", object.String())
	}
	return deleteRcloneArtifact(ctx, object)
}

func verifyRecordedArtifactForPrune(ctx context.Context, path string, artifact Artifact) (bool, error) {
	initial, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect expired backup %s: %w", path, err)
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() {
		return false, fmt.Errorf("retention refused non-regular artifact: %s", path)
	}
	if artifact.Size > 0 && initial.Size() != artifact.Size {
		return false, fmt.Errorf("retention refused changed artifact %s: size is %d, catalog recorded %d", path, initial.Size(), artifact.Size)
	}
	if strings.TrimSpace(artifact.SHA256) == "" {
		return true, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open expired backup %s for checksum verification: %w", path, err)
	}
	digest, hashErr := hashOpenedFile(ctx, file)
	after, statErr := file.Stat()
	closeErr := file.Close()
	if hashErr != nil {
		return false, fmt.Errorf("verify expired backup %s before deletion: %w", path, hashErr)
	}
	if statErr != nil {
		return false, fmt.Errorf("recheck expired backup %s: %w", path, statErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close expired backup %s: %w", path, closeErr)
	}
	if !os.SameFile(initial, after) || initial.Size() != after.Size() || initial.ModTime() != after.ModTime() {
		return false, fmt.Errorf("retention refused artifact that changed during verification: %s", path)
	}
	if !strings.EqualFold(digest, strings.TrimSpace(artifact.SHA256)) {
		return false, fmt.Errorf("retention refused changed artifact %s: SHA-256 no longer matches the catalog", path)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("recheck expired backup %s before deletion: %w", path, err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(initial, current) || current.Size() != initial.Size() || current.ModTime() != initial.ModTime() {
		return false, fmt.Errorf("retention refused artifact that changed before deletion: %s", path)
	}
	return true, nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
