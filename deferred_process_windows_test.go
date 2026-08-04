//go:build windows

package main

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigureDetachedProcessSeparatesWindowsConsole(t *testing.T) {
	command := exec.Command("cmd.exe", "/C", "exit", "0")
	configureDetachedProcess(command)
	if command.SysProcAttr == nil {
		t.Fatal("configureDetachedProcess() did not set process attributes")
	}
	wantFlags := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	if command.SysProcAttr.CreationFlags&wantFlags != wantFlags {
		t.Fatalf("CreationFlags = %#x, want detached flags %#x", command.SysProcAttr.CreationFlags, wantFlags)
	}
	if !command.SysProcAttr.HideWindow {
		t.Fatal("configureDetachedProcess() should hide the helper window")
	}
}
