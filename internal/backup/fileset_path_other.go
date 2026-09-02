//go:build !windows

package backup

import "os"

func fileSetPathIsReparsePoint(_ string, info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink != 0
}

func sameFileSetPath(left, right string) bool {
	return left == right
}
