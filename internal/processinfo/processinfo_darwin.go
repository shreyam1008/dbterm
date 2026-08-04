//go:build darwin

package processinfo

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func readPlatform(pid int) (Metrics, error) {
	metrics := Metrics{PID: pid}
	exists, err := darwinProcessExists(pid)
	if err != nil {
		return metrics, err
	}
	if !exists {
		return metrics, nil
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return metrics, nil
		}
		// kern.proc.pid can report a zero-length result as EIO when the process
		// exits between the existence probe and the sysctl read.
		if errors.Is(err, unix.EIO) {
			stillExists, probeErr := darwinProcessExists(pid)
			if probeErr == nil && !stillExists {
				return metrics, nil
			}
		}
		return metrics, fmt.Errorf("query kern.proc.pid with sysctl: %w", err)
	}
	if process.Proc.P_pid == 0 {
		return metrics, nil
	}

	nameBytes := make([]byte, 0, len(process.Proc.P_comm))
	for _, character := range process.Proc.P_comm {
		if character == 0 {
			break
		}
		nameBytes = append(nameBytes, byte(character))
	}
	metrics.Name = string(nameBytes)
	if process.Eproc.Xrssize > 0 {
		metrics.RSSBytes = uint64(process.Eproc.Xrssize) * uint64(os.Getpagesize())
	}
	started := process.Proc.P_starttime
	metrics.StartTime = time.Unix(started.Sec, int64(started.Usec)*int64(time.Microsecond))
	metrics.Alive = true
	return metrics, nil
}

func darwinProcessExists(pid int) (bool, error) {
	err := unix.Kill(pid, 0)
	switch {
	case err == nil, errors.Is(err, unix.EPERM):
		return true, nil
	case errors.Is(err, unix.ESRCH):
		return false, nil
	default:
		return false, fmt.Errorf("probe process existence: %w", err)
	}
}
