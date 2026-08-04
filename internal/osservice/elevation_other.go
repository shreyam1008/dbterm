//go:build !linux && !darwin && !windows

package osservice

func platformIsElevated() (bool, error) {
	return false, nil
}
