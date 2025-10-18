package ui

import (
	"bufio"
	"context"
	"database/sql"
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

		if filepath.Ext(strings.ToLower(fileName)) == "" {
			fileName += plan.extension
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
		" [#a6adc8]%s[-]  │  [green]%s[-]  │  [#a6adc8]%s[-]  │  [yellow]Esc[-] Cancel",
		backupTargetLabel(cfg),
		plan.formatLabel,
		plan.toolLabel,
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
	plan, err := backupPlanFor(cfg)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, err), returnPage)
		return
	}

	a.showLoadingModal(fmt.Sprintf("%s Creating %s...", iconBackup, plan.formatLabel))

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
			a.ShowAlert(fmt.Sprintf("%s Backup created\n\nType: %s\nFormat: %s\nPath: %s%s", iconSuccess, cfg.TypeLabel(), plan.formatLabel, outputPath, sizeLine), returnPage)
		})
	}()
}

func runDatabaseDump(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	switch cfg.Type {
	case config.PostgreSQL:
		return runPostgresDump(ctx, cfg, outputPath)
	case config.MySQL:
		return runMySQLDump(ctx, cfg, outputPath)
	case config.SQLite:
		return runSQLiteSnapshot(ctx, cfg, outputPath)
	case config.Turso, config.CloudflareD1:
		return runSQLiteCompatibleDump(ctx, cfg, outputPath)
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
		"--format=custom",
		"--encoding=UTF8",
		"--compress=6",
		"--no-owner",
		"--no-privileges",
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

func runSQLiteSnapshot(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	if strings.TrimSpace(cfg.FilePath) == "" {
		return fmt.Errorf("sqlite backup requires a file-backed database")
	}
	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("backup file already exists: %s", outputPath)
	}

	db, err := utils.ConnectDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO %s", sqliteStringLiteral(outputPath)))
	if err != nil {
		return fmt.Errorf("sqlite backup failed: %w", err)
	}
	return nil
}

func runSQLiteCompatibleDump(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	db, err := utils.ConnectDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	return writeSQLiteCompatibleDump(ctx, db, cfg, outputPath)
}

func writeSQLiteCompatibleDump(ctx context.Context, db *sql.DB, cfg *config.ConnectionConfig, outputPath string) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("could not create backup file %s: %w", outputPath, err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	if err := writeDumpLine(writer, "PRAGMA foreign_keys=OFF;"); err != nil {
		return err
	}
	if err := writeDumpLine(writer, "BEGIN TRANSACTION;"); err != nil {
		return err
	}

	tables, err := sqliteDumpTables(ctx, db)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if err := writeDumpLine(writer, table.createSQL+";"); err != nil {
			return err
		}
	}
	for _, table := range tables {
		if err := sqliteDumpTableData(ctx, db, writer, cfg.Type, table); err != nil {
			return err
		}
	}

	extraObjects, err := sqliteDumpExtraObjects(ctx, db)
	if err != nil {
		return err
	}
	for _, ddl := range extraObjects {
		if err := writeDumpLine(writer, ddl+";"); err != nil {
			return err
		}
	}

	if err := writeDumpLine(writer, "COMMIT;"); err != nil {
		return err
	}

	return writer.Flush()
}

type sqliteDumpTable struct {
	name      string
	createSQL string
	columns   []string
}

func sqliteDumpTables(ctx context.Context, db *sql.DB) ([]sqliteDumpTable, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, sql
FROM sqlite_master
WHERE type = 'table'
  AND name NOT LIKE 'sqlite_%'
  AND sql IS NOT NULL
ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("could not read sqlite tables: %w", err)
	}
	defer rows.Close()

	var tables []sqliteDumpTable
	for rows.Next() {
		var name, createSQL string
		if err := rows.Scan(&name, &createSQL); err != nil {
			return nil, fmt.Errorf("could not scan sqlite table metadata: %w", err)
		}

		columns, err := sqliteDumpColumns(ctx, db, name)
		if err != nil {
			return nil, err
		}
		tables = append(tables, sqliteDumpTable{name: name, createSQL: createSQL, columns: columns})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("could not iterate sqlite tables: %w", err)
	}
	return tables, nil
}

func sqliteDumpColumns(ctx context.Context, db *sql.DB, tableName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(config.SQLite, tableName)))
	if err != nil {
		return nil, fmt.Errorf("could not read columns for %s: %w", tableName, err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("could not scan column metadata for %s: %w", tableName, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("could not iterate columns for %s: %w", tableName, err)
	}
	return columns, nil
}

func sqliteDumpTableData(ctx context.Context, db *sql.DB, writer *bufio.Writer, dbType config.DBType, table sqliteDumpTable) error {
	if len(table.columns) == 0 {
		return nil
	}

	selectParts := make([]string, 0, len(table.columns))
	insertColumns := make([]string, 0, len(table.columns))
	for _, column := range table.columns {
		quotedColumn := quoteIdentifier(dbType, column)
		selectParts = append(selectParts, fmt.Sprintf("quote(%s)", quotedColumn))
		insertColumns = append(insertColumns, quoteIdentifier(dbType, column))
	}

	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectParts, ", "), quoteIdentifier(dbType, table.name))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("could not dump rows for %s: %w", table.name, err)
	}
	defer rows.Close()

	values := make([]sql.NullString, len(table.columns))
	scanTargets := make([]any, len(table.columns))
	for i := range values {
		scanTargets[i] = &values[i]
	}

	insertPrefix := fmt.Sprintf("INSERT INTO %s (%s) VALUES(", quoteIdentifier(dbType, table.name), strings.Join(insertColumns, ", "))
	for rows.Next() {
		if err := rows.Scan(scanTargets...); err != nil {
			return fmt.Errorf("could not scan dump row for %s: %w", table.name, err)
		}

		literals := make([]string, len(values))
		for i, value := range values {
			if value.Valid {
				literals[i] = value.String
				continue
			}
			literals[i] = "NULL"
		}
		if err := writeDumpLine(writer, insertPrefix+strings.Join(literals, ", ")+");"); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("could not iterate rows for %s: %w", table.name, err)
	}
	return nil
}

func sqliteDumpExtraObjects(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT sql
FROM sqlite_master
WHERE type IN ('view', 'index', 'trigger')
  AND name NOT LIKE 'sqlite_%'
  AND sql IS NOT NULL
ORDER BY CASE type
  WHEN 'view' THEN 0
  WHEN 'index' THEN 1
  WHEN 'trigger' THEN 2
  ELSE 3
END, name`)
	if err != nil {
		return nil, fmt.Errorf("could not read sqlite objects: %w", err)
	}
	defer rows.Close()

	var objects []string
	for rows.Next() {
		var ddl string
		if err := rows.Scan(&ddl); err != nil {
			return nil, fmt.Errorf("could not scan sqlite object definition: %w", err)
		}
		objects = append(objects, ddl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("could not iterate sqlite objects: %w", err)
	}
	return objects, nil
}

func writeDumpLine(writer *bufio.Writer, line string) error {
	if writer == nil {
		return fmt.Errorf("dump writer is not available")
	}
	if _, err := writer.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("could not write dump output: %w", err)
	}
	return nil
}

func backupPlanFor(cfg *config.ConnectionConfig) (backupPlan, error) {
	switch cfg.Type {
	case config.PostgreSQL:
		return backupPlan{
			formatLabel: "pg_dump custom archive (.dump)",
			toolLabel:   "pg_dump / pg_restore",
			extension:   ".dump",
		}, nil
	case config.MySQL:
		return backupPlan{
			formatLabel: "mysqldump SQL (.sql)",
			toolLabel:   "mysqldump / mysql",
			extension:   ".sql",
		}, nil
	case config.SQLite:
		return backupPlan{
			formatLabel: "SQLite snapshot (.sqlite3)",
			toolLabel:   "VACUUM INTO",
			extension:   ".sqlite3",
		}, nil
	case config.Turso:
		return backupPlan{
			formatLabel: "SQLite-compatible SQL dump (.sql)",
			toolLabel:   "dbterm logical exporter",
			extension:   ".sql",
		}, nil
	case config.CloudflareD1:
		return backupPlan{
			formatLabel: "SQLite-compatible SQL dump (.sql)",
			toolLabel:   "dbterm logical exporter",
			extension:   ".sql",
		}, nil
	default:
		return backupPlan{}, fmt.Errorf("backup is not supported for %s", cfg.TypeLabel())
	}
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

func sqliteStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
