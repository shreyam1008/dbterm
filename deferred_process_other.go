//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func startDetachedProcess(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	_ = command.Process.Release()
	return nil
}
