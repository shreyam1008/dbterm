//go:build windows

package backup

import "golang.org/x/sys/windows"

func atomicPublicationSupported() error { return nil }

func atomicPublishNoReplace(source, destination string) error {
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	// Omitting MOVEFILE_REPLACE_EXISTING makes an existing destination an
	// error. Both paths are destination-local, so this is an atomic rename.
	return windows.MoveFileEx(sourcePointer, destinationPointer, windows.MOVEFILE_WRITE_THROUGH)
}
