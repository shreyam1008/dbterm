package ui

import (
	"context"
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
	if got := defaultBackupRestoreMode(backupcore.FormatSQLiteDatabase); got != 1 {
		t.Fatalf("SQLite snapshot default mode index = %d, want clean/replace", got)
	}
	for _, format := range []backupcore.Format{
		backupcore.FormatSQLiteSQL,
		backupcore.FormatPostgresSQL,
		backupcore.FormatPostgresCustom,
		backupcore.FormatMySQLSQL,
	} {
		if got := defaultBackupRestoreMode(format); got != 0 {
			t.Errorf("%s default mode index = %d, want merge", format, got)
		}
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

func TestBackupScheduleLabelIncludesWeeklyDays(t *testing.T) {
	label := backupScheduleLabel(backupcore.Schedule{
		Kind: backupcore.ScheduleWeekly, Weekdays: []int{int(time.Monday), int(time.Friday)}, TimeOfDay: "02:30", Timezone: "UTC",
	})
	for _, want := range []string{"Mon,Fri", "02:30", "UTC"} {
		if !strings.Contains(label, want) {
			t.Errorf("weekly schedule label %q missing %q", label, want)
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
	for _, label := range []string{"Database", backupFormLabelDestination, "Schedule", "Run At (HH:MM)", "Enable Schedule", "Included", backupFormLabelAdvanced} {
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

func TestBackupStorageTextShowsDestinationAndPrivateStageVolumes(t *testing.T) {
	t.Setenv("DBTERM_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	text := backupDestinationStorageText(t.TempDir())
	for _, want := range []string{"DEST", "STAGE", "available", "total"} {
		if !strings.Contains(text, want) {
			t.Errorf("storage text %q missing %q", text, want)
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
