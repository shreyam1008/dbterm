//go:build !linux && !darwin && !windows

package osservice

func resolveRunAsUser(Options) (string, error) {
	return "", nil
}
