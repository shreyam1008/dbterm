//go:build windows

package osservice

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
)

const (
	windowsManagerName = "task_scheduler"
	// Task Scheduler's schtasks status text and CSV values are localized. Use
	// the numeric ScheduledTask.State enum for a locale-independent live state.
	windowsUserStateScript   = `$ErrorActionPreference = 'Stop'; $task = Get-ScheduledTask -TaskPath '\' | Where-Object { $_.TaskName -ceq 'dbterm Backup Agent' } | Select-Object -First 1; if ($null -eq $task) { exit 3 }; [Console]::Out.Write([int]$task.State)`
	windowsSystemStateScript = `$ErrorActionPreference = 'Stop'; $task = Get-ScheduledTask -TaskPath '\' | Where-Object { $_.TaskName -ceq 'dbterm Backup Agent (System)' } | Select-Object -First 1; if ($null -eq $task) { exit 3 }; [Console]::Out.Write([int]$task.State)`
)

type windowsManager struct {
	options   Options
	userID    string
	name      string
	runner    commandRunner
	elevation elevationProbe
}

var (
	_ StartupManager = (*windowsManager)(nil)
	_ Restarter      = (*windowsManager)(nil)
)

type windowsTaskSnapshot struct {
	status  Status
	enabled bool
}

func newPlatformManager(options Options, runner commandRunner) (Manager, error) {
	if _, err := resolveRunAsUser(options); err != nil {
		return nil, err
	}
	if options.Scope == ScopeSystem {
		return &windowsManager{
			options: options,
			userID:  windowsLocalSystemSID,
			name:    windowsSystemTaskName,
			runner:  runner,
		}, nil
	}
	current, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("resolve current Windows user for backup task: %w", err)
	}
	if strings.TrimSpace(current.Uid) == "" {
		return nil, fmt.Errorf("current Windows user SID is empty")
	}
	return &windowsManager{options: options, userID: current.Uid, name: windowsTaskName, runner: runner}, nil
}

func (m *windowsManager) Install(ctx context.Context) error {
	if err := m.requireElevation("install Windows backup task"); err != nil {
		return err
	}
	if m.scope() == ScopeSystem {
		if err := validateSystemRuntimePaths(m.options, ""); err != nil {
			return fmt.Errorf("validate Windows system backup task paths: %w", err)
		}
	} else if err := ensurePrivateDirectory(m.options.LogDir); err != nil {
		return fmt.Errorf("prepare backup agent logs: %w", err)
	}

	status, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if status.Running {
		if _, err := runRequired(ctx, m.runner, "stop existing backup task", "schtasks.exe", "/End", "/TN", m.taskName()); err != nil {
			return err
		}
	}

	taskXML, err := m.renderTaskXML()
	if err != nil {
		return err
	}
	var taskXMLPath string
	cleanupTaskXML := func() {}
	if m.scope() == ScopeSystem {
		taskXMLPath, cleanupTaskXML, err = writeSecureSystemTaskTempFile(taskXML)
	} else {
		taskXMLPath, err = writePrivateTempFile(m.options.LogDir, ".dbterm-backup-task-*.xml", taskXML)
		cleanupTaskXML = func() { _ = os.Remove(taskXMLPath) }
	}
	if err != nil {
		return fmt.Errorf("write temporary Windows backup task definition: %w", err)
	}
	defer cleanupTaskXML()

	if _, err := runRequired(ctx, m.runner, "register Windows backup task", "schtasks.exe", "/Create", "/TN", m.taskName(), "/XML", taskXMLPath, "/F"); err != nil {
		return err
	}
	if _, err := runRequired(ctx, m.runner, "start Windows backup task", "schtasks.exe", "/Run", "/TN", m.taskName()); err != nil {
		return err
	}
	return nil
}

func (m *windowsManager) Uninstall(ctx context.Context) error {
	if err := m.requireElevation("uninstall Windows backup task"); err != nil {
		return err
	}
	snapshot, err := m.inspectTask(ctx)
	if err != nil {
		return err
	}
	status := snapshot.status
	if !status.Installed {
		return nil
	}
	ended := false
	if status.Running {
		if _, err := runRequired(ctx, m.runner, "stop Windows backup task", "schtasks.exe", "/End", "/TN", m.taskName()); err != nil {
			return err
		}
		ended = true
	}
	if _, err := runRequired(ctx, m.runner, "delete Windows backup task", "schtasks.exe", "/Delete", "/TN", m.taskName(), "/F"); err != nil {
		if !ended {
			return err
		}
		if restoreErr := m.restoreRunningTask(ctx, snapshot.enabled); restoreErr != nil {
			return fmt.Errorf("%w; additionally could not restore the previously-running Windows backup task: %v", err, restoreErr)
		}
		return fmt.Errorf("%w; the previously-running Windows backup task was restarted because removal did not complete", err)
	}
	return nil
}

func (m *windowsManager) Start(ctx context.Context) error {
	if err := m.requireElevation("start Windows backup task"); err != nil {
		return err
	}
	snapshot, err := m.inspectTask(ctx)
	if err != nil {
		return err
	}
	status := snapshot.status
	if !status.Installed {
		return fmt.Errorf("Windows backup task is not installed; call Install first")
	}
	if status.Running {
		return nil
	}
	return m.runPreservingStartupSetting(ctx, snapshot.enabled)
}

func (m *windowsManager) Stop(ctx context.Context) error {
	if err := m.requireElevation("stop Windows backup task"); err != nil {
		return err
	}
	snapshot, err := m.inspectTask(ctx)
	if err != nil {
		return err
	}
	status := snapshot.status
	if !status.Installed {
		return nil
	}
	if !status.Running {
		return nil
	}
	if _, err := runRequired(ctx, m.runner, "stop Windows backup task", "schtasks.exe", "/End", "/TN", m.taskName()); err != nil {
		return fmt.Errorf("%w; the task's startup setting was left unchanged", err)
	}
	return nil
}

func (m *windowsManager) Restart(ctx context.Context) error {
	if err := m.requireElevation("restart Windows backup task"); err != nil {
		return err
	}
	snapshot, err := m.inspectTask(ctx)
	if err != nil {
		return err
	}
	if !snapshot.status.Installed {
		return fmt.Errorf("Windows backup task is not installed; call Install first")
	}
	if snapshot.status.Running {
		if _, err := runRequired(ctx, m.runner, "stop Windows backup task for restart", "schtasks.exe", "/End", "/TN", m.taskName()); err != nil {
			return err
		}
	}
	return m.runPreservingStartupSetting(ctx, snapshot.enabled)
}

func (m *windowsManager) SetStartupEnabled(ctx context.Context, enabled bool) error {
	action := "disable Windows backup task at startup"
	argument := "/DISABLE"
	if enabled {
		action = "enable Windows backup task at startup"
		argument = "/ENABLE"
	}
	if err := m.requireElevation(action); err != nil {
		return err
	}
	snapshot, err := m.inspectTask(ctx)
	if err != nil {
		return err
	}
	if !snapshot.status.Installed {
		return fmt.Errorf("Windows backup task is not installed; call Install first")
	}
	if snapshot.enabled == enabled {
		return nil
	}
	if _, err := runRequired(ctx, m.runner, action, "schtasks.exe", "/Change", "/TN", m.taskName(), argument); err != nil {
		return err
	}
	return nil
}

func (m *windowsManager) Status(ctx context.Context) (Status, error) {
	snapshot, err := m.inspectTask(ctx)
	return snapshot.status, err
}

func (m *windowsManager) inspectTask(ctx context.Context) (windowsTaskSnapshot, error) {
	snapshot := windowsTaskSnapshot{status: baseStatus(windowsManagerName, m.taskName(), m.scope())}
	status := &snapshot.status
	definitionArgs := []string{"/Query", "/TN", m.taskName(), "/XML"}
	definition, err := m.runner.Run(ctx, "schtasks.exe", definitionArgs...)
	if err != nil {
		return snapshot, fmt.Errorf("query Windows backup task definition: %w", err)
	}
	if definition.ExitCode != 0 {
		_, present, stateErr := m.queryTaskState(ctx)
		if stateErr != nil {
			return snapshot, stateErr
		}
		if !present {
			status.Detail = "not installed"
			return snapshot, nil
		}
		return snapshot, commandResultError("query Windows backup task definition", "schtasks.exe", definitionArgs, definition)
	}
	status.Installed = true

	enabled, parseErr := windowsTaskEnabled(definition.Output)
	if parseErr != nil {
		return snapshot, parseErr
	}
	snapshot.enabled = enabled
	status.StartupEnabled = enabled

	state, present, err := m.queryTaskState(ctx)
	if err != nil {
		return snapshot, err
	}
	if !present {
		status.Installed = false
		status.Detail = "not installed"
		return snapshot, nil
	}
	status.Running = state == 4
	enabledState := "enabled"
	if !enabled {
		enabledState = "disabled"
	}
	status.Detail = "installed, " + enabledState + ", state=" + windowsTaskStateName(state)
	return snapshot, nil
}

func (m *windowsManager) restoreRunningTask(ctx context.Context, originallyEnabled bool) error {
	if !originallyEnabled {
		if _, err := runRequired(ctx, m.runner, "temporarily enable Windows backup task for recovery", "schtasks.exe", "/Change", "/TN", m.taskName(), "/ENABLE"); err != nil {
			return err
		}
	}
	if _, err := runRequired(ctx, m.runner, "restart Windows backup task after failed removal", "schtasks.exe", "/Run", "/TN", m.taskName()); err != nil {
		return err
	}
	if !originallyEnabled {
		if _, err := runRequired(ctx, m.runner, "restore Windows backup task disabled state", "schtasks.exe", "/Change", "/TN", m.taskName(), "/DISABLE"); err != nil {
			return err
		}
	}
	return nil
}

func (m *windowsManager) queryTaskState(ctx context.Context) (int, bool, error) {
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", m.stateScript()}
	result, err := m.runner.Run(ctx, "powershell.exe", args...)
	if err != nil {
		return 0, false, fmt.Errorf("query locale-independent Windows backup task state: %w", err)
	}
	if result.ExitCode == 3 {
		return 0, false, nil
	}
	if result.ExitCode != 0 {
		return 0, false, commandResultError("query locale-independent Windows backup task state", "powershell.exe", args, result)
	}
	state, err := strconv.Atoi(strings.TrimSpace(result.Output))
	if err != nil || state < 0 || state > 4 {
		return 0, false, fmt.Errorf("parse Windows backup task numeric state %q", result.Output)
	}
	return state, true, nil
}

func (m *windowsManager) runPreservingStartupSetting(ctx context.Context, startupEnabled bool) error {
	if !startupEnabled {
		if _, err := runRequired(ctx, m.runner, "temporarily enable Windows backup task for a manual start", "schtasks.exe", "/Change", "/TN", m.taskName(), "/ENABLE"); err != nil {
			return err
		}
	}
	startErr := error(nil)
	if _, err := runRequired(ctx, m.runner, "start Windows backup task", "schtasks.exe", "/Run", "/TN", m.taskName()); err != nil {
		startErr = err
	}
	if !startupEnabled {
		if _, restoreErr := runRequired(ctx, m.runner, "restore Windows backup task disabled-at-startup setting", "schtasks.exe", "/Change", "/TN", m.taskName(), "/DISABLE"); restoreErr != nil {
			if startErr != nil {
				return fmt.Errorf("%w; additionally could not restore disabled-at-startup setting: %v", startErr, restoreErr)
			}
			return restoreErr
		}
	}
	return startErr
}

func (m *windowsManager) renderTaskXML() ([]byte, error) {
	if m.scope() == ScopeSystem {
		return renderWindowsSystemTaskXML(m.options)
	}
	return renderWindowsTaskXML(m.options, m.userID)
}

func (m *windowsManager) requireElevation(operation string) error {
	return requireElevation(m.scope(), operation, m.elevation)
}

func (m *windowsManager) scope() Scope {
	if m.options.Scope == ScopeSystem {
		return ScopeSystem
	}
	return ScopeUser
}

func (m *windowsManager) taskName() string {
	if m.name != "" {
		return m.name
	}
	if m.scope() == ScopeSystem {
		return windowsSystemTaskName
	}
	return windowsTaskName
}

func (m *windowsManager) stateScript() string {
	if m.scope() == ScopeSystem {
		return windowsSystemStateScript
	}
	return windowsUserStateScript
}

func windowsTaskStateName(state int) string {
	switch state {
	case 0:
		return "unknown"
	case 1:
		return "disabled"
	case 2:
		return "queued"
	case 3:
		return "ready"
	case 4:
		return "running"
	default:
		return fmt.Sprintf("invalid(%d)", state)
	}
}

func windowsTaskEnabled(payload string) (bool, error) {
	var definition struct {
		Settings struct {
			Enabled *bool `xml:"Enabled"`
		} `xml:"Settings"`
	}
	if err := xml.Unmarshal([]byte(strings.TrimPrefix(payload, "\ufeff")), &definition); err != nil {
		return false, fmt.Errorf("parse Windows backup task definition: %w", err)
	}
	if definition.Settings.Enabled == nil {
		return false, fmt.Errorf("Windows backup task definition does not contain Settings.Enabled")
	}
	return *definition.Settings.Enabled, nil
}
