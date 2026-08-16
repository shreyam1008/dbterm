package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

type queryContext interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *service) followRecord(ctx context.Context, input followRecordInput) (followRecordOutput, error) {
	auditID := s.auditID()
	started := time.Now()
	if len(input.Key) == 0 {
		return followRecordOutput{}, fmt.Errorf("at least one key column is required [%s]", auditID)
	}
	if _, _, err := splitTableName(input.Table); err != nil {
		return followRecordOutput{}, fmt.Errorf("invalid source table [%s]: %w", auditID, err)
	}
	cfg, err := s.resolveConnection(input.ConnectionID)
	if err != nil {
		return followRecordOutput{}, fmt.Errorf("resolve connection [%s]: %w", auditID, err)
	}
	db, queryCtx, closeFn, err := s.connect(ctx, cfg)
	if err != nil {
		return followRecordOutput{}, fmt.Errorf("follow record failed [%s]: %w", auditID, err)
	}
	defer closeFn()
	q, finish, err := readOnlyQueryer(queryCtx, db, cfg)
	if err != nil {
		return followRecordOutput{}, fmt.Errorf("follow record failed [%s]: %w", auditID, err)
	}
	defer finish()

	sourceResult, err := s.selectExact(queryCtx, q, cfg.Type, input.Table, input.Key, 1)
	if err != nil {
		return followRecordOutput{}, fmt.Errorf("load source record [%s]: %s", auditID, redactError(err, cfg))
	}
	if len(sourceResult.Rows) == 0 {
		return followRecordOutput{}, fmt.Errorf("source record not found [%s]", auditID)
	}
	if sourceResult.Truncated {
		return followRecordOutput{}, fmt.Errorf("key matched more than one source record; provide a unique key [%s]", auditID)
	}
	source := sourceResult.Rows[0]
	perLink := input.RowsPerLink
	if perLink <= 0 {
		perLink = 5
	}
	if perLink > 20 {
		perLink = 20
	}
	result := followRecordOutput{SourceTable: input.Table, Source: source, Related: []relatedRows{}, AuditID: auditID}
	usedBytes := encodedSize(source) + len(input.Table) + len(auditID)

	outgoing, err := loadForeignKeysWith(queryCtx, q, cfg, input.Table)
	if err != nil {
		return followRecordOutput{}, fmt.Errorf("load outgoing relationships [%s]: %s", auditID, redactError(err, cfg))
	}
	for _, fk := range outgoing {
		predicates, ok := mappedValues(source, fk.Columns, true)
		if !ok {
			continue
		}
		rows, err := s.selectExact(queryCtx, q, cfg.Type, fk.TargetTable, predicates, perLink)
		if err != nil {
			return followRecordOutput{}, fmt.Errorf("follow outgoing relationship %s [%s]: %s", fk.Name, auditID, redactError(err, cfg))
		}
		relation, fits := boundedRelationship(relatedRows{Direction: "outgoing", Constraint: fk.Name, Table: fk.TargetTable, Rows: rows.Rows, Truncated: rows.Truncated}, &usedBytes, s.limits.maxOutputBytes)
		if !fits {
			result.Truncated = true
			break
		}
		result.Related = append(result.Related, relation)
		result.Truncated = result.Truncated || relation.Truncated
	}

	tables, tablesTruncated, err := listTablesWith(queryCtx, q, cfg.Type, s.limits.maxTables)
	if err != nil {
		return followRecordOutput{}, fmt.Errorf("discover incoming relationships [%s]: %s", auditID, redactError(err, cfg))
	}
	result.Truncated = result.Truncated || tablesTruncated
	stopIncoming := false
	for _, table := range tables {
		fks, err := loadForeignKeysWith(queryCtx, q, cfg, table)
		if err != nil {
			return followRecordOutput{}, fmt.Errorf("inspect relationship source %s [%s]: %s", table, auditID, redactError(err, cfg))
		}
		for _, fk := range fks {
			if !sameTable(cfg, fk.TargetTable, input.Table) {
				continue
			}
			predicates, ok := mappedValues(source, fk.Columns, false)
			if !ok {
				continue
			}
			rows, err := s.selectExact(queryCtx, q, cfg.Type, table, predicates, perLink)
			if err != nil {
				return followRecordOutput{}, fmt.Errorf("follow incoming relationship %s [%s]: %s", fk.Name, auditID, redactError(err, cfg))
			}
			relation, fits := boundedRelationship(relatedRows{Direction: "incoming", Constraint: fk.Name, Table: table, Rows: rows.Rows, Truncated: rows.Truncated}, &usedBytes, s.limits.maxOutputBytes)
			if !fits {
				result.Truncated = true
				stopIncoming = true
				break
			}
			result.Related = append(result.Related, relation)
			result.Truncated = result.Truncated || relation.Truncated
			if len(result.Related) >= 50 {
				result.Truncated = true
				stopIncoming = true
				break
			}
		}
		if stopIncoming {
			break
		}
	}
	s.logAudit(auditID, "follow_record", cfg.ID, "ok", fmt.Sprintf("relationships=%d", len(result.Related)), started)
	return result, nil
}

func boundedRelationship(relation relatedRows, used *int, maximum int) (relatedRows, bool) {
	base := len(relation.Direction) + len(relation.Constraint) + len(relation.Table) + 64
	if *used+base > maximum {
		return relatedRows{}, false
	}
	*used += base
	kept := relation.Rows[:0]
	for _, row := range relation.Rows {
		size := encodedSize(row)
		if *used+size > maximum {
			relation.Truncated = true
			break
		}
		*used += size
		kept = append(kept, row)
	}
	relation.Rows = kept
	return relation, true
}

func encodedSize(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func readOnlyQueryer(ctx context.Context, db *sql.DB, cfg config.ConnectionConfig) (queryContext, func(), error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err == nil {
		return tx, func() { _ = tx.Rollback() }, nil
	}
	if !cfg.ReadOnly {
		return nil, nil, fmt.Errorf("driver could not enforce a read-only transaction; mark profile %q read-only or use SELECT-only credentials", cfg.ID)
	}
	return db, func() {}, nil
}

func (s *service) selectExact(ctx context.Context, q queryContext, dbType config.DBType, table string, key map[string]any, maxRows int) (queryOutput, error) {
	quotedTable, err := quoteTable(dbType, table)
	if err != nil {
		return queryOutput{}, err
	}
	where, args, err := exactPredicates(dbType, key)
	if err != nil {
		return queryOutput{}, err
	}
	query := fmt.Sprintf("SELECT * FROM %s WHERE %s", quotedTable, where)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return queryOutput{}, err
	}
	return collectRows(rows, maxRows, s.limits)
}

func exactPredicates(dbType config.DBType, values map[string]any) (string, []any, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		if !safeIdentifier.MatchString(key) {
			return "", nil, fmt.Errorf("unsafe or unsupported column identifier %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		quoted := quoteIdentifier(dbType, key)
		if values[key] == nil {
			parts = append(parts, quoted+" IS NULL")
			continue
		}
		args = append(args, values[key])
		placeholder := "?"
		if dbType == config.PostgreSQL {
			placeholder = fmt.Sprintf("$%d", len(args))
		}
		parts = append(parts, quoted+" = "+placeholder)
	}
	return strings.Join(parts, " AND "), args, nil
}

func mappedValues(row map[string]any, columns []foreignKeyColumn, outgoing bool) (map[string]any, bool) {
	result := make(map[string]any, len(columns))
	for _, column := range columns {
		lookup, predicate := column.Source, column.Target
		if !outgoing {
			lookup, predicate = column.Target, column.Source
		}
		value, ok := row[lookup]
		if !ok || value == nil {
			return nil, false
		}
		result[predicate] = value
	}
	return result, true
}

func sameTable(cfg config.ConnectionConfig, a, b string) bool {
	as, at, aerr := splitTableName(a)
	bs, bt, berr := splitTableName(b)
	if aerr != nil || berr != nil || !strings.EqualFold(at, bt) {
		return false
	}

	defaultSchema := ""
	switch cfg.Type {
	case config.PostgreSQL:
		defaultSchema = "public"
	case config.MySQL:
		defaultSchema = cfg.Database
	case config.SQLite, config.Turso:
		defaultSchema = "main"
	}
	if as == "" {
		as = defaultSchema
	}
	if bs == "" {
		bs = defaultSchema
	}
	return strings.EqualFold(as, bs)
}

func listTablesWith(ctx context.Context, q queryContext, dbType config.DBType, max int) ([]string, bool, error) {
	query := database.ListTablesQuery(dbType)
	if query == "" {
		return nil, false, fmt.Errorf("unsupported database type %q", dbType)
	}
	rows, err := q.QueryContext(ctx, query)
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
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, false, err
		}
		result = append(result, name)
	}
	return result, truncated, rows.Err()
}
