package backup

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type RcloneSourceMeasurement struct {
	ListDuration       time.Duration `json:"list_duration"`
	Objects            int           `json:"objects"`
	CompletedManifests int           `json:"completed_manifests"`
}

// MeasureRcloneSource performs a read-only capability and latency check. It
// intentionally uses no copy, move, delete, or cleanup command.
func MeasureRcloneSource(ctx context.Context, endpoint CopyEndpoint) (RcloneSourceMeasurement, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if endpoint.Kind != CopyEndpointRclone {
		return RcloneSourceMeasurement{}, fmt.Errorf("rclone source endpoint is required")
	}
	root, err := parseDestination(endpoint.Location)
	if err != nil {
		return RcloneSourceMeasurement{}, fmt.Errorf("parse rclone source: %w", err)
	}
	if root.kind != destinationRclone {
		return RcloneSourceMeasurement{}, fmt.Errorf("rclone source endpoint is required")
	}
	started := time.Now()
	items, err := listRcloneCopyObjects(ctx, root)
	if err != nil {
		return RcloneSourceMeasurement{}, err
	}
	measurement := RcloneSourceMeasurement{ListDuration: time.Since(started)}
	for _, item := range items {
		if item.IsDir {
			continue
		}
		measurement.Objects++
		if strings.HasSuffix(item.Name, ArtifactManifestSuffix) && !hasUnsafeRcloneCopyName(item.Name) {
			measurement.CompletedManifests++
		}
	}
	return measurement, nil
}
