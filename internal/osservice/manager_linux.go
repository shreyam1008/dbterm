//go:build linux

package osservice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	systemdManagerName = "systemd"
	systemdUnitName    = "dbterm-backup.service"
)

type linuxManager struct {
	options   Options
	unitPath  string
	runAsUser string
	runner    commandRunner
	elevation elevationProbe
}

var (
	_ StartupManager = (*linuxManager)(nil)
	_ Restarter      = (*linuxManager)(nil)
)

func newPlatformManager(options Options, runner commandRunner) (Manager, error) {
	runAsUser, err := resolveRunAsUser(options)
	if err != nil {
		return nil, err
	}
	if options.Scope == ScopeSystem {
		return &linuxManager{
			options:   options,
			unitPath:  filepath.Join("/etc", "systemd", "system", systemdUnitName),
			runAsUser: runAsUser,
			runner:    runner,
		}, nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve systemd user config directory: %w", err)
	}
	return &linuxManager{
		options:  options,
		unitPath: filepath.Join(configDir, "systemd", "user", systemdUnitName),
		runner:   runner,
	}, nil
}

func (m *linuxManager) Install(ctx context.Context) error {
	if err := m.requireElevation("install systemd backup service"); err != nil {
		return err
	}
	if m.scope() == ScopeSystem {
		if err := validateSystemRuntimePaths(m.options, m.runAsUser); err != nil {
			return fmt.Errorf("validate system backup service paths: %w", err)
		}
	} else if err := ensurePrivateDirectory(m.options.LogDir); err != nil {
		return fmt.Errorf("prepare backup agent logs: %w", err)
	}
	writeDefinition := writePrivateFile
	if m.scope() == ScopeSystem {
		writeDefinition = writePrivateFileInExistingDirectory
	}
	if err := writeDefinition(m.unitPath, m.renderDefinition()); err != nil {
		return fmt.Errorf("install systemd %s unit: %w", m.scope(), err)
	}
	if _, err := m.runRequired(ctx, "reload systemd units", "daemon-reload"); err != nil {
		return err
	}
	if _, err := m.runRequired(ctx, "enable backup agent at startup", "enable", systemdUnitName); err != nil {
		return err
	}
	// enable --now does not restart an already-running unit after an update.
	if _, err := m.runRequired(ctx, "restart backup agent", "restart", systemdUnitName); err != nil {
		return err
	}
	return nil
}

func (m *linuxManager) Uninstall(ctx context.Context) error {
	if err := m.requireElevation("uninstall systemd backup service"); err != nil {
		return err
	}
	status, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if status.Installed || status.Running {
		if _, err := m.runRequired(ctx, "disable and stop backup agent", "disable", "--now", systemdUnitName); err != nil {
			return err
		}
	}

	removed := false
	if err := os.Remove(m.unitPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("remove systemd user unit %s: %w", m.unitPath, err)
		}
	} else {
		removed = true
	}
	if removed || status.Installed {
		if _, err := m.runRequired(ctx, "reload systemd units", "daemon-reload"); err != nil {
			return err
		}
	}
	return nil
}

func (m *linuxManager) Start(ctx context.Context) error {
	if err := m.requireElevation("start systemd backup service"); err != nil {
		return err
	}
	if _, err := m.runRequired(ctx, "start backup agent", "start", systemdUnitName); err != nil {
		return err
	}
	return nil
}

func (m *linuxManager) Stop(ctx context.Context) error {
	if err := m.requireElevation("stop systemd backup service"); err != nil {
		return err
	}
	if _, err := m.runRequired(ctx, "stop backup agent", "stop", systemdUnitName); err != nil {
		return err
	}
	return nil
}

func (m *linuxManager) Restart(ctx context.Context) error {
	if err := m.requireElevation("restart systemd backup service"); err != nil {
		return err
	}
	if _, err := m.runRequired(ctx, "restart backup agent", "restart", systemdUnitName); err != nil {
		return err
	}
	return nil
}

func (m *linuxManager) SetStartupEnabled(ctx context.Context, enabled bool) error {
	operation := "disable"
	action := "disable backup agent at startup"
	if enabled {
		operation = "enable"
		action = "enable backup agent at startup"
	}
	if err := m.requireElevation(action); err != nil {
		return err
	}
	if _, err := m.runRequired(ctx, action, operation, systemdUnitName); err != nil {
		return err
	}
	return nil
}

func (m *linuxManager) Status(ctx context.Context) (Status, error) {
	status := baseStatus(systemdManagerName, systemdUnitName, m.scope())
	installed, err := definitionExists(m.unitPath)
	if err != nil {
		return status, err
	}
	status.Installed = installed

	args := m.systemctlArgs(
		"show", systemdUnitName,
		"--property=LoadState", "--property=UnitFileState", "--property=ActiveState", "--property=SubState",
		"--property=MainPID", "--property=Result", "--property=ExecMainStatus", "--property=FragmentPath",
		"--no-pager",
	)
	result, err := m.runner.Run(ctx, "systemctl", args...)
	if err != nil {
		return status, fmt.Errorf("query systemd backup agent: %w", err)
	}
	if result.ExitCode != 0 {
		properties := parseProperties(result.Output)
		status.StartupEnabled = systemdStartupEnabled(properties["UnitFileState"])
		status.Detail = strings.TrimSpace(result.Output)
		if properties["LoadState"] == "not-found" || !status.Installed {
			if status.Detail == "" {
				status.Detail = "not installed"
			}
			return status, nil
		}
		return status, commandResultError("query systemd backup agent", "systemctl", args, result)
	}

	properties := parseProperties(result.Output)
	loadState := properties["LoadState"]
	unitFileState := properties["UnitFileState"]
	activeState := properties["ActiveState"]
	subState := properties["SubState"]
	mainPID := properties["MainPID"]
	resultState := properties["Result"]
	exitStatus := properties["ExecMainStatus"]
	fragmentPath := properties["FragmentPath"]
	if loadState == "loaded" {
		status.Installed = true
	}
	status.Running = activeState == "active"
	status.StartupEnabled = systemdStartupEnabled(unitFileState)
	status.Detail = compactStateDetail(loadState, unitFileState, activeState, subState, mainPID, resultState, exitStatus)
	if status.Detail == "" {
		status.Detail = strings.TrimSpace(result.Output)
	}
	if fragmentPath != "" && !sameDefinitionPath(fragmentPath, m.unitPath) {
		return status, fmt.Errorf("systemd unit %s resolves to %s instead of managed definition %s", systemdUnitName, fragmentPath, m.unitPath)
	}
	return status, nil
}

func (m *linuxManager) requireElevation(operation string) error {
	return requireElevation(m.scope(), operation, m.elevation)
}

func (m *linuxManager) scope() Scope {
	if m.options.Scope == ScopeSystem {
		return ScopeSystem
	}
	return ScopeUser
}

func (m *linuxManager) renderDefinition() []byte {
	if m.scope() == ScopeSystem {
		return renderSystemdSystemUnit(m.options, m.runAsUser)
	}
	return renderSystemdUnit(m.options)
}

func (m *linuxManager) systemctlArgs(arguments ...string) []string {
	if m.scope() == ScopeUser {
		return append([]string{"--user"}, arguments...)
	}
	return append([]string(nil), arguments...)
}

func (m *linuxManager) runRequired(ctx context.Context, action string, arguments ...string) (commandResult, error) {
	return runRequired(ctx, m.runner, action, "systemctl", m.systemctlArgs(arguments...)...)
}

func systemdStartupEnabled(unitFileState string) bool {
	switch unitFileState {
	case "enabled", "enabled-runtime", "linked", "linked-runtime":
		return true
	default:
		return false
	}
}

func sameDefinitionPath(first, second string) bool {
	if filepath.Clean(first) == filepath.Clean(second) {
		return true
	}
	firstInfo, firstErr := os.Stat(first)
	secondInfo, secondErr := os.Stat(second)
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo)
}

func parseProperties(output string) map[string]string {
	properties := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			properties[key] = strings.TrimSpace(value)
		}
	}
	return properties
}

func compactStateDetail(loadState, unitFileState, activeState, subState, mainPID, resultState, exitStatus string) string {
	values := []struct{ key, value string }{
		{"load", loadState},
		{"unit_file", unitFileState},
		{"active", activeState},
		{"sub", subState},
		{"pid", mainPID},
		{"result", resultState},
		{"exit_status", exitStatus},
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value.value != "" {
			parts = append(parts, value.key+"="+value.value)
		}
	}
	return strings.Join(parts, ", ")
}
