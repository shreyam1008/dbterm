//go:build darwin

package osservice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const launchdManagerName = "launchd"

type darwinManager struct {
	options   Options
	plistPath string
	domain    string
	target    string
	runAsUser string
	runner    commandRunner
	elevation elevationProbe
}

var (
	_ StartupManager = (*darwinManager)(nil)
	_ Restarter      = (*darwinManager)(nil)
)

func newPlatformManager(options Options, runner commandRunner) (Manager, error) {
	runAsUser, err := resolveRunAsUser(options)
	if err != nil {
		return nil, err
	}
	if options.Scope == ScopeSystem {
		domain := "system"
		return &darwinManager{
			options:   options,
			plistPath: filepath.Join("/Library", "LaunchDaemons", launchdLabel+".plist"),
			domain:    domain,
			target:    domain + "/" + launchdLabel,
			runAsUser: runAsUser,
			runner:    runner,
		}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve LaunchAgents directory: %w", err)
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	return &darwinManager{
		options:   options,
		plistPath: filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"),
		domain:    domain,
		target:    domain + "/" + launchdLabel,
		runner:    runner,
	}, nil
}

func (m *darwinManager) Install(ctx context.Context) error {
	if err := m.requireElevation("install launchd backup service"); err != nil {
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
	if err := writeDefinition(m.plistPath, m.renderDefinition()); err != nil {
		return fmt.Errorf("install launchd %s definition: %w", m.scope(), err)
	}

	loaded, _, err := m.queryLoaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		if _, err := runRequired(ctx, m.runner, "unload previous backup agent", "launchctl", "bootout", m.target); err != nil {
			return err
		}
	}
	if _, err := runRequired(ctx, m.runner, "enable backup agent", "launchctl", "enable", m.target); err != nil {
		return err
	}
	if _, err := runRequired(ctx, m.runner, "load backup agent", "launchctl", "bootstrap", m.domain, m.plistPath); err != nil {
		return err
	}
	return nil
}

func (m *darwinManager) Uninstall(ctx context.Context) error {
	if err := m.requireElevation("uninstall launchd backup service"); err != nil {
		return err
	}
	loaded, _, err := m.queryLoaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		if _, err := runRequired(ctx, m.runner, "unload backup agent", "launchctl", "bootout", m.target); err != nil {
			return err
		}
	}
	if err := os.Remove(m.plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove launchd agent definition %s: %w", m.plistPath, err)
	}
	return nil
}

func (m *darwinManager) Start(ctx context.Context) error {
	if err := m.requireElevation("start launchd backup service"); err != nil {
		return err
	}
	installed, err := definitionExists(m.plistPath)
	if err != nil {
		return err
	}
	if !installed {
		return fmt.Errorf("launchd backup agent is not installed; call Install first")
	}
	startupEnabled, err := m.queryStartupEnabled(ctx)
	if err != nil {
		return err
	}
	if !startupEnabled {
		if _, err := runRequired(ctx, m.runner, "temporarily enable backup agent for a manual start", "launchctl", "enable", m.target); err != nil {
			return err
		}
	}

	startErr := m.startRuntime(ctx)
	if !startupEnabled {
		if _, restoreErr := runRequired(ctx, m.runner, "restore disabled-at-startup setting", "launchctl", "disable", m.target); restoreErr != nil {
			if startErr != nil {
				return fmt.Errorf("%w; additionally could not restore disabled-at-startup setting: %v", startErr, restoreErr)
			}
			return restoreErr
		}
	}
	return startErr
}

func (m *darwinManager) Stop(ctx context.Context) error {
	if err := m.requireElevation("stop launchd backup service"); err != nil {
		return err
	}
	installed, err := definitionExists(m.plistPath)
	if err != nil {
		return err
	}
	loaded, _, err := m.queryLoaded(ctx)
	if err != nil {
		return err
	}
	if !installed && !loaded {
		return nil
	}
	if loaded {
		if _, err := runRequired(ctx, m.runner, "stop backup agent", "launchctl", "bootout", m.target); err != nil {
			return err
		}
	}
	return nil
}

func (m *darwinManager) Restart(ctx context.Context) error {
	return m.Start(ctx)
}

func (m *darwinManager) SetStartupEnabled(ctx context.Context, enabled bool) error {
	action := "disable backup agent at startup"
	operation := "disable"
	if enabled {
		action = "enable backup agent at startup"
		operation = "enable"
	}
	if err := m.requireElevation(action); err != nil {
		return err
	}
	installed, err := definitionExists(m.plistPath)
	if err != nil {
		return err
	}
	if !installed {
		return fmt.Errorf("launchd backup agent is not installed; call Install first")
	}
	if _, err := runRequired(ctx, m.runner, action, "launchctl", operation, m.target); err != nil {
		return err
	}
	return nil
}

func (m *darwinManager) Status(ctx context.Context) (Status, error) {
	status := baseStatus(launchdManagerName, launchdLabel, m.scope())
	installed, err := definitionExists(m.plistPath)
	if err != nil {
		return status, err
	}
	status.Installed = installed

	loaded, result, err := m.queryLoaded(ctx)
	if err != nil {
		return status, err
	}
	if loaded {
		status.Installed = true
		status.Running = launchdIsRunning(result.Output)
		status.Detail = launchdStateDetail(result.Output)
	} else {
		status.Detail = strings.TrimSpace(result.Output)
		if status.Detail == "" {
			if installed {
				status.Detail = "installed but not loaded"
			} else {
				status.Detail = "not installed"
			}
		}
	}
	if status.Installed {
		startupEnabled, err := m.queryStartupEnabled(ctx)
		if err != nil {
			return status, err
		}
		status.StartupEnabled = startupEnabled
	}
	return status, nil
}

func (m *darwinManager) startRuntime(ctx context.Context) error {
	loaded, _, err := m.queryLoaded(ctx)
	if err != nil {
		return err
	}
	if !loaded {
		if _, err := runRequired(ctx, m.runner, "load backup agent", "launchctl", "bootstrap", m.domain, m.plistPath); err != nil {
			return err
		}
	}
	if _, err := runRequired(ctx, m.runner, "start backup agent", "launchctl", "kickstart", "-k", m.target); err != nil {
		return err
	}
	return nil
}

func (m *darwinManager) queryLoaded(ctx context.Context) (bool, commandResult, error) {
	args := []string{"print", m.target}
	result, err := m.runner.Run(ctx, "launchctl", args...)
	if err != nil {
		return false, result, fmt.Errorf("query launchd backup agent: %w", err)
	}
	if result.ExitCode == 0 {
		return true, result, nil
	}
	if launchdServiceNotFound(result) {
		return false, result, nil
	}
	return false, result, commandResultError("query launchd backup agent", "launchctl", args, result)
}

func (m *darwinManager) queryStartupEnabled(ctx context.Context) (bool, error) {
	args := []string{"print-disabled", m.domain}
	result, err := m.runner.Run(ctx, "launchctl", args...)
	if err != nil {
		return false, fmt.Errorf("query launchd backup agent startup setting: %w", err)
	}
	if result.ExitCode != 0 {
		return false, commandResultError("query launchd backup agent startup setting", "launchctl", args, result)
	}
	return !launchdIsDisabled(result.Output, launchdLabel), nil
}

func (m *darwinManager) requireElevation(operation string) error {
	return requireElevation(m.scope(), operation, m.elevation)
}

func (m *darwinManager) scope() Scope {
	if m.options.Scope == ScopeSystem {
		return ScopeSystem
	}
	return ScopeUser
}

func (m *darwinManager) renderDefinition() []byte {
	if m.scope() == ScopeSystem {
		return renderLaunchdSystemPlist(m.options, m.runAsUser)
	}
	return renderLaunchdPlist(m.options)
}

func launchdServiceNotFound(result commandResult) bool {
	// launchctl maps "service not found" to 113 on supported macOS
	// releases. Keep narrow text fallbacks for older launchctl variants while
	// treating permission/domain/IPC errors as real lifecycle failures.
	if result.ExitCode == 113 {
		return true
	}
	detail := strings.ToLower(strings.TrimSpace(result.Output))
	for _, marker := range []string{
		"could not find service",
		"could not find specified service",
		"service not found",
		"no such process",
	} {
		if strings.Contains(detail, marker) {
			return true
		}
	}
	return false
}

func launchdIsRunning(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(key) == "state" {
			return strings.EqualFold(strings.TrimSpace(value), "running")
		}
	}
	return false
}

func launchdStateDetail(output string) string {
	interesting := map[string]bool{"state": true, "pid": true, "last exit code": true}
	parts := make([]string, 0, len(interesting))
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		key = strings.TrimSpace(key)
		if ok && interesting[key] {
			parts = append(parts, key+"="+strings.TrimSpace(value))
		}
	}
	if len(parts) == 0 {
		return strings.TrimSpace(output)
	}
	return strings.Join(parts, ", ")
}

func launchdIsDisabled(output, label string) bool {
	marker := `"` + label + `"`
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		markerIndex := strings.Index(line, marker)
		if markerIndex < 0 {
			continue
		}
		_, value, ok := strings.Cut(line[markerIndex+len(marker):], "=>")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), ",;")
		fields := strings.Fields(value)
		return len(fields) > 0 && strings.EqualFold(strings.Trim(fields[0], ",;"), "true")
	}
	return false
}
