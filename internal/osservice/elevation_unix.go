//go:build linux || darwin

package osservice

import "os"

func platformIsElevated() (bool, error) {
	return os.Geteuid() == 0, nil
}
