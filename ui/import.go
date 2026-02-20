package ui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// showImportModal opens a modal for importing SQL dumps into the active PG/MySQL connection.
func (a *App) showImportModal() {
	returnPage, _ := a.pages.GetFrontPage()
	if returnPage == "" {
		returnPage = "main"
	}

	if a.db == nil {
		a.ShowAlert(fmt.Sprintf("%s No active database connection.\n\nConnect to PostgreSQL or MySQL first.", iconInfo), returnPage)
		return
	}

	cfg := a.currentConnectionConfig()
	if cfg == nil {
		a.ShowAlert(fmt.Sprintf("%s Could not resolve active connection details for import.", iconWarn), returnPage)
		return
	}

	if cfg.Type != config.PostgreSQL && cfg.Type != config.MySQL {
		a.ShowAlert(fmt.Sprintf("%s SQL import is supported for PostgreSQL and MySQL only.", iconInfo), returnPage)
		return
	}

	if strings.TrimSpace(cfg.Database) == "" {
		a.ShowAlert(fmt.Sprintf("%s Active connection is missing a database name.\n\nUpdate the connection and try again.", iconWarn), returnPage)
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
		rawPath := formInputValue(form, importLabelSQLPath)
		sqlPath, err := resolveImportSQLPath(rawPath)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, err), pageImportModal)
			return
		}

		stopOnError := formCheckboxChecked(form, importLabelStopOnError)
		a.pages.RemovePage(pageImportModal)
		a.runSQLImport(cfg, sqlPath, stopOnError, returnPage)
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

	typeHint := "PostgreSQL uses ON_ERROR_STOP when enabled."
	if cfg.Type == config.MySQL {
		typeHint = "Disable stop-on-error to use mysql --force."
	}

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(fmt.Sprintf(
		" [#a6adc8]%s@%s:%s/%s[-]  │  [green]%s[-] recommended\n [#a6adc8]%s[-]",
		nonEmptyOr(cfg.User, "user"),
		nonEmptyOr(cfg.Host, "localhost"),
		defaultPortFor(cfg),
		nonEmptyOr(cfg.Database, "database"),
		importLabelStopOnError,
		typeHint,
	))

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(footer, 2, 0, false)

	modalW, modalH := a.modalSize(72, 112, 12, 18)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageImportModal, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) runSQLImport(cfg *config.ConnectionConfig, sqlPath string, stopOnError bool, returnPage string) {
	outputView := a.showImportProgressModal(cfg, sqlPath, stopOnError)
	outputBuffer := newRollingImportOutput(importOutputLineLimit)

	appendOutput := func(line string) {
		line = strings.TrimRight(line, "\r\n")
		a.app.QueueUpdateDraw(func() {
			outputBuffer.Add(line)
			outputView.SetText(outputBuffer.Text())
			outputView.ScrollToEnd()
		})
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), importCommandTimeout)
		defer cancel()

		var err error
		switch cfg.Type {
		case config.PostgreSQL:
			err = runPostgresSQLImport(ctx, cfg, sqlPath, stopOnError, appendOutput)
		case config.MySQL:
			err = runMySQLSQLImport(ctx, cfg, sqlPath, stopOnError, appendOutput)
		default:
			err = fmt.Errorf("SQL import is not supported for %s", cfg.TypeLabel())
		}

		a.app.QueueUpdateDraw(func() {
			a.pages.RemovePage(pageImportProgressModal)

			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s SQL import failed:\n\n%v", iconFail, err), returnPage)
				return
			}

			if refreshErr := a.refreshData(); refreshErr != nil {
				a.ShowAlert(fmt.Sprintf("%s SQL import completed, but refresh failed:\n\n%v", iconWarn, refreshErr), returnPage)
				return
			}

			a.ShowAlert(fmt.Sprintf("%s SQL import complete.\n\nType: %s\nDatabase: %s\nFile: %s", iconSuccess,
				cfg.TypeLabel(), nonEmptyOr(cfg.Database, "database"), sqlPath), returnPage)
		})
	}()
}

func (a *App) showImportProgressModal(cfg *config.ConnectionConfig, sqlPath string, stopOnError bool) *tview.TextView {
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
	outputView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			// Ignore Esc while import is running to avoid losing visibility of command output.
			return nil
		}
		return event
	})

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(fmt.Sprintf(" [#a6adc8]Streaming client output... timeout: %s[-] ", importCommandTimeout.Round(time.Minute)))

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
	return outputView
}

func runPostgresSQLImport(ctx context.Context, cfg *config.ConnectionConfig, sqlPath string, stopOnError bool, emit func(string)) error {
	if _, err := exec.LookPath("psql"); err != nil {
		return fmt.Errorf("psql not found in PATH. Install PostgreSQL client tools (for example: postgresql-client)")
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
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("psql import timed out after %s", importCommandTimeout.Round(time.Minute))
		}

		detail := strings.TrimSpace(tail)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("psql import failed: %s", detail)
	}
	return nil
}

func runMySQLSQLImport(ctx context.Context, cfg *config.ConnectionConfig, sqlPath string, stopOnError bool, emit func(string)) error {
	if _, err := exec.LookPath("mysql"); err != nil {
		return fmt.Errorf("mysql not found in PATH. Install MySQL client tools (for example: mysql-client)")
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
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("mysql import timed out after %s", importCommandTimeout.Round(time.Minute))
		}

		detail := strings.TrimSpace(tail)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("mysql import failed: %s", detail)
	}
	return nil
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
