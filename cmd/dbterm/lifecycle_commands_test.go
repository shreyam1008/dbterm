package main

import (
	"strings"
	"testing"
)

func TestWindowsUpdateCommandRestartsOnlyAfterSuccessfulMove(t *testing.T) {
	command := windowsUpdateCommand(`C:\Program Files\dbterm\dbterm.exe`, `C:\Program Files\dbterm\dbterm.exe.new`, true)
	if strings.Contains(command, " & ") {
		t.Fatalf("windowsUpdateCommand() contains unconditional command chaining: %s", command)
	}
	if strings.Count(command, " && ") != 2 {
		t.Fatalf("windowsUpdateCommand() = %q, want wait, move, and restart chained conditionally", command)
	}
	moveIndex := strings.Index(command, "move /Y")
	restartIndex := strings.Index(command, "backup service install")
	if moveIndex < 0 || restartIndex <= moveIndex {
		t.Fatalf("windowsUpdateCommand() = %q, want service restart after replacement", command)
	}
}

func TestWindowsUpdateCommandOmitsRestartWhenAgentWasStopped(t *testing.T) {
	command := windowsUpdateCommand(`C:\dbterm.exe`, `C:\dbterm.exe.new`, false)
	if strings.Contains(command, "backup service install") {
		t.Fatalf("windowsUpdateCommand() unexpectedly restarts service: %s", command)
	}
	if strings.Count(command, " && ") != 1 {
		t.Fatalf("windowsUpdateCommand() = %q, want wait and move chained conditionally", command)
	}
}

func TestWindowsUninstallCommandDeletesDataOnlyAfterBinary(t *testing.T) {
	command := windowsUninstallCommand(`C:\dbterm.exe`, []string{`C:\Users\me\AppData\dbterm`}, true)
	deleteIndex := strings.Index(command, `del /f /q "C:\dbterm.exe"`)
	purgeIndex := strings.Index(command, "rmdir /s /q")
	if deleteIndex < 0 || purgeIndex <= deleteIndex {
		t.Fatalf("windowsUninstallCommand() = %q, want data purge after binary deletion", command)
	}
	if strings.Count(command, " && ") != 2 {
		t.Fatalf("windowsUninstallCommand() = %q, want all destructive steps chained conditionally", command)
	}
}
