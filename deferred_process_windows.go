//go:build windows

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func startDetachedProcess(name string, args ...string) error {
	command := exec.Command(name, args...)
	configureDetachedProcess(command)
	if err := command.Start(); err != nil {
		return err
	}
	// The updater/uninstaller must outlive this process. Release only drops the
	// parent-side process handle; it does not terminate the child.
	_ = command.Process.Release()
	return nil
}

func configureDetachedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}
