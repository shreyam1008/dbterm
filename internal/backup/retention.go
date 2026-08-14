package backup

import (
	"context"
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
		if root.kind == destinationRclone {
			object, pathErr := parseRemoteArtifactWithin(root, artifactPath)
			if pathErr != nil {
				return removed, pathErr
			}
			exists, verifyErr := verifyRcloneArtifactForPrune(ctx, object, run.Artifact)
			if verifyErr != nil {
				return removed, verifyErr
			}
			if !exists {
				if err := store.MarkArtifactPruned(ctx, run.ID, "missing", now); err != nil {
					return removed, err
				}
				continue
			}
			if err := deleteRcloneArtifact(ctx, object); err != nil {
				return removed, err
			}
			artifactPath = object.String()
		} else {
			localPath, pathErr := filepath.Abs(filepath.Clean(artifactPath))
			if pathErr != nil || !pathWithin(root.localPath, localPath) {
				return removed, fmt.Errorf("retention refused path outside destination: %s", run.Artifact.Path)
			}
			exists, verifyErr := verifyRecordedArtifactForPrune(ctx, localPath, run.Artifact)
			if verifyErr != nil {
				return removed, verifyErr
			}
			if !exists {
				if err := store.MarkArtifactPruned(ctx, run.ID, "missing", now); err != nil {
					return removed, err
				}
				continue
			}
			if err := os.Remove(localPath); err != nil {
				return removed, fmt.Errorf("remove expired backup %s: %w", localPath, err)
			}
			artifactPath = localPath
		}
		if err := store.MarkArtifactPruned(ctx, run.ID, "retention", now); err != nil {
			return removed, err
		}
		removed = append(removed, artifactPath)
	}
	return removed, nil
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
