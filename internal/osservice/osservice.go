// Package osservice installs and controls dbterm's backup agent using the
// service manager native to the host operating system.
package osservice

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	serviceDescription = "dbterm backup agent"
	backupLogName      = "dbterm-backup.log"
	backupErrorLogName = "dbterm-backup-error.log"
)

// Scope selects whether a backup agent is registered for the current user or
// for the whole machine.
type Scope string

const (
	// ScopeUser registers the backup agent with the current user's service
	// manager. It is the default when Options.Scope is empty.
	ScopeUser Scope = "user"
	// ScopeSystem registers the backup agent with the machine's service manager.
	// Mutating a system registration requires elevation.
	ScopeSystem Scope = "system"
)

// ErrElevationRequired identifies a system-service mutation that must be
// retried with administrator or root privileges.
var ErrElevationRequired = errors.New("service operation requires elevation")

// Options configures the command, runtime paths, and registration scope used
// by the backup agent.
type Options struct {
	Executable string
	ConfigDir  string
	StateDir   string
	LogDir     string
	Scope      Scope
	// RunAsUser selects the non-root account for a Unix system service. When it
	// is empty, dbterm resolves the real user that invoked the elevated process.
	// Windows system tasks always run as LocalSystem.
	RunAsUser string
}

// Status describes the registration and runtime state of the backup agent.
type Status struct {
	Installed      bool
	Running        bool
	StartupEnabled bool
	Detail         string
	Manager        string
	Name           string
	Scope          Scope
}

// Manager registers and controls one selected dbterm backup-agent scope.
//
// Manager intentionally retains its original method set so existing callers
// and test doubles remain source compatible. Managers returned by New also
// implement StartupManager and Restarter.
type Manager interface {
	Install(context.Context) error
	Uninstall(context.Context) error
	Start(context.Context) error
	Stop(context.Context) error
	Status(context.Context) (Status, error)
}

// StartupManager controls whether an installed agent starts automatically.
// Start and Stop control only the current runtime; this interface controls
// persistence independently.
type StartupManager interface {
	Manager
	SetStartupEnabled(context.Context, bool) error
}

// Restarter restarts an installed agent without changing its startup setting.
type Restarter interface {
	Manager
	Restart(context.Context) error
}

// SetStartupEnabled updates an agent's automatic-start setting while keeping
// Manager source compatible for existing integrations.
func SetStartupEnabled(ctx context.Context, manager Manager, enabled bool) error {
	startupManager, ok := manager.(interface {
		SetStartupEnabled(context.Context, bool) error
	})
	if !ok {
		return fmt.Errorf("backup service manager %T does not support startup control", manager)
	}
	return startupManager.SetStartupEnabled(ctx, enabled)
}

// Restart restarts an installed agent without changing its startup setting.
func Restart(ctx context.Context, manager Manager) error {
	restarter, ok := manager.(interface {
		Restart(context.Context) error
	})
	if !ok {
		return fmt.Errorf("backup service manager %T does not support restart", manager)
	}
	return restarter.Restart(ctx)
}

// RequiresElevation reports whether err asks the caller to retry a
// system-scope mutation with administrator or root privileges.
func RequiresElevation(err error) bool {
	return errors.Is(err, ErrElevationRequired)
}

// New returns the current platform's service manager for Options.Scope.
//
// Executable and LogDir must be absolute. The managed command is always the
// specified executable followed by "backup agent --service-mode" and explicit
// runtime paths. System scope additionally requires absolute ConfigDir and
// StateDir paths.
func New(options Options) (Manager, error) {
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	return newPlatformManager(normalized, execCommandRunner{})
}

func normalizeOptions(options Options) (Options, error) {
	scope, err := normalizeScope(options.Scope)
	if err != nil {
		return Options{}, err
	}
	options.Scope = scope

	if strings.TrimSpace(options.Executable) == "" {
		return Options{}, fmt.Errorf("backup agent executable is required")
	}
	if !filepath.IsAbs(options.Executable) {
		return Options{}, fmt.Errorf("backup agent executable must be absolute: %q", options.Executable)
	}
	if transientGoRunExecutable(options.Executable) {
		return Options{}, fmt.Errorf("cannot install the backup agent from a temporary `go run` executable (%s); build or install dbterm at a stable path, launch that binary, and retry", options.Executable)
	}
	if strings.TrimSpace(options.LogDir) == "" {
		return Options{}, fmt.Errorf("backup agent log directory is required")
	}
	if !filepath.IsAbs(options.LogDir) {
		return Options{}, fmt.Errorf("backup agent log directory must be absolute: %q", options.LogDir)
	}
	if containsDefinitionControl(options.Executable) {
		return Options{}, fmt.Errorf("backup agent executable contains an unsupported control character")
	}
	if containsDefinitionControl(options.LogDir) {
		return Options{}, fmt.Errorf("backup agent log directory contains an unsupported control character")
	}
	for _, path := range []struct{ label, value string }{
		{label: "config directory", value: options.ConfigDir},
		{label: "state directory", value: options.StateDir},
	} {
		label, value := path.label, path.value
		if strings.TrimSpace(value) == "" {
			if scope == ScopeSystem {
				return Options{}, fmt.Errorf("backup agent %s is required for system scope", label)
			}
			continue
		}
		if !filepath.IsAbs(value) {
			return Options{}, fmt.Errorf("backup agent %s must be absolute: %q", label, value)
		}
		if containsDefinitionControl(value) {
			return Options{}, fmt.Errorf("backup agent %s contains an unsupported control character", label)
		}
	}
	if scope == ScopeSystem {
		for _, path := range []struct{ label, value string }{
			{label: "config directory", value: options.ConfigDir},
			{label: "state directory", value: options.StateDir},
			{label: "log directory", value: options.LogDir},
		} {
			label, value := path.label, path.value
			if isFilesystemRoot(value) {
				return Options{}, fmt.Errorf("backup agent %s cannot be a filesystem root in system scope: %q", label, value)
			}
		}
	}
	if options.RunAsUser != "" {
		options.RunAsUser = strings.TrimSpace(options.RunAsUser)
		if scope != ScopeSystem {
			return Options{}, fmt.Errorf("backup agent run-as user is only valid in system scope")
		}
		if err := validateRunAsUser(options.RunAsUser); err != nil {
			return Options{}, err
		}
	}

	options.Executable = filepath.Clean(options.Executable)
	if options.ConfigDir != "" {
		options.ConfigDir = filepath.Clean(options.ConfigDir)
	}
	if options.StateDir != "" {
		options.StateDir = filepath.Clean(options.StateDir)
	}
	options.LogDir = filepath.Clean(options.LogDir)
	return options, nil
}

func normalizeScope(scope Scope) (Scope, error) {
	switch scope {
	case "", ScopeUser:
		return ScopeUser, nil
	case ScopeSystem:
		return ScopeSystem, nil
	default:
		return "", fmt.Errorf("unsupported backup service scope %q (want %q or %q)", scope, ScopeUser, ScopeSystem)
	}
}

func isFilesystemRoot(value string) bool {
	cleaned := filepath.Clean(value)
	return filepath.Dir(cleaned) == cleaned
}

func validateRunAsUser(value string) error {
	if value == "" {
		return fmt.Errorf("backup agent run-as user is empty")
	}
	if strings.EqualFold(value, "root") {
		return fmt.Errorf("backup agent system service must not run as root; choose the invoking non-root user")
	}
	for index, character := range value {
		first := index == 0
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character == '_' || !first && character >= '0' && character <= '9' || !first && (character == '-' || character == '.') {
			continue
		}
		return fmt.Errorf("backup agent run-as user %q contains unsafe characters", value)
	}
	return nil
}

func baseStatus(manager, name string, scope Scope) Status {
	if scope == "" {
		scope = ScopeUser
	}
	return Status{Manager: manager, Name: name, Scope: scope}
}

func transientGoRunExecutable(value string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(filepath.Clean(value), `\`, "/"))
	parts := strings.Split(normalized, "/")
	for index, part := range parts {
		if !strings.HasPrefix(part, "go-build") {
			continue
		}
		for _, descendant := range parts[index+1:] {
			if descendant == "exe" {
				return true
			}
		}
	}
	return false
}

func containsDefinitionControl(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n")
}
