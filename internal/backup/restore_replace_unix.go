//go:build !windows

package backup

import "os"

func replaceSQLiteStagedFile(source, destination string) error {
	return os.Rename(source, destination)
}
