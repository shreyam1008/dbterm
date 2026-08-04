//go:build linux

package osservice

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxInstallWritesUnitAndRestartsAgent(t *testing.T) {
	root := t.TempDir()
	options := Options{
		Executable: filepath.Join(root, "DB Term", "dbterm"),
		LogDir:     filepath.Join(root, "logs"),
	}
	unitPath := filepath.Join(root, "systemd", "user", systemdUnitName)
	runner := &fakeCommandRunner{}
	manager := &linuxManager{options: options, unitPath: unitPath, runner: runner}

	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	payload, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read installed unit: %v", err)
	}
	if string(payload) != string(renderSystemdUnit(options)) {
		t.Fatalf("installed unit does not match renderer\n%s", payload)
	}
	info, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("stat installed unit: %v", err)
	}
	if got := info.Mode().Perm(); got != privateFileMode {
		t.Fatalf("installed unit mode = %o, want %o", got, privateFileMode)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "systemctl", args: []string{"--user", "daemon-reload"}},
		recordedCommand{name: "systemctl", args: []string{"--user", "enable", systemdUnitName}},
		recordedCommand{name: "systemctl", args: []string{"--user", "restart", systemdUnitName}},
	)
}

func TestNewUsesSystemdUserConfigDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	manager, err := New(Options{
		Executable: filepath.Join(root, "bin", "dbterm"),
		LogDir:     filepath.Join(root, "logs"),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	linux, ok := manager.(*linuxManager)
	if !ok {
		t.Fatalf("New() manager type = %T, want *linuxManager", manager)
	}
	want := filepath.Join(root, "config", "systemd", "user", systemdUnitName)
	if linux.unitPath != want {
		t.Fatalf("unit path = %q, want %q", linux.unitPath, want)
	}
}

func TestNewUsesSystemdSystemDirectory(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current() error = %v", err)
	}
	if current.Uid == "0" {
		t.Skip("system services intentionally reject root as the run-as account")
	}
	root := t.TempDir()
	manager, err := New(Options{
		Executable: filepath.Join(root, "bin", "dbterm"),
		ConfigDir:  filepath.Join(root, "config"),
		StateDir:   filepath.Join(root, "state"),
		LogDir:     filepath.Join(root, "logs"),
		Scope:      ScopeSystem,
		RunAsUser:  current.Username,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	linux, ok := manager.(*linuxManager)
	if !ok {
		t.Fatalf("New() manager type = %T, want *linuxManager", manager)
	}
	want := filepath.Join("/etc", "systemd", "system", systemdUnitName)
	if linux.unitPath != want || linux.runAsUser != current.Username {
		t.Fatalf("system manager = %#v, want path %q and user %q", linux, want, current.Username)
	}
}

func TestLinuxInstallStopsAfterManagerFailure(t *testing.T) {
	root := t.TempDir()
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{},
		{result: commandResult{ExitCode: 1, Output: "user manager unavailable"}},
	}}
	manager := &linuxManager{
		options:  Options{Executable: filepath.Join(root, "dbterm"), LogDir: filepath.Join(root, "logs")},
		unitPath: filepath.Join(root, systemdUnitName),
		runner:   runner,
	}

	err := manager.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "user manager unavailable") {
		t.Fatalf("Install() error = %v, want command output", err)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "systemctl", args: []string{"--user", "daemon-reload"}},
		recordedCommand{name: "systemctl", args: []string{"--user", "enable", systemdUnitName}},
	)
}

func TestLinuxStatusParsesMachineProperties(t *testing.T) {
	root := t.TempDir()
	unitPath := filepath.Join(root, systemdUnitName)
	if err := os.WriteFile(unitPath, []byte("unit"), privateFileMode); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	output := strings.Join([]string{
		"LoadState=loaded",
		"UnitFileState=enabled",
		"ActiveState=active",
		"SubState=running",
		"MainPID=4242",
		"Result=success",
		"ExecMainStatus=0",
		"FragmentPath=" + unitPath,
	}, "\n")
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{Output: output}}}}
	manager := &linuxManager{unitPath: unitPath, runner: runner}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed || !status.Running || !status.StartupEnabled || status.Manager != systemdManagerName {
		t.Fatalf("Status() = %#v, want installed and running systemd service", status)
	}
	if status.Scope != ScopeUser || status.Name != systemdUnitName {
		t.Fatalf("Status() identity = %#v", status)
	}
	for _, detail := range []string{"unit_file=enabled", "active=active", "pid=4242", "result=success"} {
		if !strings.Contains(status.Detail, detail) {
			t.Fatalf("Status().Detail = %q, missing %q", status.Detail, detail)
		}
	}
}

func TestLinuxStatusRejectsShadowingUnitDefinition(t *testing.T) {
	root := t.TempDir()
	unitPath := filepath.Join(root, systemdUnitName)
	if err := os.WriteFile(unitPath, []byte("unit"), privateFileMode); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{Output: strings.Join([]string{
		"LoadState=loaded",
		"ActiveState=active",
		"FragmentPath=/etc/systemd/user/dbterm-backup.service",
	}, "\n")}}}}
	manager := &linuxManager{unitPath: unitPath, runner: runner}

	status, err := manager.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "instead of managed definition") {
		t.Fatalf("Status() error = %v, want definition conflict", err)
	}
	if !status.Installed || !status.Running {
		t.Fatalf("Status() should retain observed state on conflict: %#v", status)
	}
}

func TestLinuxUninstallIsIdempotentWhenAbsent(t *testing.T) {
	root := t.TempDir()
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{
		ExitCode: 4,
		Output:   "LoadState=not-found\nActiveState=inactive",
	}}}}
	manager := &linuxManager{unitPath: filepath.Join(root, systemdUnitName), runner: runner}

	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0].name != "systemctl" || !containsArgument(runner.calls[0].args, "--property=LoadState") {
		t.Fatalf("Uninstall() commands = %#v, want status query only", runner.calls)
	}
}

func TestLinuxRuntimeAndStartupControlsAreIndependent(t *testing.T) {
	runner := &fakeCommandRunner{}
	manager := &linuxManager{runner: runner}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := manager.SetStartupEnabled(context.Background(), false); err != nil {
		t.Fatalf("SetStartupEnabled(false) error = %v", err)
	}
	if err := manager.SetStartupEnabled(context.Background(), true); err != nil {
		t.Fatalf("SetStartupEnabled(true) error = %v", err)
	}
	if err := manager.Restart(context.Background()); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "systemctl", args: []string{"--user", "start", systemdUnitName}},
		recordedCommand{name: "systemctl", args: []string{"--user", "stop", systemdUnitName}},
		recordedCommand{name: "systemctl", args: []string{"--user", "disable", systemdUnitName}},
		recordedCommand{name: "systemctl", args: []string{"--user", "enable", systemdUnitName}},
		recordedCommand{name: "systemctl", args: []string{"--user", "restart", systemdUnitName}},
	)
}

func TestLinuxSystemInstallUsesMachineManagerAndRunAsUser(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current() error = %v", err)
	}
	if current.Uid == "0" {
		t.Skip("system services intentionally reject root as the run-as account")
	}
	root := t.TempDir()
	executable := filepath.Join(root, "dbterm")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	options := Options{
		Executable: executable,
		ConfigDir:  filepath.Join(root, "config"),
		StateDir:   filepath.Join(root, "state"),
		LogDir:     filepath.Join(root, "logs"),
		Scope:      ScopeSystem,
	}
	for _, directory := range []string{options.ConfigDir, options.StateDir, options.LogDir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("create runtime directory: %v", err)
		}
	}
	unitPath := filepath.Join(root, systemdUnitName)
	runner := &fakeCommandRunner{}
	manager := &linuxManager{
		options:   options,
		unitPath:  unitPath,
		runAsUser: current.Username,
		runner:    runner,
		elevation: func() (bool, error) { return true, nil },
	}

	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	payload, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read system unit: %v", err)
	}
	if string(payload) != string(renderSystemdSystemUnit(options, current.Username)) {
		t.Fatalf("installed system unit does not match renderer\n%s", payload)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "systemctl", args: []string{"daemon-reload"}},
		recordedCommand{name: "systemctl", args: []string{"enable", systemdUnitName}},
		recordedCommand{name: "systemctl", args: []string{"restart", systemdUnitName}},
	)
}

func TestLinuxSystemMutationFailsBeforeCommandsWithoutElevation(t *testing.T) {
	runner := &fakeCommandRunner{}
	manager := &linuxManager{
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

func TestLinuxSystemStatusDoesNotRequireElevation(t *testing.T) {
	root := t.TempDir()
	unitPath := filepath.Join(root, systemdUnitName)
	if err := os.WriteFile(unitPath, []byte("unit"), privateFileMode); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{Output: strings.Join([]string{
		"LoadState=loaded",
		"UnitFileState=enabled",
		"ActiveState=inactive",
		"FragmentPath=" + unitPath,
	}, "\n")}}}}
	manager := &linuxManager{
		options:   Options{Scope: ScopeSystem},
		unitPath:  unitPath,
		runner:    runner,
		elevation: func() (bool, error) { t.Fatal("Status called elevation probe"); return false, nil },
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed || !status.StartupEnabled || status.Running || status.Scope != ScopeSystem {
		t.Fatalf("Status() = %#v", status)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "systemctl", args: []string{"show", systemdUnitName, "--property=LoadState", "--property=UnitFileState", "--property=ActiveState", "--property=SubState", "--property=MainPID", "--property=Result", "--property=ExecMainStatus", "--property=FragmentPath", "--no-pager"}},
	)
}
