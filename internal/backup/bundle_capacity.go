package backup

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
)

const (
	// The estimate deliberately assumes that incompressible input grows while
	// being wrapped. The normal codecs and age envelope use substantially less
	// overhead, but backup preflight must remain safe for adversarial input.
	bundleArtifactExpansionDivisor = 4
	bundleArtifactFixedOverhead    = 16 << 20
	bundleCapacitySafetyMargin     = 64 << 20
	bundleTarEntryOverhead         = 16 << 10
)

type diskUsageProbe func(string) (DiskUsage, error)

type insufficientBundleCapacityError struct {
	location  string
	available uint64
	required  uint64
}

func (err *insufficientBundleCapacityError) Error() string {
	return fmt.Sprintf("%s has %s available; %s is required for the remaining private backup stages and safety margin", err.location, FormatByteSize(err.available), FormatByteSize(err.required))
}

func isInsufficientBundleCapacity(err error) bool {
	var target *insufficientBundleCapacityError
	return errors.As(err, &target)
}

// bundleCapacityGuard accounts only for bytes that do not exist at the time
// of each probe. The raw dump and any already-copied files are therefore not
// subtracted twice: the filesystem's current AvailableBytes already reflects
// them. Volume identity is checked on every dynamic probe so a mount change
// cannot invalidate a prior capacity decision.
type bundleCapacityGuard struct {
	probe            diskUsageProbe
	stagePath        string
	artifactPath     string
	stageVolume      string
	artifactVolume   string
	pathsEqual       bool
	sharedFilesystem bool
	rawBytes         uint64
}

func newBundleCapacityGuard(stagePath, artifactPath string, rawSize int64, probe diskUsageProbe) (*bundleCapacityGuard, error) {
	if rawSize <= 0 {
		return nil, fmt.Errorf("verified native database payload must have a positive size for capacity planning")
	}
	if probe == nil {
		probe = DestinationDiskUsage
	}
	guard := &bundleCapacityGuard{
		probe:        probe,
		stagePath:    filepath.Clean(stagePath),
		artifactPath: filepath.Clean(artifactPath),
		pathsEqual:   sameFileSetPath(filepath.Clean(stagePath), filepath.Clean(artifactPath)),
		rawBytes:     uint64(rawSize),
	}
	stage, artifact, err := guard.readUsage(false)
	if err != nil {
		return nil, err
	}
	guard.stageVolume = stage.Volume
	guard.artifactVolume = artifact.Volume
	guard.sharedFilesystem = sameCapacityVolume(stage.Volume, artifact.Volume)
	return guard, nil
}

func sameCapacityVolume(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return sameFileSetPath(filepath.Clean(left), filepath.Clean(right))
}

func (guard *bundleCapacityGuard) readUsage(checkIdentity bool) (DiskUsage, DiskUsage, error) {
	stage, err := guard.probe(guard.stagePath)
	if err != nil {
		return DiskUsage{}, DiskUsage{}, fmt.Errorf("read private staging capacity: %w", err)
	}
	if stage.Volume == "" {
		return DiskUsage{}, DiskUsage{}, fmt.Errorf("private staging filesystem identity is unavailable")
	}
	artifact := stage
	if !guard.pathsEqual {
		artifact, err = guard.probe(guard.artifactPath)
		if err != nil {
			return DiskUsage{}, DiskUsage{}, fmt.Errorf("read local artifact staging capacity: %w", err)
		}
		if artifact.Volume == "" {
			return DiskUsage{}, DiskUsage{}, fmt.Errorf("local artifact filesystem identity is unavailable")
		}
	}
	if checkIdentity {
		if !sameCapacityVolume(stage.Volume, guard.stageVolume) {
			return DiskUsage{}, DiskUsage{}, fmt.Errorf("private staging filesystem changed during backup capacity checks")
		}
		if !sameCapacityVolume(artifact.Volume, guard.artifactVolume) {
			return DiskUsage{}, DiskUsage{}, fmt.Errorf("local artifact filesystem changed during backup capacity checks")
		}
		if sameCapacityVolume(stage.Volume, artifact.Volume) != guard.sharedFilesystem {
			return DiskUsage{}, DiskUsage{}, fmt.Errorf("private staging and local artifact filesystem relationship changed during backup")
		}
	}
	return stage, artifact, nil
}

// ensurePipeline reserves the still-unwritten file-set copies, a conservative
// tar upper bound, the temporary internal manifest, the final wrapped artifact,
// and a safety margin. When staging and destination share a filesystem all of
// those bytes are charged to that one filesystem.
func (guard *bundleCapacityGuard) ensurePipeline(remainingCopyBytes, selectedBytes, selectedFiles uint64) error {
	stageRequired, artifactRequired, err := bundlePipelineRequirements(guard.rawBytes, remainingCopyBytes, selectedBytes, selectedFiles, guard.sharedFilesystem)
	if err != nil {
		return err
	}
	stage, artifact, err := guard.readUsage(true)
	if err != nil {
		return err
	}
	stageAvailable := stage.AvailableBytes
	if guard.sharedFilesystem && artifact.AvailableBytes < stageAvailable {
		stageAvailable = artifact.AvailableBytes
	}
	if stageAvailable < stageRequired {
		return &insufficientBundleCapacityError{location: "private backup staging filesystem", available: stageAvailable, required: stageRequired}
	}
	if !guard.sharedFilesystem && artifact.AvailableBytes < artifactRequired {
		return &insufficientBundleCapacityError{location: "local artifact filesystem", available: artifact.AvailableBytes, required: artifactRequired}
	}
	return nil
}

// ensureArtifact is a final dynamic check made after the real tar size is
// known and immediately before the artifact partial is created.
func (guard *bundleCapacityGuard) ensureArtifact(payloadSize int64) error {
	if payloadSize <= 0 {
		return fmt.Errorf("dbterm bundle must have a positive size for artifact capacity planning")
	}
	required, err := wrappedArtifactUpperBound(uint64(payloadSize))
	if err != nil {
		return err
	}
	required, err = checkedCapacityAdd("wrapped artifact and safety margin", required, bundleCapacitySafetyMargin)
	if err != nil {
		return err
	}
	stage, artifact, err := guard.readUsage(true)
	if err != nil {
		return err
	}
	available := artifact.AvailableBytes
	if guard.sharedFilesystem && stage.AvailableBytes < available {
		available = stage.AvailableBytes
	}
	if available < required {
		return &insufficientBundleCapacityError{location: "local artifact filesystem", available: available, required: required}
	}
	return nil
}

func bundlePipelineRequirements(rawBytes, remainingCopyBytes, selectedBytes, selectedFiles uint64, shared bool) (uint64, uint64, error) {
	bundleBytes, err := bundleTarUpperBound(rawBytes, selectedBytes, selectedFiles)
	if err != nil {
		return 0, 0, err
	}
	artifactBytes, err := wrappedArtifactUpperBound(bundleBytes)
	if err != nil {
		return 0, 0, err
	}
	stageRequired, err := checkedCapacityAdd("file-set copies and bundle", remainingCopyBytes, uint64(maxDBTermBundleManifest), bundleBytes, bundleCapacitySafetyMargin)
	if err != nil {
		return 0, 0, err
	}
	if shared {
		stageRequired, err = checkedCapacityAdd("shared staging and artifact pipeline", stageRequired, artifactBytes)
		if err != nil {
			return 0, 0, err
		}
		return stageRequired, 0, nil
	}
	artifactRequired, err := checkedCapacityAdd("wrapped artifact and safety margin", artifactBytes, bundleCapacitySafetyMargin)
	if err != nil {
		return 0, 0, err
	}
	return stageRequired, artifactRequired, nil
}

func bundleTarUpperBound(rawBytes, selectedBytes, selectedFiles uint64) (uint64, error) {
	entries, err := checkedCapacityAdd("dbterm bundle entry count", selectedFiles, 2)
	if err != nil {
		return 0, err
	}
	metadata, err := checkedCapacityMultiply("dbterm bundle metadata", entries, bundleTarEntryOverhead)
	if err != nil {
		return 0, err
	}
	// Every tar payload may need up to 511 bytes of block padding. The 1024
	// final bytes are the two required zero blocks.
	padding, err := checkedCapacityMultiply("dbterm bundle padding", entries, 511)
	if err != nil {
		return 0, err
	}
	return checkedCapacityAdd("dbterm bundle upper bound", rawBytes, selectedBytes, uint64(maxDBTermBundleManifest), metadata, padding, 1024)
}

func wrappedArtifactUpperBound(payloadBytes uint64) (uint64, error) {
	expansion := payloadBytes / bundleArtifactExpansionDivisor
	return checkedCapacityAdd("wrapped artifact upper bound", payloadBytes, expansion, bundleArtifactFixedOverhead)
}

func checkedCapacityAdd(label string, values ...uint64) (uint64, error) {
	var total uint64
	for _, value := range values {
		if value > math.MaxUint64-total {
			return 0, fmt.Errorf("%s exceeds supported capacity range", label)
		}
		total += value
	}
	return total, nil
}

func checkedCapacityMultiply(label string, left, right uint64) (uint64, error) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, fmt.Errorf("%s exceeds supported capacity range", label)
	}
	return left * right, nil
}
