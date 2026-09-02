//go:build windows

package backup

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

func fileSetPathIsReparsePoint(path string, info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	attributes, err := windows.GetFileAttributes(pointer)
	return err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func sameFileSetPath(left, right string) bool {
	return strings.EqualFold(left, right)
}
