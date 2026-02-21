package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

const (
	pageImportModal         = "importModal"
	pageImportProgressModal = "importProgressModal"

	importCommandTimeout  = 30 * time.Minute
	importOutputLineLimit = 220
	importTailLineLimit   = 80

	importLabelSQLPath     = "SQL File Path"
	importLabelStopOnError = "Stop on first error"
)

var errImportCancelled = errors.New("sql import cancelled")

type importClientRequirement struct {
	binary string
	label  string
}

type importProgressModal struct {
	output                *tview.TextView
	notifyCancelRequested func()
}

// showImportModal opens a modal for importing SQL dumps into the active PG/MySQL connection.
func (a *App) showImportModal() {
	returnPage, _ := a.pages.GetFrontPage()
	if returnPage == "" {
		returnPage = "main"
	}

	cfg := a.currentConnectionConfig()
	if cfg == nil {
		a.ShowAlert(fmt.Sprintf("%s No active database connection.\n\nConnect to PostgreSQL or MySQL first, or import from Dashboard using a saved connection.", iconInfo), returnPage)
		return
	}

	a.showImportModalForConnection(cfg, returnPage)
}

func (a *App) showImportModalForConnection(cfg *config.ConnectionConfig, returnPage string) {
	if strings.TrimSpace(returnPage) == "" {
		returnPage = "main"
	}

	targetCfg, err := validateImportTarget(cfg)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, err), returnPage)
		return
	}

	if a.isImportRunning() {
		a.ShowAlert(fmt.Sprintf("%s Another SQL import is already running.\n\nWait for it to finish or cancel it first (Esc/Ctrl+C).", iconInfo), returnPage)
		return
	}

	defaultPath := ""
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		candidate := filepath.Join(wd, "dump.sql")
		if _, statErr := os.Stat(candidate); statErr == nil {
			defaultPath = candidate
		}
	}

	form := tview.NewForm()
	form.SetBorder(true).
		SetTitle(" SQL Dump Import ").
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetLabelColor(text).
		SetFieldTextColor(text)

	form.AddInputField(importLabelSQLPath, defaultPath, 76, nil, nil)
	form.AddCheckbox(importLabelStopOnError, true, nil)

	form.AddButton("Import", func() {
		if a.isImportRunning() {
			a.ShowAlert(fmt.Sprintf("%s Another SQL import is already running.", iconInfo), pageImportModal)
			return
		}

		rawPath := formInputValue(form, importLabelSQLPath)
		sqlPath, err := resolveImportSQLPath(rawPath)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, err), pageImportModal)
			return
		}

		if err := ensureImportClientAvailable(targetCfg.Type); err != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, err), pageImportModal)
			return
		}

		stopOnError := formCheckboxChecked(form, importLabelStopOnError)
		a.pages.RemovePage(pageImportModal)
		a.runSQLImport(targetCfg, sqlPath, stopOnError, returnPage)
	})
	form.AddButton("Cancel", func() {
		a.pages.RemovePage(pageImportModal)
		a.pages.ShowPage(returnPage)
	})

	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage(pageImportModal)
			a.pages.ShowPage(returnPage)
			return nil
		}
		return event
	})

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(fmt.Sprintf(
		" [#a6adc8]%s@%s:%s/%s[-]  │  [green]%s[-] recommended\n [#a6adc8]%s[-]\n %s",
		nonEmptyOr(targetCfg.User, "user"),
		nonEmptyOr(targetCfg.Host, "localhost"),
		defaultPortFor(targetCfg),
		nonEmptyOr(targetCfg.Database, "database"),
		importLabelStopOnError,
		importStopOnErrorHint(targetCfg.Type),
		importClientStatusText(targetCfg.Type),
	))

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(footer, 3, 0, false)

	modalW, modalH := a.modalSize(72, 112, 14, 20)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageImportModal, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) runSQLImport(cfg *config.ConnectionConfig, sqlPath string, stopOnError bool, returnPage string) {
	targetCfg, err := validateImportTarget(cfg)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, err), returnPage)
		return
	}

	if err := ensureImportClientAvailable(targetCfg.Type); err != nil {
		a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, err), returnPage)
		return
	}

	if a.isImportRunning() {
		a.ShowAlert(fmt.Sprintf("%s Another SQL import is already running.", iconInfo), returnPage)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), importCommandTimeout)
	if !a.beginImportRun(cancel, nil) {
		cancel()
		a.ShowAlert(fmt.Sprintf("%s Another SQL import is already running.", iconInfo), returnPage)
		return
	}

	progress := a.showImportProgressModal(targetCfg, sqlPath, stopOnError)
	a.setImportCancelNotifier(progress.notifyCancelRequested)

	outputBuffer := newRollingImportOutput(importOutputLineLimit)
	appendOutput := func(line string) {
		line = strings.TrimRight(line, "\r\n")
		a.app.QueueUpdateDraw(func() {
			outputBuffer.Add(line)
			progress.output.SetText(outputBuffer.Text())
			progress.output.ScrollToEnd()
		})
	}

	go func() {
		defer cancel()
		defer a.finishImportRun()

		var runErr error
		switch targetCfg.Type {
		case config.PostgreSQL:
			runErr = runPostgresSQLImport(ctx, targetCfg, sqlPath, stopOnError, appendOutput)
		case config.MySQL:
			runErr = runMySQLSQLImport(ctx, targetCfg, sqlPath, stopOnError, appendOutput)
		default:
			runErr = fmt.Errorf("SQL import is not supported for %s", targetCfg.TypeLabel())
		}

		a.app.QueueUpdateDraw(func() {
			a.pages.RemovePage(pageImportProgressModal)

			if runErr != nil {
				if errors.Is(runErr, errImportCancelled) {
					a.ShowAlert(fmt.Sprintf("%s SQL import cancelled by user.\n\nType: %s\nDatabase: %s", iconWarn,
						targetCfg.TypeLabel(), nonEmptyOr(targetCfg.Database, "database")), returnPage)
					return
				}
				a.ShowAlert(fmt.Sprintf("%s SQL import failed:\n\n%v", iconFail, runErr), returnPage)
				return
			}

			refreshedWorkspace := false
			if a.shouldRefreshAfterImport(targetCfg) {
				if refreshErr := a.refreshData(); refreshErr != nil {
					a.ShowAlert(fmt.Sprintf("%s SQL import completed, but refresh failed:\n\n%v", iconWarn, refreshErr), returnPage)
					return
				}
				refreshedWorkspace = true
			}

			a.ShowAlert(buildImportSuccessMessage(targetCfg, sqlPath, returnPage, refreshedWorkspace), returnPage)
		})
	}()
}

func (a *App) showImportProgressModal(cfg *config.ConnectionConfig, sqlPath string, stopOnError bool) *importProgressModal {
	stopMode := "enabled"
	if !stopOnError {
		stopMode = "disabled"
	}

	header := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft).
		SetWrap(true)
	header.SetBackgroundColor(crust)
	header.SetText(fmt.Sprintf(
		" [#a6adc8]Importing into[-] [green]%s[-] [#a6adc8](%s)[-]\n [#a6adc8]File[-]: %s  │  [#a6adc8]Stop on error[-]: %s",
		nonEmptyOr(cfg.Database, "database"),
		cfg.TypeLabel(),
		sqlPath,
		stopMode,
	))

	outputView := tview.NewTextView().
		SetDynamicColors(false).
		SetScrollable(true).
		SetWrap(true)
	outputView.SetBorder(true).
		SetTitle(" Import Output ").
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	outputView.SetBackgroundColor(bg)
	outputView.SetTextColor(text)
	outputView.SetText("Starting import...\n")

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(fmt.Sprintf(" [#a6adc8]Streaming client output... timeout: %s  │  [yellow]Esc[-]/[yellow]Ctrl+C[-] cancel[-] ", importCommandTimeout.Round(time.Minute)))

	var cancelOnce sync.Once
	notifyCancelRequested := func() {
		cancelOnce.Do(func() {
			a.app.QueueUpdateDraw(func() {
				footer.SetText(" [yellow]Cancel requested... waiting for database client to stop safely.[-] ")
				current := outputView.GetText(false)
				if strings.TrimSpace(current) == "" {
					current = "Starting import..."
				}
				if !strings.Contains(current, "Cancellation requested") {
					outputView.SetText(current + "\nCancellation requested... stopping import.")
					outputView.ScrollToEnd()
				}
			})
		})
	}

	outputView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.requestImportCancel()
			return nil
		}
		return event
	})

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 2, 0, false).
		AddItem(outputView, 0, 1, true).
		AddItem(footer, 1, 0, false)

	modalW, modalH := a.modalSize(86, 124, 16, 28)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageImportProgressModal, grid, true, true)
	a.app.SetFocus(outputView)
	return &importProgressModal{
		output:                outputView,
		notifyCancelRequested: notifyCancelRequested,
	}
}

func validateImportTarget(cfg *config.ConnectionConfig) (*config.ConnectionConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("could not resolve connection details for import")
	}

	target := cloneConnectionConfig(cfg)
	target.Host = strings.TrimSpace(target.Host)
	target.Port = strings.TrimSpace(target.Port)
	target.User = strings.TrimSpace(target.User)
	target.Database = strings.TrimSpace(target.Database)

	if target.Type != config.PostgreSQL && target.Type != config.MySQL {
		return nil, fmt.Errorf("SQL import is supported for PostgreSQL and MySQL only")
	}

	if target.Database == "" {
		return nil, fmt.Errorf("selected connection is missing a database name. Update it and try again")
	}

	return target, nil
}

func importStopOnErrorHint(dbType config.DBType) string {
	if dbType == config.MySQL {
		return "Disable stop-on-error to run with mysql --force."
	}
	return "PostgreSQL uses ON_ERROR_STOP when enabled."
}

func importClientRequirementForType(dbType config.DBType) (importClientRequirement, error) {
	switch dbType {
	case config.PostgreSQL:
		return importClientRequirement{binary: "psql", label: "PostgreSQL client"}, nil
	case config.MySQL:
		return importClientRequirement{binary: "mysql", label: "MySQL client"}, nil
	default:
		return importClientRequirement{}, fmt.Errorf("SQL import is not supported for %s", dbType)
	}
}

func importClientStatusText(dbType config.DBType) string {
	req, err := importClientRequirementForType(dbType)
	if err != nil {
		return "[#6c7086]Import client unavailable for this database type[-]"
	}

	if _, lookErr := exec.LookPath(req.binary); lookErr == nil {
		return fmt.Sprintf(" [green]%s ready[-] ([#a6adc8]%s in PATH[-])", req.label, req.binary)
	}

	return fmt.Sprintf(" [red]%s missing[-] ([#a6adc8]%s[-])  │  [yellow]Install once to enable import[-]", req.label, req.binary)
}

func ensureImportClientAvailable(dbType config.DBType) error {
	req, err := importClientRequirementForType(dbType)
	if err != nil {
		return err
	}

	if _, lookErr := exec.LookPath(req.binary); lookErr != nil {
		return fmt.Errorf("%s not found in PATH (required binary: %s).\n\n%s", req.label, req.binary, importClientSetupHint(req.binary))
	}

	return nil
}

func importClientSetupHint(binary string) string {
	switch runtime.GOOS {
	case "linux":
		switch binary {
		case "psql":
			return "Install PostgreSQL client tools and retry.\nUbuntu/Debian: sudo apt install postgresql-client\nFedora/RHEL: sudo dnf install postgresql\nArch: sudo pacman -S postgresql"
		case "mysql":
			return "Install MySQL client tools and retry.\nUbuntu/Debian: sudo apt install mysql-client\nFedora/RHEL: sudo dnf install community-mysql\nArch: sudo pacman -S mysql-clients"
		}
	case "darwin":
		switch binary {
		case "psql":
			return "Install PostgreSQL client tools and retry.\nHomebrew: brew install libpq && brew link --force libpq"
		case "mysql":
			return "Install MySQL client tools and retry.\nHomebrew: brew install mysql-client\nThen add to PATH (if needed): export PATH=\"$(brew --prefix)/opt/mysql-client/bin:$PATH\""
		}
	case "windows":
		switch binary {
		case "psql":
			return "Install PostgreSQL command-line tools and retry.\nwinget: winget install PostgreSQL.PostgreSQL\nThen reopen terminal so PATH updates."
		case "mysql":
			return "Install MySQL command-line tools and retry.\nwinget: winget install Oracle.MySQL\nThen reopen terminal so PATH updates."
		}
	}

	return fmt.Sprintf("Install %q and ensure it is available in PATH, then retry.", binary)
}

func (a *App) beginImportRun(cancel context.CancelFunc, notify func()) bool {
	a.importMu.Lock()
	defer a.importMu.Unlock()

	if a.importRunning {
		return false
	}

	a.importRunning = true
	a.importCancelRequested = false
	a.importCancel = cancel
	a.importCancelNotify = notify
	return true
}

func (a *App) setImportCancelNotifier(notify func()) {
	a.importMu.Lock()
	defer a.importMu.Unlock()

	if !a.importRunning {
		return
	}
	a.importCancelNotify = notify
}

func (a *App) isImportRunning() bool {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	return a.importRunning
}

func (a *App) requestImportCancel() bool {
	var cancel func()
	var notify func()

	a.importMu.Lock()
	if !a.importRunning || a.importCancelRequested {
		a.importMu.Unlock()
		return false
	}
	a.importCancelRequested = true
	cancel = a.importCancel
	notify = a.importCancelNotify
	a.importMu.Unlock()

	if notify != nil {
		notify()
	}
	if cancel != nil {
		cancel()
	}
	return true
}

func (a *App) finishImportRun() {
	a.importMu.Lock()
	defer a.importMu.Unlock()

	a.importRunning = false
	a.importCancelRequested = false
	a.importCancel = nil
	a.importCancelNotify = nil
}

func (a *App) shouldRefreshAfterImport(importCfg *config.ConnectionConfig) bool {
	if a == nil || a.db == nil || importCfg == nil {
		return false
	}

	activeCfg := a.currentConnectionConfig()
	if activeCfg == nil {
		return false
	}

	return sameImportConnection(activeCfg, importCfg)
}

func sameImportConnection(activeCfg, importCfg *config.ConnectionConfig) bool {
	if activeCfg == nil || importCfg == nil {
		return false
	}

	if activeCfg.Type != importCfg.Type {
		return false
	}

	if !strings.EqualFold(strings.TrimSpace(activeCfg.Host), strings.TrimSpace(importCfg.Host)) {
		return false
	}

	if normalizeImportPort(activeCfg) != normalizeImportPort(importCfg) {
		return false
	}

	if strings.TrimSpace(activeCfg.User) != strings.TrimSpace(importCfg.User) {
		return false
	}

	if strings.TrimSpace(activeCfg.Database) != strings.TrimSpace(importCfg.Database) {
		return false
	}

	return true
}

func normalizeImportPort(cfg *config.ConnectionConfig) string {
	if cfg == nil {
		return ""
	}

	port := strings.TrimSpace(cfg.Port)
	if port != "" {
		return port
	}
	if cfg.Type == config.MySQL {
		return "3306"
	}
	if cfg.Type == config.PostgreSQL {
		return "5432"
	}
	return ""
}

func buildImportSuccessMessage(cfg *config.ConnectionConfig, sqlPath, returnPage string, refreshedWorkspace bool) string {
	msg := fmt.Sprintf("%s SQL import complete.\n\nType: %s\nDatabase: %s\nFile: %s",
		iconSuccess,
		cfg.TypeLabel(),
		nonEmptyOr(cfg.Database, "database"),
		sqlPath,
	)

	if refreshedWorkspace {
		msg += "\nWorkspace: refreshed"
		return msg
	}

	if returnPage == "dashboard" {
		msg += "\nTip: Press Enter on this connection to inspect imported data."
	}
	return msg
}

func mapImportCommandError(ctx context.Context, clientName, tailOutput string, runErr error) error {
	if runErr == nil {
		return nil
	}

	if errors.Is(ctx.Err(), context.Canceled) {
		return errImportCancelled
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s import timed out after %s", clientName, importCommandTimeout.Round(time.Minute))
	}

	detail := strings.TrimSpace(tailOutput)
	if detail == "" {
		detail = runErr.Error()
	}
	return fmt.Errorf("%s import failed: %s", clientName, detail)
}

func runPostgresSQLImport(ctx context.Context, cfg *config.ConnectionConfig, sqlPath string, stopOnError bool, emit func(string)) error {
	if err := ensureImportClientAvailable(config.PostgreSQL); err != nil {
		return err
	}

	database := strings.TrimSpace(cfg.Database)
	if database == "" {
		return fmt.Errorf("active PostgreSQL connection is missing a database name")
	}

	args := []string{
		"--host", nonEmptyOr(cfg.Host, "localhost"),
		"--port", defaultPortFor(cfg),
		"--dbname", database,
	}
	if user := strings.TrimSpace(cfg.User); user != "" {
		args = append(args, "--username", user)
	}
	if stopOnError {
		args = append(args, "--set", "ON_ERROR_STOP=1")
	}
	if strings.TrimSpace(cfg.Password) == "" {
		args = append(args, "--no-password")
	}
	args = append(args, "--file", sqlPath)

	emitImportCommand(emit, "psql", args, "")

	cmd := exec.CommandContext(ctx, "psql", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if cfg.Password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+cfg.Password)
	}
	if cfg.SSLMode != "" {
		cmd.Env = append(cmd.Env, "PGSSLMODE="+cfg.SSLMode)
	}

	tail, err := runStreamingCommand(cmd, emit, importTailLineLimit)
	return mapImportCommandError(ctx, "psql", tail, err)
}

func runMySQLSQLImport(ctx context.Context, cfg *config.ConnectionConfig, sqlPath string, stopOnError bool, emit func(string)) error {
	if err := ensureImportClientAvailable(config.MySQL); err != nil {
		return err
	}

	database := strings.TrimSpace(cfg.Database)
	if database == "" {
		return fmt.Errorf("active MySQL connection is missing a database name")
	}

	inputFile, err := os.Open(sqlPath)
	if err != nil {
		return fmt.Errorf("could not open SQL file %q: %w", sqlPath, err)
	}
	defer inputFile.Close()

	args := []string{
		fmt.Sprintf("--host=%s", nonEmptyOr(cfg.Host, "localhost")),
		fmt.Sprintf("--port=%s", defaultPortFor(cfg)),
		fmt.Sprintf("--database=%s", database),
	}
	if user := strings.TrimSpace(cfg.User); user != "" {
		args = append(args, fmt.Sprintf("--user=%s", user))
	}
	if !stopOnError {
		args = append(args, "--force")
	}

	emitImportCommand(emit, "mysql", args, sqlPath)

	cmd := exec.CommandContext(ctx, "mysql", args...)
	cmd.Stdin = inputFile
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if cfg.Password != "" {
		cmd.Env = append(cmd.Env, "MYSQL_PWD="+cfg.Password)
	}

	tail, err := runStreamingCommand(cmd, emit, importTailLineLimit)
	return mapImportCommandError(ctx, "mysql", tail, err)
}

func resolveImportSQLPath(rawPath string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", fmt.Errorf("SQL file path is required")
	}

	expanded, err := expandHomePath(trimmed)
	if err != nil {
		return "", err
	}

	cleaned := filepath.Clean(expanded)
	if abs, absErr := filepath.Abs(cleaned); absErr == nil {
		cleaned = abs
	}

	info, err := os.Stat(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("SQL file not found: %s", cleaned)
		}
		return "", fmt.Errorf("could not access SQL file %s: %w", cleaned, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("SQL file path points to a directory: %s", cleaned)
	}

	f, err := os.Open(cleaned)
	if err != nil {
		return "", fmt.Errorf("could not read SQL file %s: %w", cleaned, err)
	}
	_ = f.Close()

	return cleaned, nil
}

func expandHomePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not resolve home directory for path %q: %w", path, err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func emitImportCommand(emit func(string), name string, args []string, stdinPath string) {
	if emit == nil {
		return
	}
	emit("$ " + formatImportCommand(name, args, stdinPath))
}

func formatImportCommand(name string, args []string, stdinPath string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, name)
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}

	cmd := strings.Join(parts, " ")
	if strings.TrimSpace(stdinPath) != "" {
		cmd += " < " + strconv.Quote(stdinPath)
	}
	return cmd
}

func runStreamingCommand(cmd *exec.Cmd, emit func(string), tailLimit int) (string, error) {
	pipeReader, pipeWriter := io.Pipe()
	cmd.Stdout = pipeWriter
	cmd.Stderr = pipeWriter

	tail := newCommandOutputTail(tailLimit)

	if err := cmd.Start(); err != nil {
		_ = pipeWriter.Close()
		_ = pipeReader.Close()
		return "", err
	}

	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(pipeReader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r")
			tail.Add(line)
			if emit != nil {
				emit(line)
			}
		}
		scanDone <- scanner.Err()
	}()

	waitErr := cmd.Wait()
	_ = pipeWriter.Close()
	scanErr := <-scanDone
	_ = pipeReader.Close()

	if scanErr != nil {
		msg := fmt.Sprintf("output stream error: %v", scanErr)
		tail.Add(msg)
		if emit != nil {
			emit(msg)
		}
		if waitErr == nil {
			waitErr = scanErr
		}
	}

	return tail.String(), waitErr
}

type commandOutputTail struct {
	lines []string
	limit int
}

func newCommandOutputTail(limit int) *commandOutputTail {
	if limit < 1 {
		limit = 1
	}
	return &commandOutputTail{limit: limit}
}

func (t *commandOutputTail) Add(line string) {
	if len(t.lines) >= t.limit {
		copy(t.lines, t.lines[1:])
		t.lines[t.limit-1] = line
		return
	}
	t.lines = append(t.lines, line)
}

func (t *commandOutputTail) String() string {
	return strings.TrimSpace(strings.Join(t.lines, "\n"))
}

type rollingImportOutput struct {
	lines   []string
	limit   int
	clipped bool
}

func newRollingImportOutput(limit int) *rollingImportOutput {
	if limit < 1 {
		limit = 1
	}
	return &rollingImportOutput{limit: limit}
}

func (r *rollingImportOutput) Add(line string) {
	if len(r.lines) >= r.limit {
		copy(r.lines, r.lines[1:])
		r.lines[r.limit-1] = line
		r.clipped = true
		return
	}
	r.lines = append(r.lines, line)
}

func (r *rollingImportOutput) Text() string {
	if len(r.lines) == 0 {
		return "Waiting for command output..."
	}

	var b strings.Builder
	if r.clipped {
		b.WriteString("(showing latest output)\n")
	}
	for i, line := range r.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String()
}
