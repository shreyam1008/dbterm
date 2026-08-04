//go:build linux || darwin

package osservice

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSystemRuntimePathsForRunAsUser(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current() error = %v", err)
	}
	root := t.TempDir()
	options := systemRuntimePathFixture(t, root)
	if err := validateSystemRuntimePaths(options, current.Username); err != nil {
		t.Fatalf("validateSystemRuntimePaths() error = %v", err)
	}
}

func TestValidateSystemRuntimePathsRefusesMissingLogDirectory(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current() error = %v", err)
	}
	root := t.TempDir()
	options := systemRuntimePathFixture(t, root)
	if err := os.Remove(options.LogDir); err != nil {
		t.Fatalf("remove log fixture: %v", err)
	}
	err = validateSystemRuntimePaths(options, current.Username)
	if err == nil || !strings.Contains(err.Error(), "create it as the run-as user before elevated installation") {
		t.Fatalf("validateSystemRuntimePaths() error = %v", err)
	}
}

func TestValidateSystemRuntimePathsRefusesUnwritableStateDirectory(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current() error = %v", err)
	}
	root := t.TempDir()
	options := systemRuntimePathFixture(t, root)
	if err := os.Chmod(options.StateDir, 0o500); err != nil {
		t.Fatalf("chmod state fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(options.StateDir, 0o700) })
	err = validateSystemRuntimePaths(options, current.Username)
	if err == nil || !strings.Contains(err.Error(), "required mode permissions 007") {
		t.Fatalf("validateSystemRuntimePaths() error = %v", err)
	}
}

func systemRuntimePathFixture(t *testing.T, root string) Options {
	t.Helper()
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
			t.Fatalf("create runtime fixture: %v", err)
		}
	}
	return options
}
