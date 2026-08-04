//go:build !linux && !darwin && !windows

package osservice

func validateSystemRuntimePaths(Options, string) error {
	return nil
}
