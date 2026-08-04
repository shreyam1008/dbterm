//go:build !windows

package backup

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func tryLockAgentFile(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func unlockAgentFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
