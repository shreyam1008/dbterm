package backup

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/peterheb/cfd1"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

type NativePlan struct {
	Format      string
	FormatLabel string
	ToolLabel   string
	Extension   string
}

type NativeOptions struct {
	PostgresCompression int
	Progress            ProgressFunc

	// d1ExportURL and d1HTTPClient are private test seams. Production always
	// requests a native export from Cloudflare and downloads its signed URL;
	// tests can keep that workflow deterministic without external network use.
	d1ExportURL  func(context.Context, string, string, string) (string, error)
	d1HTTPClient *http.Client
}

func PlanFor(cfg *config.ConnectionConfig) (NativePlan, error) {
	if cfg == nil {
		return NativePlan{}, fmt.Errorf("database connection is required")
	}
	switch cfg.Type {
	case config.PostgreSQL:
		return NativePlan{Format: "postgres_custom", FormatLabel: "pg_dump custom archive", ToolLabel: "pg_dump / pg_restore", Extension: ".dump"}, nil
	case config.MySQL:
		return NativePlan{Format: "mysql_sql", FormatLabel: "mysqldump SQL", ToolLabel: "mysqldump / mysql", Extension: ".sql"}, nil
	case config.SQLite:
		return NativePlan{Format: "sqlite_database", FormatLabel: "SQLite snapshot", ToolLabel: "VACUUM INTO", Extension: ".sqlite3"}, nil
	case config.Turso:
		return NativePlan{Format: "sqlite_sql", FormatLabel: "SQLite-compatible SQL dump", ToolLabel: "dbterm logical exporter", Extension: ".sql"}, nil
	case config.CloudflareD1:
		return NativePlan{Format: "sqlite_sql", FormatLabel: "Cloudflare D1 native SQL export", ToolLabel: "Cloudflare D1 export API", Extension: ".sql"}, nil
	default:
		return NativePlan{}, fmt.Errorf("backup is not supported for %s", cfg.TypeLabel())
	}
}

// CreateNativeBackup writes the engine-native format and refuses to replace an
// existing destination. It is shared by the instant-backup UI and scheduler.
func CreateNativeBackup(ctx context.Context, cfg *config.ConnectionConfig, outputPath string, options NativeOptions) (err error) {
	defer func() { err = redactConnectionError(err, cfg) }()
	if options.Progress != nil {
		started := time.Now()
		originalProgress := options.Progress
		options.Progress = func(event ProgressEvent) {
			event.Elapsed = time.Since(started)
			originalProgress(event)
		}
	}
	plan, err := PlanFor(cfg)
	if err != nil {
		return err
	}
	outputPath = filepath.Clean(strings.TrimSpace(outputPath))
	if outputPath == "." || outputPath == "" {
		return fmt.Errorf("backup output path is required")
	}
	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("backup file already exists: %s", outputPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check backup output %s: %w", outputPath, err)
	}
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create backup output directory: %w", err)
	}
	stageDir, err := newPrivateNativeStage(time.Now())
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	tempPath := filepath.Join(stageDir, "native"+plan.Extension)

	if err := createNativeAtPath(ctx, cfg, tempPath, options); err != nil {
		return err
	}
	if err := verifyNativeBackup(cfg, tempPath); err != nil {
		return err
	}
	if err := syncRegularFile(tempPath); err != nil {
		return fmt.Errorf("sync native backup before publication: %w", err)
	}
	// Cancellation is still meaningful after the database client exits: do not
	// publish a fully staged artifact if the user canceled while verification
	// or syncing was finishing. Once publication itself begins it is atomic and
	// the completed file is preserved.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("backup stopped before publication: %w", err)
	}
	if err := publishNoReplace(ctx, tempPath, outputPath, options.Progress); err != nil {
		return err
	}
	return nil
}

func createNativeAtPath(ctx context.Context, cfg *config.ConnectionConfig, outputPath string, options NativeOptions) (err error) {
	if cfg == nil {
		return fmt.Errorf("database connection is required")
	}
	stopProgress := monitorNativeProgress(ctx, outputPath, options.Progress)
	defer func() { stopProgress(err == nil) }()
	switch cfg.Type {
	case config.PostgreSQL:
		return runPostgresDump(ctx, cfg, outputPath, options.PostgresCompression)
	case config.MySQL:
		return runMySQLDump(ctx, cfg, outputPath)
	case config.SQLite:
		return runSQLiteSnapshot(ctx, cfg, outputPath)
	case config.Turso:
		return runSQLiteCompatibleDump(ctx, cfg, outputPath)
	case config.CloudflareD1:
		return runCloudflareD1Export(ctx, cfg, outputPath, options)
	default:
		return fmt.Errorf("backup is not supported for %s", cfg.TypeLabel())
	}
}

func monitorNativeProgress(ctx context.Context, outputPath string, progress ProgressFunc) func(bool) {
	if progress == nil {
		return func(bool) {}
	}
	progress(ProgressEvent{Phase: "dump", Message: "creating engine-native backup"})
	monitorContext, cancel := context.WithCancel(ctx)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorContext.Done():
				return
			case <-ticker.C:
				if info, err := os.Stat(outputPath); err == nil && info.Mode().IsRegular() {
					progress(ProgressEvent{Phase: "dump", Message: "creating engine-native backup", CurrentBytes: info.Size()})
				}
			}
		}
	}()
	return func(succeeded bool) {
		cancel()
		wait.Wait()
		current := int64(0)
		if info, err := os.Stat(outputPath); err == nil && info.Mode().IsRegular() {
			current = info.Size()
		}
		message := "engine-native backup stopped before completion"
		if succeeded {
			message = "engine-native backup created"
		}
		progress(ProgressEvent{Phase: "dump", Message: message, CurrentBytes: current})
	}
}

func runPostgresDump(ctx context.Context, cfg *config.ConnectionConfig, outputPath string, compression int) error {
	tool, err := requireClientTool("pg_dump")
	if err != nil {
		return fmt.Errorf("PostgreSQL backup requires pg_dump: %w", err)
	}
	if compression < 0 || compression > 9 {
		compression = 6
	}
	passwordFile, cleanup, err := writePGPassFile(filepath.Dir(outputPath), cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	args := []string{
		"--host", nonEmpty(cfg.Host, "localhost"),
		"--port", defaultPort(cfg),
		"--username", cfg.User,
		"--format=custom",
		"--encoding=UTF8",
		fmt.Sprintf("--compress=%d", compression),
		"--no-owner",
		"--no-privileges",
		"--no-password",
		cfg.Database,
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = environmentWithout(os.Environ(), "PGPASSWORD", "PGPASSFILE", "PGSSLMODE")
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	if passwordFile != "" {
		cmd.Env = append(cmd.Env, "PGPASSFILE="+passwordFile)
	}
	if strings.TrimSpace(cfg.SSLMode) != "" {
		cmd.Env = append(cmd.Env, "PGSSLMODE="+strings.TrimSpace(cfg.SSLMode))
	}
	return runNativeCommandToFile(ctx, "pg_dump", cmd, outputPath)
}

func runMySQLDump(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	tool, err := requireClientTool("mysqldump")
	if err != nil {
		return fmt.Errorf("MySQL backup requires mysqldump: %w", err)
	}
	defaultsFile, cleanup, err := writeMySQLDefaultsFile(filepath.Dir(outputPath), cfg.Password)
	if err != nil {
		return err
	}
	defer cleanup()

	args := make([]string, 0, 16)
	if defaultsFile != "" {
		// MySQL requires defaults-file options before every other option.
		args = append(args, "--defaults-extra-file="+defaultsFile)
	}
	args = append(args,
		"--single-transaction",
		"--quick",
		"--routines",
		"--events",
		"--triggers",
		fmt.Sprintf("--host=%s", nonEmpty(cfg.Host, "localhost")),
		fmt.Sprintf("--port=%s", defaultPort(cfg)),
		fmt.Sprintf("--user=%s", cfg.User),
		"--", cfg.Database,
	)
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = environmentWithout(os.Environ(), "MYSQL_PWD")
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	return runNativeCommandToFile(ctx, "mysqldump", cmd, outputPath)
}

func runNativeCommandToFile(ctx context.Context, label string, cmd *exec.Cmd, outputPath string) error {
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create private %s output: %w", label, err)
	}
	cleanup := true
	defer func() {
		_ = output.Close()
		if cleanup {
			_ = os.Remove(outputPath)
		}
	}()
	stderr := &restoreTailBuffer{limit: restoreOutputTailBytes}
	cmd.Stdout = output
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return externalCommandError(ctx, label, []byte(stderr.String()), err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync private %s output: %w", label, err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close private %s output: %w", label, err)
	}
	cleanup = false
	return nil
}

func runSQLiteSnapshot(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	if strings.TrimSpace(cfg.FilePath) == "" {
		return fmt.Errorf("SQLite backup requires a file-backed database")
	}
	sourcePath, err := validateSQLiteSnapshotPaths(cfg.FilePath, outputPath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return fmt.Errorf("backup staging file already exists: %s", outputPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check SQLite backup staging path: %w", err)
	}
	connection := *cfg
	connection.FilePath = sqliteExistingFileDSN(sourcePath)
	db, err := database.Connect(&connection)
	if err != nil {
		return fmt.Errorf("connect to SQLite source: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO %s", sqliteLiteral(outputPath))); err != nil {
		return fmt.Errorf("create consistent SQLite snapshot: %w", err)
	}
	// SQLite creates the VACUUM target itself, so its initial mode follows the
	// process umask. Tighten it before a no-compression instant backup can be
	// hard-linked into the user-selected destination.
	if err := os.Chmod(outputPath, 0o600); err != nil {
		return fmt.Errorf("protect SQLite snapshot: %w", err)
	}
	return nil
}

func validateSQLiteSnapshotPaths(sourcePath, outputPath string) (string, error) {
	sourcePath = filepath.Clean(strings.TrimSpace(sourcePath))
	sourceAbsolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite source path: %w", err)
	}
	sourceInfo, err := os.Lstat(sourceAbsolute)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("SQLite source database does not exist: %s", sourceAbsolute)
		}
		return "", fmt.Errorf("inspect SQLite source database: %w", err)
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("SQLite source database must not be a symbolic link: %s", sourceAbsolute)
	}
	if !sourceInfo.Mode().IsRegular() {
		return "", fmt.Errorf("SQLite source database is not a regular file: %s", sourceAbsolute)
	}
	resolvedSource, err := filepath.EvalSymlinks(sourceAbsolute)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite source database: %w", err)
	}

	outputAbsolute, err := filepath.Abs(filepath.Clean(outputPath))
	if err != nil {
		return "", fmt.Errorf("resolve SQLite backup staging path: %w", err)
	}
	resolvedOutputDirectory, err := filepath.EvalSymlinks(filepath.Dir(outputAbsolute))
	if err != nil {
		return "", fmt.Errorf("resolve SQLite backup staging directory: %w", err)
	}
	resolvedOutput := filepath.Join(resolvedOutputDirectory, filepath.Base(outputAbsolute))
	if sameFilesystemPath(resolvedOutput, resolvedSource) {
		return "", fmt.Errorf("SQLite backup output must differ from its source database")
	}
	return sourceAbsolute, nil
}

func sqliteExistingFileDSN(path string) string {
	uriPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := &url.URL{Scheme: "file", Path: uriPath}
	query := uri.Query()
	// mode=rw opens an existing database read/write but never creates a missing
	// one if it disappears between path validation and sql.Open.
	query.Set("mode", "rw")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func sameFilesystemPath(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

func runCloudflareD1Export(ctx context.Context, cfg *config.ConnectionConfig, outputPath string, options NativeOptions) error {
	if cfg == nil {
		return fmt.Errorf("Cloudflare D1 connection is required")
	}
	accountID := strings.TrimSpace(cfg.AccountID)
	databaseID := strings.TrimSpace(cfg.DatabaseID)
	authToken := strings.TrimSpace(cfg.AuthToken)
	required := []struct {
		label string
		value string
	}{
		{label: "account ID", value: accountID},
		{label: "database ID", value: databaseID},
		{label: "API token", value: authToken},
	}
	for _, field := range required {
		if field.value == "" {
			return fmt.Errorf("Cloudflare D1 backup requires %s", field.label)
		}
	}
	pathFields := required[:2]
	for _, field := range pathFields {
		if strings.ContainsAny(field.value, `/\\?#`) || strings.IndexFunc(field.value, func(r rune) bool { return r < ' ' || r == 0x7f }) >= 0 {
			return fmt.Errorf("Cloudflare D1 %s contains characters that are unsafe in an API path", field.label)
		}
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return fmt.Errorf("backup staging file already exists: %s", outputPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check Cloudflare D1 backup staging path: %w", err)
	}

	exportURL := options.d1ExportURL
	if exportURL == nil {
		exportURL = requestCloudflareD1ExportURL
	}
	if options.Progress != nil {
		options.Progress(ProgressEvent{Phase: "dump", Message: "requesting a consistent native export from Cloudflare D1"})
	}
	signedURL, err := exportURL(ctx, accountID, authToken, databaseID)
	if err != nil {
		return fmt.Errorf("request Cloudflare D1 native export: %w", err)
	}
	parsedURL, err := validateCloudflareD1SignedURL(signedURL)
	if err != nil {
		return err
	}

	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create private Cloudflare D1 export staging file: %w", err)
	}
	cleanup := true
	defer func() {
		_ = output.Close()
		if cleanup {
			_ = os.Remove(outputPath)
		}
	}()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return fmt.Errorf("prepare Cloudflare D1 signed export download")
	}
	client := options.d1HTTPClient
	if client == nil {
		client = defaultCloudflareD1DownloadClient()
	}
	downloadClient := *client
	previousRedirectCheck := client.CheckRedirect
	downloadClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("Cloudflare D1 export download exceeded 10 redirects")
		}
		if _, err := validateCloudflareD1SignedURL(request.URL.String()); err != nil {
			return err
		}
		if previousRedirectCheck != nil {
			return previousRedirectCheck(request, via)
		}
		return nil
	}
	if options.Progress != nil {
		options.Progress(ProgressEvent{Phase: "dump", Message: "downloading the completed Cloudflare D1 native export"})
	}
	response, err := downloadClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("download Cloudflare D1 export: %w", ctx.Err())
		}
		return fmt.Errorf("download Cloudflare D1 export from its signed URL: %s", cloudflareD1DownloadErrorMessage(err, signedURL))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download Cloudflare D1 export: server returned HTTP %d", response.StatusCode)
	}
	if _, err := io.CopyBuffer(output, &contextReader{ctx: ctx, reader: response.Body}, make([]byte, 128*1024)); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("download Cloudflare D1 export: %w", ctx.Err())
		}
		return fmt.Errorf("write Cloudflare D1 export to private staging: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync private Cloudflare D1 export: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close private Cloudflare D1 export: %w", err)
	}
	cleanup = false
	return nil
}

func requestCloudflareD1ExportURL(ctx context.Context, accountID, authToken, databaseID string) (signedURL string, err error) {
	// The pinned client dereferences the result field returned by Cloudflare.
	// Convert an incomplete/unexpected response into an ordinary backup failure
	// rather than allowing a third-party panic to terminate the TUI or agent.
	defer func() {
		if recover() != nil {
			signedURL = ""
			err = fmt.Errorf("Cloudflare D1 returned an incomplete native-export response")
		}
	}()
	return cfd1.NewClient(accountID, authToken).Export(ctx, databaseID, nil)
}

func validateCloudflareD1SignedURL(raw string) (*url.URL, error) {
	const maximumSignedURLBytes = 32 * 1024
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maximumSignedURLBytes {
		return nil, fmt.Errorf("Cloudflare D1 returned an invalid signed export URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("Cloudflare D1 returned an invalid signed export URL; refusing a non-HTTPS or credential-bearing download")
	}
	return parsed, nil
}

func defaultCloudflareD1DownloadClient() *http.Client {
	var transport http.RoundTripper = http.DefaultTransport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	}
	return &http.Client{Transport: transport}
}

func cloudflareD1DownloadErrorMessage(downloadErr error, originalSignedURL string) string {
	message := downloadErr.Error()
	urlsToRedact := []string{originalSignedURL}
	var requestErr *url.Error
	if errors.As(downloadErr, &requestErr) {
		urlsToRedact = append(urlsToRedact, requestErr.URL)
		if requestErr.Err != nil {
			message = requestErr.Err.Error()
		}
	}
	for _, signedURL := range urlsToRedact {
		message = strings.ReplaceAll(message, signedURL, "[signed URL redacted]")
		if parsed, err := url.Parse(signedURL); err == nil && parsed.RawQuery != "" {
			message = strings.ReplaceAll(message, parsed.RawQuery, "[signature redacted]")
		}
	}
	return message
}

func runSQLiteCompatibleDump(ctx context.Context, cfg *config.ConnectionConfig, outputPath string) error {
	db, err := database.Connect(cfg)
	if err != nil {
		return fmt.Errorf("connect to backup source: %w", err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start consistent Turso backup snapshot: %w", err)
	}
	dumpErr := writeSQLiteCompatibleDump(ctx, tx, cfg.Type, outputPath)
	rollbackErr := tx.Rollback()
	if dumpErr != nil {
		return dumpErr
	}
	if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		_ = os.Remove(outputPath)
		return fmt.Errorf("close Turso backup snapshot: %w", rollbackErr)
	}
	return nil
}

type logicalDumpQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func writeSQLiteCompatibleDump(ctx context.Context, db logicalDumpQueryer, dbType config.DBType, outputPath string) error {
	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create logical backup staging file: %w", err)
	}
	writer := bufio.NewWriterSize(file, 128*1024)
	fail := func(err error) error {
		_ = file.Close()
		_ = os.Remove(outputPath)
		return err
	}
	for _, line := range []string{"PRAGMA foreign_keys=OFF;", "BEGIN TRANSACTION;"} {
		if err := writeLine(writer, line); err != nil {
			return fail(err)
		}
	}
	tables, err := logicalDumpTables(ctx, db, dbType)
	if err != nil {
		return fail(err)
	}
	for _, table := range tables {
		if err := writeLine(writer, table.createSQL+";"); err != nil {
			return fail(err)
		}
	}
	for _, table := range tables {
		if err := logicalDumpTableData(ctx, db, writer, dbType, table); err != nil {
			return fail(err)
		}
	}
	objects, err := logicalDumpExtraObjects(ctx, db)
	if err != nil {
		return fail(err)
	}
	for _, ddl := range objects {
		if err := writeLine(writer, ddl+";"); err != nil {
			return fail(err)
		}
	}
	if err := writeLine(writer, "COMMIT;"); err != nil {
		return fail(err)
	}
	if err := writer.Flush(); err != nil {
		return fail(fmt.Errorf("flush logical backup: %w", err))
	}
	if err := file.Sync(); err != nil {
		return fail(fmt.Errorf("sync logical backup: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(outputPath)
		return fmt.Errorf("close logical backup: %w", err)
	}
	return nil
}

type logicalDumpTable struct {
	name      string
	createSQL string
	columns   []string
}

func logicalDumpTables(ctx context.Context, db logicalDumpQueryer, dbType config.DBType) ([]logicalDumpTable, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, sql FROM sqlite_master
WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND sql IS NOT NULL ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read logical backup tables: %w", err)
	}
	var tables []logicalDumpTable
	for rows.Next() {
		var table logicalDumpTable
		if err := rows.Scan(&table.name, &table.createSQL); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read logical backup table metadata: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read logical backup table metadata: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close logical backup table metadata: %w", err)
	}
	if dbType == config.Turso {
		if err := rejectLogicalDumpVirtualTables(tables); err != nil {
			return nil, err
		}
	}
	// Fully consume and close the schema result before issuing PRAGMA queries.
	// Besides avoiding a second pooled connection, this keeps every Turso read
	// on the single transaction-pinned remote session.
	for index := range tables {
		tables[index].columns, err = logicalDumpColumns(ctx, db, tables[index].name)
		if err != nil {
			return nil, err
		}
	}
	return tables, nil
}

func logicalDumpColumns(ctx context.Context, db logicalDumpQueryer, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteSQLiteIdentifier(table)))
	if err != nil {
		return nil, fmt.Errorf("read columns for %s: %w", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func logicalDumpTableData(ctx context.Context, db logicalDumpQueryer, writer *bufio.Writer, _ config.DBType, table logicalDumpTable) error {
	if len(table.columns) == 0 {
		return nil
	}
	selects := make([]string, len(table.columns))
	columns := make([]string, len(table.columns))
	for i, column := range table.columns {
		quoted := quoteSQLiteIdentifier(column)
		selects[i] = "quote(" + quoted + ")"
		columns[i] = quoted
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s", strings.Join(selects, ", "), quoteSQLiteIdentifier(table.name)))
	if err != nil {
		return fmt.Errorf("read rows for %s: %w", table.name, err)
	}
	defer rows.Close()
	values := make([]sql.NullString, len(table.columns))
	targets := make([]any, len(values))
	for i := range values {
		targets[i] = &values[i]
	}
	prefix := fmt.Sprintf("INSERT INTO %s (%s) VALUES(", quoteSQLiteIdentifier(table.name), strings.Join(columns, ", "))
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		literals := make([]string, len(values))
		for i, value := range values {
			if value.Valid {
				literals[i] = value.String
			} else {
				literals[i] = "NULL"
			}
		}
		if err := writeLine(writer, prefix+strings.Join(literals, ", ")+");"); err != nil {
			return err
		}
	}
	return rows.Err()
}

func logicalDumpExtraObjects(ctx context.Context, db logicalDumpQueryer) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT sql FROM sqlite_master
WHERE type IN ('view', 'index', 'trigger') AND name NOT LIKE 'sqlite_%' AND sql IS NOT NULL
ORDER BY CASE type WHEN 'view' THEN 0 WHEN 'index' THEN 1 WHEN 'trigger' THEN 2 ELSE 3 END, name`)
	if err != nil {
		return nil, fmt.Errorf("read logical backup objects: %w", err)
	}
	defer rows.Close()
	var objects []string
	for rows.Next() {
		var ddl string
		if err := rows.Scan(&ddl); err != nil {
			return nil, err
		}
		objects = append(objects, ddl)
	}
	return objects, rows.Err()
}

func rejectLogicalDumpVirtualTables(tables []logicalDumpTable) error {
	for _, table := range tables {
		fields := strings.Fields(table.createSQL)
		if len(fields) >= 3 && strings.EqualFold(fields[0], "CREATE") && strings.EqualFold(fields[1], "VIRTUAL") && strings.EqualFold(fields[2], "TABLE") {
			return fmt.Errorf("Turso logical backup cannot safely export virtual table %q (for example FTS or R-Tree) and its shadow tables; use a Turso-native export workflow or remove the virtual table before retrying", table.name)
		}
	}
	return nil
}

func writeLine(writer *bufio.Writer, line string) error {
	if _, err := writer.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("write logical backup: %w", err)
	}
	return nil
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sqliteLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
