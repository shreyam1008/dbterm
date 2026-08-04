package osservice

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeOptions(t *testing.T) {
	root := t.TempDir()
	wantExecutable := filepath.Join(root, "bin", "dbterm")
	wantLogDir := filepath.Join(root, "logs")

	got, err := normalizeOptions(Options{Executable: wantExecutable, LogDir: wantLogDir})
	if err != nil {
		t.Fatalf("normalizeOptions() error = %v", err)
	}
	if got.Executable != wantExecutable || got.LogDir != wantLogDir {
		t.Fatalf("normalizeOptions() = %#v, want executable %q and log dir %q", got, wantExecutable, wantLogDir)
	}
	if got.Scope != ScopeUser {
		t.Fatalf("normalizeOptions() scope = %q, want default %q", got.Scope, ScopeUser)
	}
}

func TestNormalizeSystemOptionsRequiresExplicitSafePaths(t *testing.T) {
	root := t.TempDir()
	options := Options{
		Executable: filepath.Join(root, "bin", "dbterm"),
		ConfigDir:  filepath.Join(root, "config"),
		StateDir:   filepath.Join(root, "state"),
		LogDir:     filepath.Join(root, "logs"),
		Scope:      ScopeSystem,
		RunAsUser:  "backup-user",
	}
	got, err := normalizeOptions(options)
	if err != nil {
		t.Fatalf("normalizeOptions() error = %v", err)
	}
	if got.Scope != ScopeSystem || got.RunAsUser != "backup-user" {
		t.Fatalf("normalizeOptions() = %#v", got)
	}
}

func TestNormalizeOptionsRejectsUnsafeValues(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name    string
		options Options
		want    string
	}{
		{name: "missing executable", options: Options{LogDir: root}, want: "executable is required"},
		{name: "relative executable", options: Options{Executable: "dbterm", LogDir: root}, want: "executable must be absolute"},
		{name: "go run executable", options: Options{Executable: filepath.Join(root, "go-build123", "b001", "exe", "dbterm"), LogDir: root}, want: "temporary `go run` executable"},
		{name: "missing log directory", options: Options{Executable: filepath.Join(root, "dbterm")}, want: "log directory is required"},
		{name: "relative log directory", options: Options{Executable: filepath.Join(root, "dbterm"), LogDir: "logs"}, want: "log directory must be absolute"},
		{name: "newline in executable", options: Options{Executable: filepath.Join(root, "dbterm") + "\nunit", LogDir: root}, want: "unsupported control character"},
		{name: "carriage return in log directory", options: Options{Executable: filepath.Join(root, "dbterm"), LogDir: root + "\rlogs"}, want: "unsupported control character"},
		{name: "unsupported scope", options: Options{Executable: filepath.Join(root, "dbterm"), LogDir: root, Scope: "global"}, want: "unsupported backup service scope"},
		{name: "system paths required", options: Options{Executable: filepath.Join(root, "dbterm"), LogDir: root, Scope: ScopeSystem}, want: "is required for system scope"},
		{name: "system root path rejected", options: Options{Executable: filepath.Join(root, "dbterm"), ConfigDir: string(filepath.Separator), StateDir: filepath.Join(root, "state"), LogDir: filepath.Join(root, "logs"), Scope: ScopeSystem}, want: "cannot be a filesystem root"},
		{name: "run as user only for system", options: Options{Executable: filepath.Join(root, "dbterm"), LogDir: root, RunAsUser: "operator"}, want: "only valid in system scope"},
		{name: "unsafe run as user", options: Options{Executable: filepath.Join(root, "dbterm"), ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), LogDir: filepath.Join(root, "logs"), Scope: ScopeSystem, RunAsUser: "bad user"}, want: "unsafe characters"},
		{name: "root run as user", options: Options{Executable: filepath.Join(root, "dbterm"), ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), LogDir: filepath.Join(root, "logs"), Scope: ScopeSystem, RunAsUser: "root"}, want: "must not run as root"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeOptions(test.options)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalizeOptions() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestSystemMutationRequiresElevation(t *testing.T) {
	err := requireElevation(ScopeSystem, "install backup service", func() (bool, error) { return false, nil })
	if err == nil || !RequiresElevation(err) {
		t.Fatalf("requireElevation() error = %v, want ErrElevationRequired", err)
	}
	if err := requireElevation(ScopeUser, "install backup service", func() (bool, error) { return false, nil }); err != nil {
		t.Fatalf("user scope unexpectedly requires elevation: %v", err)
	}
}
