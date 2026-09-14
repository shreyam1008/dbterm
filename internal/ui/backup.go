package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/folderpicker"
	"github.com/shreyam1008/dbterm/internal/format"
)

const backupTimestampLayout = "20060102_150405"

const (
	instantBackupPage             = "backupModal"
	instantBackupDestinationLabel = "Save to"
	instantBackupFilenameLabel    = "File name"
)

type backupPlan struct {
	formatLabel string
	toolLabel   string
	extension   string
}

// showBackupModal opens a modal for creating timestamped database backups.
func (a *App) showBackupModal() {
	returnPage, _ := a.pages.GetFrontPage()
	if returnPage == "" {
		returnPage = "main"
	}
	returnFocus := a.app.GetFocus()

	if a.db == nil {
		a.ShowAlert(fmt.Sprintf("%s No active database connection.\n\nConnect to a database first.", iconInfo), returnPage)
		return
	}

	cfg := a.currentConnectionConfig()
	if cfg == nil {
		a.ShowAlert(fmt.Sprintf("%s Could not resolve active connection details for backup.", iconWarn), returnPage)
		return
	}

	plan, err := backupPlanFor(cfg)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s %v", iconInfo, err), returnPage)
		return
	}

	defaultDir := ""
	if home, homeErr := os.UserHomeDir(); homeErr == nil && strings.TrimSpace(home) != "" {
		defaultDir = filepath.Join(home, "dbterm-backups")
	}
	if defaultDir == "" {
		defaultDir, err = os.Getwd()
		if err != nil || strings.TrimSpace(defaultDir) == "" {
			defaultDir = "."
		}
	}

	defaultFile := defaultBackupFilename(cfg)
	var closed atomic.Bool
	var pickerCancel context.CancelFunc

	form := tview.NewForm()
	form.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Instant Backup ", iconBackup)).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetItemPadding(1)
	form.SetBorderPadding(1, 1, 2, 2)
	form.SetFieldBackgroundColor(mantle).
		SetFieldTextColor(text).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetLabelColor(text)

	form.AddTextView("Database", tview.Escape(nonEmptyOr(cfg.Name, cfg.Database)+" · "+cfg.TypeLabel()), 0, 1, true, false)
	form.AddTextView("Format", fmt.Sprintf("[green]%s[-]  [#a6adc8]%s[-]", tview.Escape(plan.formatLabel), tview.Escape(plan.toolLabel)), 0, 1, true, false)
	destinationField := newBackupFolderField(instantBackupDestinationLabel, defaultDir, 48, nil, nil)
	form.AddFormItem(destinationField)
	form.AddInputField(instantBackupFilenameLabel, defaultFile, 48, nil, nil)
	form.AddTextView("Storage", "", 0, 3, true, false)
	form.AddTextView("", "", 0, 2, true, false)

	filenameField, _ := form.GetFormItemByLabel(instantBackupFilenameLabel).(*tview.InputField)
	storageView, _ := form.GetFormItemByLabel("Storage").(*tview.TextView)
	statusView, _ := form.GetFormItemByLabel("").(*tview.TextView)
	databaseView := form.GetFormItemByLabel("Database")
	formatView := form.GetFormItemByLabel("Format")
	details := tview.NewCheckbox().SetLabel("Details")
	detailsExpanded := false
	renderFields := func() {
		form.Clear(false)
		form.SetItemPadding(1)
		form.AddFormItem(databaseView).AddFormItem(destinationField).AddFormItem(filenameField)
		if detailsExpanded {
			form.SetItemPadding(0)
			form.AddFormItem(formatView).AddFormItem(storageView)
		}
		form.AddFormItem(details).AddFormItem(statusView)
		styleBackupFormControls(form)
	}
	details.SetChangedFunc(func(expanded bool) {
		// tview invokes this callback before updating the checkbox itself.
		detailsExpanded = expanded
		if expanded {
			storageView.SetText(backupDestinationStorageText(destinationField.GetText()))
		}
		renderFields()
		setBackupFormFocus(form, "Details")
		a.app.SetFocus(form)
	})
	renderFields()
	setStatus := func(color, message string) {
		if statusView == nil {
			return
		}
		statusView.SetText(fmt.Sprintf("[%s]%s[-]", color, tview.Escape(message)))
	}
	if destinationField != nil {
		destinationField.SetChangedFunc(func(string) {
			if storageView != nil {
				storageView.SetText("[#a6adc8]Path changed; press F3 to inspect its destination volume.[-]")
			}
			setStatus("#a6adc8", "")
		})
	}
	if filenameField != nil {
		filenameField.SetChangedFunc(func(string) {
			setStatus("#a6adc8", "")
		})
	}
	restoreReturnFocus := func() {
		frontPage, _ := a.pages.GetFrontPage()
		if frontPage != returnPage {
			return
		}
		a.restoreLoadingReturnState(loadingReturnState{page: returnPage, focus: returnFocus})
	}
	closeForm := func() {
		if closed.Swap(true) {
			return
		}
		if pickerCancel != nil {
			pickerCancel()
			pickerCancel = nil
		}
		a.pages.RemovePage(instantBackupPage)
		restoreReturnFocus()
	}
	chooseFolder := func() {
		if closed.Load() {
			return
		}
		initial := strings.TrimSpace(destinationField.GetText())
		ctx, cancel := context.WithCancel(context.Background())
		pickerCancel = cancel
		token := a.showLoadingModal("Opening the system folder chooser...", withLoadingCancelOutcome("Press Esc to keep the typed destination.", cancel))
		go func() {
			selected, chooseErr := folderpicker.Choose(ctx, initial)
			cancel()
			a.app.QueueUpdateDraw(func() {
				pickerCancel = nil
				if !a.finishLoadingModal(token) || closed.Load() {
					return
				}
				if chooseErr != nil {
					if errors.Is(chooseErr, folderpicker.ErrCancelled) || errors.Is(chooseErr, context.Canceled) {
						setStatus("#a6adc8", "Folder selection canceled; the typed destination is unchanged.")
						return
					}
					setStatus("#f9e2af", fmt.Sprintf("Native chooser unavailable: %v. Type or paste a folder path instead.", chooseErr))
					return
				}
				if destinationField != nil {
					destinationField.SetText(selected)
				}
				if storageView != nil {
					storageView.SetText(backupDestinationStorageText(selected))
				}
				setStatus("#a6e3a1", "Destination selected. Review the filename, then create the backup.")
			})
		}()
	}

	form.AddButton("Create Backup", func() {
		output, prepareErr := prepareInstantBackupOutput(
			strings.TrimSpace(destinationField.GetText()),
			formInputValueByLabel(form, instantBackupFilenameLabel),
			defaultFile,
			plan.extension,
		)
		if prepareErr != nil {
			setStatus("#f38ba8", prepareErr.Error())
			return
		}
		if closed.Swap(true) {
			return
		}
		a.pages.RemovePage(instantBackupPage)
		a.runDatabaseBackup(cfg, output, returnPage)
	})
	destinationField.browse.SetSelectedFunc(chooseFolder)
	form.AddButton("Cancel", closeForm)

	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyF2 {
			chooseFolder()
			return nil
		}
		if event.Key() == tcell.KeyF3 {
			details.SetChecked(true)
			if storageView != nil {
				storageView.SetText(backupDestinationStorageText(destinationField.GetText()))
			}
			return nil
		}
		if event.Key() == tcell.KeyF4 {
			details.SetChecked(!details.IsChecked())
			return nil
		}
		if event.Key() == tcell.KeyEscape {
			closeForm()
			return nil
		}
		return event
	})

	modalW, modalH := a.modalSize(64, 88, 18, 18)
	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(instantBackupFooterText(modalW))

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(footer, 1, 0, false)

	grid := newBackupFormModal(container, modalW, modalH, footer, instantBackupFooterText)
	grid.heightHint = func() int {
		padding := 1
		if detailsExpanded {
			padding = 0
		}
		return backupFormContentHeight(form, padding)
	}

	a.pages.AddPage(instantBackupPage, grid, true, true)
	a.app.SetFocus(form)
}

func instantBackupFooterText(width int) string {
	return footerTextThatFits(width,
		" [yellow]Tab[-] Move · [yellow]F2[-] Folder · [yellow]F3[-] Space · [yellow]F4[-] Details · [yellow]Esc[-] Cancel ",
		" [yellow]F2[-] Folder · [yellow]F4[-] Details · [yellow]Esc[-] Cancel ",
		" [yellow]Esc[-] Cancel ",
	)
}

type instantBackupOutput struct {
	directory string
	filename  string
	path      string
}

// prepareInstantBackupOutput performs read-only validation. In particular it
// must not create the destination: closing the form before explicit
// confirmation should never leave filesystem state behind.
func prepareInstantBackupOutput(rawDirectory, rawFilename, defaultFilename, extension string) (instantBackupOutput, error) {
	directory := strings.TrimSpace(rawDirectory)
	if directory == "" {
		return instantBackupOutput{}, fmt.Errorf("backup destination is required")
	}
	if backupcore.IsRemoteBackupDestination(directory) {
		return instantBackupOutput{}, backupcore.ErrRcloneBackupPublicationDisabled
	}
	expanded, err := expandHomePath(directory)
	if err != nil {
		return instantBackupOutput{}, fmt.Errorf("invalid destination folder: %w", err)
	}
	directory = expanded
	directory, err = backupcore.NormalizeBackupDestination(directory)
	if err != nil {
		return instantBackupOutput{}, fmt.Errorf("resolve backup destination: %w", err)
	}
	if info, statErr := os.Stat(directory); statErr == nil {
		if !info.IsDir() {
			return instantBackupOutput{}, fmt.Errorf("destination is not a folder: %s", directory)
		}
	} else if !os.IsNotExist(statErr) {
		return instantBackupOutput{}, fmt.Errorf("inspect destination folder: %w", statErr)
	}

	filename := strings.TrimSpace(rawFilename)
	if filename == "" {
		filename = strings.TrimSpace(defaultFilename)
	}
	if filename == "" {
		return instantBackupOutput{}, fmt.Errorf("file name is required")
	}
	if filename == "." || filename == ".." || filepath.IsAbs(filename) || filepath.VolumeName(filename) != "" || strings.ContainsAny(filename, `/\\`) || filepath.Base(filename) != filename {
		return instantBackupOutput{}, fmt.Errorf("file name must be a single name without folders")
	}
	if filepath.Ext(strings.ToLower(filename)) == "" {
		filename += extension
	}
	outputPath, err := backupcore.JoinBackupDestination(directory, filename)
	if err != nil {
		return instantBackupOutput{}, err
	}
	if _, statErr := os.Lstat(outputPath); statErr == nil {
		return instantBackupOutput{}, fmt.Errorf("backup file already exists; choose another name: %s", outputPath)
	} else if !os.IsNotExist(statErr) {
		return instantBackupOutput{}, fmt.Errorf("inspect backup output: %w", statErr)
	}
	return instantBackupOutput{directory: directory, filename: filename, path: outputPath}, nil
}

func (a *App) runDatabaseBackup(cfg *config.ConnectionConfig, output instantBackupOutput, returnPage string) {
	plan, err := backupPlanFor(cfg)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, err), returnPage)
		return
	}

	// Instant backups have no hidden wall-clock cutoff. The user can stop them
	// explicitly, while scheduled jobs retain their configurable timeout.
	ctx, cancel := context.WithCancel(context.Background())
	var canceled atomic.Bool
	const cancelText = "Press Esc to cancel safely; partial output is never published."
	loadingTitle := fmt.Sprintf("%s Creating %s...", iconBackup, plan.formatLabel)
	loadingToken := a.showLoadingModal(loadingTitle,
		withLoadingCancelOutcome(cancelText, func() {
			canceled.Store(true)
			cancel()
		}))

	go func() {
		defer cancel()
		started := time.Now()
		var lastProgress atomic.Value
		lastProgress.Store(backupcore.ProgressEvent{Phase: "preflight", Message: "preparing the instant backup"})
		job := backupcore.Job{
			Name:             "Instant backup",
			ConnectionID:     nonEmptyOr(cfg.ID, "instant"),
			Destination:      output.directory,
			FilenameTemplate: backupcore.DefaultFilenameTemplate,
			Compression:      backupcore.CompressionNone,
			Encryption:       backupcore.EncryptionNone,
			Schedule:         backupcore.Schedule{Kind: backupcore.ScheduleManual},
			Retention:        backupcore.Retention{KeepLast: 1},
			TimeoutMinutes:   backupcore.DefaultTimeoutMinutes,
		}
		dumpErr := job.ApplyDefaults(time.Now())
		var artifact backupcore.Artifact
		if dumpErr == nil {
			var runID string
			runID, dumpErr = backupcore.NewID("run")
			if dumpErr == nil {
				artifact, dumpErr = (backupcore.Runner{OutputFilename: output.filename, Progress: func(event backupcore.ProgressEvent) {
					if event.Elapsed <= 0 {
						event.Elapsed = time.Since(started)
					}
					lastProgress.Store(event)
					a.updateBackupProgress(loadingToken, loadingTitle, event, cancelText)
				}}).Run(ctx, job, cfg, runID)
			}
		}

		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(loadingToken) {
				return
			}

			if canceled.Load() && dumpErr == nil && artifact.PublicationState == backupcore.ArtifactPublicationComplete {
				message := fmt.Sprintf("%s Cancellation arrived after the artifact and completion manifest were published. The successful backup was preserved at:\n\n%s", iconWarn, tview.Escape(artifact.Path))
				a.showBackupOutcome(fmt.Sprintf("%s Published backup preserved.\n\nCancellation arrived after publication.", iconWarn), message, returnPage)
				return
			}
			if canceled.Load() && strings.TrimSpace(artifact.Path) == "" && errors.Is(dumpErr, context.Canceled) {
				a.ShowAlert(fmt.Sprintf("%s Backup canceled. No partial artifact was published.", iconWarn), returnPage)
				return
			}

			if dumpErr != nil {
				last := lastProgress.Load().(backupcore.ProgressEvent)
				preserved := ""
				if strings.TrimSpace(artifact.Path) != "" {
					preserved = fmt.Sprintf("\n\nCandidate path: %s\nPublication: %s\nThis is not recorded as a successful backup. Inspect it and its sidecar, then manually remove or reconcile it.", tview.Escape(artifact.Path), tview.Escape(backupArtifactPublicationLabel(artifact)))
				}
				a.showBackupOutcome(fmt.Sprintf("%s Backup failed\n\nOpen Details for the cause and publication state.", iconFail), fmt.Sprintf("%s Backup failed:\n\n%s%s\n\nLast phase: %s — %s", iconFail, tview.Escape(dumpErr.Error()), preserved, tview.Escape(nonEmptyOr(last.Phase, "unknown")), tview.Escape(nonEmptyOr(last.Message, "no progress detail"))), returnPage)
				return
			}

			a.showBackupOutcome(fmt.Sprintf("%s Backup created\n\n%s · verified\nArtifact and manifest saved.", iconSuccess, format.FormatBytes(uint64(artifact.Size))), fmt.Sprintf("%s Backup created\n\nType: %s\nFormat: %s\nPath: %s\nManifest: %s\nSize: %s\nSHA-256: %s\nPublication: %s", iconSuccess, cfg.TypeLabel(), plan.formatLabel, tview.Escape(artifact.Path), tview.Escape(artifact.ManifestPath), format.FormatBytes(uint64(artifact.Size)), artifact.SHA256, tview.Escape(backupArtifactPublicationLabel(artifact))), returnPage)
		})
	}()
}

func runDatabaseDump(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	return runDatabaseDumpWithProgress(ctx, cfg, outputPath, nil)
}

func runDatabaseDumpWithProgress(ctx context.Context, cfg *config.ConnectionConfig, outputPath string, progress backupcore.ProgressFunc) error {
	return backupcore.CreateNativeBackup(ctx, cfg, outputPath, backupcore.NativeOptions{PostgresCompression: 6, Progress: progress})
}

func backupPlanFor(cfg *config.ConnectionConfig) (backupPlan, error) {
	plan, err := backupcore.PlanFor(cfg)
	if err != nil {
		return backupPlan{}, err
	}
	return backupPlan{
		formatLabel: plan.FormatLabel + " (" + plan.Extension + ")",
		toolLabel:   plan.ToolLabel,
		extension:   plan.Extension,
	}, nil
}

func (a *App) currentConnectionConfig() *config.ConnectionConfig {
	if a == nil {
		return nil
	}
	if a.activeConn != nil {
		return cloneConnectionConfig(a.activeConn)
	}
	if a.store == nil {
		return nil
	}

	for i := range a.store.Connections {
		conn := &a.store.Connections[i]
		if conn.Active && conn.Name == a.dbName && conn.Type == a.dbType {
			return cloneConnectionConfig(conn)
		}
	}
	for i := range a.store.Connections {
		conn := &a.store.Connections[i]
		if conn.Name == a.dbName && conn.Type == a.dbType {
			return cloneConnectionConfig(conn)
		}
	}
	return nil
}

func defaultBackupFilename(cfg *config.ConnectionConfig) string {
	plan, err := backupPlanFor(cfg)
	if err != nil {
		return "database_backup"
	}

	base := sanitizeBackupName(backupBaseName(cfg))
	timestamp := time.Now().Format(backupTimestampLayout)
	return fmt.Sprintf("%s_%s_%s%s", base, strings.ToLower(string(cfg.Type)), timestamp, plan.extension)
}

func backupBaseName(cfg *config.ConnectionConfig) string {
	if cfg == nil {
		return "database"
	}
	switch cfg.Type {
	case config.SQLite:
		if strings.TrimSpace(cfg.FilePath) != "" {
			return strings.TrimSuffix(filepath.Base(cfg.FilePath), filepath.Ext(cfg.FilePath))
		}
	case config.CloudflareD1:
		if strings.TrimSpace(cfg.DatabaseID) != "" {
			return cfg.DatabaseID
		}
	}
	return nonEmptyOr(cfg.Database, cfg.Name)
}

func backupTargetLabel(cfg *config.ConnectionConfig) string {
	if cfg == nil {
		return "database"
	}
	switch cfg.Type {
	case config.SQLite:
		return nonEmptyOr(cfg.FilePath, cfg.Name)
	case config.Turso:
		return nonEmptyOr(cfg.Host, cfg.Name)
	case config.CloudflareD1:
		return nonEmptyOr(cfg.DatabaseID, cfg.Name)
	default:
		return fmt.Sprintf("%s@%s:%s/%s",
			nonEmptyOr(cfg.User, "user"),
			nonEmptyOr(cfg.Host, "localhost"),
			defaultPortFor(cfg),
			nonEmptyOr(cfg.Database, "database"),
		)
	}
}

func sanitizeBackupName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "database"
	}

	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	cleaned := strings.Trim(b.String(), "_")
	if cleaned == "" {
		return "database"
	}
	return cleaned
}

func defaultPortFor(cfg *config.ConnectionConfig) string {
	if strings.TrimSpace(cfg.Port) != "" {
		return strings.TrimSpace(cfg.Port)
	}
	if cfg.Type == config.MySQL {
		return "3306"
	}
	return "5432"
}

func nonEmptyOr(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
