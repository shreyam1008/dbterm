//go:build !windows

package backup

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func platformDiskUsage(path string) (DiskUsage, error) {
	var statistics unix.Statfs_t
	if err := unix.Statfs(path, &statistics); err != nil {
		return DiskUsage{}, err
	}
	if statistics.Bsize <= 0 {
		return DiskUsage{}, fmt.Errorf("filesystem reported an invalid block size")
	}
	blockSize := uint64(statistics.Bsize)
	return DiskUsage{
		Volume:         unixMountPoint(path),
		CapacityBytes:  saturatingBlockBytes(uint64(statistics.Blocks), blockSize),
		FreeBytes:      saturatingBlockBytes(uint64(statistics.Bfree), blockSize),
		AvailableBytes: saturatingBlockBytes(uint64(statistics.Bavail), blockSize),
	}, nil
}

func unixMountPoint(path string) string {
	path = filepath.Clean(path)
	var child unix.Stat_t
	if err := unix.Stat(path, &child); err != nil {
		return string(filepath.Separator)
	}
	for {
		parentPath := filepath.Dir(path)
		if parentPath == path {
			return path
		}
		var parent unix.Stat_t
		if err := unix.Stat(parentPath, &parent); err != nil || parent.Dev != child.Dev {
			return path
		}
		path = parentPath
		child = parent
	}
}
