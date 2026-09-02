package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrRemoteCopyRetentionDisabled = errors.New("automatic retention for remote copy destinations is disabled until the transport can prove contained create ownership and verify-before-delete semantics")
	ErrCopyRetentionPlanChanged    = errors.New("copy retention plan changed after preview; preview again before deleting anything")
)

type CopyRetentionCandidate struct {
	RunID           string    `json:"run_id"`
	ArtifactID      string    `json:"artifact_id"`
	Path            string    `json:"path"`
	ManifestPath    string    `json:"manifest_path"`
	SizeBytes       int64     `json:"size_bytes"`
	SHA256          string    `json:"sha256"`
	ManifestSize    int64     `json:"manifest_size"`
	ManifestSHA256  string    `json:"manifest_sha256"`
	SourceCreatedAt time.Time `json:"source_created_at"`
}

// PreviewCopyRetention returns the exact local artifacts selected by policy
// without deleting or changing catalog state. The newest verified recovery
// point is always retained.
func PreviewCopyRetention(ctx context.Context, store *Store, job CopyJob, now time.Time) ([]CopyRetentionCandidate, error) {
	entries, err := planCopyRetention(ctx, store, job, now)
	if err != nil {
		return nil, err
	}
	candidates := make([]CopyRetentionCandidate, 0, len(entries))
	for _, entry := range entries {
		candidates = append(candidates, copyRetentionCandidate(entry))
	}
	return candidates, nil
}

// ApplyCopyRetention deletes only complete, verified artifacts durably recorded
// for this exact copy job and still contained by its configured destination.
func ApplyCopyRetention(ctx context.Context, store *Store, job CopyJob, now time.Time) ([]string, error) {
	entries, err := planCopyRetention(ctx, store, job, now)
	if err != nil {
		return nil, err
	}
	return applyCopyRetentionEntries(ctx, store, job, entries, now)
}

// ApplyCopyRetentionPlan applies only the exact immutable candidate set shown
// by PreviewCopyRetention. Any newly completed, pruned, moved, or changed
// ownership record invalidates the plan before the first deletion.
func ApplyCopyRetentionPlan(ctx context.Context, store *Store, job CopyJob, now time.Time, expected []CopyRetentionCandidate) ([]string, error) {
	entries, err := planCopyRetention(ctx, store, job, now)
	if err != nil {
		return nil, err
	}
	if !sameCopyRetentionPlan(entries, expected) {
		return nil, ErrCopyRetentionPlanChanged
	}
	return applyCopyRetentionEntries(ctx, store, job, entries, now)
}

func applyCopyRetentionEntries(ctx context.Context, store *Store, job CopyJob, entries []copyRetentionEntry, now time.Time) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	switch job.Destination.Kind {
	case CopyEndpointLocal:
		return applyLocalCopyRetention(ctx, store, job, entries, now)
	case CopyEndpointSSH, CopyEndpointSFTP:
		return applySFTPCopyRetention(ctx, store, job, entries, now)
	default:
		return nil, ErrRemoteCopyRetentionDisabled
	}
}

func copyRetentionCandidate(entry copyRetentionEntry) CopyRetentionCandidate {
	return CopyRetentionCandidate{
		RunID: entry.RunID, ArtifactID: entry.Artifact.ArtifactID,
		Path: entry.Artifact.Destination, ManifestPath: entry.Artifact.ManifestPath,
		SizeBytes: entry.Artifact.SizeBytes, SHA256: entry.Artifact.SHA256,
		ManifestSize: entry.Artifact.ManifestSize, ManifestSHA256: entry.Artifact.ManifestSHA256,
		SourceCreatedAt: copyArtifactRecoveryTime(entry),
	}
}

func sameCopyRetentionPlan(entries []copyRetentionEntry, expected []CopyRetentionCandidate) bool {
	if len(entries) != len(expected) {
		return false
	}
	for index, entry := range entries {
		actual := copyRetentionCandidate(entry)
		candidate := expected[index]
		if actual.RunID != candidate.RunID || actual.ArtifactID != candidate.ArtifactID ||
			actual.Path != candidate.Path || actual.ManifestPath != candidate.ManifestPath ||
			actual.SizeBytes != candidate.SizeBytes || !strings.EqualFold(actual.SHA256, candidate.SHA256) ||
			actual.ManifestSize != candidate.ManifestSize || !strings.EqualFold(actual.ManifestSHA256, candidate.ManifestSHA256) ||
			!actual.SourceCreatedAt.Equal(candidate.SourceCreatedAt) {
			return false
		}
	}
	return true
}

func applyLocalCopyRetention(ctx context.Context, store *Store, job CopyJob, entries []copyRetentionEntry, now time.Time) ([]string, error) {
	root, err := filepath.Abs(filepath.Clean(job.Destination.Location))
	if err != nil {
		return nil, fmt.Errorf("resolve copy retention destination: %w", err)
	}
	var removed []string
	// Policy selection is newest-first; execution is oldest-first so a partial
	// failure preserves the most recent possible recovery points.
	for index := len(entries) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		entry := entries[index]
		artifactPath, err := filepath.Abs(filepath.Clean(entry.Artifact.Destination))
		if err != nil || !pathWithin(root, artifactPath) {
			return removed, fmt.Errorf("copy retention refused artifact outside destination: %s", entry.Artifact.Destination)
		}
		manifestPath, err := filepath.Abs(filepath.Clean(entry.Artifact.ManifestPath))
		if err != nil || !pathWithin(root, manifestPath) {
			return removed, fmt.Errorf("copy retention refused manifest outside destination: %s", entry.Artifact.ManifestPath)
		}

		exists, verifyErr := verifyLocalPruneCandidate(ctx, artifactPath, Artifact{Size: entry.Artifact.SizeBytes, SHA256: entry.Artifact.SHA256})
		if verifyErr != nil {
			return removed, verifyErr
		}
		_, manifestErr := removeVerifiedLocalForPruneWithRename(ctx, manifestPath, Artifact{
			Size: entry.Artifact.ManifestSize, SHA256: entry.Artifact.ManifestSHA256,
		}, func(capturedPath string) error {
			manifest, err := ReadArtifactManifest(capturedPath)
			if err != nil {
				return err
			}
			if manifest.ArtifactID != entry.Artifact.ArtifactID || manifest.SizeBytes != entry.Artifact.SizeBytes || !strings.EqualFold(manifest.SHA256, entry.Artifact.SHA256) {
				return fmt.Errorf("manifest identity does not match the recorded copy")
			}
			if manifest.Verification != ArtifactVerificationPassed {
				return fmt.Errorf("manifest no longer records passed verification")
			}
			return nil
		}, os.Rename)
		if manifestErr != nil {
			return removed, fmt.Errorf("remove copied artifact manifest %s: %w", manifestPath, manifestErr)
		}
		if exists {
			exists, verifyErr = removeVerifiedLocalForPruneWithRename(ctx, artifactPath, Artifact{
				Size: entry.Artifact.SizeBytes, SHA256: entry.Artifact.SHA256,
			}, nil, os.Rename)
			if verifyErr != nil {
				return removed, verifyErr
			}
		}
		reason := "retention"
		if !exists {
			reason = "missing"
		}
		if err := store.MarkCopyArtifactPruned(ctx, entry.RunID, entry.Artifact.ArtifactID, entry.Artifact.Destination, reason, now); err != nil {
			return removed, err
		}
		if exists {
			removed = append(removed, artifactPath)
		}
	}
	return removed, nil
}

func planCopyRetention(ctx context.Context, store *Store, job CopyJob, now time.Time) ([]copyRetentionEntry, error) {
	if store == nil {
		return nil, fmt.Errorf("backup store is required")
	}
	if err := job.Validate(); err != nil {
		return nil, err
	}
	if job.Destination.Kind != CopyEndpointLocal && job.Destination.Kind != CopyEndpointSSH && job.Destination.Kind != CopyEndpointSFTP {
		return nil, ErrRemoteCopyRetentionDisabled
	}
	if now.IsZero() {
		now = time.Now()
	}
	entries, err := store.listOwnedUnprunedCopyArtifacts(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if len(entries) <= 1 || (job.Retention.KeepLast == 0 && job.Retention.MaxAgeDays == 0 && job.Retention.MaxTotalBytes == 0) {
		return nil, nil
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, right := copyArtifactRecoveryTime(entries[i]), copyArtifactRecoveryTime(entries[j])
		if left.Equal(right) {
			if entries[i].Artifact.ArtifactID == entries[j].Artifact.ArtifactID {
				return entries[i].Artifact.Destination > entries[j].Artifact.Destination
			}
			return entries[i].Artifact.ArtifactID > entries[j].Artifact.ArtifactID
		}
		return left.After(right)
	})
	cutoff := time.Time{}
	if job.Retention.MaxAgeDays > 0 {
		cutoff = now.AddDate(0, 0, -job.Retention.MaxAgeDays)
	}
	prune := make([]bool, len(entries))
	for index := 1; index < len(entries); index++ {
		tooMany := job.Retention.KeepLast > 0 && index >= job.Retention.KeepLast
		tooOld := !cutoff.IsZero() && copyArtifactRecoveryTime(entries[index]).Before(cutoff)
		prune[index] = tooMany || tooOld
	}
	if job.Retention.MaxTotalBytes > 0 {
		var retainedBytes int64
		for index, entry := range entries {
			if prune[index] || entry.Artifact.SizeBytes <= 0 {
				continue
			}
			over := retainedBytes > job.Retention.MaxTotalBytes || entry.Artifact.SizeBytes > job.Retention.MaxTotalBytes-retainedBytes
			if index > 0 && over {
				prune[index] = true
				continue
			}
			if over {
				retainedBytes = job.Retention.MaxTotalBytes
			} else {
				retainedBytes += entry.Artifact.SizeBytes
			}
		}
	}
	selected := make([]copyRetentionEntry, 0)
	for index, entry := range entries {
		if prune[index] {
			selected = append(selected, entry)
		}
	}
	return selected, nil
}

func copyArtifactRecoveryTime(entry copyRetentionEntry) time.Time {
	if !entry.Artifact.SourceCreatedAt.IsZero() {
		return entry.Artifact.SourceCreatedAt.UTC()
	}
	if !entry.Artifact.VerifiedAt.IsZero() {
		return entry.Artifact.VerifiedAt.UTC()
	}
	return entry.FinishedAt.UTC()
}
