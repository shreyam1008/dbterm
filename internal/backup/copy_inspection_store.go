package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ListCopyArtifactsForInspection returns every active recovery point owned by
// one copy job. Unlike the run-history view, this read is deliberately
// unbounded: an older retained artifact must remain selectable even when more
// than the UI history limit of runs have been recorded.
func (store *Store) ListCopyArtifactsForInspection(ctx context.Context, jobID string) ([]CopyArtifactResult, error) {
	entries, err := store.listOwnedUnprunedCopyArtifacts(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("list copied recovery points for inspection: %w", err)
	}

	artifacts := make([]CopyArtifactResult, 0, len(entries))
	seen := make(map[string]CopyArtifactResult, len(entries))
	for _, entry := range entries {
		artifact := entry.Artifact
		artifactID := strings.TrimSpace(artifact.ArtifactID)
		if artifactID == "" {
			return nil, fmt.Errorf("list copied recovery points for inspection: completed copy catalog record has no artifact ID")
		}
		if previous, duplicate := seen[artifactID]; duplicate {
			return nil, fmt.Errorf("list copied recovery points for inspection: artifact ID %q is ambiguous at %s and %s", artifactID, previous.Destination, artifact.Destination)
		}
		seen[artifactID] = artifact
		artifacts = append(artifacts, artifact)
	}

	sortCopyArtifactsForInspection(artifacts)
	return artifacts, nil
}

func sortCopyArtifactsForInspection(artifacts []CopyArtifactResult) {
	sort.Slice(artifacts, func(i, j int) bool {
		left, right := copyArtifactSelectionTime(artifacts[i]), copyArtifactSelectionTime(artifacts[j])
		if !left.Equal(right) {
			return left.After(right)
		}
		if artifacts[i].ArtifactID != artifacts[j].ArtifactID {
			return artifacts[i].ArtifactID < artifacts[j].ArtifactID
		}
		if !artifacts[i].VerifiedAt.Equal(artifacts[j].VerifiedAt) {
			return artifacts[i].VerifiedAt.After(artifacts[j].VerifiedAt)
		}
		return artifacts[i].Destination < artifacts[j].Destination
	})
}
