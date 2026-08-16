package changeprofiler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

const (
	largeTableRows  = int64(250_000)
	largeTableBytes = int64(100 << 20)
)

func Preflight(ctx context.Context, db *sql.DB, engine config.DBType) ([]TablePlan, error) {
	return PreflightWithProgress(ctx, db, engine, nil)
}

func PreflightWithProgress(ctx context.Context, db *sql.DB, engine config.DBType, progress ProgressFunc) ([]TablePlan, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	return inspectAllTablesWithProgress(ctx, db, engine, progress)
}

func inspectAllTables(ctx context.Context, db Queryer, engine config.DBType) ([]TablePlan, error) {
	return inspectAllTablesWithProgress(ctx, db, engine, nil)
}

func inspectAllTablesWithProgress(ctx context.Context, db Queryer, engine config.DBType, progress ProgressFunc) ([]TablePlan, error) {
	query := database.ListTablesQuery(engine)
	if query == "" {
		return nil, fmt.Errorf("change profiling is not supported for %s", engine)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list tables for change profiling: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	plans := make([]TablePlan, 0, len(names))
	for index, name := range names {
		if progress != nil {
			progress(Progress{Phase: "inspecting", Table: name, TableIndex: index + 1, TableCount: len(names), Percent: index * 100 / max(len(names), 1)})
		}
		plan, err := inspectTable(ctx, db, engine, name)
		if err != nil {
			return nil, fmt.Errorf("inspect table %s: %w", name, err)
		}
		plan.Included = len(plan.Risks) == 0
		plans = append(plans, plan)
		if progress != nil {
			progress(Progress{Phase: "inspecting", Table: name, TableIndex: index + 1, TableCount: len(names), Percent: (index + 1) * 100 / max(len(names), 1)})
		}
	}
	return plans, nil
}

func inspectTable(ctx context.Context, db Queryer, engine config.DBType, name string) (TablePlan, error) {
	var plan TablePlan
	var err error
	switch engine {
	case config.PostgreSQL:
		plan, err = inspectPostgresTable(ctx, db, name)
	case config.MySQL:
		plan, err = inspectMySQLTable(ctx, db, name)
	case config.SQLite, config.Turso, config.CloudflareD1:
		plan, err = inspectSQLiteTable(ctx, db, engine, name)
	default:
		err = fmt.Errorf("unsupported database type %s", engine)
	}
	if err != nil {
		return TablePlan{}, err
	}
	if plan.KeyKind == KeyFullRow {
		plan.Risks = append(plan.Risks, RiskKeyless)
	}
	if plan.EstimatedRows >= largeTableRows {
		plan.Risks = append(plan.Risks, RiskLargeRows)
	}
	if plan.EstimatedBytes >= largeTableBytes {
		plan.Risks = append(plan.Risks, RiskLargeBytes)
	}
	if (engine == config.Turso || engine == config.CloudflareD1) && plan.EstimatedRows == 0 && plan.EstimatedBytes == 0 {
		plan.Risks = append(plan.Risks, RiskUnknownSize)
	}
	return plan, nil
}

func inspectPostgresTable(ctx context.Context, db Queryer, table string) (TablePlan, error) {
	schema, name := splitQualified(table, "public")
	plan := TablePlan{Name: table, KeyKind: KeyFullRow}
	rows, err := db.QueryContext(ctx, `SELECT column_name, data_type, is_nullable, COALESCE(column_default, '')
FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 ORDER BY ordinal_position`, schema, name)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var column Column
		var nullable string
		if err := rows.Scan(&column.Name, &column.Type, &nullable, &column.Default); err != nil {
			_ = rows.Close()
			return plan, err
		}
		column.Nullable = nullable == "YES"
		plan.Columns = append(plan.Columns, column)
	}
	if err := rows.Close(); err != nil {
		return plan, err
	}
	if err := rows.Err(); err != nil {
		return plan, err
	}
	constraints, err := postgresKeys(ctx, db, schema, name)
	if err != nil {
		return plan, err
	}
	chooseKey(&plan, constraints)
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(c.reltuples::bigint, 0), pg_total_relation_size(c.oid)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`, schema, name).
		Scan(&plan.EstimatedRows, &plan.EstimatedBytes)
	return plan, nil
}

func inspectMySQLTable(ctx context.Context, db Queryer, table string) (TablePlan, error) {
	plan := TablePlan{Name: table, KeyKind: KeyFullRow}
	rows, err := db.QueryContext(ctx, `SELECT column_name, column_type, is_nullable, COALESCE(column_default, '')
FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position`, table)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var column Column
		var nullable string
		if err := rows.Scan(&column.Name, &column.Type, &nullable, &column.Default); err != nil {
			_ = rows.Close()
			return plan, err
		}
		column.Nullable = nullable == "YES"
		plan.Columns = append(plan.Columns, column)
	}
	if err := rows.Close(); err != nil {
		return plan, err
	}
	if err := rows.Err(); err != nil {
		return plan, err
	}
	constraints, err := mysqlKeys(ctx, db, table)
	if err != nil {
		return plan, err
	}
	chooseKey(&plan, constraints)
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(table_rows, 0), COALESCE(data_length + index_length, 0)
FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?`, table).
		Scan(&plan.EstimatedRows, &plan.EstimatedBytes)
	return plan, nil
}

func inspectSQLiteTable(ctx context.Context, db Queryer, engine config.DBType, table string) (TablePlan, error) {
	plan := TablePlan{Name: table, KeyKind: KeyFullRow}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(config.SQLite, table)))
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var cid, notNull, pk int
		var column Column
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &column.Name, &column.Type, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return plan, err
		}
		column.Nullable = notNull == 0 && pk == 0
		column.Default = defaultValue.String
		column.PrimaryPos = pk
		plan.Columns = append(plan.Columns, column)
	}
	if err := rows.Close(); err != nil {
		return plan, err
	}
	if err := rows.Err(); err != nil {
		return plan, err
	}
	var primary []string
	for _, column := range plan.Columns {
		if column.PrimaryPos > 0 {
			primary = append(primary, column.Name)
		}
	}
	if len(primary) > 0 {
		sort.SliceStable(primary, func(i, j int) bool {
			return columnPrimaryPos(plan.Columns, primary[i]) < columnPrimaryPos(plan.Columns, primary[j])
		})
		plan.KeyKind, plan.KeyColumns = KeyPrimary, primary
	} else {
		unique, err := sqliteUniqueKey(ctx, db, table, plan.Columns)
		if err != nil {
			return plan, err
		}
		if len(unique) > 0 {
			plan.KeyKind, plan.KeyColumns = KeyUnique, unique
		} else {
			var createSQL sql.NullString
			if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&createSQL); err == nil &&
				!strings.Contains(strings.ToUpper(createSQL.String), "WITHOUT ROWID") {
				plan.KeyKind, plan.KeyColumns = KeyRowID, []string{"__dbterm_rowid"}
			}
		}
	}
	// ANALYZE statistics are optional. Use them when available to drive honest
	// progress without adding a costly COUNT(*) or walking database pages.
	if engine == config.SQLite {
		var stat string
		if err := db.QueryRowContext(ctx, `SELECT stat FROM sqlite_stat1 WHERE tbl = ? ORDER BY idx IS NULL DESC LIMIT 1`, table).Scan(&stat); err == nil {
			if fields := strings.Fields(stat); len(fields) > 0 {
				plan.EstimatedRows, _ = strconv.ParseInt(fields[0], 10, 64)
			}
		}
	}
	if plan.EstimatedRows == 0 {
		// Ordinary SQLite rowids are indexed, so MIN/MAX provides a fast range
		// estimate without an extra COUNT(*) scan. Gaps make this approximate,
		// which the progress UI labels honestly with '~'. WITHOUT ROWID tables
		// simply reject the query and retain table-level progress.
		_ = db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COALESCE(MAX(_rowid_) - MIN(_rowid_) + 1, 0) FROM %s`, quoteIdentifier(config.SQLite, table))).
			Scan(&plan.EstimatedRows)
	}
	return plan, nil
}

type keyCandidate struct {
	Kind    KeyKind
	Name    string
	Columns []string
}

func postgresKeys(ctx context.Context, db Queryer, schema, table string) ([]keyCandidate, error) {
	rows, err := db.QueryContext(ctx, `SELECT tc.constraint_type, tc.constraint_name, kcu.column_name, kcu.ordinal_position
FROM information_schema.table_constraints tc JOIN information_schema.key_column_usage kcu
ON tc.constraint_catalog = kcu.constraint_catalog AND tc.constraint_schema = kcu.constraint_schema AND tc.constraint_name = kcu.constraint_name
WHERE tc.table_schema = $1 AND tc.table_name = $2 AND tc.constraint_type IN ('PRIMARY KEY', 'UNIQUE')
ORDER BY CASE tc.constraint_type WHEN 'PRIMARY KEY' THEN 0 ELSE 1 END, tc.constraint_name, kcu.ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKeyCandidates(rows)
}

func mysqlKeys(ctx context.Context, db Queryer, table string) ([]keyCandidate, error) {
	rows, err := db.QueryContext(ctx, `SELECT tc.constraint_type, tc.constraint_name, kcu.column_name, kcu.ordinal_position
FROM information_schema.table_constraints tc JOIN information_schema.key_column_usage kcu
ON tc.constraint_schema = kcu.constraint_schema AND tc.table_name = kcu.table_name AND tc.constraint_name = kcu.constraint_name
WHERE tc.table_schema = DATABASE() AND tc.table_name = ? AND tc.constraint_type IN ('PRIMARY KEY', 'UNIQUE')
ORDER BY CASE tc.constraint_type WHEN 'PRIMARY KEY' THEN 0 ELSE 1 END, tc.constraint_name, kcu.ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanKeyCandidates(rows)
}

func scanKeyCandidates(rows *sql.Rows) ([]keyCandidate, error) {
	byName := map[string]*keyCandidate{}
	var order []string
	for rows.Next() {
		var constraintType, name, column string
		var ordinal int
		if err := rows.Scan(&constraintType, &name, &column, &ordinal); err != nil {
			return nil, err
		}
		candidate := byName[name]
		if candidate == nil {
			kind := KeyUnique
			if constraintType == "PRIMARY KEY" {
				kind = KeyPrimary
			}
			candidate = &keyCandidate{Kind: kind, Name: name}
			byName[name] = candidate
			order = append(order, name)
		}
		candidate.Columns = append(candidate.Columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]keyCandidate, 0, len(order))
	for _, name := range order {
		result = append(result, *byName[name])
	}
	return result, nil
}

func chooseKey(plan *TablePlan, candidates []keyCandidate) {
	for _, candidate := range candidates {
		if candidate.Kind == KeyUnique {
			valid := true
			for _, name := range candidate.Columns {
				if columnNullable(plan.Columns, name) {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
		}
		plan.KeyKind, plan.KeyColumns = candidate.Kind, candidate.Columns
		return
	}
}

func sqliteUniqueKey(ctx context.Context, db Queryer, table string, columns []Column) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list(%s)", quoteIdentifier(config.SQLite, table)))
	if err != nil {
		return nil, err
	}
	type indexRow struct{ name string }
	var indexes []indexRow
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return nil, err
		}
		// Partial unique indexes and expression indexes cannot identify every
		// row in a table, so neither is safe as an anchor identity.
		if unique != 0 && partial == 0 {
			indexes = append(indexes, indexRow{name: name})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, index := range indexes {
		info, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA index_info(%s)", quoteIdentifier(config.SQLite, index.name)))
		if err != nil {
			return nil, err
		}
		var names []string
		valid := true
		for info.Next() {
			var seqno, cid int
			var name sql.NullString
			if err := info.Scan(&seqno, &cid, &name); err != nil {
				_ = info.Close()
				return nil, err
			}
			if !name.Valid || cid < 0 || columnNullable(columns, name.String) {
				valid = false
			}
			if name.Valid {
				names = append(names, name.String)
			}
		}
		_ = info.Close()
		if valid && len(names) > 0 {
			return names, nil
		}
	}
	return nil, nil
}

func tableSchemaPayload(plan TablePlan) ([]byte, []byte, error) {
	payload, err := json.Marshal(struct {
		Columns    []Column `json:"columns"`
		KeyColumns []string `json:"key_columns"`
		KeyKind    KeyKind  `json:"key_kind"`
	}{plan.Columns, plan.KeyColumns, plan.KeyKind})
	if err != nil {
		return nil, nil, err
	}
	hash := sha256Bytes(payload)
	return payload, hash, nil
}

func sha256Bytes(payload []byte) []byte {
	hash := sha256.Sum256(payload)
	return hash[:]
}

func splitQualified(value, fallbackSchema string) (string, string) {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return fallbackSchema, value
}

func quoteIdentifier(engine config.DBType, identifier string) string {
	parts := strings.Split(identifier, ".")
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		if engine == config.MySQL {
			quoted = append(quoted, "`"+strings.ReplaceAll(part, "`", "``")+"`")
		} else {
			quoted = append(quoted, `"`+strings.ReplaceAll(part, `"`, `""`)+`"`)
		}
	}
	return strings.Join(quoted, ".")
}

func columnNullable(columns []Column, name string) bool {
	for _, column := range columns {
		if column.Name == name {
			return column.Nullable
		}
	}
	return true
}

func columnPrimaryPos(columns []Column, name string) int {
	for _, column := range columns {
		if column.Name == name {
			return column.PrimaryPos
		}
	}
	return 0
}
