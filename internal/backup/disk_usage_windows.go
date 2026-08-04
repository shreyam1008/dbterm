//go:build windows

package backup

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func platformDiskUsage(path string) (DiskUsage, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return DiskUsage{}, err
	}
	var available, capacity, free uint64
	if err := windows.GetDiskFreeSpaceEx(pathPointer, &available, &capacity, &free); err != nil {
		return DiskUsage{}, err
	}
	volume := filepath.VolumeName(path)
	volumeBuffer := make([]uint16, 1024)
	if err := windows.GetVolumePathName(pathPointer, &volumeBuffer[0], uint32(len(volumeBuffer))); err == nil {
		if resolvedVolume := windows.UTF16ToString(volumeBuffer); resolvedVolume != "" {
			volume = resolvedVolume
		}
	}
	return DiskUsage{
		Volume:         volume,
		CapacityBytes:  capacity,
		FreeBytes:      free,
		AvailableBytes: available,
	}, nil
}
