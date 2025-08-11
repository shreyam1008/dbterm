package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/utils"
)

const backupTimestampLayout = "20060102_150405"

// showBackupModal opens a modal for creating timestamped SQL dumps.
func (a *App) showBackupModal() {
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
		a.ShowAlert(fmt.Sprintf("%s Could not resolve active connection details for backup.", iconWarn), returnPage)
		return
	}

	if cfg.Type != config.PostgreSQL && cfg.Type != config.MySQL {
		a.ShowAlert(fmt.Sprintf("%s Backup dumps are supported for PostgreSQL and MySQL only.", iconInfo), returnPage)
		return
	}

	defaultDir, err := os.Getwd()
	if err != nil || strings.TrimSpace(defaultDir) == "" {
		defaultDir = "."
	}

	defaultFile := defaultBackupFilename(cfg)

	form := tview.NewForm()
	form.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Database Backup ", iconBackup)).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetLabelColor(text)

	form.AddInputField("Output Directory", defaultDir, 72, nil, nil)
	form.AddInputField("File Name", defaultFile, 56, nil, nil)
	form.AddButton("Backup", func() {
		outputDir := strings.TrimSpace(formInputValue(form, "Output Directory"))
		if outputDir == "" {
			a.ShowAlert(fmt.Sprintf("%s Output directory is required.", iconInfo), "backupModal")
			return
		}

		fileName := strings.TrimSpace(formInputValue(form, "File Name"))
		if fileName == "" {
			fileName = defaultFile
		}

		if filepath.Ext(strings.ToLower(fileName)) != ".sql" {
			fileName += ".sql"
		}

		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not create output directory:\n\n%v", iconWarn, err), "backupModal")
			return
		}

		outputPath := filepath.Join(outputDir, fileName)
		a.pages.RemovePage("backupModal")
		a.runDatabaseBackup(cfg, outputPath, returnPage)
	})
	form.AddButton("Cancel", func() {
		a.pages.RemovePage("backupModal")
		a.pages.ShowPage(returnPage)
	})

	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage("backupModal")
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
		" [#a6adc8]%s %s@%s:%s/%s[-]  │  [yellow]Esc[-] Cancel  │  [green]Backup[-] writes timestamped .sql",
		cfg.TypeLabel(),
		nonEmptyOr(cfg.User, "user"),
		nonEmptyOr(cfg.Host, "localhost"),
		defaultPortFor(cfg),
		nonEmptyOr(cfg.Database, "database"),
	))

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(form, 0, 1, true).
		AddItem(footer, 1, 0, false)

	modalW, modalH := a.modalSize(62, 96, 11, 15)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage("backupModal", grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) runDatabaseBackup(cfg *config.ConnectionConfig, outputPath, returnPage string) {
	a.showLoadingModal(fmt.Sprintf("%s Creating %s dump...", iconBackup, cfg.TypeLabel()))

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()

		dumpErr := runDatabaseDump(ctx, cfg, outputPath)
		var infoErr error
		var fileSize string
		if dumpErr == nil {
			var stat os.FileInfo
			stat, infoErr = os.Stat(outputPath)
			if infoErr == nil {
				fileSize = utils.FormatBytes(uint64(stat.Size()))
			}
		}

		a.app.QueueUpdateDraw(func() {
			a.pages.RemovePage("loading")

			if dumpErr != nil {
				a.ShowAlert(fmt.Sprintf("%s Backup failed:\n\n%v", iconFail, dumpErr), returnPage)
				return
			}

			sizeLine := ""
			if infoErr == nil {
				sizeLine = fmt.Sprintf("\nSize: %s", fileSize)
			}
			a.ShowAlert(fmt.Sprintf("%s Backup created\n\nType: %s\nPath: %s%s", iconSuccess, cfg.TypeLabel(), outputPath, sizeLine), returnPage)
		})
	}()
}

func runDatabaseDump(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	switch cfg.Type {
	case config.PostgreSQL:
		return runPostgresDump(ctx, cfg, outputPath)
	case config.MySQL:
		return runMySQLDump(ctx, cfg, outputPath)
	default:
		return fmt.Errorf("backup is not supported for %s", cfg.TypeLabel())
	}
}

func runPostgresDump(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		return fmt.Errorf("pg_dump not found in PATH")
	}

	args := []string{
		"--host", nonEmptyOr(cfg.Host, "localhost"),
		"--port", defaultPortFor(cfg),
		"--username", cfg.User,
		"--format=plain",
		"--encoding=UTF8",
		"--file", outputPath,
		cfg.Database,
	}
	if cfg.Password == "" {
		args = append(args, "--no-password")
	}

	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if cfg.Password != "" {
		cmd.Env = append(cmd.Env, "PGPASSWORD="+cfg.Password)
	}
	if cfg.SSLMode != "" {
		cmd.Env = append(cmd.Env, "PGSSLMODE="+cfg.SSLMode)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("pg_dump timed out")
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("pg_dump failed: %s", msg)
	}

	return nil
}

func runMySQLDump(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	if _, err := exec.LookPath("mysqldump"); err != nil {
		return fmt.Errorf("mysqldump not found in PATH")
	}

	args := []string{
		"--single-transaction",
		"--quick",
		"--routines",
		"--events",
		"--triggers",
		fmt.Sprintf("--host=%s", nonEmptyOr(cfg.Host, "localhost")),
		fmt.Sprintf("--port=%s", defaultPortFor(cfg)),
		fmt.Sprintf("--user=%s", cfg.User),
		fmt.Sprintf("--result-file=%s", outputPath),
		"--databases", cfg.Database,
	}

	cmd := exec.CommandContext(ctx, "mysqldump", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if cfg.Password != "" {
		cmd.Env = append(cmd.Env, "MYSQL_PWD="+cfg.Password)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("mysqldump timed out")
		}
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("mysqldump failed: %s", msg)
	}

	return nil
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
	base := sanitizeBackupName(nonEmptyOr(cfg.Database, cfg.Name))
	timestamp := time.Now().Format(backupTimestampLayout)
	return fmt.Sprintf("%s_%s_%s.sql", base, strings.ToLower(string(cfg.Type)), timestamp)
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
