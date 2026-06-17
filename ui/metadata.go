package ui

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

func (a *App) showSelectedTableMetadata() {
	if a.db == nil {
		a.ShowAlert(fmt.Sprintf("%s Connect to a database first.", iconInfo), "main")
		return
	}
	if strings.TrimSpace(a.selectedTable) == "" {
		a.ShowAlert(fmt.Sprintf("%s Select a table first, then inspect its schema.", iconInfo), "main")
		return
	}

	tableName := a.selectedTable
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	var canceled atomic.Bool
	a.showLoadingModal(fmt.Sprintf("%s Inspecting %s...", iconTables, tableName),
		withLoadingCancel("Press Esc to cancel schema inspection.", func() {
			canceled.Store(true)
			cancel()
		}))

	go func() {
		defer cancel()
		summary, err := a.buildSelectedTableMetadata(ctx, tableName)
		a.app.QueueUpdateDraw(func() {
			a.pages.RemovePage("loading")
			if canceled.Load() {
				a.ShowAlert(fmt.Sprintf("%s Schema inspection canceled for %s.", iconWarn, tableName), "main")
				return
			}
			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not inspect %s:\n\n%v", iconWarn, tableName, err), "main")
				return
			}
			a.showMetadataModal(tableName, summary)
		})
	}()
}

func (a *App) showMetadataModal(tableName, summary string) {
	view := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetText(summary)
	view.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Structure: %s [yellow](Esc/Enter to close)[-] ", iconDatabase, tableName)).
		SetBorderColor(surface1).
		SetTitleColor(mauve).
		SetBackgroundColor(mantle)

	view.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyEnter {
			a.pages.RemovePage("tableMetadata")
			a.app.SetFocus(a.tables)
			return nil
		}
		return event
	})

	frame := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(view, 0, 4, true).
			AddItem(nil, 0, 1, false),
			0, 4, true).
		AddItem(nil, 0, 1, false)

	a.pages.AddPage("tableMetadata", frame, true, true)
	a.app.SetFocus(view)
}

func (a *App) buildSelectedTableMetadata(ctx context.Context, tableName string) (string, error) {
	switch a.dbType {
	case config.PostgreSQL:
		return a.buildPostgresTableMetadata(ctx, tableName)
	case config.MySQL:
		return a.buildMySQLTableMetadata(ctx, tableName)
	case config.SQLite, config.Turso, config.CloudflareD1:
		return a.buildSQLiteTableMetadata(ctx, tableName)
	default:
		return "", fmt.Errorf("schema inspection is not supported for %s", a.dbType)
	}
}

func (a *App) buildPostgresTableMetadata(ctx context.Context, tableName string) (string, error) {
	schemaName, tableOnly := splitQualifiedIdentifier(tableName)
	schemaName = a.defaultObjectNamespace(schemaName)

	var out strings.Builder
	out.WriteString(fmt.Sprintf("[::b][#89b4fa]%s[-][-]\n\n", tableName))
	if cfg := a.currentConnectionConfig(); cfg != nil {
		out.WriteString(fmt.Sprintf("[#a6adc8]Database:[-] %s\n", nonEmptyOr(cfg.Database, a.dbName)))
	}
	out.WriteString(fmt.Sprintf("[#a6adc8]Schema:[-] %s\n\n", schemaName))

	appendSectionTitle(&out, "Columns")
	colRows, err := a.db.QueryContext(ctx, `SELECT column_name, data_type, is_nullable, COALESCE(column_default, '')
FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2
ORDER BY ordinal_position`, schemaName, tableOnly)
	if err != nil {
		return "", err
	}
	defer colRows.Close()

	for colRows.Next() {
		var name, dataType, nullable, defaultValue string
		if err := colRows.Scan(&name, &dataType, &nullable, &defaultValue); err != nil {
			return "", err
		}
		line := fmt.Sprintf("• %s  [%s]", name, dataType)
		if nullable == "NO" {
			line += " not null"
		}
		if strings.TrimSpace(defaultValue) != "" {
			line += " default=" + defaultValue
		}
		out.WriteString(line + "\n")
	}

	appendSectionTitle(&out, "Primary / Unique")
	keyRows, err := a.db.QueryContext(ctx, `SELECT tc.constraint_type, tc.constraint_name, kcu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
 AND tc.table_name = kcu.table_name
WHERE tc.table_schema = $1
  AND tc.table_name = $2
  AND tc.constraint_type IN ('PRIMARY KEY', 'UNIQUE')
ORDER BY tc.constraint_type, tc.constraint_name, kcu.ordinal_position`, schemaName, tableOnly)
	if err != nil {
		return "", err
	}
	defer keyRows.Close()
	if err := appendConstraintRows(&out, keyRows, "constraint"); err != nil {
		return "", err
	}

	appendSectionTitle(&out, "Foreign Keys")
	fkRows, err := a.db.QueryContext(ctx, `SELECT tc.constraint_name, kcu.column_name, ccu.table_schema, ccu.table_name, ccu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name
 AND tc.table_schema = kcu.table_schema
JOIN information_schema.constraint_column_usage ccu
  ON tc.constraint_name = ccu.constraint_name
 AND tc.table_schema = ccu.table_schema
WHERE tc.constraint_type = 'FOREIGN KEY'
  AND tc.table_schema = $1
  AND tc.table_name = $2
ORDER BY tc.constraint_name, kcu.ordinal_position`, schemaName, tableOnly)
	if err != nil {
		return "", err
	}
	defer fkRows.Close()
	for fkRows.Next() {
		var name, columnName, refSchema, refTable, refColumn string
		if err := fkRows.Scan(&name, &columnName, &refSchema, &refTable, &refColumn); err != nil {
			return "", err
		}
		out.WriteString(fmt.Sprintf("• %s: %s -> %s.%s(%s)\n", name, columnName, refSchema, refTable, refColumn))
	}

	appendSectionTitle(&out, "Indexes")
	indexRows, err := a.db.QueryContext(ctx, `SELECT indexname, indexdef
FROM pg_indexes
WHERE schemaname = $1 AND tablename = $2
ORDER BY indexname`, schemaName, tableOnly)
	if err != nil {
		return "", err
	}
	defer indexRows.Close()
	for indexRows.Next() {
		var name, definition string
		if err := indexRows.Scan(&name, &definition); err != nil {
			return "", err
		}
		out.WriteString(fmt.Sprintf("• %s\n  %s\n", name, definition))
	}

	return strings.TrimSpace(out.String()), nil
}

func (a *App) buildMySQLTableMetadata(ctx context.Context, tableName string) (string, error) {
	schemaName, tableOnly := splitQualifiedIdentifier(tableName)
	schemaName = a.defaultObjectNamespace(schemaName)

	var out strings.Builder
	out.WriteString(fmt.Sprintf("[::b][#f9e2af]%s[-][-]\n\n", tableName))
	out.WriteString(fmt.Sprintf("[#a6adc8]Database:[-] %s\n\n", schemaName))

	appendSectionTitle(&out, "Columns")
	colRows, err := a.db.QueryContext(ctx, `SELECT column_name, column_type, is_nullable, COALESCE(column_default, ''), column_key, extra
FROM information_schema.columns
WHERE table_schema = ? AND table_name = ?
ORDER BY ordinal_position`, schemaName, tableOnly)
	if err != nil {
		return "", err
	}
	defer colRows.Close()
	for colRows.Next() {
		var name, columnType, nullable, defaultValue, columnKey, extra string
		if err := colRows.Scan(&name, &columnType, &nullable, &defaultValue, &columnKey, &extra); err != nil {
			return "", err
		}
		line := fmt.Sprintf("• %s  [%s]", name, columnType)
		if nullable == "NO" {
			line += " not null"
		}
		if columnKey != "" {
			line += " key=" + columnKey
		}
		if strings.TrimSpace(extra) != "" {
			line += " " + extra
		}
		if strings.TrimSpace(defaultValue) != "" {
			line += " default=" + defaultValue
		}
		out.WriteString(line + "\n")
	}

	appendSectionTitle(&out, "Foreign Keys")
	fkRows, err := a.db.QueryContext(ctx, `SELECT constraint_name, column_name, referenced_table_schema, referenced_table_name, referenced_column_name
FROM information_schema.key_column_usage
WHERE table_schema = ?
  AND table_name = ?
  AND referenced_table_name IS NOT NULL
ORDER BY constraint_name, ordinal_position`, schemaName, tableOnly)
	if err != nil {
		return "", err
	}
	defer fkRows.Close()
	for fkRows.Next() {
		var name, columnName, refSchema, refTable, refColumn string
		if err := fkRows.Scan(&name, &columnName, &refSchema, &refTable, &refColumn); err != nil {
			return "", err
		}
		out.WriteString(fmt.Sprintf("• %s: %s -> %s.%s(%s)\n", name, columnName, refSchema, refTable, refColumn))
	}

	appendSectionTitle(&out, "Indexes")
	indexRows, err := a.db.QueryContext(ctx, `SELECT index_name, non_unique, seq_in_index, column_name
FROM information_schema.statistics
WHERE table_schema = ? AND table_name = ?
ORDER BY index_name, seq_in_index`, schemaName, tableOnly)
	if err != nil {
		return "", err
	}
	defer indexRows.Close()
	for indexRows.Next() {
		var indexName, columnName string
		var nonUnique, seqInIndex int
		if err := indexRows.Scan(&indexName, &nonUnique, &seqInIndex, &columnName); err != nil {
			return "", err
		}
		kind := "unique"
		if nonUnique == 1 {
			kind = "index"
		}
		out.WriteString(fmt.Sprintf("• %s [%s] #%d %s\n", indexName, kind, seqInIndex, columnName))
	}

	return strings.TrimSpace(out.String()), nil
}

func (a *App) buildSQLiteTableMetadata(ctx context.Context, tableName string) (string, error) {
	_, tableOnly := splitQualifiedIdentifier(tableName)
	target := quoteIdentifier(a.dbType, tableOnly)

	var out strings.Builder
	out.WriteString(fmt.Sprintf("[::b][#a6e3a1]%s[-][-]\n\n", tableName))
	if cfg := a.currentConnectionConfig(); cfg != nil {
		switch a.dbType {
		case config.SQLite:
			out.WriteString(fmt.Sprintf("[#a6adc8]File:[-] %s\n\n", nonEmptyOr(cfg.FilePath, cfg.Name)))
		case config.Turso:
			out.WriteString(fmt.Sprintf("[#a6adc8]Host:[-] %s\n\n", nonEmptyOr(cfg.Host, cfg.Name)))
		case config.CloudflareD1:
			out.WriteString(fmt.Sprintf("[#a6adc8]Database ID:[-] %s\n\n", nonEmptyOr(cfg.DatabaseID, cfg.Name)))
		}
	}

	appendSectionTitle(&out, "Columns")
	colRows, err := a.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, target))
	if err != nil {
		return "", err
	}
	defer colRows.Close()
	for colRows.Next() {
		var cid, notNull, pk int
		var name, dataType string
		var defaultValue sql.NullString
		if err := colRows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return "", err
		}
		line := fmt.Sprintf("• %s  [%s]", name, dataType)
		if notNull == 1 {
			line += " not null"
		}
		if pk > 0 {
			line += fmt.Sprintf(" pk#%d", pk)
		}
		if defaultValue.Valid && strings.TrimSpace(defaultValue.String) != "" {
			line += " default=" + defaultValue.String
		}
		out.WriteString(line + "\n")
	}

	appendSectionTitle(&out, "Foreign Keys")
	fkRows, err := a.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA foreign_key_list(%s)`, target))
	if err != nil {
		return "", err
	}
	defer fkRows.Close()
	for fkRows.Next() {
		var id, seq int
		var refTable, fromCol, toCol, onUpdate, onDelete, match string
		if err := fkRows.Scan(&id, &seq, &refTable, &fromCol, &toCol, &onUpdate, &onDelete, &match); err != nil {
			return "", err
		}
		out.WriteString(fmt.Sprintf("• fk#%d.%d: %s -> %s(%s) on_update=%s on_delete=%s\n", id, seq, fromCol, refTable, toCol, onUpdate, onDelete))
	}

	appendSectionTitle(&out, "Indexes")
	indexRows, err := a.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA index_list(%s)`, target))
	if err != nil {
		return "", err
	}
	defer indexRows.Close()
	for indexRows.Next() {
		var seq int
		var name, origin string
		var unique, partial int
		if err := indexRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return "", err
		}
		kind := "index"
		if unique == 1 {
			kind = "unique"
		}
		out.WriteString(fmt.Sprintf("• %s [%s origin=%s partial=%t]\n", name, kind, origin, partial == 1))
	}

	return strings.TrimSpace(out.String()), nil
}

func appendSectionTitle(out *strings.Builder, title string) {
	if out == nil {
		return
	}
	out.WriteString(fmt.Sprintf("[#a6adc8]%s[-]\n", title))
}

func appendConstraintRows(out *strings.Builder, rows *sql.Rows, emptyLabel string) error {
	if out == nil || rows == nil {
		return nil
	}

	var wrote bool
	for rows.Next() {
		var constraintType, constraintName, columnName string
		if err := rows.Scan(&constraintType, &constraintName, &columnName); err != nil {
			return err
		}
		wrote = true
		out.WriteString(fmt.Sprintf("• %s [%s] %s\n", constraintName, strings.ToLower(constraintType), columnName))
	}
	if !wrote {
		out.WriteString(fmt.Sprintf("• no %s metadata\n", emptyLabel))
	}
	return nil
}
