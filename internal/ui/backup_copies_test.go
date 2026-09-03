package ui

import (
	"context"
	"encoding/base64"
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
)

func TestCopyHealthSeparatesSuccessWarningsStalenessAndDisabledSchedule(t *testing.T) {
	now := time.Date(2026, time.September, 3, 15, 0, 0, 0, time.UTC)
	job := backupcore.CopyJob{Trigger: backupcore.CopyTriggerManual, ExpectedFreshnessMinutes: 60}
	run := backupcore.CopyRun{
		Status: backupcore.RunSucceeded, Discovered: 1,
		Artifacts: []backupcore.CopyArtifactResult{{PublicationState: backupcore.ArtifactPublicationComplete, SourceCreatedAt: now.Add(-2 * time.Hour)}},
	}
	if got := copyHealthClass(job, run, true, now); got != "warning" {
		t.Fatalf("stale copy health = %q, want warning", got)
	}
	if got := copyLagLabel(job, run, true, now); !strings.Contains(got, "STALE") || !strings.Contains(got, "2h00m00s") {
		t.Fatalf("stale copy lag = %q", got)
	}
	if got := copyWarningsLabel(job, run, true, now); !strings.Contains(got, "stale") {
		t.Fatalf("stale copy warnings = %q", got)
	}

	noOp := backupcore.CopyRun{Status: backupcore.RunSucceeded, Discovered: 1, AlreadyPresent: 1, NewestSourceAt: now.Add(-30 * time.Minute)}
	if got := copyHealthClass(job, noOp, true, now); got != "healthy" {
		t.Fatalf("fresh no-op copy health = %q, want healthy", got)
	}
	if got := copyLagLabel(job, noOp, true, now); strings.Contains(got, "unknown") || !strings.Contains(got, "30m") {
		t.Fatalf("fresh no-op copy lag = %q", got)
	}
	noOp.NotificationError = "SMTP unavailable"
	if got := copyHealthClass(job, noOp, true, now); got != "warning" {
		t.Fatalf("notification-failed copy health = %q, want warning", got)
	}
	if got := copyWarningsLabel(job, noOp, true, now); !strings.Contains(got, "notification") || !strings.Contains(got, "SMTP unavailable") {
		t.Fatalf("notification warning = %q", got)
	}

	run.Artifacts[0].SourceCreatedAt = now.Add(-30 * time.Minute)
	if got := copyHealthClass(job, run, true, now); got != "healthy" {
		t.Fatalf("fresh copy health = %q, want healthy", got)
	}
	run.Warnings = []string{"optional format probe unavailable"}
	if got := copyHealthClass(job, run, true, now); got != "warning" {
		t.Fatalf("warned copy health = %q, want warning", got)
	}

	timed := job
	timed.Trigger = backupcore.CopyTriggerTimed
	timed.Enabled = false
	if got := copyHealthClass(timed, backupcore.CopyRun{}, false, now); got != "disabled" {
		t.Fatalf("never-run paused timed copy health = %q, want disabled", got)
	}
	if got := copyHealthClass(timed, run, true, now); got != "warning" {
		t.Fatalf("previously successful paused copy health = %q, want warning", got)
	}
	timed.Enabled = true
	if got := copyHealthClass(timed, backupcore.CopyRun{}, false, now); got != "warning" {
		t.Fatalf("legacy enabled copy without a real-transfer proof health = %q, want warning", got)
	}
	if got := copyWarningsLabel(timed, backupcore.CopyRun{}, false, now); !strings.Contains(got, "successful real transfer") {
		t.Fatalf("missing automation proof warning = %q", got)
	}
}

func TestCopyRunSpeedLabelSeparatesTransferFromNoOpScan(t *testing.T) {
	started := time.Date(2026, time.September, 3, 1, 0, 0, 0, time.UTC)
	run := backupcore.CopyRun{StartedAt: started, FinishedAt: started.Add(2 * time.Second), BytesCopied: 4 * 1024 * 1024}
	if got := copyRunSpeedLabel(run, true); !strings.Contains(got, "2.00 MiB/s") || !strings.Contains(got, "2s") {
		t.Fatalf("transfer speed label = %q", got)
	}
	run.BytesCopied = 0
	if got := copyRunSpeedLabel(run, true); !strings.Contains(got, "no bytes transferred") {
		t.Fatalf("no-op speed label = %q", got)
	}
	if got := copyRunSpeedLabel(backupcore.CopyRun{}, false); !strings.Contains(got, "not measured") {
		t.Fatalf("never-run speed label = %q", got)
	}
}

func TestMeasureBackupCopyEndpointLocalIsReadOnlyAndReportsCapacity(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source, destination := filepath.Join(t.TempDir(), "source"), filepath.Join(t.TempDir(), "destination")
	for _, directory := range []string{source, destination} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{backupStore: store}
	message, err := app.measureBackupCopyEndpoint(context.Background(), backupcore.CopyJob{
		Source:      backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: source},
		Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: destination},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Destination:", "Volume:", "Available:", "verified"} {
		if !strings.Contains(message, want) {
			t.Errorf("local endpoint test output %q missing %q", message, want)
		}
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only endpoint test changed destination: entries=%v err=%v", entries, err)
	}
}

func TestMeasureBackupCopyEndpointRequiresConfiguredVolumeIdentity(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	source := filepath.Join(t.TempDir(), "source")
	mountPoint := filepath.Join(t.TempDir(), "vault")
	destination := filepath.Join(mountPoint, "copies")
	for _, directory := range []string{source, destination} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := filepath.Join(mountPoint, ".vault-id")
	if err := os.WriteFile(sentinel, []byte("wrong-vault\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := backupcore.CopyJob{
		Source:      backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: source},
		Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: destination},
		DestinationVolume: &backupcore.CopyDestinationVolume{
			Mode: backupcore.CopyVolumeAlreadyMounted, MountPoint: mountPoint,
			SentinelFile: ".vault-id", SentinelValue: "expected-vault",
		},
	}
	app := &App{backupStore: store}
	if _, err := app.measureBackupCopyEndpoint(context.Background(), job); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong destination volume test error = %v", err)
	}
	if err := os.WriteFile(sentinel, []byte("expected-vault\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	message, err := app.measureBackupCopyEndpoint(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Destination volume identity: verified", "mount, unmount, sync, and spindown were not exercised"} {
		if !strings.Contains(message, want) {
			t.Fatalf("volume endpoint test output %q omits %q", message, want)
		}
	}
}

func TestCopyTopologyAndTransportHelpersStayTruthful(t *testing.T) {
	jobs := []backupcore.CopyJob{
		{ID: "push", Name: "Offsite vault", Mode: backupcore.CopyModePush, SourceBackupJobID: "orders", Source: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal}, Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointSFTP, Location: "sftp://backup@vault.example/archives"}},
		{ID: "pull", Name: "Collector", Mode: backupcore.CopyModePull, ArtifactFilter: backupcore.CopyArtifactFilter{JobID: "orders"}, Source: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointRclone, Location: "rclone://archive/orders"}, Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "vault")}},
		{ID: "other", Name: "Other", Mode: backupcore.CopyModePush, SourceBackupJobID: "customers"},
	}
	topology := backupCopyTopologySummary(jobs, "orders")
	for _, want := range []string{"2 configured", "Offsite vault", "push to", "Collector", "pull from"} {
		if !strings.Contains(topology, want) {
			t.Errorf("topology %q missing %q", topology, want)
		}
	}
	if strings.Contains(topology, "Other") {
		t.Fatalf("topology included an unrelated copy job: %q", topology)
	}

	rclonePush := backupcore.CopyJob{Mode: backupcore.CopyModePush, Source: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal}, Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointRclone}}
	if got := copyTransportLabel(rclonePush); !strings.Contains(got, "UNSUPPORTED") || !strings.Contains(got, "rclone push") {
		t.Fatalf("rclone push label = %q, want explicit unsupported state", got)
	}
	for _, option := range backupCopyTopologyOptions {
		if strings.Contains(strings.ToLower(option), "rclone") && strings.Contains(strings.ToLower(option), "push") {
			t.Fatalf("wizard offers unsupported rclone push: %q", option)
		}
	}
}

func TestCopyWizardShowsPinnedSFTPIdentityFields(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	application := tview.NewApplication()
	pages := tview.NewPages()
	copyList := tview.NewList()
	pages.AddPage(pageBackupCopies, copyList, true, true)
	application.SetRoot(pages, true).SetFocus(copyList)
	app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}
	job := backupcore.CopyJob{
		Name: "Producer pull", Mode: backupcore.CopyModePull, Trigger: backupcore.CopyTriggerManual,
		Source:      backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointSFTP, Location: "sftp://backup@producer.example/archives", CredentialRef: filepath.Join(t.TempDir(), "id_ed25519"), PinnedHostKey: validCopyUIHostFingerprint()},
		Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "vault")},
		Schedule:    backupcore.Schedule{Kind: backupcore.ScheduleManual}, Verification: backupcore.CopyVerificationSHA256Format, TimeoutMinutes: 30,
	}
	form := app.showBackupCopyForm(&job)
	if form == nil {
		t.Fatal("SFTP copy form was not created")
	}
	for _, label := range []string{"SFTP Location", "Private Identity File", "Pinned Host Key (SHA256:...)", "Destination Folder", "Verification", "Keep Latest"} {
		if form.GetFormItemByLabel(label) == nil {
			t.Errorf("SFTP copy form missing %q", label)
		}
	}
	if form.GetFormItemByLabel("Send Email") == nil {
		t.Fatal("copy form does not expose its email policy")
	}
	for _, hidden := range []string{"SMTP Host", "SMTP Port", "SMTP App Password", "From Address"} {
		if form.GetFormItemByLabel(hidden) != nil {
			t.Fatalf("copy form with email disabled unexpectedly shows %q", hidden)
		}
	}
	if form.GetButtonIndex("Send Test Email") >= 0 {
		t.Fatal("copy form with email disabled unexpectedly offers a delivery test")
	}
	verification, ok := form.GetFormItemByLabel("Verification").(*tview.DropDown)
	if !ok {
		t.Fatal("copy form verification selector was not created")
	}
	_, option := verification.GetCurrentOption()
	if strings.Contains(strings.ToLower(option), "size only") {
		t.Fatalf("copy form offers weak verification: %q", option)
	}
}

func TestCopyNotificationDraftParsesRecipientsAndPort(t *testing.T) {
	draft := backupCopyFormDraft{
		job: backupcore.CopyJob{Notification: backupcore.EmailNotification{
			Policy: backupcore.NotificationBoth, SMTPHost: "smtp.example.test", TLSMode: backupcore.SMTPTLSStartTLS,
			Username: "mailer@example.test", Password: "secret", From: "mailer@example.test",
		}},
		smtpPort:   "2525",
		recipients: "ops@example.test; owner@example.test\nthird@example.test",
	}
	notification, err := backupCopyNotificationFromDraft(&draft)
	if err != nil {
		t.Fatalf("build copy notification draft: %v", err)
	}
	if notification.SMTPPort != 2525 {
		t.Fatalf("SMTP port = %d, want 2525", notification.SMTPPort)
	}
	if got := strings.Join(notification.Recipients, ","); got != "ops@example.test,owner@example.test,third@example.test" {
		t.Fatalf("recipients = %q", got)
	}
	if err := notification.Validate(); err != nil {
		t.Fatalf("notification from draft did not validate: %v", err)
	}
}

func TestCopyWizardEmailSettingsAreMaskedPreservedAndEditable(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const originalPassword = "copy-mail-password-original"
	job := backupcore.CopyJob{
		Name: "emailed vault copy", Mode: backupcore.CopyModePush, Trigger: backupcore.CopyTriggerManual,
		Source:      backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: t.TempDir()},
		Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "vault")},
		Schedule:    backupcore.Schedule{Kind: backupcore.ScheduleManual}, Verification: backupcore.CopyVerificationSHA256Format,
		Retention: backupcore.Retention{KeepLast: 14}, TimeoutMinutes: 30,
		Notification: backupcore.EmailNotification{
			Policy: backupcore.NotificationBoth, SMTPHost: "smtp.example.test", SMTPPort: 2525, TLSMode: backupcore.SMTPTLSStartTLS,
			Recipients: []string{"ops@example.test"}, Username: "mailer@example.test", Password: originalPassword, From: "mailer@example.test",
		},
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}

	newWizard := func(t *testing.T, existing *backupcore.CopyJob) (*tview.Application, *tview.Form, tcell.SimulationScreen) {
		t.Helper()
		screen := tcell.NewSimulationScreen("UTF-8")
		screen.SetSize(80, 24)
		application := tview.NewApplication().SetScreen(screen)
		pages := tview.NewPages()
		copyList := tview.NewList()
		pages.AddPage(pageBackupCopies, copyList, true, true)
		application.SetRoot(pages, true).SetFocus(copyList)
		app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}
		form := app.showBackupCopyForm(existing)
		if form == nil {
			screen.Fini()
			t.Fatal("copy wizard was not created")
		}
		return application, form, screen
	}

	application, form, screen := newWizard(t, &job)
	for _, label := range []string{"Send Email", "SMTP Host", "SMTP Port", "TLS", "Recipients (comma separated)", "SMTP Username", "SMTP App Password", "From Address", "Email Test"} {
		if form.GetFormItemByLabel(label) == nil {
			t.Errorf("enabled copy email form missing %q", label)
		}
	}
	if form.GetButtonIndex("Send Test Email") < 0 {
		t.Fatal("enabled copy email form does not offer an explicit delivery test")
	}
	passwordField, ok := form.GetFormItemByLabel("SMTP App Password").(*tview.InputField)
	if !ok {
		t.Fatal("SMTP app password is not a masked input field")
	}
	form.SetFocus(form.GetFormItemIndex("SMTP App Password"))
	application.SetFocus(form)
	application.ForceDraw()
	rendered := backupSimulationScreenText(screen)
	if strings.Contains(rendered, originalPassword) {
		t.Fatalf("80x24 copy form rendered the SMTP password in clear text:\n%s", rendered)
	}
	if !strings.Contains(rendered, "SMTP App Password") {
		t.Fatalf("80x24 copy form could not scroll to the email secret field:\n%s", rendered)
	}
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(primitive tview.Primitive) { application.SetFocus(primitive) })
	screen.Fini()
	stored, err := store.GetCopyJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("saved copy job: %v", err)
	}
	if stored.Notification.Password != originalPassword {
		t.Fatalf("untouched SMTP password was not preserved")
	}
	if stored.Notification.Policy != backupcore.NotificationBoth || stored.Notification.SMTPPort != 2525 || len(stored.Notification.Recipients) != 1 {
		t.Fatalf("saved copy notification metadata = policy %q, port %d, recipient count %d", stored.Notification.Policy, stored.Notification.SMTPPort, len(stored.Notification.Recipients))
	}

	application, form, screen = newWizard(t, &stored)
	passwordField, ok = form.GetFormItemByLabel("SMTP App Password").(*tview.InputField)
	if !ok {
		t.Fatal("SMTP app password field disappeared on edit")
	}
	const replacementPassword = "copy-mail-password-replacement"
	setFocus := func(primitive tview.Primitive) { application.SetFocus(primitive) }
	passwordField.InputHandler()(tcell.NewEventKey(tcell.KeyCtrlU, 0, tcell.ModNone), setFocus)
	for _, character := range replacementPassword {
		passwordField.InputHandler()(tcell.NewEventKey(tcell.KeyRune, character, tcell.ModNone), setFocus)
	}
	if passwordField.GetText() != replacementPassword {
		t.Fatal("SMTP password field did not accept the intentional replacement")
	}
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(primitive tview.Primitive) { application.SetFocus(primitive) })
	screen.Fini()
	stored, err = store.GetCopyJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("saved edited copy job: %v", err)
	}
	if stored.Notification.Password != replacementPassword {
		t.Fatal("intentionally changed SMTP password was not saved")
	}
}

func TestCopyWizardVolumeFieldsUseProgressiveDisclosure(t *testing.T) {
	newWizard := func(t *testing.T, job backupcore.CopyJob) *tview.Form {
		t.Helper()
		store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		application := tview.NewApplication()
		pages := tview.NewPages()
		copyList := tview.NewList()
		pages.AddPage(pageBackupCopies, copyList, true, true)
		application.SetRoot(pages, true).SetFocus(copyList)
		app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}
		form := app.showBackupCopyForm(&job)
		if form == nil {
			t.Fatal("copy wizard was not created")
		}
		return form
	}
	localJob := func(t *testing.T) backupcore.CopyJob {
		t.Helper()
		return backupcore.CopyJob{
			Name: "local vault", Mode: backupcore.CopyModePush, Trigger: backupcore.CopyTriggerManual,
			Source:      backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: t.TempDir()},
			Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "copies")},
			Schedule:    backupcore.Schedule{Kind: backupcore.ScheduleManual}, Verification: backupcore.CopyVerificationSHA256Format,
			Retention: backupcore.Retention{KeepLast: 14}, TimeoutMinutes: 30,
		}
	}

	t.Run("ordinary local folder", func(t *testing.T) {
		form := newWizard(t, localJob(t))
		if form.GetFormItemByLabel("Destination Volume") == nil {
			t.Fatal("local destination does not offer a volume safety selector")
		}
		for _, hidden := range []string{"Volume Mount Point", "Volume Identity", "Filesystem UUID", "Spin Down After Copy"} {
			if form.GetFormItemByLabel(hidden) != nil {
				t.Fatalf("ordinary folder unexpectedly shows %q", hidden)
			}
		}
	})

	t.Run("OS managed verifies only", func(t *testing.T) {
		job := localJob(t)
		job.DestinationVolume = &backupcore.CopyDestinationVolume{
			Mode: backupcore.CopyVolumeOSManaged, MountPoint: filepath.Dir(job.Destination.Location),
			SentinelFile: ".vault-id", SentinelValue: "os-managed-vault",
		}
		form := newWizard(t, job)
		for _, visible := range []string{"Destination Volume", "Volume Mount Point", "Sentinel File", "Volume Identity", "Mounted Volume Safety"} {
			if form.GetFormItemByLabel(visible) == nil {
				t.Fatalf("OS-managed volume form omits %q", visible)
			}
		}
		for _, hidden := range []string{"Filesystem UUID", "Filesystem Type", "Mount Options (comma-separated)", "Warmup Seconds", "Cooldown Seconds", "Spin Down After Copy"} {
			if form.GetFormItemByLabel(hidden) != nil {
				t.Fatalf("verify-only volume form unexpectedly shows %q", hidden)
			}
		}
	})

	t.Run("managed Linux", func(t *testing.T) {
		job := localJob(t)
		job.DestinationVolume = &backupcore.CopyDestinationVolume{
			Mode: backupcore.CopyVolumeManagedLinuxBlockDevice, MountPoint: filepath.Dir(job.Destination.Location),
			SentinelFile: ".vault-id", SentinelValue: "managed-linux-vault",
			FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4", ExpectedVolumeLabel: "BACKUP",
			MountOptions: []string{"rw", "nodev", "nosuid", "noexec"}, WarmupSeconds: 2, CooldownSeconds: 3, Spindown: true,
		}
		form := newWizard(t, job)
		for _, visible := range []string{
			"Destination Volume", "Volume Mount Point", "Sentinel File", "Volume Identity", "Filesystem UUID", "Filesystem Type",
			"Volume Label (optional)", "Mount Options (comma-separated)", "Warmup Seconds", "Cooldown Seconds", "Spin Down After Copy", "Managed Disk Safety",
		} {
			if form.GetFormItemByLabel(visible) == nil {
				t.Fatalf("managed Linux volume form omits %q", visible)
			}
		}
	})

	t.Run("remote destination", func(t *testing.T) {
		job := localJob(t)
		job.Destination = backupcore.CopyEndpoint{
			Kind: backupcore.CopyEndpointSFTP, Location: "sftp://backup@vault.example/archives",
			CredentialRef: filepath.Join(t.TempDir(), "id_ed25519"), PinnedHostKey: validCopyUIHostFingerprint(),
		}
		form := newWizard(t, job)
		if form.GetFormItemByLabel("Destination Volume") != nil {
			t.Fatal("remote destination offers an irrelevant local volume selector")
		}
	})
}

func TestCopyWizardManagedVolumeSaveDoesNotCreateFallbackDirectory(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	application := tview.NewApplication()
	pages := tview.NewPages()
	pages.AddPage(pageBackupCopies, tview.NewList(), true, true)
	application.SetRoot(pages, true)
	app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}
	mountPoint := filepath.Join(t.TempDir(), "vault-mount")
	if err := os.MkdirAll(mountPoint, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(mountPoint, "copies")
	job := backupcore.CopyJob{
		ID: "copy_managed_ui", Name: "managed vault", Mode: backupcore.CopyModePush, Trigger: backupcore.CopyTriggerManual,
		Source:      backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: t.TempDir()},
		Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: destination},
		DestinationVolume: &backupcore.CopyDestinationVolume{
			Mode: backupcore.CopyVolumeManagedLinuxBlockDevice, MountPoint: mountPoint,
			SentinelFile: ".vault-id", SentinelValue: "managed-ui-vault",
			FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4", MountOptions: []string{"errors=remount-ro"}, Spindown: true,
		},
		Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}, Verification: backupcore.CopyVerificationSHA256Format,
		Retention: backupcore.Retention{KeepLast: 14}, TimeoutMinutes: 30,
	}
	form := app.showBackupCopyForm(&job)
	if form == nil {
		t.Fatal("managed copy wizard was not created")
	}
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(primitive tview.Primitive) { application.SetFocus(primitive) })
	stored, err := store.GetCopyJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("managed volume copy was not saved: %v", err)
	}
	if stored.DestinationVolume == nil || stored.DestinationVolume.Mode != backupcore.CopyVolumeManagedLinuxBlockDevice || !stored.DestinationVolume.Spindown {
		t.Fatalf("saved managed volume = %#v", stored.DestinationVolume)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("saving an unmounted managed destination created a fallback directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(mountPoint, ".vault-id")); !os.IsNotExist(err) {
		t.Fatalf("wizard silently created a volume sentinel: %v", err)
	}
}

func TestCopyDestinationVolumeLabelDoesNotExposeSentinelIdentity(t *testing.T) {
	volume := &backupcore.CopyDestinationVolume{
		Mode: backupcore.CopyVolumeManagedLinuxBlockDevice, MountPoint: "/mnt/vault",
		SentinelFile: ".vault-id", SentinelValue: "do-not-render-this-token",
		FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4", Spindown: true,
	}
	label := copyDestinationVolumeLabel(volume)
	for _, want := range []string{"managed_linux_block_device", "/mnt/vault", ".vault-id", "abcd-1234", "ext4", "spindown"} {
		if !strings.Contains(label, want) {
			t.Fatalf("volume label %q omits %q", label, want)
		}
	}
	if strings.Contains(label, volume.SentinelValue) {
		t.Fatalf("volume label exposes sentinel identity: %q", label)
	}
}

func TestCopyWizardSFTPPushExplainsScopedRemoteRetention(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	application := tview.NewApplication()
	pages := tview.NewPages()
	pages.AddPage(pageBackupCopies, tview.NewList(), true, true)
	application.SetRoot(pages, true)
	app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}
	job := backupcore.CopyJob{
		Name: "SFTP vault", Mode: backupcore.CopyModePush, Trigger: backupcore.CopyTriggerManual,
		Source:      backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "source")},
		Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointSFTP, Location: "sftp://backup@vault.example/archives", CredentialRef: filepath.Join(t.TempDir(), "id_ed25519"), PinnedHostKey: validCopyUIHostFingerprint()},
		Schedule:    backupcore.Schedule{Kind: backupcore.ScheduleManual}, Verification: backupcore.CopyVerificationSHA256Format,
		Retention: backupcore.Retention{KeepLast: 14}, TimeoutMinutes: 30,
	}
	form := app.showBackupCopyForm(&job)
	if form == nil || form.GetFormItemByLabel("Keep Latest") == nil {
		t.Fatal("SFTP push form does not expose its active remote retention count")
	}
	item := form.GetFormItemByLabel("Remote Retention")
	view, ok := item.(*tview.TextView)
	if !ok {
		t.Fatal("SFTP push form does not explain remote retention scope")
	}
	text := view.GetText(true)
	for _, want := range []string{"exact SFTP artifacts", "recorded", "reverified"} {
		if !strings.Contains(text, want) {
			t.Fatalf("remote retention explanation %q missing %q", text, want)
		}
	}
}

func TestBackupCenterCOpensCopiesEvenWithoutPlans(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	application := tview.NewApplication()
	pages := tview.NewPages()
	pages.AddPage("origin", tview.NewTextView(), true, true)
	application.SetRoot(pages, true)
	app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}
	app.showBackupCenter()
	list, ok := application.GetFocus().(*tview.List)
	if !ok {
		t.Fatalf("focus = %T, want Backup Center list", application.GetFocus())
	}
	list.InputHandler()(tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone), func(primitive tview.Primitive) { application.SetFocus(primitive) })
	front, _ := pages.GetFrontPage()
	if front != pageBackupCopies {
		t.Fatalf("front page = %q, want %q", front, pageBackupCopies)
	}
}

func TestBackupCopiesSpacePausesOnlyTimedJob(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job := backupcore.CopyJob{
		Name: "Twice daily local vault", Enabled: true, Mode: backupcore.CopyModePush,
		Source:       backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "source")},
		Destination:  backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "vault")},
		Trigger:      backupcore.CopyTriggerTimed,
		Schedule:     backupcore.Schedule{Kind: backupcore.ScheduleDaily, TimesOfDay: []string{"13:00", "01:00"}, Timezone: "Asia/Kolkata"},
		Verification: backupcore.CopyVerificationSHA256Format, TimeoutMinutes: 30,
	}
	recordCopyJobProofForUITest(t, store, &job)
	application := tview.NewApplication()
	pages := tview.NewPages()
	pages.AddPage(pageBackupCenter, tview.NewTextView(), true, true)
	application.SetRoot(pages, true)
	app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}
	app.showBackupCopies("")
	list, ok := application.GetFocus().(*tview.List)
	if !ok {
		t.Fatalf("focus = %T, want copy list", application.GetFocus())
	}
	list.InputHandler()(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone), func(primitive tview.Primitive) { application.SetFocus(primitive) })
	stored, err := store.GetCopyJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Fatal("Space did not pause the selected timed copy")
	}
	if got := backupScheduleLabel(stored.Schedule); !strings.Contains(got, "01:00, 13:00") {
		t.Fatalf("pausing copy lost plural schedule: %q", got)
	}
}

func TestCopyWizardShowsExistingPluralTimedSchedule(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	application := tview.NewApplication()
	pages := tview.NewPages()
	copyList := tview.NewList()
	pages.AddPage(pageBackupCopies, copyList, true, true)
	application.SetRoot(pages, true).SetFocus(copyList)
	app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}
	job := backupcore.CopyJob{
		ID: "copy_timed", Name: "Timed vault", Enabled: true, Mode: backupcore.CopyModePush,
		Source:       backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "source")},
		Destination:  backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "vault")},
		Trigger:      backupcore.CopyTriggerTimed,
		Schedule:     backupcore.Schedule{Kind: backupcore.ScheduleDaily, TimeOfDay: "09:00", TimesOfDay: []string{"14:30", "02:30"}, Timezone: "Asia/Kolkata"},
		Verification: backupcore.CopyVerificationSHA256Format, TimeoutMinutes: 30,
	}
	recordCopyJobProofForUITest(t, store, &job)
	form := app.showBackupCopyForm(&job)
	automaticSafety, ok := form.GetFormItemByLabel("Automatic Safety").(*tview.TextView)
	if !ok || !strings.Contains(automaticSafety.GetText(false), "Real transfer proven") || !strings.Contains(automaticSafety.GetText(false), "Changing source") {
		t.Fatalf("copy form automatic safety guidance = %T %q", automaticSafety, automaticSafety.GetText(false))
	}
	field, ok := form.GetFormItemByLabel("Run At (comma-separated HH:MM)").(*tview.InputField)
	if !ok {
		t.Fatal("copy plural run-time field was not created")
	}
	if got := field.GetText(); got != "02:30, 14:30" {
		t.Fatalf("copy run-time field = %q, want plural values", got)
	}
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(primitive tview.Primitive) { application.SetFocus(primitive) })
	stored, err := store.GetCopyJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("saved timed copy: %v", err)
	}
	times, err := stored.Schedule.WallClockTimes()
	if err != nil || strings.Join(times, ",") != "02:30,14:30" {
		t.Fatalf("saved copy times = %#v, %v", times, err)
	}
}

func TestCopyAutomationProofLabelExplainsMissingAndStaleProof(t *testing.T) {
	job := backupcore.CopyJob{
		Name: "timed vault", Mode: backupcore.CopyModePush,
		Source:       backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "source")},
		Destination:  backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "vault")},
		Trigger:      backupcore.CopyTriggerTimed,
		Schedule:     backupcore.Schedule{Kind: backupcore.ScheduleInterval, EveryMinutes: 30},
		Verification: backupcore.CopyVerificationSHA256Format,
	}
	if got := copyAutomationProofLabel(job); !strings.Contains(got, "required") || !strings.Contains(got, "press R") {
		t.Fatalf("missing proof label = %q", got)
	}
	proveCopyJobForUITest(t, &job)
	if got := copyAutomationProofLabel(job); !strings.Contains(got, "real transfer verified") {
		t.Fatalf("current proof label = %q", got)
	}
	job.Destination.Location = filepath.Join(t.TempDir(), "different-vault")
	if got := copyAutomationProofLabel(job); !strings.Contains(got, "configuration changed") || !strings.Contains(got, "manually again") {
		t.Fatalf("stale proof label = %q", got)
	}
}

func proveCopyJobForUITest(t *testing.T, job *backupcore.CopyJob) {
	t.Helper()
	proofAt := time.Now().UTC().Add(-time.Minute)
	if err := job.ApplyDefaults(proofAt); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := job.TransferConfigurationFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	job.TransferProofAt = proofAt
	job.TransferProofFingerprint = fingerprint
}

func recordCopyJobProofForUITest(t *testing.T, store *backupcore.Store, job *backupcore.CopyJob) {
	t.Helper()
	wantEnabled := job.Enabled
	job.Enabled = false
	if err := store.UpsertCopyJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	owner := "ui-proof-owner-" + job.ID
	if _, err := store.ClaimCopyJob(context.Background(), job.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(context.Background(), job.ID, backupcore.CopyTriggerManual, now)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = backupcore.RunSucceeded
	run.FinishedAt = now.Add(time.Second)
	run.Discovered = 1
	run.BytesCopied = 4096
	run.Artifacts = []backupcore.CopyArtifactResult{{
		ArtifactID: "artifact_ui_proof_" + job.ID, Source: "source/proof", Destination: job.Destination.Location,
		SizeBytes: 4096, SHA256: strings.Repeat("a", 64), Verification: backupcore.CopyVerificationSHA256Format,
		VerifiedAt: run.FinishedAt, ManifestPath: job.Destination.Location + backupcore.ArtifactManifestSuffix, ManifestSize: 512,
		ManifestSHA256: strings.Repeat("b", 64), PublicationState: backupcore.ArtifactPublicationComplete,
	}}
	if err := store.FinishCopyRun(context.Background(), &run, owner); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetCopyJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	*job = stored
	job.Enabled = wantEnabled
	if err := store.UpsertCopyJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
}

func TestBackupCopiesRenderAtCommonTerminalSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 30}, {120, 35}} {
		t.Run(strconvItoa(size.width)+"x"+strconvItoa(size.height), func(t *testing.T) {
			store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			sourceDir := filepath.Join(t.TempDir(), "producer")
			if err := os.MkdirAll(sourceDir, 0o700); err != nil {
				t.Fatal(err)
			}
			backupJob := backupcore.Job{ID: "job_orders", Name: "Orders production", ConnectionID: "orders", Destination: sourceDir, Compression: backupcore.CompressionZstd, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}}
			if err := store.UpsertJob(context.Background(), &backupJob); err != nil {
				t.Fatal(err)
			}
			copyJob := backupcore.CopyJob{
				ID: "copy_vault", Name: "CT400 vault", Enabled: true, Mode: backupcore.CopyModePush,
				Source: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal}, SourceBackupJobID: backupJob.ID,
				Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointSFTP, Location: "sftp://backup@ct400.example/registration_backups", CredentialRef: filepath.Join(t.TempDir(), "id_ed25519"), PinnedHostKey: validCopyUIHostFingerprint()},
				Trigger:     backupcore.CopyTriggerAfterSuccess, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}, Verification: backupcore.CopyVerificationSHA256Format, Retention: backupcore.Retention{KeepLast: 14}, TimeoutMinutes: 30,
			}
			recordCopyJobProofForUITest(t, store, &copyJob)
			const owner = "copy-ui-render"
			now := time.Date(2026, time.September, 3, 2, 31, 0, 0, time.Local)
			if _, err := store.ClaimCopyJob(context.Background(), copyJob.ID, owner, now.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			run, err := store.StartCopyRun(context.Background(), copyJob.ID, backupcore.CopyTriggerManual, now.Add(-time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			run.Status = backupcore.RunSucceeded
			run.FinishedAt = now
			run.Discovered = 1
			run.BytesCopied = 4096
			run.Artifacts = []backupcore.CopyArtifactResult{{
				ArtifactID: "artifact_orders", Source: filepath.Join(sourceDir, "orders.dump"), Destination: "sftp://backup@ct400.example/registration_backups/orders.dump",
				SourceCreatedAt: now.Add(-90 * time.Minute), SizeBytes: 4096, SHA256: strings.Repeat("a", 64), Verification: backupcore.CopyVerificationSHA256Format,
				VerifiedAt: now, ManifestPath: "orders.dump.dbterm.json", ManifestSize: 512, ManifestSHA256: strings.Repeat("b", 64), PublicationState: backupcore.ArtifactPublicationComplete,
			}}
			if err := store.FinishCopyRun(context.Background(), &run, owner); err != nil {
				t.Fatal(err)
			}

			application := tview.NewApplication()
			pages := tview.NewPages()
			pages.AddPage(pageBackupCenter, tview.NewTextView(), true, true)
			application.SetRoot(pages, true)
			app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}, lastScreenW: size.width, lastScreenH: size.height}
			screen := tcell.NewSimulationScreen("UTF-8")
			application.SetScreen(screen)
			screen.SetSize(size.width, size.height)
			t.Cleanup(screen.Fini)

			app.showBackupCopies(backupJob.ID)
			application.ForceDraw()
			rendered := backupSimulationScreenText(screen)
			t.Logf("%dx%d Copies render:\n%s", size.width, size.height, rendered)
			for _, want := range []string{"Copies", "CT400 vault", "PUSH", "MODE", "SOURCE", "DESTINATION", "TRIGGER", "VERIFY", "sha256+format", "SFTP push available", "LAST", "succeeded", "SPEED", "B/s", "LAG", "WARNINGS"} {
				if !strings.Contains(rendered, want) {
					t.Errorf("render missing %q:\n%s", want, rendered)
				}
			}
			if strings.Contains(strings.ToLower(rendered), "rclone push available") {
				t.Fatalf("render claims rclone push support:\n%s", rendered)
			}
		})
	}
}

func TestBackupCopiesManagedVolumeRendersSafetyState(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mountPoint := filepath.Join(t.TempDir(), "vault")
	copyJob := backupcore.CopyJob{
		ID: "copy_managed_render", Name: "Sleeping archive disk", Mode: backupcore.CopyModePull,
		Source:      backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "producer")},
		Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(mountPoint, "copies")},
		DestinationVolume: &backupcore.CopyDestinationVolume{
			Mode: backupcore.CopyVolumeManagedLinuxBlockDevice, MountPoint: mountPoint,
			SentinelFile: ".vault-id", SentinelValue: "never-render-this-token",
			FilesystemUUID: "abcd-1234", ExpectedFilesystem: "ext4", ExpectedVolumeLabel: "BACKUP",
			MountOptions: []string{"rw", "nodev", "nosuid", "noexec"}, Spindown: true,
		},
		Trigger: backupcore.CopyTriggerManual, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual},
		Verification: backupcore.CopyVerificationSHA256Format, Retention: backupcore.Retention{KeepLast: 14}, TimeoutMinutes: 30,
	}
	if err := store.UpsertCopyJob(context.Background(), &copyJob); err != nil {
		t.Fatal(err)
	}

	application := tview.NewApplication()
	pages := tview.NewPages()
	pages.AddPage(pageBackupCenter, tview.NewTextView(), true, true)
	application.SetRoot(pages, true)
	app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}, lastScreenW: 120, lastScreenH: 35}
	screen := tcell.NewSimulationScreen("UTF-8")
	application.SetScreen(screen)
	screen.SetSize(120, 35)
	t.Cleanup(screen.Fini)

	app.showBackupCopies("")
	application.ForceDraw()
	rendered := backupSimulationScreenText(screen)
	t.Logf("managed volume Copies render:\n%s", rendered)
	for _, want := range []string{"Sleeping archive disk", "VOLUME", "managed_linux_block_device", ".vault-id"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("managed-volume render missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, copyJob.DestinationVolume.SentinelValue) {
		t.Fatalf("managed-volume render exposes sentinel identity:\n%s", rendered)
	}
}

func TestBackupCopyArtifactPickerPropagatesExactOlderArtifact(t *testing.T) {
	newer := backupcore.CopyArtifactResult{
		ArtifactID: "artifact_newest_000000000001", SourceCreatedAt: time.Date(2026, time.September, 3, 13, 0, 0, 0, time.UTC),
		SizeBytes: 8 << 20, Verification: backupcore.CopyVerificationSHA256Format,
	}
	older := backupcore.CopyArtifactResult{
		ArtifactID: "artifact_older_000000000002", SourceCreatedAt: time.Date(2026, time.September, 3, 1, 0, 0, 0, time.UTC),
		SizeBytes: 4 << 20, Verification: backupcore.CopyVerificationSHA256Format,
	}
	var selected backupcore.CopyArtifactResult
	picker := newBackupCopyArtifactPicker(backupcore.CopyJob{Name: "CT400 vault"}, []backupcore.CopyArtifactResult{newer, older}, func(artifact backupcore.CopyArtifactResult) {
		selected = artifact
	}, nil)
	picker.SetCurrentItem(1)
	picker.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(tview.Primitive) {})
	if selected.ArtifactID != older.ArtifactID {
		t.Fatalf("picker propagated artifact ID %q, want exact older ID %q", selected.ArtifactID, older.ArtifactID)
	}
}

func TestBackupCopyInspectPickerRendersCompactlyAndKeepsSelectedIdentity(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job := backupcore.CopyJob{
		Name: "CT400 vault", Mode: backupcore.CopyModePush,
		Source:       backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "source")},
		Destination:  backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "vault")},
		Trigger:      backupcore.CopyTriggerManual,
		Schedule:     backupcore.Schedule{Kind: backupcore.ScheduleManual},
		Verification: backupcore.CopyVerificationSHA256Format, TimeoutMinutes: 30,
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}
	newerID := "artifact_newest_1234567890abcdef"
	olderID := "artifact_older_0987654321fedcba"
	recordCopyArtifactForInspectionUITest(t, store, job, newerID, time.Date(2026, time.September, 3, 13, 0, 0, 0, time.UTC), 8<<20)
	recordCopyArtifactForInspectionUITest(t, store, job, olderID, time.Date(2026, time.September, 3, 1, 0, 0, 0, time.UTC), 4<<20)

	application := tview.NewApplication()
	pages := tview.NewPages()
	pages.AddPage(pageBackupCopies, tview.NewTextView(), true, true)
	application.SetRoot(pages, true)
	app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}, lastScreenW: 80, lastScreenH: 24}
	screen := tcell.NewSimulationScreen("UTF-8")
	application.SetScreen(screen)
	screen.SetSize(80, 24)
	t.Cleanup(screen.Fini)

	app.showBackupCopyInspectForm(job)
	application.ForceDraw()
	rendered := backupSimulationScreenText(screen)
	t.Logf("80x24 recovery-point picker:\n%s", rendered)
	for _, want := range []string{"Recovery Points", "CT400 vault", "2026-09-03 13:00", "2026-09-03 01:00", "8.0 MiB", "4.0 MiB", "sha256+format", shortCopyArtifactID(newerID), shortCopyArtifactID(olderID), "Enter", "Inspect selected"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("compact recovery-point picker missing %q:\n%s", want, rendered)
		}
	}
	picker, ok := application.GetFocus().(*tview.List)
	if !ok {
		t.Fatalf("picker focus = %T, want *tview.List", application.GetFocus())
	}
	first, _ := picker.GetItemText(0)
	second, _ := picker.GetItemText(1)
	if !strings.Contains(first, shortCopyArtifactID(newerID)) || !strings.Contains(second, shortCopyArtifactID(olderID)) {
		t.Fatalf("picker order is not newest first: first=%q second=%q", first, second)
	}

	picker.SetCurrentItem(1)
	picker.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(primitive tview.Primitive) { application.SetFocus(primitive) })
	application.ForceDraw()
	rendered = backupSimulationScreenText(screen)
	if !strings.Contains(rendered, "Exact ID: "+olderID) || strings.Contains(rendered, newerID) {
		t.Fatalf("options form did not retain exact older artifact ID:\n%s", rendered)
	}
}

func TestBackupCopyInspectPickerHandlesEmptyAndHistoryErrors(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	job := backupcore.CopyJob{
		Name: "Empty vault", Mode: backupcore.CopyModePush,
		Source:      backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "source")},
		Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(t.TempDir(), "vault")},
		Trigger:     backupcore.CopyTriggerManual, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual},
		Verification: backupcore.CopyVerificationSHA256Format, TimeoutMinutes: 30,
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		t.Fatal(err)
	}

	newApp := func() (*App, *tview.Pages) {
		application := tview.NewApplication()
		pages := tview.NewPages()
		pages.AddPage(pageBackupCopies, tview.NewTextView(), true, true)
		application.SetRoot(pages, true)
		return &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}, pages
	}
	app, pages := newApp()
	app.showBackupCopyInspectForm(job)
	if front, _ := pages.GetFrontPage(); front != "alert" {
		t.Fatalf("empty recovery history front page = %q, want alert", front)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	app, pages = newApp()
	app.showBackupCopyInspectForm(job)
	if front, _ := pages.GetFrontPage(); front != "alert" {
		t.Fatalf("history read failure front page = %q, want alert", front)
	}
}

func recordCopyArtifactForInspectionUITest(t *testing.T, store *backupcore.Store, job backupcore.CopyJob, artifactID string, createdAt time.Time, size int64) {
	t.Helper()
	startedAt := createdAt.Add(time.Hour)
	owner := "copy-inspection-ui-" + artifactID
	if _, err := store.ClaimCopyJob(context.Background(), job.ID, owner, startedAt); err != nil {
		t.Fatal(err)
	}
	run, err := store.StartCopyRun(context.Background(), job.ID, backupcore.CopyTriggerManual, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = backupcore.RunSucceeded
	run.FinishedAt = startedAt.Add(time.Second)
	run.Discovered = 1
	run.BytesCopied = size
	run.Artifacts = []backupcore.CopyArtifactResult{{
		ArtifactID: artifactID, Source: filepath.Join(job.Source.Location, artifactID+".sqlite"), Destination: filepath.Join(job.Destination.Location, artifactID+".sqlite"),
		SourceCreatedAt: createdAt, SizeBytes: size, SHA256: strings.Repeat("a", 64), Verification: backupcore.CopyVerificationSHA256Format,
		VerifiedAt: startedAt.Add(500 * time.Millisecond), ManifestPath: filepath.Join(job.Destination.Location, artifactID+".sqlite"+backupcore.ArtifactManifestSuffix), ManifestSize: 512,
		ManifestSHA256: strings.Repeat("b", 64), PublicationState: backupcore.ArtifactPublicationComplete,
	}}
	if err := store.FinishCopyRun(context.Background(), &run, owner); err != nil {
		t.Fatal(err)
	}
}

func validCopyUIHostFingerprint() string {
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(make([]byte, 32))
}

func strconvItoa(value int) string {
	return fmt.Sprintf("%d", value)
}
