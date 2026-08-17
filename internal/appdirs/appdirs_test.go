package appdirs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func clearDirectoryOverrides(t *testing.T) {
	t.Helper()
	t.Setenv(configDirEnvName, "")
	t.Setenv(stateDirEnvName, "")
	t.Setenv(logDirEnvName, "")
}

func TestDirectoryOverridesTakePrecedence(t *testing.T) {
	root := t.TempDir()
	configOverride := filepath.Join(root, "custom-config")
	stateOverride := filepath.Join(root, "custom-state")
	logOverride := filepath.Join(root, "custom-logs")
	t.Setenv(configDirEnvName, configOverride)
	t.Setenv(stateDirEnvName, stateOverride)
	t.Setenv(logDirEnvName, logOverride)

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	stateDir, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir() error = %v", err)
	}
	logDir, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir() error = %v", err)
	}

	if configDir != configOverride || stateDir != stateOverride || logDir != logOverride {
		t.Fatalf("override mismatch: config=%q state=%q log=%q", configDir, stateDir, logDir)
	}
	for _, dir := range []string{configOverride, stateOverride, logOverride} {
		owned, err := IsOwnedDirectory(dir)
		if err != nil {
			t.Fatalf("IsOwnedDirectory(%q) error = %v", dir, err)
		}
		if !owned {
			t.Fatalf("new override directory %q should have an ownership marker", dir)
		}
	}
}

func TestNonEmptyOverrideDirectoryIsNotClaimed(t *testing.T) {
	root := t.TempDir()
	override := filepath.Join(root, "shared", appDirName)
	if err := os.MkdirAll(override, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(override, "unrelated.txt"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configDirEnvName, override)

	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	if got != override {
		t.Fatalf("ConfigDir() = %q, want %q", got, override)
	}
	owned, err := IsOwnedDirectory(override)
	if err != nil {
		t.Fatalf("IsOwnedDirectory() error = %v", err)
	}
	if owned {
		t.Fatal("existing non-empty override directory must not be claimed automatically")
	}
}

func TestInvalidOwnershipMarkerIsRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ownershipMarker), []byte("not dbterm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	owned, err := IsOwnedDirectory(dir)
	if err != nil {
		t.Fatalf("IsOwnedDirectory() error = %v", err)
	}
	if owned {
		t.Fatal("invalid ownership marker should not establish ownership")
	}
}

func TestLinuxUsesXDGDirectories(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-specific path assertion")
	}
	clearDirectoryOverrides(t)
	home := t.TempDir()
	configHome := filepath.Join(home, "xdg-config")
	stateHome := filepath.Join(home, "xdg-state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	stateDir, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir() error = %v", err)
	}
	logDir, err := LogDir()
	if err != nil {
		t.Fatalf("LogDir() error = %v", err)
	}

	if want := filepath.Join(configHome, appDirName); configDir != want {
		t.Fatalf("ConfigDir() = %q, want %q", configDir, want)
	}
	if want := filepath.Join(stateHome, appDirName); stateDir != want {
		t.Fatalf("StateDir() = %q, want %q", stateDir, want)
	}
	if want := filepath.Join(stateHome, appDirName, "logs"); logDir != want {
		t.Fatalf("LogDir() = %q, want %q", logDir, want)
	}
}

func TestConfigDirKeepsLegacyDirectoryInPlaceWhenNativeMissing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses Linux XDG_CONFIG_HOME to exercise distinct native and legacy paths")
	}
	clearDirectoryOverrides(t)
	home := t.TempDir()
	legacy := filepath.Join(home, ".config", appDirName)
	nativeRoot := filepath.Join(home, "native-config")
	native := filepath.Join(nativeRoot, appDirName)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", nativeRoot)

	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("create legacy config: %v", err)
	}
	legacyFile := filepath.Join(legacy, "connections.json")
	if err := os.WriteFile(legacyFile, []byte("[]\n"), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	if got != legacy {
		t.Fatalf("ConfigDir() = %q, want stable legacy path %q", got, legacy)
	}
	if _, err := os.Stat(legacyFile); err != nil {
		t.Fatalf("legacy connections file was moved or removed: %v", err)
	}
	if _, err := os.Lstat(native); !os.IsNotExist(err) {
		t.Fatalf("temporary native directory was created unexpectedly: %v", err)
	}
}

func TestConfigDirNeverMergesExistingNativeAndLegacyTrees(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "native")
	legacy := filepath.Join(root, "legacy")
	if err := os.MkdirAll(native, 0o700); err != nil {
		t.Fatalf("create native directory: %v", err)
	}
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("create legacy directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(native, "settings.json"), []byte("native"), 0o600); err != nil {
		t.Fatalf("write native marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "connections.json"), []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}

	got, err := selectConfigDir(native, legacy)
	if err != nil {
		t.Fatalf("selectConfigDir() error = %v", err)
	}
	if got != native {
		t.Fatalf("selectConfigDir() = %q, want native %q", got, native)
	}
	if _, err := os.Stat(filepath.Join(native, "connections.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy file was merged into native tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "connections.json")); err != nil {
		t.Fatalf("legacy tree was changed: %v", err)
	}
}

func TestConfigOverrideDoesNotMigrateLegacyDirectory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	legacy := filepath.Join(home, ".config", appDirName)
	override := filepath.Join(root, "explicit-config")
	t.Setenv("HOME", home)
	t.Setenv(configDirEnvName, override)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("create legacy directory: %v", err)
	}

	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	if got != override {
		t.Fatalf("ConfigDir() = %q, want override %q", got, override)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("explicit override changed legacy directory: %v", err)
	}
}
