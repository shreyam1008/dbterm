//go:build !windows

package osservice

import "os"

func replaceDefinition(source, destination string) error {
	return os.Rename(source, destination)
}
