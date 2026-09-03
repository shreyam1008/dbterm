package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/osservice"
)

type fakeBackupAgentManager struct {
	status    osservice.Status
	calls     []string
	onInstall func()
	onStart   func()
	onStop    func()
	onStartup func(bool)
}

func (m *fakeBackupAgentManager) Install(context.Context) error {
	m.calls = append(m.calls, "install")
	m.status.Installed = true
	m.status.Running = true
	if m.onInstall != nil {
		m.onInstall()
	}
	return nil
}

func (m *fakeBackupAgentManager) Uninstall(context.Context) error {
	m.calls = append(m.calls, "uninstall")
	m.status.Installed = false
	m.status.Running = false
	if m.onStop != nil {
		m.onStop()
	}
	return nil
}

func (m *fakeBackupAgentManager) Start(context.Context) error {
	m.calls = append(m.calls, "start")
	m.status.Running = true
	if m.onStart != nil {
		m.onStart()
	}
	return nil
}

func (m *fakeBackupAgentManager) Stop(context.Context) error {
	m.calls = append(m.calls, "stop")
	m.status.Running = false
	if m.onStop != nil {
		m.onStop()
	}
	return nil
}

func (m *fakeBackupAgentManager) Status(context.Context) (osservice.Status, error) {
	m.calls = append(m.calls, "status")
	return m.status, nil
}

func (m *fakeBackupAgentManager) SetStartupEnabled(_ context.Context, enabled bool) error {
	m.calls = append(m.calls, "startup:"+map[bool]string{true: "on", false: "off"}[enabled])
	m.status.StartupEnabled = enabled
	if m.onStartup != nil {
		m.onStartup(enabled)
	}
	return nil
}

func TestDescribeBackupAgentStatusSeparatesForegroundFromNativeService(t *testing.T) {
	heartbeat := backupcore.AgentStatus{Healthy: true, PID: 4242, Heartbeat: time.Now()}
	view := describeBackupAgentStatus(osservice.Status{Installed: true, Running: false}, true, heartbeat)
	if !view.foreground {
		t.Fatal("live process with stopped native manager was not classified as foreground")
	}
	if !strings.Contains(view.registration, "stopped") {
		t.Fatalf("registration = %q, want stopped", view.registration)
	}
	if !strings.Contains(view.process, "4242") {
		t.Fatalf("process = %q, want heartbeat PID", view.process)
	}
	if !strings.Contains(view.note, "Ctrl+C") {
		t.Fatalf("note = %q, want foreground stop guidance", view.note)
	}
}

func TestDescribeBackupAgentStatusDoesNotTrustHeartbeatWithoutLock(t *testing.T) {
	view := describeBackupAgentStatus(osservice.Status{Installed: true}, false, backupcore.AgentStatus{Healthy: true, PID: 77, Heartbeat: time.Now()})
	if view.foreground {
		t.Fatal("stale heartbeat was classified as a foreground process")
	}
	if view.process != "not running" {
		t.Fatalf("process = %q, want not running", view.process)
	}
	if !strings.Contains(view.note, "stale") {
		t.Fatalf("note = %q, want stale-heartbeat explanation", view.note)
	}
}

func TestBackupRunNotificationSummary(t *testing.T) {
	tests := []struct {
		name string
		run  backupcore.Run
		want string
	}{
		{name: "not requested", want: "email not requested"},
		{name: "attempted", run: backupcore.Run{NotificationAttempted: true}, want: "email attempted"},
		{name: "failed", run: backupcore.Run{NotificationAttempted: true, NotificationError: "SMTP unavailable"}, want: "email failed"},
		{name: "sent", run: backupcore.Run{NotificationAttempted: true, NotificationSent: true}, want: "email sent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := backupRunNotificationSummary(test.run); got != test.want {
				t.Fatalf("backupRunNotificationSummary() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBackupNotificationDraftKeepsSMTPSettingsWhenDeliveryDisabled(t *testing.T) {
	draft := backupJobFormDraft{
		job: backupcore.Job{Notification: backupcore.EmailNotification{
			Policy: backupcore.NotificationNever, SMTPHost: "smtp.example.test",
		}},
		smtpPort: "2525",
	}
	notification, err := backupNotificationFromDraft(&draft)
	if err != nil {
		t.Fatalf("build notification draft: %v", err)
	}
	if notification.SMTPPort != 2525 {
		t.Fatalf("SMTP port = %d, want persisted 2525", notification.SMTPPort)
	}
}

func TestExecuteBackupAgentActionRefusesForegroundAgent(t *testing.T) {
	manager := &fakeBackupAgentManager{status: osservice.Status{Installed: true}}
	err := executeBackupAgentAction(context.Background(), manager, "start", func() (bool, error) { return true, nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "foreground") {
		t.Fatalf("error = %v, want foreground-agent refusal", err)
	}
	if got := strings.Join(manager.calls, ","); got != "status" {
		t.Fatalf("manager calls = %q, want status only", got)
	}
}

func TestExecuteBackupAgentRestartObservesLockReleaseBeforeStart(t *testing.T) {
	manager := &fakeBackupAgentManager{status: osservice.Status{Installed: true, Running: true}}
	probeCalls := 0
	releaseObserved := false
	manager.onStart = func() {
		if !releaseObserved {
			t.Fatal("native Start ran before the previous process lock was released")
		}
	}
	probe := func() (bool, error) {
		probeCalls++
		switch probeCalls {
		case 1, 2:
			return true, nil
		case 3:
			releaseObserved = true
			return false, nil
		default:
			return true, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := executeBackupAgentAction(ctx, manager, "restart", probe, nil); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	if got := strings.Join(manager.calls, ","); got != "status,stop,start" {
		t.Fatalf("manager calls = %q, want status,stop,start", got)
	}
}

func TestExecuteBackupAgentStopReportsLockThatWillNotRelease(t *testing.T) {
	manager := &fakeBackupAgentManager{status: osservice.Status{Installed: true, Running: true}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := executeBackupAgentAction(ctx, manager, "stop", func() (bool, error) { return true, nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "still owns its lock") {
		t.Fatalf("error = %v, want unreleased-lock detail", err)
	}
}

func TestExecuteBackupAgentActionControlsStartupSeparately(t *testing.T) {
	manager := &fakeBackupAgentManager{status: osservice.Status{Installed: true}}
	if err := executeBackupAgentAction(context.Background(), manager, "enable", func() (bool, error) { return false, nil }, nil); err != nil {
		t.Fatalf("enable startup: %v", err)
	}
	if got := strings.Join(manager.calls, ","); got != "status,startup:on" {
		t.Fatalf("manager calls = %q, want status,startup:on", got)
	}
	if !manager.status.StartupEnabled {
		t.Fatal("startup was not enabled")
	}
}

func TestParseBackupDecodedGiB(t *testing.T) {
	got, err := parseBackupDecodedGiB(" 8 ")
	if err != nil {
		t.Fatalf("parse decoded limit: %v", err)
	}
	if got != 8*backupDecodedGiB {
		t.Fatalf("decoded limit = %d, want %d", got, 8*backupDecodedGiB)
	}
	for _, value := range []string{"", "0", "-1", "9223372036854775807"} {
		if _, err := parseBackupDecodedGiB(value); err == nil {
			t.Fatalf("parseBackupDecodedGiB(%q) succeeded, want error", value)
		}
	}
}

func TestDefaultBackupRestoreModeOnlyReplacesSQLiteSnapshots(t *testing.T) {
	if got := defaultBackupRestoreMode(&backupcore.Inspection{Format: backupcore.FormatSQLiteDatabase}); got != 1 {
		t.Fatalf("SQLite snapshot default mode index = %d, want clean/replace", got)
	}
	for _, format := range []backupcore.Format{
		backupcore.FormatSQLiteSQL,
		backupcore.FormatPostgresSQL,
		backupcore.FormatPostgresCustom,
		backupcore.FormatMySQLSQL,
	} {
		if got := defaultBackupRestoreMode(&backupcore.Inspection{Format: format}); got != 0 {
			t.Errorf("%s default mode index = %d, want merge", format, got)
		}
	}
	if got := defaultBackupRestoreMode(&backupcore.Inspection{Format: backupcore.FormatDBTermBundle, DatabaseFormat: backupcore.FormatSQLiteDatabase}); got != 1 {
		t.Fatalf("SQLite bundle default mode index = %d, want clean/replace", got)
	}
	if got := defaultBackupRestoreMode(&backupcore.Inspection{Format: backupcore.FormatDBTermBundle, DatabaseFormat: backupcore.FormatMySQLSQL}); got != 0 {
		t.Fatalf("MySQL bundle default mode index = %d, want merge", got)
	}
	if got := defaultBackupRestoreMode(nil); got != 0 {
		t.Fatalf("nil inspection default mode index = %d, want merge", got)
	}
}

func TestRestoreConfirmationValueUsesCompleteNormalizedSQLitePath(t *testing.T) {
	target := config.ConnectionConfig{Type: config.SQLite, FilePath: filepath.Join("restore-target", "orders.sqlite3")}
	want, err := filepath.Abs(filepath.Clean(target.FilePath))
	if err != nil {
		t.Fatal(err)
	}
	if got := restoreConfirmationValue(target); got != want {
		t.Fatalf("restoreConfirmationValue() = %q, want complete normalized path %q", got, want)
	}
	if got := restoreConfirmationValue(target); got == filepath.Base(target.FilePath) {
		t.Fatalf("restore confirmation fell back to ambiguous basename %q", got)
	}
}

func TestRestoreTargetFormDisplaysCompleteSQLiteConfirmation(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	pages.AddPage(pageBackupCenter, tview.NewTextView(), true, true)
	application.SetRoot(pages, true)
	target := config.ConnectionConfig{
		ID: "restore_target", Name: "Restore target", Type: config.SQLite,
		FilePath: "orders.sqlite3",
	}
	app := &App{
		app: application, pages: pages, store: &config.Store{Connections: []config.ConnectionConfig{target}},
		lastScreenW: 160, lastScreenH: 40,
	}
	screen := tcell.NewSimulationScreen("UTF-8")
	application.SetScreen(screen)
	screen.SetSize(160, 40)
	t.Cleanup(screen.Fini)

	app.showRestoreTargetForm(&backupcore.Inspection{
		Format: backupcore.FormatSQLiteDatabase, Engine: config.SQLite, Confidence: "high",
	}, "", backupcore.DefaultMaxDecodedBytes, nil, pageBackupCenter)
	application.ForceDraw()
	rendered := backupSimulationScreenText(screen)
	want := restoreConfirmationValue(target)
	if !strings.Contains(rendered, "Clean target (type exactly)") || !strings.Contains(rendered, want) {
		t.Fatalf("restore target form did not display the complete normalized confirmation %q:\n%s", want, rendered)
	}
}

func TestParseBackupRestoreFileTargets(t *testing.T) {
	targets, err := parseBackupRestoreFileTargets(` photos = D:\Restore\Photos ; documents = D:\Restore\Documents `)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Label != "photos" || targets[0].Root != `D:\Restore\Photos` ||
		targets[1].Label != "documents" || targets[1].Root != `D:\Restore\Documents` {
		t.Fatalf("targets = %#v", targets)
	}
	if targets, err := parseBackupRestoreFileTargets("  "); err != nil || targets != nil {
		t.Fatalf("blank targets = %#v, %v; want nil", targets, err)
	}
	for _, invalid := range []string{"photos", "=folder", "photos=", "photos=one;broken"} {
		if _, err := parseBackupRestoreFileTargets(invalid); err == nil {
			t.Errorf("parseBackupRestoreFileTargets(%q) succeeded", invalid)
		}
	}
}

func TestBackupBundleRestoreFormRendersAtCommonTerminalSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 30}, {120, 35}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			application := tview.NewApplication()
			pages := tview.NewPages()
			pages.AddPage(pageBackupCenter, tview.NewTextView(), true, true)
			application.SetRoot(pages, true)
			app := &App{
				app: application, pages: pages, lastScreenW: size.width, lastScreenH: size.height,
				store: &config.Store{Connections: []config.ConnectionConfig{{
					ID: "restore", Name: "Isolated restore", Type: config.MySQL, Host: "127.0.0.1", Port: "3306", Database: "restore_test",
				}}},
			}
			screen := tcell.NewSimulationScreen("UTF-8")
			application.SetScreen(screen)
			screen.SetSize(size.width, size.height)
			t.Cleanup(screen.Fini)

			app.showRestoreTargetForm(&backupcore.Inspection{
				Format: backupcore.FormatDBTermBundle, DatabaseFormat: backupcore.FormatMySQLSQL,
				Engine: config.MySQL, Confidence: backupcore.ConfidenceExact,
				FileSets: []backupcore.ManifestFileSet{{Label: "profile-photos", FileCount: 234, SizeBytes: 12 << 20}},
			}, "", backupcore.DefaultMaxDecodedBytes, nil, pageBackupCenter)
			application.ForceDraw()
			rendered := backupSimulationScreenText(screen)
			t.Logf("%dx%d bundle restore form:\n%s", size.width, size.height, rendered)
			for _, want := range []string{"Restore Destination", "Isolated restore", "Bundle files", "profile-photos", "File targets", "blank =", "Review Restore", "Esc Cancel"} {
				if !strings.Contains(rendered, want) {
					t.Errorf("render missing %q:\n%s", want, rendered)
				}
			}
		})
	}
}

func TestRenderBackupProgressShowsDeterminateStats(t *testing.T) {
	text := renderBackupProgress(backupcore.ProgressEvent{
		Phase: "compress", Message: "wrapping artifact",
		CurrentBytes: 512 * 1024 * 1024, TotalBytes: 1024 * 1024 * 1024,
		Elapsed: 2 * time.Second,
	}, 20)
	for _, want := range []string{"COMPRESS", "wrapping artifact", "50.0%", "512.0 MiB", "1.0 GiB", "rate", "ETA"} {
		if !strings.Contains(text, want) {
			t.Errorf("progress text missing %q:\n%s", want, text)
		}
	}
}

func TestParseOptionalGiBAllowsDecimalAndRejectsInvalid(t *testing.T) {
	got, err := parseOptionalGiB("1.5")
	if err != nil {
		t.Fatalf("parseOptionalGiB: %v", err)
	}
	if got != int64(3*backupDecodedGiB/2) {
		t.Fatalf("bytes = %d, want %d", got, 3*backupDecodedGiB/2)
	}
	for _, value := range []string{"", "-1", "NaN", "1.2.3"} {
		if _, err := parseOptionalGiB(value); err == nil {
			t.Errorf("parseOptionalGiB(%q) succeeded", value)
		}
	}
}

func TestLoadBackupAgentLogTailIsBoundedAndIncludesPath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "dbterm-backup-agent.log")
	payload := strings.Repeat("old-line\n", 20) + "new-line\n"
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	content, paths, err := loadBackupAgentLogTail(directory, 32)
	if err != nil {
		t.Fatalf("load log tail: %v", err)
	}
	if len(paths) != 1 || paths[0] != path {
		t.Fatalf("paths = %#v, want %q", paths, path)
	}
	if !strings.Contains(content, "new-line") || strings.Contains(content, strings.Repeat("old-line\n", 10)) {
		t.Fatalf("unexpected bounded tail:\n%s", content)
	}
}

func TestBackupAgentScopeSummaryShowsScopeAndStartup(t *testing.T) {
	text := backupAgentScopeSummary(backupAgentScopeState{
		scope:  osservice.ScopeSystem,
		status: osservice.Status{Installed: true, Running: true, StartupEnabled: true, Scope: osservice.ScopeSystem, Manager: "systemd", Name: "dbterm-backup.service"},
	})
	for _, want := range []string{"Scope: system", "Registration: installed", "Runtime: manager reports running", "Startup: on"} {
		if !strings.Contains(text, want) {
			t.Errorf("scope summary missing %q:\n%s", want, text)
		}
	}
}

func TestShellQuotesKeepElevatedTemplateArgumentsDistinct(t *testing.T) {
	if got := unixShellQuote("/tmp/DB Term/it's"); got != `'/tmp/DB Term/it'"'"'s'` {
		t.Fatalf("unix quote = %q", got)
	}
	if got := windowsCommandQuote(`C:\Program Files\dbterm.exe`); got != `"C:\Program Files\dbterm.exe"` {
		t.Fatalf("windows quote = %q", got)
	}
}

func TestBackupCenterFooterFitsCommonTerminalWidths(t *testing.T) {
	for _, width := range []int{80, 120, 180} {
		footer := backupCenterFooterText(width)
		if got := tview.TaggedStringWidth(footer); got > width {
			t.Errorf("footer width = %d, terminal width = %d: %s", got, width, footer)
		}
	}
}

func TestBackupAutomationStatusDoesNotCallSchedulerReadinessProtection(t *testing.T) {
	tests := []struct {
		name           string
		jobCount       int
		scheduledCount int
		agentHealthy   bool
		want           string
	}{
		{name: "empty", want: "setup needed"},
		{name: "manual only", jobCount: 1, want: "on demand"},
		{name: "agent missing", jobCount: 1, scheduledCount: 1, want: "agent needed"},
		{name: "scheduler ready", jobCount: 1, scheduledCount: 1, agentHealthy: true, want: "schedule ready"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := backupAutomationStatus(test.jobCount, test.scheduledCount, test.agentHealthy)
			if !strings.Contains(got, test.want) {
				t.Fatalf("automation status = %q, want %q", got, test.want)
			}
			if strings.Contains(strings.ToLower(got), "protected") {
				t.Fatalf("automation status %q claims a protection state", got)
			}
		})
	}
}

func TestLatestVerifiedBackupRunsRequiresSuccessfulUnprunedArtifact(t *testing.T) {
	base := time.Date(2026, time.September, 3, 1, 0, 0, 0, time.UTC)
	verified := func(id, jobID string, finished time.Time) backupcore.Run {
		return backupcore.Run{
			ID: id, JobID: jobID, Status: backupcore.RunSucceeded, StartedAt: finished.Add(-time.Minute), FinishedAt: finished,
			Artifact: backupcore.Artifact{Path: id + ".dump", Verified: true, CreatedAt: finished.Add(-time.Minute)},
		}
	}
	older := verified("run_old", "orders", base)
	newer := verified("run_new", "orders", base.Add(time.Hour))
	pruned := verified("run_pruned", "orders", base.Add(2*time.Hour))
	pruned.Artifact.PrunedAt = base.Add(3 * time.Hour)
	unverified := verified("run_unverified", "orders", base.Add(4*time.Hour))
	unverified.Artifact.Verified = false
	failed := verified("run_failed", "orders", base.Add(5*time.Hour))
	failed.Status = backupcore.RunFailed
	incomplete := verified("run_incomplete", "orders", base.Add(6*time.Hour))
	incomplete.Artifact.PublicationState = backupcore.ArtifactPublicationArtifactOnly
	other := verified("run_other", "customers", base.Add(6*time.Hour))

	got := latestVerifiedBackupRuns([]backupcore.Run{older, failed, pruned, other, incomplete, unverified, newer})
	if len(got) != 2 {
		t.Fatalf("verified job count = %d, want 2: %#v", len(got), got)
	}
	if got["orders"].ID != newer.ID {
		t.Fatalf("orders recovery point = %q, want %q", got["orders"].ID, newer.ID)
	}
	if got["customers"].ID != other.ID {
		t.Fatalf("customers recovery point = %q, want %q", got["customers"].ID, other.ID)
	}
}

func TestBackupProtectionSummarySeparatesCopyLocationAndCount(t *testing.T) {
	verifiedAt := time.Date(2026, time.September, 3, 1, 4, 0, 0, time.Local)
	localDestination := t.TempDir()
	localArtifact := filepath.Join(localDestination, "orders.dump")
	if err := os.WriteFile(localArtifact, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name           string
		destination    string
		wantKind       string
		wantProtection string
		wantCount      string
		wantDetail     string
		wantCopyDetail string
		reject         []string
	}{
		{name: "local", destination: localDestination, wantKind: "LOCAL", wantProtection: "LOCAL COPY PRESENT", wantCount: "1 found", wantDetail: "last verified", wantCopyDetail: "checksum not re-read", reject: []string{"ONE VERIFIED COPY", "1 verified", "checksum verified"}},
		{name: "rclone", destination: "rclone://vault/dbterm", wantKind: "rclone", wantProtection: "LEGACY REMOTE COPY RECORDED", wantCount: "1 legacy record", wantDetail: "size checked", wantCopyDetail: "availability not rechecked", reject: []string{"LOCAL COPY PRESENT", "checksum verified"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := backupcore.Run{FinishedAt: verifiedAt, Artifact: backupcore.Artifact{PublicationState: backupcore.ArtifactPublicationComplete}}
			if test.name == "local" {
				run.Artifact.Path = localArtifact
				run.Artifact.Size = int64(len("safe"))
			} else {
				run.Artifact.Path = test.destination + "/orders.dump"
			}
			protection, copies := backupProtectionSummary(backupcore.Job{Destination: test.destination}, run, true)
			for _, want := range []string{test.wantProtection, test.wantKind, test.wantDetail, "Sep 03 01:04"} {
				if want == "" {
					continue
				}
				if !strings.Contains(protection, want) {
					t.Errorf("protection summary %q missing %q", protection, want)
				}
			}
			for _, want := range []string{test.wantCount, test.wantCopyDetail} {
				if !strings.Contains(copies, want) {
					t.Errorf("copies summary %q missing %q", copies, want)
				}
			}
			for _, rejected := range test.reject {
				if strings.Contains(protection, rejected) || strings.Contains(copies, rejected) {
					t.Errorf("copy summary overstates verification with %q: protection=%q copies=%q", rejected, protection, copies)
				}
			}
		})
	}

	protection, copies := backupProtectionSummary(backupcore.Job{Destination: "rclone://vault/dbterm"}, backupcore.Run{}, false)
	if !strings.Contains(protection, "NO LEGACY REMOTE COPY") || !strings.Contains(protection, "rclone") {
		t.Fatalf("empty protection summary = %q", protection)
	}
	if !strings.Contains(copies, "0 recorded") || !strings.Contains(copies, "new rclone publication disabled") {
		t.Fatalf("empty copies summary = %q", copies)
	}
	protection, copies = backupProtectionSummary(backupcore.Job{Destination: filepath.Join(t.TempDir(), "backups")}, backupcore.Run{}, false)
	if !strings.Contains(protection, "NO SUCCESSFUL COPY RECORDED") || !strings.Contains(copies, "0 recorded") {
		t.Fatalf("empty local summary = protection %q, copies %q", protection, copies)
	}
	missingRun := backupcore.Run{FinishedAt: verifiedAt, Artifact: backupcore.Artifact{Path: filepath.Join(localDestination, "missing.dump"), Size: 4}}
	protection, copies = backupProtectionSummary(backupcore.Job{Destination: localDestination}, missingRun, true)
	if !strings.Contains(protection, "RECORDED COPY MISSING") || !strings.Contains(copies, "0 found") {
		t.Fatalf("missing local summary = protection %q, copies %q", protection, copies)
	}

	remoteRun := backupcore.Run{FinishedAt: verifiedAt, Artifact: backupcore.Artifact{Path: "rclone://old-vault/orders.dump"}}
	protection, _ = backupProtectionSummary(backupcore.Job{Destination: localDestination}, remoteRun, true)
	if !strings.Contains(protection, "LEGACY REMOTE COPY RECORDED") {
		t.Fatalf("edited remote-to-local job mislabeled its recorded artifact: %q", protection)
	}
	localRun := backupcore.Run{FinishedAt: verifiedAt, Artifact: backupcore.Artifact{Path: localArtifact, Size: int64(len("safe"))}}
	protection, _ = backupProtectionSummary(backupcore.Job{Destination: "rclone://new-vault/orders"}, localRun, true)
	if !strings.Contains(protection, "LOCAL COPY PRESENT") {
		t.Fatalf("edited local-to-remote job mislabeled its recorded artifact: %q", protection)
	}
}

func TestBackupRunSummaryUsesCompletionTime(t *testing.T) {
	started := time.Date(2026, time.September, 3, 1, 3, 0, 0, time.Local)
	finished := started.Add(time.Minute)
	summary := backupRunSummary(backupcore.Run{Status: backupcore.RunSucceeded, StartedAt: started, FinishedAt: finished})
	if !strings.Contains(summary, "Sep 03 01:04") || strings.Contains(summary, "Sep 03 01:03") {
		t.Fatalf("run summary = %q, want completion time", summary)
	}
}

func TestBackupEncryptionLabelNamesAgeStandard(t *testing.T) {
	if got := backupEncryptionLabel(backupcore.EncryptionAge); got != "age (X25519 recipient)" {
		t.Fatalf("age encryption label = %q", got)
	}
	if got := backupEncryptionLabel(backupcore.EncryptionNone); got != "not encrypted" {
		t.Fatalf("unencrypted label = %q", got)
	}
}

func TestBackupConnectionChoicesKeepSavedConnectionsBeforeAddAction(t *testing.T) {
	connections := []config.ConnectionConfig{
		{ID: "conn_local", Name: "local", Type: config.SQLite, FilePath: "/tmp/local.db"},
		{ID: "conn_prod", Name: "production", Type: config.PostgreSQL, Host: "db.example", Port: "5432", Database: "app"},
		{ID: "conn_server", Name: "server", Type: config.MySQL, Host: "db.example", Port: "3306"},
	}
	choices := backupConnectionChoices(connections)
	if len(choices) != 4 {
		t.Fatalf("choice count = %d, want 4", len(choices))
	}
	if choices[0].connectionID != "conn_local" || choices[1].connectionID != "conn_prod" {
		t.Fatalf("saved connection order changed: %#v", choices)
	}
	if !choices[2].requiresDatabaseChoice || !strings.Contains(choices[2].detail, "choose") {
		t.Fatalf("server-only choice = %#v, want database guidance", choices[2])
	}
	if !choices[3].addNew || !strings.Contains(choices[3].name, "Add") {
		t.Fatalf("last choice = %#v, want explicit add action", choices[3])
	}
}

func TestBackupConnectionSummaryIsUsefulAndNeverIncludesSecrets(t *testing.T) {
	connection := config.ConnectionConfig{
		Name: "production", Type: config.PostgreSQL, Host: "db.example", Port: "5432",
		Database: "app", User: "operator", Password: "do-not-show", AuthToken: "also-secret",
	}
	summary := backupConnectionSummary(connection)
	for _, want := range []string{"PostgreSQL", "db.example:5432/app"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q missing %q", summary, want)
		}
	}
	for _, secret := range []string{connection.Password, connection.AuthToken, connection.User} {
		if strings.Contains(summary, secret) {
			t.Errorf("summary %q contains credential %q", summary, secret)
		}
	}
}

func TestBackupJobConnectionDetailCallsOutDeletedConnection(t *testing.T) {
	detail := backupJobConnectionDetail(nil, "deleted")
	if !strings.Contains(detail, "Missing saved connection") || !strings.Contains(detail, "edit") {
		t.Fatalf("detail = %q, want actionable missing-connection guidance", detail)
	}
}

func TestBackupRetentionSummaryOnlyShowsActiveLimits(t *testing.T) {
	retention := backupcore.Retention{KeepLast: 14, MaxTotalBytes: 8 << 30}
	got := backupRetentionSummary(retention)
	for _, want := range []string{"latest 14", "8.0 GiB"} {
		if !strings.Contains(got, want) {
			t.Errorf("retention summary %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "days") {
		t.Fatalf("retention summary %q includes disabled age limit", got)
	}
}

func TestBackupPlanDefaultsSummaryMakesSafeDefaultsVisible(t *testing.T) {
	got := backupPlanDefaultsSummary(backupcore.Job{
		Compression:    backupcore.CompressionZstd,
		Encryption:     backupcore.EncryptionNone,
		Retention:      backupcore.Retention{KeepLast: 14, MaxAgeDays: 30},
		TimeoutMinutes: 30,
	})
	for _, want := range []string{"zstd compression", "latest 14 / 30 days", "no encryption", "30 min timeout"} {
		if !strings.Contains(got, want) {
			t.Errorf("defaults summary %q missing %q", got, want)
		}
	}
}

func TestBackupScheduleLabelIncludesCanonicalMultipleTimes(t *testing.T) {
	for _, test := range []struct {
		name     string
		schedule backupcore.Schedule
		want     []string
	}{
		{name: "daily", schedule: backupcore.Schedule{Kind: backupcore.ScheduleDaily, TimesOfDay: []string{"13:00", "01:00", "13:00"}, Timezone: "Asia/Kolkata"}, want: []string{"daily", "01:00, 13:00", "Asia/Kolkata"}},
		{name: "weekly", schedule: backupcore.Schedule{Kind: backupcore.ScheduleWeekly, Weekdays: []int{int(time.Monday), int(time.Friday)}, TimesOfDay: []string{"14:30", "02:30"}, Timezone: "UTC"}, want: []string{"Mon,Fri", "02:30, 14:30", "UTC"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			label := backupScheduleLabel(test.schedule)
			for _, want := range test.want {
				if !strings.Contains(label, want) {
					t.Errorf("schedule label %q missing %q", label, want)
				}
			}
		})
	}
}

func TestBackupScheduleTimesInputSupportsLegacyAndPlural(t *testing.T) {
	if got := backupScheduleTimesInput(backupcore.Schedule{Kind: backupcore.ScheduleDaily, TimeOfDay: "07:15"}); got != "07:15" {
		t.Fatalf("legacy input = %q, want 07:15", got)
	}
	if got := backupScheduleTimesInput(backupcore.Schedule{Kind: backupcore.ScheduleDaily, TimeOfDay: "ignored", TimesOfDay: []string{"13:00", "01:00"}}); got != "01:00, 13:00" {
		t.Fatalf("plural input = %q, want canonical plural values", got)
	}
	times, err := parseBackupScheduleTimes(" 13:00, 01:00, 13:00 ")
	if err != nil || strings.Join(times, ",") != "01:00,13:00" {
		t.Fatalf("parse plural times = %#v, %v", times, err)
	}
	for _, raw := range []string{"", "25:00", "01:00,,bad"} {
		if _, err := parseBackupScheduleTimes(raw); err == nil {
			t.Errorf("parseBackupScheduleTimes(%q) succeeded", raw)
		}
	}
}

func TestBackupConnectionPickerEscapeRestoresCenterFocus(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	centerList := tview.NewList()
	pages.AddPage(pageBackupCenter, centerList, true, true)
	application.SetRoot(pages, true).SetFocus(centerList)
	app := &App{app: application, pages: pages, store: &config.Store{}}

	app.showBackupConnectionPicker()
	pickerList, ok := application.GetFocus().(*tview.List)
	if !ok || pickerList == centerList {
		t.Fatalf("focus = %T, want connection picker list", application.GetFocus())
	}
	pickerList.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})
	if pages.HasPage(pageBackupConnectionPicker) {
		t.Fatal("connection picker page remained after Escape")
	}
	if application.GetFocus() != centerList {
		t.Fatalf("focus = %T, want original Backup Center list", application.GetFocus())
	}
}

func TestBackupPlanActionsEscapeRestoresCenterFocus(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	centerList := tview.NewList()
	pages.AddPage(pageBackupCenter, centerList, true, true)
	application.SetRoot(pages, true).SetFocus(centerList)
	app := &App{app: application, pages: pages, store: &config.Store{}}

	app.showBackupPlanActions(backupcore.Job{Name: "Orders", Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}})
	actions, ok := application.GetFocus().(*tview.List)
	if !ok || actions == centerList {
		t.Fatalf("focus = %T, want backup action list", application.GetFocus())
	}
	actions.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})
	if pages.HasPage(pageBackupPlanActions) {
		t.Fatal("backup action page remained after Escape")
	}
	if application.GetFocus() != centerList {
		t.Fatalf("focus = %T, want original Backup Center list", application.GetFocus())
	}
}

func TestBackupConnectionFormCancelRestoresCallingFlow(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	callingForm := tview.NewForm()
	pages.AddPage(pageBackupForm, callingForm, true, true)
	application.SetRoot(pages, true).SetFocus(callingForm)
	app := &App{app: application, pages: pages, store: &config.Store{}}

	connectionForm := app.showNewConnectionForBackup(nil)
	if application.GetFocus() == callingForm {
		t.Fatalf("focus = %T, want add-connection form", application.GetFocus())
	}
	connectionForm.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})
	if pages.HasPage("connectModal") {
		t.Fatal("connection form remained after Escape")
	}
	if application.GetFocus() != callingForm {
		t.Fatalf("focus = %T, want calling backup form", application.GetFocus())
	}
}

func TestNewAppOpensBackupCatalogOnlyOnDemand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DBTERM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("DBTERM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("DBTERM_LOG_DIR", filepath.Join(root, "logs"))

	app := NewApp()
	if app.backupStore != nil {
		t.Fatal("NewApp eagerly opened the backup catalog")
	}
	storePath, err := backupcore.DefaultStorePath()
	if err != nil {
		t.Fatalf("resolve backup catalog path: %v", err)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("backup catalog exists before first use: err=%v", err)
	}

	store, err := app.ensureBackupStore()
	if err != nil {
		t.Fatalf("open backup catalog on demand: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store == nil || app.backupStore != store {
		t.Fatal("ensureBackupStore did not retain the opened catalog")
	}
	again, err := app.ensureBackupStore()
	if err != nil || again != store {
		t.Fatalf("ensureBackupStore did not reuse the catalog: store=%p err=%v", again, err)
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("backup catalog was not created on first use: %v", err)
	}
}

func TestBackupCenterRefreshPreservesWorkspaceReturnFocus(t *testing.T) {
	backupStore, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatalf("open backup store: %v", err)
	}
	t.Cleanup(func() { _ = backupStore.Close() })

	application := tview.NewApplication()
	pages := tview.NewPages()
	tables := tview.NewList()
	query := tview.NewTextArea()
	results := tview.NewTable()
	pages.AddPage("main", query, true, true)
	application.SetRoot(pages, true).SetFocus(query)
	app := &App{
		app: application, pages: pages, store: &config.Store{}, backupStore: backupStore,
		tables: tables, queryInput: query, results: results, statusBar: tview.NewTextView(),
	}

	app.showBackupCenter()
	app.showBackupCenter() // internal refresh must not replace the original caller.
	list, ok := application.GetFocus().(*tview.List)
	if !ok {
		t.Fatalf("focus = %T, want refreshed Backup Center list", application.GetFocus())
	}
	list.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})

	frontPage, _ := pages.GetFrontPage()
	if frontPage != "main" {
		t.Fatalf("front page = %q, want original workspace", frontPage)
	}
	if application.GetFocus() != query {
		t.Fatalf("focus = %T, want original query editor", application.GetFocus())
	}
}

func TestBackupJobFormEscapeRestoresCenterFocus(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	centerList := tview.NewList()
	pages.AddPage(pageBackupCenter, centerList, true, true)
	application.SetRoot(pages, true).SetFocus(centerList)
	app := &App{
		app:   application,
		pages: pages,
		store: &config.Store{Connections: []config.ConnectionConfig{{
			ID: "orders", Name: "Orders", Type: config.SQLite, FilePath: filepath.Join(t.TempDir(), "orders.sqlite3"),
		}}},
	}

	app.showBackupJobFormForConnection(nil, "orders")
	formRoot := pages.GetPage(pageBackupForm)
	if formRoot == nil {
		t.Fatal("backup form page was not created")
	}
	formRoot.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})

	if pages.HasPage(pageBackupForm) {
		t.Fatal("Escape left the backup form mounted")
	}
	if application.GetFocus() != centerList {
		t.Fatalf("focus = %T, want original Backup Center list", application.GetFocus())
	}
}

func TestBackupJobFormStartsWithEssentialsOnly(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	centerList := tview.NewList()
	pages.AddPage(pageBackupCenter, centerList, true, true)
	application.SetRoot(pages, true).SetFocus(centerList)
	app := &App{
		app: application, pages: pages,
		store: &config.Store{Connections: []config.ConnectionConfig{{
			ID: "orders", Name: "Orders", Type: config.SQLite, FilePath: filepath.Join(t.TempDir(), "orders.sqlite3"),
		}}},
	}

	form := app.showBackupJobFormForConnection(nil, "orders")
	if form == nil {
		t.Fatal("backup form was not created")
	}
	for _, label := range []string{"Database", backupFormLabelDestination, "Schedule", "Run At (comma-separated HH:MM)", "Enable Schedule", "Included", backupFormLabelAdvanced} {
		if form.GetFormItemByLabel(label) == nil {
			t.Errorf("essential form is missing %q", label)
		}
	}
	for _, label := range []string{backupFormLabelConnection, "Backup Name", "Timezone", "Filename Template", "SMTP Host", "Disk Space"} {
		if form.GetFormItemByLabel(label) != nil {
			t.Errorf("advanced field %q is visible in the essential form", label)
		}
	}
}

func TestBackupJobFormShowsExistingPluralTimesWithoutLegacyOverride(t *testing.T) {
	backupStore, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backupStore.Close() })
	application := tview.NewApplication()
	pages := tview.NewPages()
	centerList := tview.NewList()
	pages.AddPage(pageBackupCenter, centerList, true, true)
	application.SetRoot(pages, true).SetFocus(centerList)
	connection := config.ConnectionConfig{ID: "orders", Name: "Orders", Type: config.SQLite, FilePath: filepath.Join(t.TempDir(), "orders.sqlite3")}
	app := &App{app: application, pages: pages, store: &config.Store{Connections: []config.ConnectionConfig{connection}}, backupStore: backupStore}
	job := backupcore.Job{
		ID: "job_plural", Name: "Orders backup", ConnectionID: connection.ID, Destination: t.TempDir(), Compression: backupcore.CompressionZstd, CompressionLevel: 3,
		Schedule:  backupcore.Schedule{Kind: backupcore.ScheduleDaily, TimeOfDay: "09:00", TimesOfDay: []string{"13:00", "01:00"}, Timezone: "Asia/Kolkata"},
		Retention: backupcore.Retention{KeepLast: 14}, TimeoutMinutes: 30,
	}
	form := app.showBackupJobFormForConnection(&job, "")
	field, ok := form.GetFormItemByLabel("Run At (comma-separated HH:MM)").(*tview.InputField)
	if !ok {
		t.Fatal("plural run-time field was not created")
	}
	if got := field.GetText(); got != "01:00, 13:00" {
		t.Fatalf("run-time field = %q, want plural values instead of legacy 09:00", got)
	}
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(primitive tview.Primitive) { application.SetFocus(primitive) })
	stored, err := backupStore.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("saved plural backup job: %v", err)
	}
	times, err := stored.Schedule.WallClockTimes()
	if err != nil || strings.Join(times, ",") != "01:00,13:00" {
		t.Fatalf("saved backup times = %#v, %v", times, err)
	}
}

func TestBackupStorageTextShowsDestinationAndPrivateStageVolumes(t *testing.T) {
	t.Setenv("DBTERM_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	text := backupDestinationStorageText(t.TempDir())
	for _, want := range []string{"DEST", "STAGE", "available", "total"} {
		if !strings.Contains(text, want) {
			t.Errorf("storage text %q missing %q", text, want)
		}
	}
	legacy := backupDestinationStorageText("rclone://vault/dbterm")
	for _, want := range []string{"Legacy rclone destination", "new backup publication is disabled", "STAGE"} {
		if !strings.Contains(legacy, want) {
			t.Errorf("legacy storage text %q missing %q", legacy, want)
		}
	}
}

func TestBackupCenterRebuildsDashboardAfterConnectionsChange(t *testing.T) {
	backupStore, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatalf("open backup store: %v", err)
	}
	t.Cleanup(func() { _ = backupStore.Close() })

	application := tview.NewApplication()
	pages := tview.NewPages()
	oldList := tview.NewList()
	pages.AddPage("dashboard", oldList, true, true)
	application.SetRoot(pages, true).SetFocus(oldList)
	settings := config.DefaultSettings()
	settings.DashboardHealthChecks = "manual"
	app := &App{
		app: application, pages: pages, backupStore: backupStore, settings: settings,
		store: &config.Store{Connections: []config.ConnectionConfig{{ID: "one", Name: "One", Type: config.SQLite, FilePath: "one.sqlite3"}}},
	}

	app.showBackupCenter()
	app.store.Connections = append(app.store.Connections, config.ConnectionConfig{ID: "two", Name: "Two", Type: config.SQLite, FilePath: "two.sqlite3"})
	centerList, ok := application.GetFocus().(*tview.List)
	if !ok {
		t.Fatalf("focus = %T, want Backup Center list", application.GetFocus())
	}
	centerList.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})

	newList, ok := application.GetFocus().(*tview.List)
	if !ok || newList == oldList {
		t.Fatalf("focus = %T, want rebuilt Dashboard list", application.GetFocus())
	}
	if got := newList.GetItemCount(); got != 2 {
		t.Fatalf("rebuilt Dashboard connection count = %d, want 2", got)
	}
}

func TestBackupCenterRefreshKeepsSelectedJob(t *testing.T) {
	backupStore, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatalf("open backup store: %v", err)
	}
	t.Cleanup(func() { _ = backupStore.Close() })
	destination := t.TempDir()
	for _, job := range []backupcore.Job{
		{ID: "job_alpha", Name: "Alpha", ConnectionID: "db", Destination: destination, Compression: backupcore.CompressionNone, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}},
		{ID: "job_bravo", Name: "Bravo", ConnectionID: "db", Destination: destination, Compression: backupcore.CompressionNone, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}},
	} {
		candidate := job
		if err := backupStore.UpsertJob(context.Background(), &candidate); err != nil {
			t.Fatalf("store %s: %v", job.Name, err)
		}
	}

	application := tview.NewApplication()
	pages := tview.NewPages()
	origin := tview.NewTextView()
	pages.AddPage("origin", origin, true, true)
	application.SetRoot(pages, true).SetFocus(origin)
	app := &App{
		app: application, pages: pages, backupStore: backupStore,
		store:                   &config.Store{Connections: []config.ConnectionConfig{{ID: "db", Name: "Database", Type: config.SQLite, FilePath: "database.sqlite3"}}},
		backupCenterSelectedJob: "job_bravo",
	}

	app.showBackupCenter()
	app.showBackupCenter()
	list, ok := application.GetFocus().(*tview.List)
	if !ok {
		t.Fatalf("focus = %T, want Backup Center list", application.GetFocus())
	}
	if got := list.GetCurrentItem(); got != 1 {
		t.Fatalf("selected job index after refresh = %d, want 1", got)
	}
}

func TestBackupCenterProtectionSummaryFitsCommonTerminalSizes(t *testing.T) {
	for _, test := range []struct {
		name           string
		width          int
		height         int
		destination    string
		wantKind       string
		wantProtection string
		wantCount      string
		wantDetail     string
		wantCopyDetail string
		reject         []string
	}{
		{name: "local 80x24 long values", width: 80, height: 24, destination: filepath.Join(t.TempDir(), "registration-production-backups", "daily-database-archives"), wantKind: "LOCAL", wantProtection: "LOCAL COPY PRESENT", wantCount: "1 found", wantDetail: "last verified", wantCopyDetail: "checksum not re-read", reject: []string{"ONE VERIFIED COPY", "1 verified", "checksum verified"}},
		{name: "legacy rclone record 120x30", width: 120, height: 30, destination: "rclone://vault/dbterm", wantKind: "rclone", wantProtection: "LEGACY REMOTE COPY RECORDED", wantCount: "1 legacy record", wantDetail: "size checked", wantCopyDetail: "availability not rechecked", reject: []string{"LOCAL COPY PRESENT", "checksum verified"}},
		{name: "legacy rclone record 120x35", width: 120, height: 35, destination: "rclone://vault/dbterm", wantKind: "rclone", wantProtection: "LEGACY REMOTE COPY RECORDED", wantCount: "1 legacy record", wantDetail: "size checked", wantCopyDetail: "availability not rechecked", reject: []string{"LOCAL COPY PRESENT", "checksum verified"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			jobDestination := test.destination
			if backupcore.IsRemoteBackupDestination(test.destination) {
				// New rclone generation fails closed. A migrated local job can
				// still display its historical remote run conservatively.
				jobDestination = filepath.Join(t.TempDir(), "current-local-destination")
			}
			if err := os.MkdirAll(jobDestination, 0o700); err != nil {
				t.Fatal(err)
			}
			backupStore, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
			if err != nil {
				t.Fatalf("open backup store: %v", err)
			}
			t.Cleanup(func() { _ = backupStore.Close() })

			job := backupcore.Job{
				ID: "job_orders", Name: "Orders", ConnectionID: "orders", Destination: jobDestination,
				Compression: backupcore.CompressionZstd, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleDaily, TimesOfDay: []string{"13:00", "01:00"}, Timezone: "Asia/Kolkata"},
			}
			if err := backupStore.UpsertJob(context.Background(), &job); err != nil {
				t.Fatalf("store backup job: %v", err)
			}
			const owner = "ui-render-test"
			verifiedAt := time.Date(2026, time.September, 3, 1, 4, 0, 0, time.Local)
			if _, err := backupStore.ClaimJob(context.Background(), job.ID, owner, verifiedAt.Add(-time.Minute)); err != nil {
				t.Fatalf("claim backup job: %v", err)
			}
			run, err := backupStore.StartRun(context.Background(), job.ID, backupcore.TriggerManual, verifiedAt.Add(-time.Minute))
			if err != nil {
				t.Fatalf("start backup run: %v", err)
			}
			artifactPath, err := backupcore.JoinBackupDestination(test.destination, "orders.sqlite3.zst")
			if err != nil {
				t.Fatalf("build artifact path: %v", err)
			}
			run.Status = backupcore.RunSucceeded
			run.FinishedAt = verifiedAt
			run.Artifact = backupcore.Artifact{
				Path: artifactPath, Size: 4096, SHA256: strings.Repeat("a", 64), Format: "sqlite+zstd", Verified: true,
				PublicationState: backupcore.ArtifactPublicationComplete, CreatedAt: verifiedAt,
			}
			if !backupcore.IsRemoteBackupDestination(test.destination) {
				if err := os.WriteFile(artifactPath, make([]byte, 4096), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := backupStore.FinishRun(context.Background(), &run, owner); err != nil {
				t.Fatalf("finish backup run: %v", err)
			}
			copyJob := backupcore.CopyJob{
				ID: "copy_vault", Name: "Vault mirror", Mode: backupcore.CopyModePush,
				Source: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal}, SourceBackupJobID: job.ID,
				Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "independent-vault")},
				Trigger:     backupcore.CopyTriggerManual, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}, Verification: backupcore.CopyVerificationSHA256Format, TimeoutMinutes: 30,
			}
			if err := backupStore.UpsertCopyJob(context.Background(), &copyJob); err != nil {
				t.Fatalf("store copy job: %v", err)
			}

			application := tview.NewApplication()
			pages := tview.NewPages()
			pages.AddPage("origin", tview.NewTextView(), true, true)
			application.SetRoot(pages, true)
			app := &App{
				app: application, pages: pages, backupStore: backupStore, lastScreenW: test.width, lastScreenH: test.height,
				store: &config.Store{Connections: []config.ConnectionConfig{{ID: "orders", Name: "Registration Production Orders Database", Type: config.SQLite, FilePath: filepath.Join(jobDestination, "source-database-with-a-long-name.sqlite3")}}},
			}
			screen := tcell.NewSimulationScreen("UTF-8")
			application.SetScreen(screen)
			screen.SetSize(test.width, test.height)
			t.Cleanup(screen.Fini)

			app.showBackupCenter()
			application.ForceDraw()
			rendered := backupSimulationScreenText(screen)
			t.Logf("%dx%d Backup Center render:\n%s", test.width, test.height, rendered)
			for _, want := range []string{"Selected Backup", "1 copy job", "LOCAL BACKUP", test.wantProtection, test.wantKind, test.wantDetail, "LOCAL CHECK", test.wantCount, test.wantCopyDetail, "COPY JOBS", "1 configured", "Vault mirror", "COPY HEALTH", "1 never", "01:00, 13:00", "POLICY", "not encrypted"} {
				if want == "" {
					continue
				}
				if !strings.Contains(rendered, want) {
					t.Errorf("%dx%d render missing %q:\n%s", test.width, test.height, want, rendered)
				}
			}
			for _, rejected := range test.reject {
				if strings.Contains(rendered, rejected) {
					t.Errorf("%dx%d render overstates verification with %q:\n%s", test.width, test.height, rejected, rendered)
				}
			}
			if strings.Contains(strings.ToLower(rendered), "protected") {
				t.Fatalf("%dx%d render claims scheduler readiness is protection:\n%s", test.width, test.height, rendered)
			}
		})
	}
}

func backupSimulationScreenText(screen tcell.SimulationScreen) string {
	cells, width, height := screen.GetContents()
	lines := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		for column := 0; column < width; column++ {
			cell := cells[row*width+column]
			if len(cell.Runes) == 0 {
				line.WriteRune(' ')
				continue
			}
			line.WriteString(string(cell.Runes))
		}
		lines[row] = strings.TrimRight(line.String(), " ")
	}
	return strings.Join(lines, "\n")
}
