//go:build darwin

package osservice

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewUsesLaunchDaemonForSystemScope(t *testing.T) {
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
	darwin, ok := manager.(*darwinManager)
	if !ok {
		t.Fatalf("New() manager type = %T, want *darwinManager", manager)
	}
	wantPath := filepath.Join("/Library", "LaunchDaemons", launchdLabel+".plist")
	if darwin.plistPath != wantPath || darwin.domain != "system" || darwin.target != "system/"+launchdLabel || darwin.runAsUser != current.Username {
		t.Fatalf("system manager = %#v", darwin)
	}
}

func TestDarwinInstallWritesPlistAndBootstrapsAgent(t *testing.T) {
	root := t.TempDir()
	options := Options{Executable: filepath.Join(root, "DB Term", "dbterm"), LogDir: filepath.Join(root, "logs")}
	plistPath := filepath.Join(root, launchdLabel+".plist")
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{ExitCode: 113, Output: "service not found"}}}}
	manager := &darwinManager{
		options:   options,
		plistPath: plistPath,
		domain:    "gui/501",
		target:    "gui/501/" + launchdLabel,
		runner:    runner,
	}

	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	payload, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read installed plist: %v", err)
	}
	if string(payload) != string(renderLaunchdPlist(options)) {
		t.Fatalf("installed plist does not match renderer\n%s", payload)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "launchctl", args: []string{"print", "gui/501/" + launchdLabel}},
		recordedCommand{name: "launchctl", args: []string{"enable", "gui/501/" + launchdLabel}},
		recordedCommand{name: "launchctl", args: []string{"bootstrap", "gui/501", plistPath}},
	)
}

func TestDarwinStatusParsesRunningState(t *testing.T) {
	root := t.TempDir()
	plistPath := filepath.Join(root, launchdLabel+".plist")
	if err := os.WriteFile(plistPath, []byte("plist"), privateFileMode); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{Output: strings.Join([]string{
		"state = running",
		"pid = 4242",
		"last exit code = 0",
	}, "\n")}}}}
	manager := &darwinManager{plistPath: plistPath, target: "gui/501/" + launchdLabel, runner: runner}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed || !status.Running || !status.StartupEnabled || status.Manager != launchdManagerName {
		t.Fatalf("Status() = %#v, want installed and running launchd agent", status)
	}
	if status.Scope != ScopeUser || status.Name != launchdLabel {
		t.Fatalf("Status() identity = %#v", status)
	}
	if !strings.Contains(status.Detail, "pid=4242") {
		t.Fatalf("Status().Detail = %q, want PID", status.Detail)
	}
}

func TestDarwinManualStartPreservesDisabledStartupSetting(t *testing.T) {
	root := t.TempDir()
	plistPath := filepath.Join(root, launchdLabel+".plist")
	if err := os.WriteFile(plistPath, []byte("plist"), privateFileMode); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	target := "gui/501/" + launchdLabel
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: `disabled services = { "` + launchdLabel + `" => true }`}},
		{},
		{result: commandResult{ExitCode: 113, Output: "service not found"}},
		{},
		{},
		{},
	}}
	manager := &darwinManager{plistPath: plistPath, domain: "gui/501", target: target, runner: runner}

	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "launchctl", args: []string{"print-disabled", "gui/501"}},
		recordedCommand{name: "launchctl", args: []string{"enable", target}},
		recordedCommand{name: "launchctl", args: []string{"print", target}},
		recordedCommand{name: "launchctl", args: []string{"bootstrap", "gui/501", plistPath}},
		recordedCommand{name: "launchctl", args: []string{"kickstart", "-k", target}},
		recordedCommand{name: "launchctl", args: []string{"disable", target}},
	)
}

func TestDarwinStopLeavesStartupSettingUnchanged(t *testing.T) {
	root := t.TempDir()
	plistPath := filepath.Join(root, launchdLabel+".plist")
	if err := os.WriteFile(plistPath, []byte("plist"), privateFileMode); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	target := "gui/501/" + launchdLabel
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{Output: "state = running"}}}}
	manager := &darwinManager{plistPath: plistPath, target: target, runner: runner}

	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "launchctl", args: []string{"print", target}},
		recordedCommand{name: "launchctl", args: []string{"bootout", target}},
	)
}

func TestDarwinSetStartupEnabledIsSeparate(t *testing.T) {
	root := t.TempDir()
	plistPath := filepath.Join(root, launchdLabel+".plist")
	if err := os.WriteFile(plistPath, []byte("plist"), privateFileMode); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	target := "gui/501/" + launchdLabel
	runner := &fakeCommandRunner{}
	manager := &darwinManager{plistPath: plistPath, target: target, runner: runner}
	if err := manager.SetStartupEnabled(context.Background(), false); err != nil {
		t.Fatalf("SetStartupEnabled(false) error = %v", err)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "launchctl", args: []string{"disable", target}},
	)
}

func TestDarwinSystemMutationFailsBeforeCommandsWithoutElevation(t *testing.T) {
	runner := &fakeCommandRunner{}
	manager := &darwinManager{
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

func TestDarwinSystemStatusDoesNotRequireElevation(t *testing.T) {
	root := t.TempDir()
	plistPath := filepath.Join(root, launchdLabel+".plist")
	if err := os.WriteFile(plistPath, []byte("plist"), privateFileMode); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: "state = waiting"}},
		{result: commandResult{Output: "disabled services = {}"}},
	}}
	manager := &darwinManager{
		options:   Options{Scope: ScopeSystem},
		plistPath: plistPath,
		domain:    "system",
		target:    "system/" + launchdLabel,
		runner:    runner,
		elevation: func() (bool, error) { t.Fatal("Status called elevation probe"); return false, nil },
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Installed || status.Running || !status.StartupEnabled || status.Scope != ScopeSystem {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestDarwinStopIsIdempotentWhenAbsent(t *testing.T) {
	root := t.TempDir()
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{ExitCode: 113}}}}
	manager := &darwinManager{plistPath: filepath.Join(root, "missing.plist"), target: "gui/501/" + launchdLabel, runner: runner}

	if err := manager.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	assertCommands(t, runner.calls,
		recordedCommand{name: "launchctl", args: []string{"print", "gui/501/" + launchdLabel}},
	)
}

func TestDarwinStatusPropagatesLaunchctlOperationalFailure(t *testing.T) {
	root := t.TempDir()
	plistPath := filepath.Join(root, launchdLabel+".plist")
	if err := os.WriteFile(plistPath, []byte("plist"), privateFileMode); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{result: commandResult{
		ExitCode: 1,
		Output:   "Operation not permitted while contacting the launchd domain",
	}}}}
	manager := &darwinManager{plistPath: plistPath, target: "gui/501/" + launchdLabel, runner: runner}

	status, err := manager.Status(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("Status() error = %v, want launchctl failure", err)
	}
	if !status.Installed || status.Running {
		t.Fatalf("Status() = %#v, want observed definition without a fabricated running state", status)
	}
}

func TestLaunchdServiceNotFoundIsNarrow(t *testing.T) {
	if !launchdServiceNotFound(commandResult{ExitCode: 113}) {
		t.Fatal("exit code 113 should identify an absent launchd service")
	}
	if !launchdServiceNotFound(commandResult{ExitCode: 3, Output: "Could not find service in domain"}) {
		t.Fatal("known missing-service output should identify an absent launchd service")
	}
	if launchdServiceNotFound(commandResult{ExitCode: 1, Output: "Operation not permitted"}) {
		t.Fatal("operational launchctl failures must not be treated as an absent service")
	}
}

func TestLaunchdIsDisabledParsesOnlyManagedLabel(t *testing.T) {
	output := "disabled services = {\n  \"other.service\" => true\n  \"" + launchdLabel + "\" => true\n}"
	if !launchdIsDisabled(output, launchdLabel) {
		t.Fatal("managed label should be disabled")
	}
	if launchdIsDisabled(output, "missing.service") {
		t.Fatal("missing label should use launchd's enabled default")
	}
}
