//go:build !windows

package persist

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
