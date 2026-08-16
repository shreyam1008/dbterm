package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func (s *service) queryReadOnly(ctx context.Context, input readQueryInput) (queryOutput, error) {
	auditID := s.auditID()
	started := time.Now()
	if err := validateReadOnlySQL(input.SQL, s.limits.maxQueryBytes); err != nil {
		s.logAudit(auditID, "query_read_only", input.ConnectionID, "denied", err.Error(), started)
		return queryOutput{}, fmt.Errorf("read-only policy denied query [%s]: %w", auditID, err)
	}
	cfg, err := s.resolveConnection(input.ConnectionID)
	if err != nil {
		s.logAudit(auditID, "query_read_only", input.ConnectionID, "denied", err.Error(), started)
		return queryOutput{}, fmt.Errorf("resolve connection [%s]: %w", auditID, err)
	}
	db, queryCtx, closeFn, err := s.connect(ctx, cfg)
	if err != nil {
		s.logAudit(auditID, "query_read_only", cfg.ID, "error", err.Error(), started)
		return queryOutput{}, fmt.Errorf("query failed [%s]: %w", auditID, err)
	}
	defer closeFn()
	rowLimit := input.MaxRows
	if rowLimit <= 0 || rowLimit > s.limits.maxRows {
		rowLimit = s.limits.maxRows
	}
	output, err := s.queryWithReadOnlyBoundary(queryCtx, db, cfg, input.SQL, rowLimit)
	if err != nil {
		s.logAudit(auditID, "query_read_only", cfg.ID, "error", "database query failed", started)
		return queryOutput{}, fmt.Errorf("read-only query failed [%s]: %s", auditID, redactError(err, cfg))
	}
	output.AuditID = auditID
	s.logAudit(auditID, "query_read_only", cfg.ID, "ok", fmt.Sprintf("rows=%d truncated=%t", output.RowCount, output.Truncated), started)
	return output, nil
}

func (s *service) explainQuery(ctx context.Context, input explainQueryInput) (explainQueryOutput, error) {
	if err := validateExplainableSQL(input.SQL, s.limits.maxQueryBytes); err != nil {
		return explainQueryOutput{Valid: false, Message: err.Error()}, nil
	}
	result, err := s.queryReadOnly(ctx, readQueryInput{
		ConnectionID: input.ConnectionID,
		SQL:          "EXPLAIN " + strings.TrimSpace(strings.TrimSuffix(input.SQL, ";")),
		MaxRows:      s.limits.maxRows,
	})
	if err != nil {
		return explainQueryOutput{}, err
	}
	return explainQueryOutput{Valid: true, Message: "Query passed the dbterm read-only policy; plan generated without ANALYZE or execution.", Plan: result}, nil
}

func (s *service) queryWithReadOnlyBoundary(ctx context.Context, db *sql.DB, cfg config.ConnectionConfig, query string, maxRows int) (queryOutput, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err == nil {
		defer tx.Rollback()
		rows, queryErr := tx.QueryContext(ctx, query)
		if queryErr != nil {
			return queryOutput{}, queryErr
		}
		return collectRows(rows, maxRows, s.limits)
	}
	// Some remote SQLite-compatible drivers do not implement transactions.
	// Direct execution is allowed only when the user explicitly marked that
	// saved profile read-only; syntax validation still applies above.
	if !cfg.ReadOnly {
		return queryOutput{}, fmt.Errorf("driver could not enforce a read-only transaction; mark profile %q read-only or use a database role with SELECT-only grants", cfg.ID)
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return queryOutput{}, err
	}
	return collectRows(rows, maxRows, s.limits)
}

func collectRows(rows *sql.Rows, maxRows int, lim limits) (queryOutput, error) {
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return queryOutput{}, err
	}
	if len(columns) > lim.maxColumns {
		return queryOutput{}, fmt.Errorf("query returned %d columns; limit is %d", len(columns), lim.maxColumns)
	}
	result := queryOutput{Columns: columns, Rows: make([]map[string]any, 0, min(maxRows, 16))}
	usedBytes := 0
	if encoded, marshalErr := json.Marshal(columns); marshalErr == nil {
		usedBytes = len(encoded)
	}
	for rows.Next() {
		if len(result.Rows) >= maxRows {
			result.Truncated = true
			break
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return queryOutput{}, err
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			row[name] = safeCellValue(values[i], lim.maxCellBytes)
		}
		encoded, marshalErr := json.Marshal(row)
		if marshalErr != nil {
			return queryOutput{}, fmt.Errorf("encode query row: %w", marshalErr)
		}
		if usedBytes+len(encoded) > lim.maxOutputBytes {
			result.Truncated = true
			break
		}
		usedBytes += len(encoded)
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return queryOutput{}, err
	}
	result.RowCount = len(result.Rows)
	return result, nil
}

func safeCellValue(value any, maxBytes int) any {
	switch value := value.(type) {
	case []byte:
		if len(value) > maxBytes {
			return string(value[:maxBytes]) + "…[truncated]"
		}
		return string(value)
	case string:
		if len(value) > maxBytes {
			return value[:maxBytes] + "…[truncated]"
		}
		return value
	case time.Time:
		return value.Format(time.RFC3339Nano)
	default:
		return value
	}
}
