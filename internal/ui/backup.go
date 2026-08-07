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
	instantBackupDestinationLabel = "Destination Folder [F2 choose]"
	instantBackupFilenameLabel    = "File Name"
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
	form.SetFieldBackgroundColor(mantle).
		SetFieldTextColor(text).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetLabelColor(text)

	addBackupFormSection(form, "SOURCE", "Current workspace; no connection details are changed")
	form.AddTextView("Connection", tview.Escape(backupTargetLabel(cfg)), 0, 1, true, false)
	form.AddTextView("Format", fmt.Sprintf("[green]%s[-]  [#a6adc8]%s[-]", tview.Escape(plan.formatLabel), tview.Escape(plan.toolLabel)), 0, 1, true, false)
	addBackupFormSection(form, "DESTINATION", "Type a folder or use the native chooser")
	form.AddInputField(instantBackupDestinationLabel, defaultDir, 72, nil, nil)
	form.AddInputField(instantBackupFilenameLabel, defaultFile, 56, nil, nil)
	form.AddTextView("Storage", backupDestinationStorageText(defaultDir), 0, 2, true, false)
	form.AddTextView("Status", "[#a6adc8]Nothing is written until Create Backup is pressed.[-]", 0, 2, true, false)

	destinationField, _ := form.GetFormItemByLabel(instantBackupDestinationLabel).(*tview.InputField)
	filenameField, _ := form.GetFormItemByLabel(instantBackupFilenameLabel).(*tview.InputField)
	storageView, _ := form.GetFormItemByLabel("Storage").(*tview.TextView)
	statusView, _ := form.GetFormItemByLabel("Status").(*tview.TextView)
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
			setStatus("#a6adc8", "Destination edited. Nothing has been written.")
		})
	}
	if filenameField != nil {
		filenameField.SetChangedFunc(func(string) {
			setStatus("#a6adc8", "Filename edited. Nothing has been written.")
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
		initial := strings.TrimSpace(formInputValueByLabel(form, instantBackupDestinationLabel))
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

	form.AddButton("Choose Folder…", chooseFolder)
	form.AddButton("Create Backup", func() {
		output, prepareErr := prepareInstantBackupOutput(
			formInputValueByLabel(form, instantBackupDestinationLabel),
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
		a.runDatabaseBackup(cfg, output.path, returnPage)
	})
	form.AddButton("Cancel", closeForm)

	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyF2 {
			chooseFolder()
			return nil
		}
		if event.Key() == tcell.KeyF3 {
			if storageView != nil {
				storageView.SetText(backupDestinationStorageText(formInputValueByLabel(form, instantBackupDestinationLabel)))
			}
			return nil
		}
		if event.Key() == tcell.KeyEscape {
			closeForm()
			return nil
		}
		return event
	})

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" [yellow]Tab / Shift+Tab[-] Move  │  [yellow]F2[-] Choose folder  │  [yellow]F3[-] Refresh disk space  │  [yellow]Esc[-] Cancel\n [#a6adc8]Typed paths remain editable; canceling this form never creates a folder or backup.[-] ")

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(footer, 2, 0, false)

	modalW, modalH := a.modalSize(78, 116, 19, 24)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(instantBackupPage, grid, true, true)
	a.app.SetFocus(form)
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
		return instantBackupOutput{}, fmt.Errorf("destination folder is required")
	}
	expanded, err := expandHomePath(directory)
	if err != nil {
		return instantBackupOutput{}, fmt.Errorf("invalid destination folder: %w", err)
	}
	directory, err = filepath.Abs(filepath.Clean(expanded))
	if err != nil {
		return instantBackupOutput{}, fmt.Errorf("resolve destination folder: %w", err)
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
	outputPath := filepath.Join(directory, filename)
	if _, statErr := os.Lstat(outputPath); statErr == nil {
		return instantBackupOutput{}, fmt.Errorf("backup file already exists; choose another name: %s", outputPath)
	} else if !os.IsNotExist(statErr) {
		return instantBackupOutput{}, fmt.Errorf("inspect backup output: %w", statErr)
	}
	return instantBackupOutput{directory: directory, filename: filename, path: outputPath}, nil
}

func (a *App) runDatabaseBackup(cfg *config.ConnectionConfig, outputPath, returnPage string) {
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
		dumpErr := runDatabaseDumpWithProgress(ctx, cfg, outputPath, func(event backupcore.ProgressEvent) {
			if event.Elapsed <= 0 {
				event.Elapsed = time.Since(started)
			}
			lastProgress.Store(event)
			a.updateBackupProgress(loadingToken, loadingTitle, event, cancelText)
		})
		var infoErr error
		var fileSize string
		if dumpErr == nil && !canceled.Load() {
			var stat os.FileInfo
			stat, infoErr = os.Stat(outputPath)
			if infoErr == nil {
				fileSize = format.FormatBytes(uint64(stat.Size()))
			}
		}

		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(loadingToken) {
				return
			}

			if canceled.Load() && (dumpErr == nil || errors.Is(dumpErr, context.Canceled)) {
				message := fmt.Sprintf("%s Backup canceled. No partial artifact was published.", iconWarn)
				if dumpErr == nil {
					message = fmt.Sprintf("%s Cancellation arrived after the complete backup was atomically published. The verified artifact was preserved at:\n\n%s", iconWarn, tview.Escape(outputPath))
				}
				a.ShowAlert(message, returnPage)
				return
			}

			if dumpErr != nil {
				last := lastProgress.Load().(backupcore.ProgressEvent)
				a.ShowAlert(fmt.Sprintf("%s Backup failed:\n\n%s\n\nLast phase: %s — %s", iconFail, tview.Escape(dumpErr.Error()), tview.Escape(nonEmptyOr(last.Phase, "unknown")), tview.Escape(nonEmptyOr(last.Message, "no progress detail"))), returnPage)
				return
			}

			sizeLine := ""
			if infoErr == nil {
				sizeLine = fmt.Sprintf("\nSize: %s", fileSize)
			}
			a.ShowAlert(fmt.Sprintf("%s Backup created\n\nType: %s\nFormat: %s\nPath: %s%s", iconSuccess, cfg.TypeLabel(), plan.formatLabel, tview.Escape(outputPath), sizeLine), returnPage)
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
	if a.activeConn != nil {
		return cloneConnectionConfig(a.activeConn)
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
