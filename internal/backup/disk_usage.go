package backup

import (
	"fmt"
	"os"
	"path/filepath"
)

type DiskUsage struct {
	Path           string
	Volume         string
	CapacityBytes  uint64
	FreeBytes      uint64
	AvailableBytes uint64
}

// DestinationDiskUsage reports capacity for the filesystem that will contain
// path. The destination itself need not exist yet; its nearest existing parent
// is used for the operating-system query.
func DestinationDiskUsage(path string) (DiskUsage, error) {
	if IsRemoteBackupDestination(path) {
		return DiskUsage{}, fmt.Errorf("remote capacity is managed by rclone; use `rclone about <remote>:` when the storage provider supports quota reporting")
	}
	resolved, err := resolveDestination(path)
	if err != nil {
		return DiskUsage{}, err
	}
	probe := resolved
	for {
		info, statErr := os.Stat(probe)
		if statErr == nil {
			if !info.IsDir() {
				probe = filepath.Dir(probe)
			}
			break
		}
		if !os.IsNotExist(statErr) {
			return DiskUsage{}, fmt.Errorf("inspect backup destination filesystem: %w", statErr)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return DiskUsage{}, fmt.Errorf("no existing parent found for backup destination %s", resolved)
		}
		probe = parent
	}
	usage, err := platformDiskUsage(probe)
	if err != nil {
		return DiskUsage{}, fmt.Errorf("read disk usage for backup destination: %w", err)
	}
	usage.Path = resolved
	return usage, nil
}

func FormatByteSize(size uint64) string {
	const unit = uint64(1024)
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	units := [...]string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	value := float64(size)
	unitIndex := -1
	for value >= float64(unit) && unitIndex < len(units)-1 {
		value /= float64(unit)
		unitIndex++
	}
	return fmt.Sprintf("%.1f %s", value, units[unitIndex])
}

func saturatingBlockBytes(blocks uint64, blockSize uint64) uint64 {
	if blocks == 0 || blockSize == 0 {
		return 0
	}
	maximum := ^uint64(0)
	if blocks > maximum/blockSize {
		return maximum
	}
	return blocks * blockSize
}
