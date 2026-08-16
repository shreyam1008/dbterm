package mcpserver

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

var safeIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

func (s *service) inspectDatabase(ctx context.Context, input inspectDatabaseInput) (inspectDatabaseOutput, error) {
	cfg, err := s.resolveConnection(input.ConnectionID)
	if err != nil {
		return inspectDatabaseOutput{}, err
	}
	db, queryCtx, closeFn, err := s.connect(ctx, cfg)
	if err != nil {
		return inspectDatabaseOutput{}, err
	}
	defer closeFn()
	schemas, schemasTruncated, err := listSchemas(queryCtx, db, cfg.Type, s.limits.maxTables)
	if err != nil {
		return inspectDatabaseOutput{}, fmt.Errorf("list schemas: %s", redactError(err, cfg))
	}
	tables, truncated, err := listTables(queryCtx, db, cfg.Type, s.limits.maxTables)
	if err != nil {
		return inspectDatabaseOutput{}, fmt.Errorf("list tables: %s", redactError(err, cfg))
	}
	output := inspectDatabaseOutput{Connection: summary(cfg), Schemas: schemas, Tables: tables, Truncated: schemasTruncated || truncated}
	return boundInspectDatabaseOutput(output, s.limits.maxOutputBytes), nil
}

func (s *service) inspectTable(ctx context.Context, input inspectTableInput) (inspectTableOutput, error) {
	cfg, err := s.resolveConnection(input.ConnectionID)
	if err != nil {
		return inspectTableOutput{}, err
	}
	if _, _, err := splitTableName(input.Table); err != nil {
		return inspectTableOutput{}, err
	}
	db, queryCtx, closeFn, err := s.connect(ctx, cfg)
	if err != nil {
		return inspectTableOutput{}, err
	}
	defer closeFn()
	columns, err := loadColumns(queryCtx, db, cfg, input.Table)
	if err != nil {
		return inspectTableOutput{}, fmt.Errorf("inspect columns: %s", redactError(err, cfg))
	}
	foreignKeys, err := loadForeignKeys(queryCtx, db, cfg, input.Table)
	if err != nil {
		return inspectTableOutput{}, fmt.Errorf("inspect foreign keys: %s", redactError(err, cfg))
	}
	output := inspectTableOutput{Table: input.Table, Columns: columns, ForeignKeys: foreignKeys}
	if len(output.Columns) > s.limits.maxColumns {
		output.Columns = output.Columns[:s.limits.maxColumns]
		output.Truncated = true
	}
	if len(output.ForeignKeys) > s.limits.maxTables {
		output.ForeignKeys = output.ForeignKeys[:s.limits.maxTables]
		output.Truncated = true
	}
	return boundInspectTableOutput(output, s.limits.maxOutputBytes), nil
}

func listSchemas(ctx context.Context, db *sql.DB, dbType config.DBType, max int) ([]string, bool, error) {
	var query string
	switch dbType {
	case config.PostgreSQL:
		query = `SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('pg_catalog', 'information_schema') ORDER BY schema_name`
	case config.MySQL:
		query = `SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys') ORDER BY schema_name`
	case config.SQLite, config.Turso, config.CloudflareD1:
		query = `PRAGMA database_list`
	default:
		return nil, false, fmt.Errorf("unsupported database type %q", dbType)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var result []string
	truncated := false
	for rows.Next() {
		if len(result) >= max {
			truncated = true
			break
		}
		if dbType == config.SQLite || dbType == config.Turso || dbType == config.CloudflareD1 {
			var sequence int
			var name, file string
			if err := rows.Scan(&sequence, &name, &file); err != nil {
				return nil, false, err
			}
			result = append(result, name) // deliberately omit local paths
		} else {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, false, err
			}
			result = append(result, name)
		}
	}
	return result, truncated, rows.Err()
}

func boundInspectDatabaseOutput(output inspectDatabaseOutput, maximum int) inspectDatabaseOutput {
	for encodedSize(output) > maximum && len(output.Tables) > 0 {
		output.Tables = output.Tables[:len(output.Tables)-1]
		output.Truncated = true
	}
	for encodedSize(output) > maximum && len(output.Schemas) > 0 {
		output.Schemas = output.Schemas[:len(output.Schemas)-1]
		output.Truncated = true
	}
	return output
}

func boundInspectTableOutput(output inspectTableOutput, maximum int) inspectTableOutput {
	for encodedSize(output) > maximum && len(output.ForeignKeys) > 0 {
		output.ForeignKeys = output.ForeignKeys[:len(output.ForeignKeys)-1]
		output.Truncated = true
	}
	for encodedSize(output) > maximum && len(output.Columns) > 0 {
		output.Columns = output.Columns[:len(output.Columns)-1]
		output.Truncated = true
	}
	return output
}

func listTables(ctx context.Context, db *sql.DB, dbType config.DBType, max int) ([]string, bool, error) {
	query := database.ListTablesQuery(dbType)
	if query == "" {
		return nil, false, fmt.Errorf("unsupported database type %q", dbType)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := make([]string, 0, min(max, 32))
	truncated := false
	for rows.Next() {
		if len(result) >= max {
			truncated = true
			break
		}
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, false, err
		}
		result = append(result, name)
	}
	return result, truncated, rows.Err()
}

func loadColumns(ctx context.Context, db *sql.DB, cfg config.ConnectionConfig, table string) ([]columnInfo, error) {
	schema, tableOnly, err := splitTableName(table)
	if err != nil {
		return nil, err
	}
	switch cfg.Type {
	case config.PostgreSQL:
		if schema == "" {
			schema = "public"
		}
		rows, err := db.QueryContext(ctx, `SELECT c.column_name, c.data_type, c.is_nullable, COALESCE(c.column_default, ''),
EXISTS (SELECT 1 FROM information_schema.table_constraints tc JOIN information_schema.key_column_usage kcu
ON tc.constraint_name=kcu.constraint_name AND tc.table_schema=kcu.table_schema
WHERE tc.constraint_type='PRIMARY KEY' AND tc.table_schema=c.table_schema AND tc.table_name=c.table_name AND kcu.column_name=c.column_name)
FROM information_schema.columns c WHERE c.table_schema=$1 AND c.table_name=$2 ORDER BY c.ordinal_position`, schema, tableOnly)
		return scanInformationSchemaColumns(rows, err)
	case config.MySQL:
		if schema == "" {
			schema = cfg.Database
		}
		rows, err := db.QueryContext(ctx, `SELECT column_name, column_type, is_nullable, COALESCE(column_default, ''), column_key='PRI'
FROM information_schema.columns WHERE table_schema=? AND table_name=? ORDER BY ordinal_position`, schema, tableOnly)
		return scanInformationSchemaColumns(rows, err)
	case config.SQLite, config.Turso, config.CloudflareD1:
		rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(cfg.Type, tableOnly)))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var result []columnInfo
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, dataType string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
				return nil, err
			}
			result = append(result, columnInfo{Name: name, Type: dataType, Nullable: notNull == 0, Default: defaultValue.String, PrimaryKey: primaryKey > 0})
		}
		return result, rows.Err()
	default:
		return nil, fmt.Errorf("unsupported database type %q", cfg.Type)
	}
}

func scanInformationSchemaColumns(rows *sql.Rows, err error) ([]columnInfo, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []columnInfo
	for rows.Next() {
		var name, dataType, nullable, defaultValue string
		var primaryKey bool
		if err := rows.Scan(&name, &dataType, &nullable, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		result = append(result, columnInfo{Name: name, Type: dataType, Nullable: strings.EqualFold(nullable, "YES"), Default: defaultValue, PrimaryKey: primaryKey})
	}
	return result, rows.Err()
}

func loadForeignKeys(ctx context.Context, db *sql.DB, cfg config.ConnectionConfig, table string) ([]foreignKeyInfo, error) {
	return loadForeignKeysWith(ctx, db, cfg, table)
}

func loadForeignKeysWith(ctx context.Context, db queryContext, cfg config.ConnectionConfig, table string) ([]foreignKeyInfo, error) {
	schema, tableOnly, err := splitTableName(table)
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	switch cfg.Type {
	case config.PostgreSQL:
		if schema == "" {
			schema = "public"
		}
		// pg_catalog arrays preserve the component pairing for composite keys;
		// information_schema.constraint_column_usage does not expose that pairing.
		rows, err = db.QueryContext(ctx, `SELECT constraint_row.conname,
source_attribute.attname, target_namespace.nspname, target_table.relname,
target_attribute.attname, key_position.ordinal
FROM pg_catalog.pg_constraint AS constraint_row
JOIN pg_catalog.pg_class AS source_table ON source_table.oid=constraint_row.conrelid
JOIN pg_catalog.pg_namespace AS source_namespace ON source_namespace.oid=source_table.relnamespace
JOIN pg_catalog.pg_class AS target_table ON target_table.oid=constraint_row.confrelid
JOIN pg_catalog.pg_namespace AS target_namespace ON target_namespace.oid=target_table.relnamespace
CROSS JOIN LATERAL generate_subscripts(constraint_row.conkey, 1) AS key_position(ordinal)
JOIN pg_catalog.pg_attribute AS source_attribute ON source_attribute.attrelid=constraint_row.conrelid AND source_attribute.attnum=constraint_row.conkey[key_position.ordinal]
JOIN pg_catalog.pg_attribute AS target_attribute ON target_attribute.attrelid=constraint_row.confrelid AND target_attribute.attnum=constraint_row.confkey[key_position.ordinal]
WHERE constraint_row.contype='f' AND source_namespace.nspname=$1 AND source_table.relname=$2
ORDER BY constraint_row.conname, key_position.ordinal`, schema, tableOnly)
	case config.MySQL:
		if schema == "" {
			schema = cfg.Database
		}
		rows, err = db.QueryContext(ctx, `SELECT constraint_name, column_name, referenced_table_schema, referenced_table_name, referenced_column_name, ordinal_position
FROM information_schema.key_column_usage WHERE table_schema=? AND table_name=? AND referenced_table_name IS NOT NULL
ORDER BY constraint_name, ordinal_position`, schema, tableOnly)
	case config.SQLite, config.Turso, config.CloudflareD1:
		rows, err = db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%s)", quoteIdentifier(cfg.Type, tableOnly)))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var result []foreignKeyInfo
		for rows.Next() {
			var id, seq int
			var target, sourceCol, targetCol, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &target, &sourceCol, &targetCol, &onUpdate, &onDelete, &match); err != nil {
				return nil, err
			}
			name := fmt.Sprintf("fk#%d", id)
			result = appendFKColumn(result, name, tableOnly, target, sourceCol, targetCol)
		}
		return result, rows.Err()
	default:
		return nil, fmt.Errorf("unsupported database type %q", cfg.Type)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []foreignKeyInfo
	for rows.Next() {
		var name, sourceCol, targetSchema, targetTable, targetCol string
		var ordinal int
		if err := rows.Scan(&name, &sourceCol, &targetSchema, &targetTable, &targetCol, &ordinal); err != nil {
			return nil, err
		}
		target := targetTable
		if targetSchema != "" {
			target = targetSchema + "." + targetTable
		}
		result = appendFKColumn(result, name, table, target, sourceCol, targetCol)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, rows.Err()
}

func appendFKColumn(result []foreignKeyInfo, name, sourceTable, targetTable, sourceCol, targetCol string) []foreignKeyInfo {
	for i := range result {
		if result[i].Name == name && result[i].TargetTable == targetTable {
			result[i].Columns = append(result[i].Columns, foreignKeyColumn{Source: sourceCol, Target: targetCol})
			return result
		}
	}
	return append(result, foreignKeyInfo{Name: name, SourceTable: sourceTable, TargetTable: targetTable, Columns: []foreignKeyColumn{{Source: sourceCol, Target: targetCol}}})
}

func splitTableName(value string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) < 1 || len(parts) > 2 {
		return "", "", fmt.Errorf("table must be an unquoted name or schema.table")
	}
	for _, part := range parts {
		if !safeIdentifier.MatchString(part) {
			return "", "", fmt.Errorf("unsafe or unsupported identifier %q", part)
		}
	}
	if len(parts) == 1 {
		return "", parts[0], nil
	}
	return parts[0], parts[1], nil
}

func quoteIdentifier(dbType config.DBType, value string) string {
	if dbType == config.MySQL {
		return "`" + strings.ReplaceAll(value, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteTable(dbType config.DBType, value string) (string, error) {
	schema, table, err := splitTableName(value)
	if err != nil {
		return "", err
	}
	if schema == "" {
		return quoteIdentifier(dbType, table), nil
	}
	return quoteIdentifier(dbType, schema) + "." + quoteIdentifier(dbType, table), nil
}
