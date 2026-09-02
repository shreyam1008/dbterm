package main

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/osservice"
)

type fakeBackupServiceManager struct {
	status osservice.Status
	events []string
	err    map[string]error
}

func (manager *fakeBackupServiceManager) record(action string) error {
	manager.events = append(manager.events, action)
	if manager.err == nil {
		return nil
	}
	return manager.err[action]
}

func (manager *fakeBackupServiceManager) Install(context.Context) error {
	if err := manager.record("install"); err != nil {
		return err
	}
	manager.status.Installed = true
	manager.status.Running = true
	return nil
}

func (manager *fakeBackupServiceManager) Uninstall(context.Context) error {
	if err := manager.record("uninstall"); err != nil {
		return err
	}
	manager.status.Installed = false
	manager.status.Running = false
	return nil
}

func (manager *fakeBackupServiceManager) Start(context.Context) error {
	if err := manager.record("start"); err != nil {
		return err
	}
	manager.status.Running = true
	return nil
}

func (manager *fakeBackupServiceManager) Stop(context.Context) error {
	if err := manager.record("stop"); err != nil {
		return err
	}
	manager.status.Running = false
	return nil
}

func (manager *fakeBackupServiceManager) Status(context.Context) (osservice.Status, error) {
	if err := manager.record("status"); err != nil {
		return manager.status, err
	}
	return manager.status, nil
}

func (manager *fakeBackupServiceManager) SetStartupEnabled(_ context.Context, enabled bool) error {
	action := "disable-startup"
	if enabled {
		action = "enable-startup"
	}
	if err := manager.record(action); err != nil {
		return err
	}
	manager.status.StartupEnabled = enabled
	return nil
}

func TestRunBackupServiceActionRefusesUnmanagedAgentBeforeStartActions(t *testing.T) {
	for _, action := range []string{"install", "start", "restart"} {
		t.Run(action, func(t *testing.T) {
			manager := &fakeBackupServiceManager{status: osservice.Status{Manager: "test", Installed: true}}
			_, err := runBackupServiceAction(context.Background(), manager, action, func() (bool, error) {
				manager.events = append(manager.events, "probe-held")
				return true, nil
			}, func(context.Context, bool) error {
				t.Fatal("lock waiter called after unmanaged-agent precondition failed")
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "foreground `dbterm backup agent`") {
				t.Fatalf("runBackupServiceAction(%q) error = %v", action, err)
			}
			if got := strings.Join(manager.events, ","); got != "status,probe-held" {
				t.Fatalf("events = %q, want no lifecycle mutation", got)
			}
		})
	}
}

func TestRunBackupServiceRestartDrainsBeforeStarting(t *testing.T) {
	manager := &fakeBackupServiceManager{status: osservice.Status{Manager: "test", Installed: true, Running: true}}
	message, err := runBackupServiceAction(context.Background(), manager, "restart", func() (bool, error) {
		manager.events = append(manager.events, "probe-held")
		return true, nil
	}, func(_ context.Context, wantHeld bool) error {
		if wantHeld {
			manager.events = append(manager.events, "wait-acquired")
		} else {
			manager.events = append(manager.events, "wait-released")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "fully stopped") {
		t.Fatalf("message = %q", message)
	}
	want := "status,probe-held,stop,wait-released,start,wait-acquired,status"
	if got := strings.Join(manager.events, ","); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestRunBackupServiceStartIsIdempotentWhenManagedAgentOwnsLock(t *testing.T) {
	manager := &fakeBackupServiceManager{status: osservice.Status{Manager: "test", Installed: true, Running: true}}
	message, err := runBackupServiceAction(context.Background(), manager, "start", func() (bool, error) {
		manager.events = append(manager.events, "probe-held")
		return true, nil
	}, func(context.Context, bool) error {
		t.Fatal("healthy start waited for a lock transition")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "already running") {
		t.Fatalf("message = %q", message)
	}
	if got := strings.Join(manager.events, ","); got != "status,probe-held" {
		t.Fatalf("events = %q, want no service restart", got)
	}
}

func TestRunBackupServiceStartupControlDoesNotChangeRuntime(t *testing.T) {
	manager := &fakeBackupServiceManager{status: osservice.Status{Manager: "test", Installed: true, Running: true}}
	message, err := runBackupServiceAction(context.Background(), manager, "enable", func() (bool, error) {
		manager.events = append(manager.events, "probe-held")
		return true, nil
	}, func(context.Context, bool) error {
		t.Fatal("startup control waited for a process transition")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !manager.status.StartupEnabled || !manager.status.Running || !strings.Contains(message, "current running state was not changed") {
		t.Fatalf("enable result = status %#v, message %q", manager.status, message)
	}
	if got := strings.Join(manager.events, ","); got != "status,probe-held,enable-startup" {
		t.Fatalf("events = %q", got)
	}

	manager.events = nil
	_, err = runBackupServiceAction(context.Background(), manager, "disable", func() (bool, error) {
		manager.events = append(manager.events, "probe-held")
		return true, nil
	}, func(context.Context, bool) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if manager.status.StartupEnabled || !manager.status.Running {
		t.Fatalf("disable changed runtime or kept startup: %#v", manager.status)
	}
}

func TestRunBackupServiceInstallDrainsManagedAgentBeforeUpdate(t *testing.T) {
	manager := &fakeBackupServiceManager{status: osservice.Status{Manager: "test", Installed: true, Running: true}}
	_, err := runBackupServiceAction(context.Background(), manager, "install", func() (bool, error) {
		manager.events = append(manager.events, "probe-held")
		return true, nil
	}, func(_ context.Context, wantHeld bool) error {
		if wantHeld {
			manager.events = append(manager.events, "wait-acquired")
		} else {
			manager.events = append(manager.events, "wait-released")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "status,probe-held,stop,wait-released,install,wait-acquired,status"
	if got := strings.Join(manager.events, ","); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestRunBackupServiceStopReportsUnmanagedAgentStillRunning(t *testing.T) {
	manager := &fakeBackupServiceManager{status: osservice.Status{Manager: "test", Installed: true}}
	waitErr := errors.New("lock remains held")
	_, err := runBackupServiceAction(context.Background(), manager, "stop", func() (bool, error) {
		manager.events = append(manager.events, "probe-held")
		return true, nil
	}, func(_ context.Context, wantHeld bool) error {
		if wantHeld {
			t.Fatal("stop waited for scheduler lock acquisition")
		}
		manager.events = append(manager.events, "wait-released")
		return waitErr
	})
	if err == nil || !strings.Contains(err.Error(), "outside test still owns") || !errors.Is(err, waitErr) {
		t.Fatalf("runBackupServiceAction(stop) error = %v", err)
	}
	if got := strings.Join(manager.events, ","); got != "status,probe-held,stop,wait-released" {
		t.Fatalf("events = %q", got)
	}
}

func TestRunBackupServiceStopAndUninstallWaitForManagedRelease(t *testing.T) {
	for _, action := range []string{"stop", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			manager := &fakeBackupServiceManager{status: osservice.Status{Manager: "test", Installed: true, Running: true}}
			_, err := runBackupServiceAction(context.Background(), manager, action, func() (bool, error) {
				manager.events = append(manager.events, "probe-held")
				return true, nil
			}, func(_ context.Context, wantHeld bool) error {
				if wantHeld {
					t.Fatal("stop action waited for scheduler lock acquisition")
				}
				manager.events = append(manager.events, "wait-released")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			want := "status,probe-held," + action + ",wait-released"
			if got := strings.Join(manager.events, ","); got != want {
				t.Fatalf("events = %q, want %q", got, want)
			}
		})
	}
}

func TestWaitForAgentLockStatePollsAndPropagatesFailures(t *testing.T) {
	attempts := 0
	err := waitForAgentLockState(context.Background(), true, time.Second, time.Millisecond, func() (bool, error) {
		attempts++
		return attempts >= 3, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("probe attempts = %d, want 3", attempts)
	}

	wantErr := errors.New("probe failed")
	err = waitForAgentLockState(context.Background(), false, time.Second, time.Millisecond, func() (bool, error) {
		return false, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("probe error = %v, want wrapped %v", err, wantErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = waitForAgentLockState(ctx, true, time.Second, time.Second, func() (bool, error) { return false, nil })
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "stopped waiting") {
		t.Fatalf("canceled wait error = %v", err)
	}

	err = waitForAgentLockState(context.Background(), true, 5*time.Millisecond, time.Second, func() (bool, error) { return false, nil })
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timed-out wait error = %v", err)
	}
}

func TestFormatBackupServiceStatusDistinguishesRuntimeOwnership(t *testing.T) {
	status := osservice.Status{Manager: "test", Installed: true}
	if got := formatBackupServiceStatus(status, true); !strings.Contains(got, "outside this native service registration") {
		t.Fatalf("unmanaged status = %q", got)
	}
	status.Running = true
	if got := formatBackupServiceStatus(status, false); !strings.Contains(got, "starting or unhealthy") {
		t.Fatalf("lock-free managed status = %q", got)
	}
	if got := formatBackupServiceStatus(status, true); !strings.Contains(got, "owns the scheduler lock") {
		t.Fatalf("managed status = %q", got)
	}
}

func TestMaxDecodedBytesFromGiB(t *testing.T) {
	got, err := maxDecodedBytesFromGiB(3)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3<<30 {
		t.Fatalf("maxDecodedBytesFromGiB(3) = %d, want %d", got, int64(3<<30))
	}
	if _, err := maxDecodedBytesFromGiB(0); err == nil {
		t.Fatal("maxDecodedBytesFromGiB(0) succeeded")
	}
	if _, err := maxDecodedBytesFromGiB(uint64(math.MaxInt64)/(1<<30) + 1); err == nil {
		t.Fatal("maxDecodedBytesFromGiB accepted an overflowing value")
	}
}

func TestRedactBackupJobsForOutputDoesNotMutateStoredJobs(t *testing.T) {
	jobs := []backupcore.Job{{
		Name: "production",
		Notification: backupcore.EmailNotification{
			Username: "alerts@example.com",
			Password: "dedicated-app-password",
		},
	}}
	redacted := redactBackupJobsForOutput(jobs)
	if redacted[0].Notification.Password != "" {
		t.Fatalf("redacted password = %q", redacted[0].Notification.Password)
	}
	if jobs[0].Notification.Password != "dedicated-app-password" {
		t.Fatal("redaction mutated the stored job")
	}
}

func TestBackupPruneRequiresExplicitConsentBeforeOpeningCatalog(t *testing.T) {
	err := backupPruneCommand([]string{"production"})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("backupPruneCommand() error = %v, want explicit consent", err)
	}
}

func TestParseRestoreMode(t *testing.T) {
	tests := []struct {
		value string
		want  backupcore.RestoreMode
	}{
		{value: "merge", want: backupcore.RestoreModeMerge},
		{value: " CLEAN ", want: backupcore.RestoreModeClean},
	}
	for _, test := range tests {
		got, err := parseRestoreMode(test.value)
		if err != nil {
			t.Fatalf("parseRestoreMode(%q) error = %v", test.value, err)
		}
		if got != test.want {
			t.Fatalf("parseRestoreMode(%q) = %q, want %q", test.value, got, test.want)
		}
	}
	if _, err := parseRestoreMode("replace-everything"); err == nil {
		t.Fatal("parseRestoreMode accepted an unknown mode")
	}
}

func TestValidateRestoreConsent(t *testing.T) {
	merge := &backupcore.RestorePlan{
		Target:  config.ConnectionConfig{Type: config.PostgreSQL, Database: "appdb"},
		Options: backupcore.RestoreOptions{Mode: backupcore.RestoreModeMerge},
	}
	if err := validateRestoreConsent(merge, false, ""); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("merge without --yes error = %v, want explicit consent error", err)
	}
	if err := validateRestoreConsent(merge, true, ""); err != nil {
		t.Fatalf("merge with --yes error = %v", err)
	}

	clean := &backupcore.RestorePlan{
		Target:  config.ConnectionConfig{Type: config.PostgreSQL, Database: "production"},
		Options: backupcore.RestoreOptions{Mode: backupcore.RestoreModeClean},
	}
	if err := validateRestoreConsent(clean, true, "prod"); err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("clean with wrong confirmation error = %v, want exact target hint", err)
	}
	if err := validateRestoreConsent(clean, true, "production"); err != nil {
		t.Fatalf("clean with exact confirmation error = %v", err)
	}
}

func TestCleanRestoreConfirmationSQLiteUsesExactPath(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "data", "app.sqlite3")
	target := &config.ConnectionConfig{Type: config.SQLite, FilePath: targetPath}
	if got := cleanRestoreConfirmation(target); got != filepath.Clean(targetPath) {
		t.Fatalf("cleanRestoreConfirmation(SQLite) = %q, want %q", got, filepath.Clean(targetPath))
	}
}

func TestRestoreTargetSummaryDoesNotExposePassword(t *testing.T) {
	target := &config.ConnectionConfig{
		Name: "Production", Type: config.PostgreSQL, Host: "db.internal", Port: "5432",
		User: "backup", Password: "super-secret", Database: "appdb",
	}
	summary := restoreTargetSummary(target)
	if strings.Contains(summary, target.Password) {
		t.Fatalf("restore target summary exposed password: %q", summary)
	}
	if !strings.Contains(summary, "Production") || !strings.Contains(summary, "appdb") {
		t.Fatalf("restore target summary is missing safe identifying details: %q", summary)
	}
}

func TestResolveBackupCLIPathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir follows HOME on Unix and USERPROFILE on Windows.
	t.Setenv("USERPROFILE", home)
	got, err := resolveBackupCLIPath("~/nightly")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "nightly")
	if got != want {
		t.Fatalf("resolveBackupCLIPath() = %q, want %q", got, want)
	}
}

func TestResolveBackupCLIPathRejectsRcloneDestination(t *testing.T) {
	got, err := resolveBackupCLIPath("rclone://offsite/team//nightly/")
	if err == nil || !errors.Is(err, backupcore.ErrRcloneBackupPublicationDisabled) {
		t.Fatalf("resolveBackupCLIPath() = %q, %v; want fail-closed rclone error", got, err)
	}
}
