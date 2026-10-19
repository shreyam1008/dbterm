package backup

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

func checkpointBackupError(name string) error {
	return fmt.Errorf("backup stopped because source checkpoint journal %q is present; finish or discard the checkpoint in Change Profiler, retry any pending source cleanup, then run the backup again (journal objects must not be restored as active recording triggers)", name)
}

func checkBackupSourceJournals(ctx context.Context, source logicalDumpQueryer, engine config.DBType) error {
	query := database.ListTablesQuery(engine)
	if query == "" {
		return nil
	}
	rows, err := source.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("check backup source for checkpoint journals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("read backup source checkpoint metadata: %w", err)
		}
		if database.IsCheckpointJournalTable(engine, name) {
			return checkpointBackupError(name)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if engine == config.SQLite || engine == config.Turso {
		// A damaged/interrupted journal can leave application-table triggers
		// after its log table was removed. A snapshot must not propagate them.
		triggers, err := source.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger'`)
		if err != nil {
			return fmt.Errorf("check backup source checkpoint triggers: %w", err)
		}
		defer triggers.Close()
		for triggers.Next() {
			var name string
			if err := triggers.Scan(&name); err != nil {
				return err
			}
			if isSQLiteCheckpointTrigger(name) {
				return checkpointBackupError(name)
			}
		}
		return triggers.Err()
	}
	return nil
}

func createServerBackupWithoutJournals(ctx context.Context, cfg *config.ConnectionConfig, create func() error) error {
	source, err := database.Connect(cfg)
	if err != nil {
		return fmt.Errorf("connect for backup checkpoint preflight: %w", err)
	}
	defer source.Close()
	if err := checkBackupSourceJournals(ctx, source, cfg.Type); err != nil {
		return err
	}
	if err := create(); err != nil {
		return err
	}
	return checkBackupSourceJournals(ctx, source, cfg.Type)
}

func checkSQLiteBackupJournals(ctx context.Context, path string) error {
	source, err := sql.Open("sqlite", sqliteFileDSN(path, "ro"))
	if err != nil {
		return fmt.Errorf("open captured SQLite snapshot for checkpoint validation: %w", err)
	}
	source.SetMaxOpenConns(1)
	defer source.Close()
	return checkBackupSourceJournals(ctx, source, config.SQLite)
}

func checkMySQLBackupJournals(ctx context.Context, path, targetDatabase string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	err = scanMySQLStatements(bufio.NewReaderSize(&contextReader{ctx: ctx, reader: file}, restoreSQLBufferSize), targetDatabase, func(tokens []string) error {
		if len(tokens) < 2 {
			return nil
		}
		if strings.EqualFold(tokens[0], "CREATE") && strings.EqualFold(tokens[1], "TABLE") {
			index := 2
			if len(tokens) > 5 && strings.EqualFold(tokens[2], "IF") && strings.EqualFold(tokens[3], "NOT") && strings.EqualFold(tokens[4], "EXISTS") {
				index = 5
			}
			if name := mysqlBackupDeclarationName(tokens, index); database.IsCheckpointJournalTable(config.MySQL, name) {
				return checkpointBackupError(name)
			}
			return nil
		}
		// mysqldump may split CREATE / DEFINER / TRIGGER into separate executable
		// comments. Inspect its TRIGGER fragment as well as ordinary CREATE.
		if strings.EqualFold(tokens[0], "CREATE") || strings.EqualFold(tokens[0], "TRIGGER") {
			for index, token := range tokens {
				if strings.EqualFold(token, "TRIGGER") {
					if name := mysqlBackupDeclarationName(tokens, index+1); isMySQLCheckpointTrigger(name) {
						return checkpointBackupError(name)
					}
					break
				}
			}
		}
		return nil
	})
	if ctx.Err() != nil {
		return fmt.Errorf("captured MySQL checkpoint validation stopped: %w", ctx.Err())
	}
	return err
}

func mysqlBackupDeclarationName(tokens []string, index int) string {
	if index >= len(tokens) {
		return ""
	}
	name := tokens[index]
	if index+2 < len(tokens) && tokens[index+1] == "." {
		return tokens[index+2]
	}
	if separator := strings.LastIndexByte(name, '.'); separator >= 0 {
		name = name[separator+1:]
	}
	return name
}

func isMySQLCheckpointTrigger(name string) bool {
	const baseLength = len("_dbterm_cp_") + 24
	if len(name) <= baseLength+3 || !database.IsCheckpointJournalTable(config.MySQL, name[:baseLength]+"_events") || name[baseLength] != '_' {
		return false
	}
	suffix := name[baseLength+1:]
	separator := strings.LastIndexByte(suffix, '_')
	if separator < 1 || len(suffix)-separator != 2 || !strings.ContainsRune("iud", rune(suffix[len(suffix)-1])) {
		return false
	}
	return backupJournalDecimal(suffix[:separator])
}

func isSQLiteCheckpointTrigger(name string) bool {
	const baseLength = len("__dbterm_cp_") + 24
	if len(name) <= baseLength+1 || !database.IsCheckpointJournalTable(config.SQLite, name[:baseLength]) || name[baseLength] != '_' {
		return false
	}
	suffix := strings.SplitN(name[baseLength+1:], "_", 2)
	if len(suffix) != 2 || !backupJournalDecimal(suffix[0]) {
		return false
	}
	switch suffix[1] {
	case "insert", "update", "delete", "probe_insert", "probe_update":
		return true
	default:
		return false
	}
}

func backupJournalDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func checkPostgresBackupJournals(ctx context.Context, path string) error {
	tool, err := requireClientTool("pg_restore")
	if err != nil {
		return fmt.Errorf("PostgreSQL backup checkpoint validation requires pg_restore: %w", err)
	}
	cmd := exec.CommandContext(ctx, tool, "--list", path)
	stderr := &restoreTailBuffer{limit: restoreOutputTailBytes}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return externalCommandError(ctx, "pg_restore --list", []byte(stderr.String()), err)
	}
	validationErr := scanPostgresBackupTOC(stdout)
	if validationErr != nil {
		// Stop rather than leaving a child blocked on an unread output pipe.
		_ = cmd.Process.Kill()
	}
	commandErr := cmd.Wait()
	if ctx.Err() != nil {
		return fmt.Errorf("captured PostgreSQL checkpoint validation stopped: %w", ctx.Err())
	}
	if validationErr != nil {
		return validationErr
	}
	if commandErr != nil {
		return externalCommandError(ctx, "pg_restore --list", []byte(stderr.String()), commandErr)
	}
	return nil
}

func scanPostgresBackupTOC(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(strings.TrimSpace(line), ";") {
			continue
		}
		separator := strings.IndexByte(line, ';')
		if separator < 0 {
			continue
		}
		fields := strings.Fields(line[separator+1:])
		// Ignore header comments and the final owner field. The private schema
		// is an exact reserved namespace, unlike similarly named user schemas.
		if len(fields) < 6 {
			continue
		}
		if strings.EqualFold(fields[2], "SCHEMA") && database.IsCheckpointJournalTable(config.PostgreSQL, fields[4]+".events") {
			return checkpointBackupError(fields[4])
		}
		if len(fields) >= 7 && strings.EqualFold(fields[2], "TRIGGER") && isPostgresCheckpointTrigger(fields[5]) {
			return checkpointBackupError(fields[5])
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read PostgreSQL backup table of contents: %w", err)
	}
	return nil
}

func isPostgresCheckpointTrigger(name string) bool {
	const schemaLength = len("dbterm_cp_") + 24
	if len(name) <= schemaLength+1 || !database.IsCheckpointJournalTable(config.PostgreSQL, name[:schemaLength]+".events") || name[schemaLength] != '_' {
		return false
	}
	suffix := strings.TrimSuffix(name[schemaLength+1:], "_truncate")
	return backupJournalDecimal(suffix)
}
