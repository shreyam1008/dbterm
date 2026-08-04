//go:build windows

package osservice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewUsesLocalSystemBootTaskForSystemScope(t *testing.T) {
	root := t.TempDir()
	manager, err := New(Options{
		Executable: filepath.Join(root, "dbterm.exe"),
		ConfigDir:  filepath.Join(root, "config"),
		StateDir:   filepath.Join(root, "state"),
		LogDir:     filepath.Join(root, "logs"),
		Scope:      ScopeSystem,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	windowsManager, ok := manager.(*windowsManager)
	if !ok {
		t.Fatalf("New() manager type = %T, want *windowsManager", manager)
	}
	if windowsManager.userID != windowsLocalSystemSID || windowsManager.taskName() != windowsSystemTaskName || windowsManager.scope() != ScopeSystem {
		t.Fatalf("system manager = %#v", windowsManager)
	}
}

func TestWindowsRemotePathRejectsUNCAndAcceptsLocalVolume(t *testing.T) {
	remote, err := windowsRemotePath(`\\server\backup\dbterm`)
	if err != nil || !remote {
		t.Fatalf("windowsRemotePath(UNC) = %v, %v", remote, err)
	}
	localFile := filepath.Join(t.TempDir(), "dbterm.exe")
	if err := os.WriteFile(localFile, []byte("binary"), 0o600); err != nil {
		t.Fatalf("write local fixture: %v", err)
	}
	remote, err = windowsRemotePath(localFile)
	if err != nil || remote {
		t.Fatalf("windowsRemotePath(local) = %v, %v", remote, err)
	}
}

func TestWindowsStatusParsesRegisteredRunningTask(t *testing.T) {
	definition, err := renderWindowsTaskXML(Options{Executable: `C:\dbterm.exe`, LogDir: `C:\Logs`}, "S-1-5-21-1000")
	if err != nil {
		t.Fatalf("render task definition: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: string(definition)}},
		{result: commandResult{Output: "4"}},
	}}
	manager := &windowsManager{runner: runner}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed || !status.Running || !status.StartupEnabled || status.Manager != windowsManagerName {
		t.Fatalf("Status() = %#v, want installed and running task", status)
	}
	if status.Scope != ScopeUser || status.Name != windowsTaskName {
		t.Fatalf("Status() identity = %#v", status)
	}
}

func TestWindowsStatusKeepsDisabledRunningTaskVisible(t *testing.T) {
	definition, err := renderWindowsTaskXML(Options{Executable: `C:\dbterm.exe`, LogDir: `C:\Logs`}, "S-1-5-21-1000")
	if err != nil {
		t.Fatalf("render task definition: %v", err)
	}
	disabledDefinition := strings.ReplaceAll(string(definition), "<Enabled>true</Enabled>", "<Enabled>false</Enabled>")
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: disabledDefinition}},
		{result: commandResult{Output: "4"}},
	}}
	manager := &windowsManager{runner: runner}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed || !status.Running {
		t.Fatalf("Status() = %#v, want disabled but running task to remain visible", status)
	}
	if status.StartupEnabled {
		t.Fatalf("Status().StartupEnabled = true for disabled task: %#v", status)
	}
	if !strings.Contains(status.Detail, "disabled") || !strings.Contains(status.Detail, "state=running") {
		t.Fatalf("Status().Detail = %q, want disabled and running detail", status.Detail)
	}
}

func TestWindowsInstallReplacesRunningTaskAndRemovesTemporaryXML(t *testing.T) {
	root := t.TempDir()
	options := Options{Executable: filepath.Join(root, "dbterm.exe"), LogDir: filepath.Join(root, "logs")}
	definition, err := renderWindowsTaskXML(options, "S-1-5-21-1000")
	if err != nil {
		t.Fatalf("render task definition: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: string(definition)}},
		{result: commandResult{Output: "4"}},
	}}
	manager := &windowsManager{options: options, userID: "S-1-5-21-1000", runner: runner}

	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("Install() command count = %d, want 5: %#v", len(runner.calls), runner.calls)
	}
	if runner.calls[2].name != "schtasks.exe" || !containsArgument(runner.calls[2].args, "/End") {
		t.Fatalf("Install() did not stop old task: %#v", runner.calls[2])
	}
	if !containsArgument(runner.calls[3].args, "/Create") || !containsArgument(runner.calls[3].args, "/XML") {
		t.Fatalf("Install() create command = %#v", runner.calls[3])
	}
	if !containsArgument(runner.calls[4].args, "/Run") {
		t.Fatalf("Install() run command = %#v", runner.calls[4])
	}
	matches, err := filepath.Glob(filepath.Join(options.LogDir, ".dbterm-backup-task-*.xml"))
	if err != nil {
		t.Fatalf("glob temporary task XML: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary task XML remains: %#v", matches)
	}
}

func TestWindowsStartDoesNotChangeEnabledStartupSetting(t *testing.T) {
	definition, err := renderWindowsTaskXML(Options{Executable: `C:\dbterm.exe`, LogDir: `C:\Logs`}, "S-1-5-21-1000")
	if err != nil {
		t.Fatalf("render task definition: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: string(definition)}},
		{result: commandResult{Output: "3"}},
	}}
	manager := &windowsManager{runner: runner}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(runner.calls) != 3 || !containsArgument(runner.calls[2].args, "/Run") || containsArgument(runner.calls[2].args, "/ENABLE") {
		t.Fatalf("Start() commands = %#v", runner.calls)
	}
}

func TestWindowsManualStartRestoresDisabledStartupSetting(t *testing.T) {
	definition, err := renderWindowsTaskXML(Options{Executable: `C:\dbterm.exe`, LogDir: `C:\Logs`}, "S-1-5-21-1000")
	if err != nil {
		t.Fatalf("render task definition: %v", err)
	}
	disabledDefinition := strings.ReplaceAll(string(definition), "<Enabled>true</Enabled>", "<Enabled>false</Enabled>")
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: disabledDefinition}},
		{result: commandResult{Output: "3"}},
		{},
		{},
		{},
	}}
	manager := &windowsManager{runner: runner}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if len(runner.calls) != 5 || !containsArgument(runner.calls[2].args, "/ENABLE") || !containsArgument(runner.calls[3].args, "/Run") || !containsArgument(runner.calls[4].args, "/DISABLE") {
		t.Fatalf("Start() commands = %#v", runner.calls)
	}
}

func TestWindowsMissingTaskIsNotInstalled(t *testing.T) {
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{
		ExitCode: 1,
		Output:   "localized scheduler error",
	}}, {result: commandResult{ExitCode: 3}}}}
	manager := &windowsManager{runner: runner}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Installed || status.Running || !strings.Contains(status.Detail, "not installed") {
		t.Fatalf("Status() = %#v, want absent task", status)
	}
}

func TestWindowsStopFailureLeavesStartupSettingUntouched(t *testing.T) {
	definition, err := renderWindowsTaskXML(Options{Executable: `C:\dbterm.exe`, LogDir: `C:\Logs`}, "S-1-5-21-1000")
	if err != nil {
		t.Fatalf("render task definition: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: string(definition)}},
		{result: commandResult{Output: "4"}},
		{result: commandResult{ExitCode: 1, Output: "task did not stop"}},
	}}
	manager := &windowsManager{runner: runner}

	err = manager.Stop(context.Background())
	if err == nil || !strings.Contains(err.Error(), "startup setting was left unchanged") {
		t.Fatalf("Stop() error = %v, want preserved startup-state detail", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("Stop() command count = %d, want 3: %#v", len(runner.calls), runner.calls)
	}
	if !containsArgument(runner.calls[2].args, "/End") || containsArgument(runner.calls[2].args, "/DISABLE") {
		t.Fatalf("Stop() commands = %#v", runner.calls)
	}
}

func TestWindowsSystemStatusUsesDistinctBootTaskIdentity(t *testing.T) {
	definition, err := renderWindowsSystemTaskXML(Options{Executable: `C:\dbterm.exe`, ConfigDir: `C:\Config`, StateDir: `C:\State`, LogDir: `C:\Logs`, Scope: ScopeSystem})
	if err != nil {
		t.Fatalf("render system task definition: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: string(definition)}},
		{result: commandResult{Output: "3"}},
	}}
	manager := &windowsManager{options: Options{Scope: ScopeSystem}, name: windowsSystemTaskName, runner: runner}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed || status.Running || !status.StartupEnabled || status.Scope != ScopeSystem || status.Name != windowsSystemTaskName {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestWindowsSystemMutationFailsBeforeCommandsWithoutElevation(t *testing.T) {
	runner := &fakeCommandRunner{}
	manager := &windowsManager{
		options:   Options{Scope: ScopeSystem},
		runner:    runner,
		elevation: func() (bool, error) { return false, nil },
	}
	err := manager.Start(context.Background())
	if err == nil || !RequiresElevation(err) {
		t.Fatalf("Start() error = %v, want elevation error", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("Start() commands = %#v, want none", runner.calls)
	}
}

func TestWindowsUninstallRestartsRunningTaskWhenDeleteFails(t *testing.T) {
	definition, err := renderWindowsTaskXML(Options{Executable: `C:\dbterm.exe`, LogDir: `C:\Logs`}, "S-1-5-21-1000")
	if err != nil {
		t.Fatalf("render task definition: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: string(definition)}},
		{result: commandResult{Output: "4"}},
		{},
		{result: commandResult{ExitCode: 5, Output: "access denied"}},
		{},
	}}
	manager := &windowsManager{runner: runner}

	err = manager.Uninstall(context.Background())
	if err == nil || !strings.Contains(err.Error(), "was restarted") {
		t.Fatalf("Uninstall() error = %v, want recovery detail", err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("Uninstall() command count = %d, want 5: %#v", len(runner.calls), runner.calls)
	}
	if !containsArgument(runner.calls[2].args, "/End") || !containsArgument(runner.calls[3].args, "/Delete") || !containsArgument(runner.calls[4].args, "/Run") {
		t.Fatalf("Uninstall() recovery commands = %#v", runner.calls)
	}
}
