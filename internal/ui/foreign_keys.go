package ui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

type foreignKeyColumnReference struct {
	localColumn  string
	targetColumn string
	ordinal      int
}

// foreignKeyReference keeps a whole constraint together. This matters for
// composite keys: following only one component can silently land on a row in
// the wrong tenant/partition.
type foreignKeyReference struct {
	name        string
	sourceTable string
	targetTable string
	columns     []foreignKeyColumnReference
}

type foreignKeyRowValue struct {
	value  any
	isNull bool
}

var errForeignKeyValueIsNull = errors.New("foreign key contains NULL and does not reference a row")

type resultNavigationState struct {
	table         string
	filter        *resultValueFilter
	pageOffset    int
	pageSize      int
	totalRowCount int
	sortColumn    int
	sortAsc       bool
	sortMode      string
	selectedRow   int
	selectedCol   int
}

func loadForeignKeyReferences(ctx context.Context, db *sql.DB, dbType config.DBType, tableName, defaultNamespace string) ([]foreignKeyReference, error) {
	if db == nil {
		return nil, fmt.Errorf("not connected")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	namespace, tableOnly := splitQualifiedIdentifier(tableName)
	if namespace == "" {
		namespace = strings.TrimSpace(defaultNamespace)
	}

	switch dbType {
	case config.PostgreSQL:
		return loadPostgresForeignKeyReferences(ctx, db, namespace, tableOnly)
	case config.MySQL:
		return loadMySQLForeignKeyReferences(ctx, db, namespace, tableOnly)
	case config.SQLite, config.Turso, config.CloudflareD1:
		return loadSQLiteForeignKeyReferences(ctx, db, dbType, tableOnly)
	default:
		return nil, fmt.Errorf("foreign-key navigation is not supported for %s", dbType)
	}
}

func loadPostgresForeignKeyReferences(ctx context.Context, db *sql.DB, schemaName, tableName string) ([]foreignKeyReference, error) {
	rows, err := db.QueryContext(ctx, `SELECT constraint_row.conname,
       source_attribute.attname,
       target_namespace.nspname,
       target_table.relname,
       target_attribute.attname,
       key_position.ordinal
FROM pg_catalog.pg_constraint AS constraint_row
JOIN pg_catalog.pg_class AS source_table
  ON source_table.oid = constraint_row.conrelid
JOIN pg_catalog.pg_namespace AS source_namespace
  ON source_namespace.oid = source_table.relnamespace
JOIN pg_catalog.pg_class AS target_table
  ON target_table.oid = constraint_row.confrelid
JOIN pg_catalog.pg_namespace AS target_namespace
  ON target_namespace.oid = target_table.relnamespace
CROSS JOIN LATERAL generate_subscripts(constraint_row.conkey, 1) AS key_position(ordinal)
JOIN pg_catalog.pg_attribute AS source_attribute
  ON source_attribute.attrelid = constraint_row.conrelid
 AND source_attribute.attnum = constraint_row.conkey[key_position.ordinal]
JOIN pg_catalog.pg_attribute AS target_attribute
  ON target_attribute.attrelid = constraint_row.confrelid
 AND target_attribute.attnum = constraint_row.confkey[key_position.ordinal]
WHERE constraint_row.contype = 'f'
  AND source_namespace.nspname = $1
  AND source_table.relname = $2
ORDER BY constraint_row.conname, key_position.ordinal`, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refs := make([]foreignKeyReference, 0)
	for rows.Next() {
		var name, localColumn, targetSchema, targetTable, targetColumn string
		var ordinal int
		if err := rows.Scan(&name, &localColumn, &targetSchema, &targetTable, &targetColumn, &ordinal); err != nil {
			return nil, err
		}
		refs = appendForeignKeyComponent(refs, name, qualifiedIdentifier(targetSchema, targetTable), foreignKeyColumnReference{
			localColumn: localColumn, targetColumn: targetColumn, ordinal: ordinal,
		})
	}
	return sortedForeignKeyReferences(refs), rows.Err()
}

func loadMySQLForeignKeyReferences(ctx context.Context, db *sql.DB, schemaName, tableName string) ([]foreignKeyReference, error) {
	rows, err := db.QueryContext(ctx, `SELECT constraint_name,
       column_name,
       referenced_table_schema,
       referenced_table_name,
       referenced_column_name,
       ordinal_position
FROM information_schema.key_column_usage
WHERE table_schema = ?
  AND table_name = ?
  AND referenced_table_name IS NOT NULL
ORDER BY constraint_name, ordinal_position`, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refs := make([]foreignKeyReference, 0)
	for rows.Next() {
		var name, localColumn, targetSchema, targetTable, targetColumn string
		var ordinal int
		if err := rows.Scan(&name, &localColumn, &targetSchema, &targetTable, &targetColumn, &ordinal); err != nil {
			return nil, err
		}
		// The MySQL sidebar is scoped to DATABASE() and shows unqualified names.
		// Keep same-schema hops consistent with it, while retaining a schema for
		// the uncommon cross-schema constraint.
		targetIdentifier := targetTable
		if !strings.EqualFold(targetSchema, schemaName) {
			targetIdentifier = qualifiedIdentifier(targetSchema, targetTable)
		}
		refs = appendForeignKeyComponent(refs, name, targetIdentifier, foreignKeyColumnReference{
			localColumn: localColumn, targetColumn: targetColumn, ordinal: ordinal,
		})
	}
	return sortedForeignKeyReferences(refs), rows.Err()
}

func loadSQLiteForeignKeyReferences(ctx context.Context, db *sql.DB, dbType config.DBType, tableName string) ([]foreignKeyReference, error) {
	if strings.TrimSpace(tableName) == "" {
		return nil, fmt.Errorf("table name is required")
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%s)", quoteIdentifier(dbType, tableName)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refs := make([]foreignKeyReference, 0)
	for rows.Next() {
		var id, seq int
		var targetTable, localColumn, targetColumn, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &targetTable, &localColumn, &targetColumn, &onUpdate, &onDelete, &match); err != nil {
			return nil, err
		}
		refs = appendForeignKeyComponent(refs, fmt.Sprintf("fk#%d", id), targetTable, foreignKeyColumnReference{
			localColumn: localColumn, targetColumn: targetColumn, ordinal: seq,
		})
	}
	return sortedForeignKeyReferences(refs), rows.Err()
}

func appendForeignKeyComponent(refs []foreignKeyReference, name, targetTable string, component foreignKeyColumnReference) []foreignKeyReference {
	for index := range refs {
		if refs[index].name == name && refs[index].targetTable == targetTable {
			refs[index].columns = append(refs[index].columns, component)
			return refs
		}
	}
	return append(refs, foreignKeyReference{name: name, targetTable: targetTable, columns: []foreignKeyColumnReference{component}})
}

func sortedForeignKeyReferences(refs []foreignKeyReference) []foreignKeyReference {
	for index := range refs {
		sort.SliceStable(refs[index].columns, func(i, j int) bool {
			return refs[index].columns[i].ordinal < refs[index].columns[j].ordinal
		})
	}
	return refs
}

func foreignKeysForColumn(refs []foreignKeyReference, column string) []foreignKeyReference {
	column = strings.TrimSpace(column)
	exactMatches := make([]foreignKeyReference, 0, 1)
	for _, ref := range refs {
		for _, component := range ref.columns {
			if strings.TrimSpace(component.localColumn) == column {
				exactMatches = append(exactMatches, ref)
				break
			}
		}
	}
	if len(exactMatches) > 0 {
		return exactMatches
	}

	// MySQL/SQLite metadata can normalize identifier case. Permit a folded
	// fallback only when every match refers to one unambiguous source spelling;
	// PostgreSQL may legally contain both quoted "Foo" and unquoted foo.
	foldedMatches := make([]foreignKeyReference, 0, 1)
	spellings := make(map[string]struct{})
	for _, ref := range refs {
		for _, component := range ref.columns {
			localColumn := strings.TrimSpace(component.localColumn)
			if strings.EqualFold(localColumn, column) {
				spellings[localColumn] = struct{}{}
				foldedMatches = append(foldedMatches, ref)
				break
			}
		}
	}
	if len(spellings) != 1 {
		return nil
	}
	return foldedMatches
}

func (a *App) captureForeignKeyRowValues(row int) map[string]foreignKeyRowValue {
	values := make(map[string]foreignKeyRowValue)
	if a == nil || a.results == nil || row <= 0 || row >= a.results.GetRowCount() {
		return values
	}
	for column := 0; column < a.results.GetColumnCount(); column++ {
		name := strings.TrimSpace(a.resultColumnName(column))
		if name == "" {
			continue
		}
		value, isNull := resultReferenceQueryValue(a.results.GetCell(row, column))
		values[name] = foreignKeyRowValue{value: value, isNull: isNull}
	}
	return values
}

func foreignKeyRowValueForColumn(values map[string]foreignKeyRowValue, column string) (foreignKeyRowValue, bool) {
	column = strings.TrimSpace(column)
	if value, ok := values[column]; ok {
		return value, true
	}
	var match foreignKeyRowValue
	found := false
	for name, value := range values {
		if !strings.EqualFold(strings.TrimSpace(name), column) {
			continue
		}
		if found {
			return foreignKeyRowValue{}, false
		}
		match = value
		found = true
	}
	return match, found
}

func resultReferenceQueryValue(cell *tview.TableCell) (any, bool) {
	if cell == nil {
		return "", false
	}
	if reference, ok := cell.GetReference().(resultCellReference); ok {
		if reference.isNull {
			return nil, true
		}
		if reference.rawValue != nil {
			return cloneResultRawValue(reference.rawValue), false
		}
		return reference.value, false
	}
	return cell.Text, false
}

func foreignKeyTargetPredicates(ref foreignKeyReference, rowValues map[string]foreignKeyRowValue) ([]resultFilterPredicate, error) {
	if strings.TrimSpace(ref.targetTable) == "" || len(ref.columns) == 0 {
		return nil, fmt.Errorf("foreign key target is incomplete")
	}
	predicates := make([]resultFilterPredicate, 0, len(ref.columns))
	for _, component := range ref.columns {
		localColumn := strings.TrimSpace(component.localColumn)
		targetColumn := strings.TrimSpace(component.targetColumn)
		if localColumn == "" || targetColumn == "" {
			return nil, fmt.Errorf("foreign key column mapping is incomplete")
		}
		rowValue, ok := foreignKeyRowValueForColumn(rowValues, component.localColumn)
		if !ok {
			return nil, fmt.Errorf("source column %s is not available in this result row", component.localColumn)
		}
		if rowValue.isNull {
			return nil, fmt.Errorf("%w: %s", errForeignKeyValueIsNull, component.localColumn)
		}
		predicates = append(predicates, resultFilterPredicate{
			column: targetColumn, operator: resultFilterEqual, value: cloneResultRawValue(rowValue.value),
		})
	}
	return predicates, nil
}

func (a *App) navigateBackFromRelationship() bool {
	if a == nil || len(a.resultNavStack) == 0 || !a.isTableResultActive() {
		return false
	}

	stackIndex := len(a.resultNavStack) - 1
	destination := a.resultNavStack[stackIndex]
	current := a.captureResultNavigationState()
	a.restoreResultNavigationState(destination)
	a.selectTableListIdentifier(destination.table)
	a.setFocusWithColor(a.results)

	a.loadCurrentTableAsync(tableLoadOptions{
		loadingText:  fmt.Sprintf("Returning to %s...", destination.table),
		cancelText:   "Press Esc to cancel returning.",
		canceledText: "Return navigation canceled",
		errorText:    fmt.Sprintf("Could not return to %s", destination.table),
		successText:  fmt.Sprintf("Returned to %s", destination.table),
		rollback: func() {
			a.restoreResultNavigationState(current)
			a.selectTableListIdentifier(current.table)
		},
		onSuccess: func() {
			a.resultNavStack = a.resultNavStack[:stackIndex]
			a.restoreResultNavigationSelection(destination)
		},
	})
	return true
}

func (a *App) captureResultNavigationState() resultNavigationState {
	state := resultNavigationState{
		table:         a.selectedTable,
		filter:        cloneResultValueFilter(a.resultFilter),
		pageOffset:    a.pageOffset,
		pageSize:      a.pageSize,
		totalRowCount: a.totalRowCount,
		sortColumn:    a.sortColumn,
		sortAsc:       a.sortAsc,
		sortMode:      a.sortMode,
	}
	if a.results != nil {
		state.selectedRow, state.selectedCol = a.results.GetSelection()
	}
	return state
}

func (a *App) restoreResultNavigationState(state resultNavigationState) {
	a.selectedTable = state.table
	a.setCurrentResultFilter(state.filter)
	a.pageOffset = state.pageOffset
	a.pageSize = state.pageSize
	a.totalRowCount = state.totalRowCount
	a.sortColumn = state.sortColumn
	a.sortAsc = state.sortAsc
	a.sortMode = state.sortMode
}

func (a *App) restoreResultNavigationSelection(state resultNavigationState) {
	if a.results == nil || a.currentResultRowCount() == 0 {
		return
	}
	row := clamp(state.selectedRow, 1, a.currentResultRowCount())
	col := clamp(state.selectedCol, 0, max(0, a.results.GetColumnCount()-1))
	a.results.Select(row, col)
}

func (a *App) selectTableListIdentifier(identifier string) {
	if a == nil || a.tables == nil {
		return
	}
	for index, tableName := range a.tableIdentifiers {
		if tableName == identifier {
			a.tables.SetCurrentItem(index)
			return
		}
	}
}

func (a *App) clearResultNavigation() {
	if a != nil {
		a.resultNavStack = nil
	}
}
