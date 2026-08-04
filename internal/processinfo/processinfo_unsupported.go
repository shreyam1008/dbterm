//go:build !linux && !darwin && !windows

package processinfo

func readPlatform(pid int) (Metrics, error) {
	return Metrics{PID: pid}, ErrUnsupported
}
