package ui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

const (
	pageRelatedData      = "relatedData"
	pageSameValueMatches = "sameValueMatches"
)

type relationshipDirection uint8

const (
	relationshipOutgoing relationshipDirection = iota
	relationshipIncoming
)

// rowRelationship keeps the database constraint in its declared orientation:
// sourceTable.localColumn -> targetTable.targetColumn. direction only describes
// which side is the row currently visible in Results.
type rowRelationship struct {
	direction   relationshipDirection
	sourceTable string
	targetTable string
	reference   foreignKeyReference
}

type sameValueMatch struct {
	table  string
	column string
}

// exploreSelectedRelationships is the single entry point for cross-table row
// navigation. It shows both directions so users can move from a parent row to
// its children and repeat the operation across an arbitrary relationship chain.
func (a *App) exploreSelectedRelationships() {
	row, _, column, _, ok := a.currentResultCell()
	if !ok || !a.isTableResultActive() {
		a.flashStatus("[yellow]Select a table data cell to explore related rows[-]", a.currentResultRowCount(), 1800*time.Millisecond)
		return
	}

	rowValues := a.captureForeignKeyRowValues(row)
	selectedValue, hasSelectedValue := foreignKeyRowValueForColumn(rowValues, column)
	if !hasSelectedValue {
		a.flashStatus("[yellow]The selected row no longer contains that column[-]", a.currentResultRowCount(), 1800*time.Millisecond)
		return
	}
	if selectedValue.isNull {
		a.flashStatus("[yellow]NULL is not a useful relationship key to explore[-]", a.currentResultRowCount(), 1800*time.Millisecond)
		return
	}

	db := a.db
	dbType := a.dbType
	tableName := a.selectedTable
	defaultNamespace := a.defaultObjectNamespace("")
	tableNames := append([]string(nil), a.tableOrder...)
	a.cancelActiveQuery()
	resultGeneration := a.advanceResultGeneration()
	a.restartTotalRowCountFetchIfNeeded()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	var canceled atomic.Bool
	loadingToken := a.showLoadingModal(
		fmt.Sprintf("Finding relationships for %s.%s...", tableName, column),
		withLoadingCancel("Press Esc to cancel relationship discovery.", func() {
			canceled.Store(true)
			cancel()
			a.setFocusWithColor(a.results)
			a.flashStatus("[yellow]Relationship discovery canceled[-]", a.currentResultRowCount(), 1500*time.Millisecond)
		}),
	)

	go func() {
		defer cancel()
		outgoing, err := loadForeignKeyReferences(ctx, db, dbType, tableName, defaultNamespace)
		var incoming []foreignKeyReference
		if err == nil {
			incoming, err = loadIncomingForeignKeyReferences(ctx, db, dbType, tableName, defaultNamespace, tableNames)
		}
		relationships := relationshipsForColumn(tableName, column, outgoing, incoming)

		a.queueUpdateDraw(func() {
			if canceled.Load() {
				return
			}
			if a.db != db || a.dbType != dbType || a.selectedTable != tableName || a.currentResultGeneration() != resultGeneration {
				return
			}
			if !a.finishLoadingModal(loadingToken) {
				return
			}
			a.setFocusWithColor(a.results)
			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not inspect relationships for %s:\n\n%v", iconWarn, tableName, err), "main")
				return
			}
			a.showRelatedDataPicker(tableName, column, selectedValue, rowValues, relationships)
		})
	}()
}

func relationshipsForColumn(tableName, column string, outgoing, incoming []foreignKeyReference) []rowRelationship {
	relationships := make([]rowRelationship, 0)
	for _, ref := range foreignKeysForColumn(outgoing, column) {
		relationships = append(relationships, rowRelationship{
			direction: relationshipOutgoing, sourceTable: tableName, targetTable: ref.targetTable, reference: ref,
		})
	}
	for _, ref := range foreignKeysForTargetColumn(incoming, column) {
		relationships = append(relationships, rowRelationship{
			direction: relationshipIncoming, sourceTable: ref.sourceTable, targetTable: tableName, reference: ref,
		})
	}
	sort.SliceStable(relationships, func(i, j int) bool {
		if relationships[i].direction != relationships[j].direction {
			return relationships[i].direction < relationships[j].direction
		}
		left, right := relationships[i], relationships[j]
		if left.sourceTable != right.sourceTable {
			return left.sourceTable < right.sourceTable
		}
		return left.reference.name < right.reference.name
	})
	return relationships
}

func foreignKeysForTargetColumn(refs []foreignKeyReference, column string) []foreignKeyReference {
	column = strings.TrimSpace(column)
	exact := make([]foreignKeyReference, 0, 1)
	for _, ref := range refs {
		for _, component := range ref.columns {
			if strings.TrimSpace(component.targetColumn) == column {
				exact = append(exact, ref)
				break
			}
		}
	}
	if len(exact) > 0 {
		return exact
	}

	folded := make([]foreignKeyReference, 0, 1)
	spellings := make(map[string]struct{})
	for _, ref := range refs {
		for _, component := range ref.columns {
			targetColumn := strings.TrimSpace(component.targetColumn)
			if strings.EqualFold(targetColumn, column) {
				spellings[targetColumn] = struct{}{}
				folded = append(folded, ref)
				break
			}
		}
	}
	if len(spellings) != 1 {
		return nil
	}
	return folded
}

func loadIncomingForeignKeyReferences(ctx context.Context, db *sql.DB, dbType config.DBType, tableName, defaultNamespace string, tableNames []string) ([]foreignKeyReference, error) {
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
		return loadPostgresIncomingForeignKeyReferences(ctx, db, namespace, tableOnly)
	case config.MySQL:
		return loadMySQLIncomingForeignKeyReferences(ctx, db, namespace, tableOnly)
	case config.SQLite, config.Turso, config.CloudflareD1:
		return loadSQLiteIncomingForeignKeyReferences(ctx, db, dbType, tableOnly, tableNames)
	default:
		return nil, fmt.Errorf("relationship discovery is not supported for %s", dbType)
	}
}

func loadPostgresIncomingForeignKeyReferences(ctx context.Context, db *sql.DB, schemaName, tableName string) ([]foreignKeyReference, error) {
	rows, err := db.QueryContext(ctx, `SELECT constraint_row.conname,
       source_namespace.nspname,
       source_table.relname,
       source_attribute.attname,
       target_attribute.attname,
       key_position.ordinal
FROM pg_catalog.pg_constraint AS constraint_row
JOIN pg_catalog.pg_class AS source_table ON source_table.oid = constraint_row.conrelid
JOIN pg_catalog.pg_namespace AS source_namespace ON source_namespace.oid = source_table.relnamespace
JOIN pg_catalog.pg_class AS target_table ON target_table.oid = constraint_row.confrelid
JOIN pg_catalog.pg_namespace AS target_namespace ON target_namespace.oid = target_table.relnamespace
CROSS JOIN LATERAL generate_subscripts(constraint_row.conkey, 1) AS key_position(ordinal)
JOIN pg_catalog.pg_attribute AS source_attribute
  ON source_attribute.attrelid = constraint_row.conrelid
 AND source_attribute.attnum = constraint_row.conkey[key_position.ordinal]
JOIN pg_catalog.pg_attribute AS target_attribute
  ON target_attribute.attrelid = constraint_row.confrelid
 AND target_attribute.attnum = constraint_row.confkey[key_position.ordinal]
WHERE constraint_row.contype = 'f'
  AND target_namespace.nspname = $1
  AND target_table.relname = $2
ORDER BY source_namespace.nspname, source_table.relname, constraint_row.conname, key_position.ordinal`, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refs := make([]foreignKeyReference, 0)
	for rows.Next() {
		var name, sourceSchema, sourceTable, localColumn, targetColumn string
		var ordinal int
		if err := rows.Scan(&name, &sourceSchema, &sourceTable, &localColumn, &targetColumn, &ordinal); err != nil {
			return nil, err
		}
		refs = appendIncomingForeignKeyComponent(refs, name, qualifiedIdentifier(sourceSchema, sourceTable), tableName, foreignKeyColumnReference{
			localColumn: localColumn, targetColumn: targetColumn, ordinal: ordinal,
		})
	}
	return sortedForeignKeyReferences(refs), rows.Err()
}

func loadMySQLIncomingForeignKeyReferences(ctx context.Context, db *sql.DB, schemaName, tableName string) ([]foreignKeyReference, error) {
	rows, err := db.QueryContext(ctx, `SELECT constraint_name,
       table_name,
       column_name,
       referenced_column_name,
       ordinal_position
FROM information_schema.key_column_usage
WHERE table_schema = ?
  AND referenced_table_schema = ?
  AND referenced_table_name = ?
ORDER BY table_name, constraint_name, ordinal_position`, schemaName, schemaName, tableName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refs := make([]foreignKeyReference, 0)
	for rows.Next() {
		var name, sourceTable, localColumn, targetColumn string
		var ordinal int
		if err := rows.Scan(&name, &sourceTable, &localColumn, &targetColumn, &ordinal); err != nil {
			return nil, err
		}
		refs = appendIncomingForeignKeyComponent(refs, name, sourceTable, tableName, foreignKeyColumnReference{
			localColumn: localColumn, targetColumn: targetColumn, ordinal: ordinal,
		})
	}
	return sortedForeignKeyReferences(refs), rows.Err()
}

func loadSQLiteIncomingForeignKeyReferences(ctx context.Context, db *sql.DB, dbType config.DBType, targetTable string, tableNames []string) ([]foreignKeyReference, error) {
	refs := make([]foreignKeyReference, 0)
	for _, sourceTable := range tableNames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, sourceOnly := splitQualifiedIdentifier(sourceTable)
		rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%s)", quoteIdentifier(dbType, sourceOnly)))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, seq int
			var declaredTarget, localColumn, targetColumn, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &declaredTarget, &localColumn, &targetColumn, &onUpdate, &onDelete, &match); err != nil {
				rows.Close()
				return nil, err
			}
			if !strings.EqualFold(strings.TrimSpace(declaredTarget), strings.TrimSpace(targetTable)) {
				continue
			}
			refs = appendIncomingForeignKeyComponent(refs, fmt.Sprintf("%s.fk#%d", sourceTable, id), sourceTable, targetTable, foreignKeyColumnReference{
				localColumn: localColumn, targetColumn: targetColumn, ordinal: seq,
			})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return sortedForeignKeyReferences(refs), nil
}

func appendIncomingForeignKeyComponent(refs []foreignKeyReference, name, sourceTable, targetTable string, component foreignKeyColumnReference) []foreignKeyReference {
	for index := range refs {
		if refs[index].name == name && refs[index].sourceTable == sourceTable && refs[index].targetTable == targetTable {
			refs[index].columns = append(refs[index].columns, component)
			return refs
		}
	}
	return append(refs, foreignKeyReference{name: name, sourceTable: sourceTable, targetTable: targetTable, columns: []foreignKeyColumnReference{component}})
}

func (a *App) showRelatedDataPicker(tableName, column string, selectedValue foreignKeyRowValue, rowValues map[string]foreignKeyRowValue, relationships []rowRelationship) {
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).
		SetTitle(fmt.Sprintf(" Related Data • %s.%s = %s ", tview.Escape(tableName), tview.Escape(column), tview.Escape(resultValuePreview(resultFilterValueString(selectedValue.value), 24)))).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text)
	list.SetSecondaryTextColor(subtext0)
	list.SetSelectedBackgroundColor(surface0)
	list.SetSelectedTextColor(green)

	if len(relationships) == 0 {
		list.AddItem("  No declared relationships for this column", "  Press V to find the exact value in same-named columns", 0, nil)
	} else {
		for _, relationship := range relationships {
			main, secondary := relationshipPickerLabels(relationship)
			list.AddItem(main, secondary, 0, nil)
		}
	}

	closePicker := func() {
		a.pages.RemovePage(pageRelatedData)
		a.setFocusWithColor(a.results)
	}
	list.SetSelectedFunc(func(index int, _ string, _ string, _ rune) {
		if index < 0 || index >= len(relationships) {
			return
		}
		relationship := relationships[index]
		closePicker()
		a.followRowRelationship(relationship, rowValues)
	})
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyEscape:
			closePicker()
			return nil
		case event.Rune() == 'v' || event.Rune() == 'V':
			closePicker()
			a.findSameValueMatches(tableName, column, selectedValue)
			return nil
		}
		return event
	})

	path := a.relationshipPathLabel(tableName)
	footerText := fmt.Sprintf(" [yellow]Enter[-] Open related rows  │  [yellow]V[-] Same value across tables  │  [yellow]Esc[-] Close  │  [#6c7086]%d relation(s)[-]", len(relationships))
	if path != "" {
		footerText = fmt.Sprintf(" [#6c7086]%s[-]\n%s", tview.Escape(path), footerText)
	}
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter).SetText(footerText)
	footer.SetBackgroundColor(crust)
	footerHeight := 1
	if path != "" {
		footerHeight = 2
	}
	container := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(list, 0, 1, true).
		AddItem(footer, footerHeight, 0, false)
	modalW, modalH := a.modalSize(76, 110, 14, 28)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageRelatedData, grid, true, true)
	a.app.SetFocus(list)
}

func relationshipPickerLabels(relationship rowRelationship) (string, string) {
	mappings := make([]string, 0, len(relationship.reference.columns))
	for _, component := range relationship.reference.columns {
		mappings = append(mappings, fmt.Sprintf("%s.%s → %s.%s", relationship.sourceTable, component.localColumn, relationship.targetTable, component.targetColumn))
	}
	if relationship.direction == relationshipIncoming {
		return fmt.Sprintf(" ← %s", tview.Escape(relationship.sourceTable)), " " + tview.Escape(strings.Join(mappings, ", ")) + " • rows referencing this row"
	}
	return fmt.Sprintf(" → %s", tview.Escape(relationship.targetTable)), " " + tview.Escape(strings.Join(mappings, ", ")) + " • referenced row"
}

func (a *App) followRowRelationship(relationship rowRelationship, rowValues map[string]foreignKeyRowValue) {
	var (
		target     string
		predicates []resultFilterPredicate
		err        error
	)
	if relationship.direction == relationshipIncoming {
		target = relationship.sourceTable
		predicates, err = incomingForeignKeyPredicates(relationship.reference, rowValues)
	} else {
		target = relationship.targetTable
		predicates, err = foreignKeyTargetPredicates(relationship.reference, rowValues)
	}
	if err != nil {
		message := err.Error()
		if errors.Is(err, errForeignKeyValueIsNull) {
			message = "This composite relationship contains NULL, so there is no row to follow."
		}
		a.ShowAlert(fmt.Sprintf("%s Cannot follow %s:\n\n%s", iconInfo, relationship.reference.name, message), "main")
		return
	}
	a.navigateToRelatedRows(target, predicates)
}

func incomingForeignKeyPredicates(ref foreignKeyReference, rowValues map[string]foreignKeyRowValue) ([]resultFilterPredicate, error) {
	if strings.TrimSpace(ref.sourceTable) == "" || len(ref.columns) == 0 {
		return nil, fmt.Errorf("incoming foreign key source is incomplete")
	}
	predicates := make([]resultFilterPredicate, 0, len(ref.columns))
	for _, component := range ref.columns {
		rowValue, ok := foreignKeyRowValueForColumn(rowValues, component.targetColumn)
		if !ok {
			return nil, fmt.Errorf("target column %s is not available in this result row", component.targetColumn)
		}
		if rowValue.isNull {
			return nil, fmt.Errorf("%w: %s", errForeignKeyValueIsNull, component.targetColumn)
		}
		predicates = append(predicates, resultFilterPredicate{
			column: component.localColumn, operator: resultFilterEqual, value: cloneResultRawValue(rowValue.value),
		})
	}
	return predicates, nil
}

func (a *App) navigateToRelatedRows(target string, predicates []resultFilterPredicate) {
	if a == nil || strings.TrimSpace(target) == "" || len(predicates) == 0 {
		return
	}
	origin := a.captureResultNavigationState()
	stackDepth := len(a.resultNavStack)
	a.resultNavStack = append(a.resultNavStack, origin)
	a.rememberCurrentResultFilter()
	a.selectedTable = target
	a.resetSort()
	a.resetPagination()
	a.setCurrentResultFilter(newResultValueFilter(target, predicates))
	a.selectTableListIdentifier(target)
	a.setFocusWithColor(a.results)

	a.loadCurrentTableAsync(tableLoadOptions{
		loadingText:  fmt.Sprintf("Opening related rows in %s...", target),
		cancelText:   "Press Esc to cancel following this relationship.",
		canceledText: "Relationship navigation canceled",
		errorText:    fmt.Sprintf("Could not open related rows in %s", target),
		successText:  fmt.Sprintf("Related path: %s • Backspace returns", tview.Escape(a.relationshipPathLabel(target))),
		rollback: func() {
			a.restoreResultNavigationState(origin)
			if len(a.resultNavStack) > stackDepth {
				a.resultNavStack = a.resultNavStack[:stackDepth]
			}
			a.selectTableListIdentifier(origin.table)
		},
	})
}

func (a *App) relationshipPathLabel(currentTable string) string {
	if a == nil {
		return ""
	}
	parts := make([]string, 0, len(a.resultNavStack)+1)
	for _, state := range a.resultNavStack {
		if strings.TrimSpace(state.table) != "" {
			parts = append(parts, state.table)
		}
	}
	if strings.TrimSpace(currentTable) != "" {
		parts = append(parts, currentTable)
	}
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts, " → ")
}

func (a *App) findSameValueMatches(sourceTable, column string, value foreignKeyRowValue) {
	db := a.db
	dbType := a.dbType
	tableNames := append([]string(nil), a.tableOrder...)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	var canceled atomic.Bool
	resultGeneration := a.advanceResultGeneration()
	loadingToken := a.showLoadingModal(
		fmt.Sprintf("Finding %s in same-named columns...", resultValuePreview(resultFilterValueString(value.value), 28)),
		withLoadingCancel("Press Esc to cancel same-value search.", func() {
			canceled.Store(true)
			cancel()
			a.setFocusWithColor(a.results)
			a.flashStatus("[yellow]Same-value search canceled[-]", a.currentResultRowCount(), 1500*time.Millisecond)
		}),
	)

	go func() {
		defer cancel()
		locations, err := loadSameNamedColumnLocations(ctx, db, dbType, column, tableNames)
		var matches []sameValueMatch
		if err == nil {
			matches, err = filterSameValueMatches(ctx, db, dbType, locations, value)
		}
		a.queueUpdateDraw(func() {
			if canceled.Load() || a.db != db || a.dbType != dbType || a.currentResultGeneration() != resultGeneration {
				return
			}
			if !a.finishLoadingModal(loadingToken) {
				return
			}
			a.setFocusWithColor(a.results)
			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not search same-named columns:\n\n%v", iconWarn, err), "main")
				return
			}
			if len(matches) == 0 {
				a.ShowAlert(fmt.Sprintf("%s No exact matches for %s were found in columns named %s.", iconInfo, resultFilterValueString(value.value), column), "main")
				return
			}
			a.showSameValueMatches(sourceTable, column, value, matches)
		})
	}()
}

func loadSameNamedColumnLocations(ctx context.Context, db *sql.DB, dbType config.DBType, column string, tableNames []string) ([]sameValueMatch, error) {
	if db == nil {
		return nil, fmt.Errorf("not connected")
	}
	column = strings.TrimSpace(column)
	if column == "" {
		return nil, fmt.Errorf("column name is required")
	}
	switch dbType {
	case config.PostgreSQL:
		rows, err := db.QueryContext(ctx, `SELECT column_row.table_schema, column_row.table_name, column_row.column_name
FROM information_schema.columns AS column_row
JOIN information_schema.tables AS table_row
  ON table_row.table_schema = column_row.table_schema
 AND table_row.table_name = column_row.table_name
WHERE column_row.column_name = $1
  AND table_row.table_type = 'BASE TABLE'
  AND column_row.table_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY column_row.table_schema, column_row.table_name`, column)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var matches []sameValueMatch
		for rows.Next() {
			var schemaName, tableName, matchedColumn string
			if err := rows.Scan(&schemaName, &tableName, &matchedColumn); err != nil {
				return nil, err
			}
			matches = append(matches, sameValueMatch{table: qualifiedIdentifier(schemaName, tableName), column: matchedColumn})
		}
		return matches, rows.Err()
	case config.MySQL:
		rows, err := db.QueryContext(ctx, `SELECT column_row.table_name, column_row.column_name
FROM information_schema.columns AS column_row
JOIN information_schema.tables AS table_row
  ON table_row.table_schema = column_row.table_schema
 AND table_row.table_name = column_row.table_name
WHERE column_row.table_schema = DATABASE()
  AND column_row.column_name = ?
  AND table_row.table_type = 'BASE TABLE'
ORDER BY column_row.table_name`, column)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var matches []sameValueMatch
		for rows.Next() {
			var tableName, matchedColumn string
			if err := rows.Scan(&tableName, &matchedColumn); err != nil {
				return nil, err
			}
			matches = append(matches, sameValueMatch{table: tableName, column: matchedColumn})
		}
		return matches, rows.Err()
	case config.SQLite, config.Turso, config.CloudflareD1:
		return loadSQLiteSameNamedColumnLocations(ctx, db, dbType, column, tableNames)
	default:
		return nil, fmt.Errorf("same-value search is not supported for %s", dbType)
	}
}

func loadSQLiteSameNamedColumnLocations(ctx context.Context, db *sql.DB, dbType config.DBType, column string, tableNames []string) ([]sameValueMatch, error) {
	var matches []sameValueMatch
	for _, tableName := range tableNames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, tableOnly := splitQualifiedIdentifier(tableName)
		rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(dbType, tableOnly)))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, declaredType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return nil, err
			}
			match := sameValueMatch{table: tableName, column: name}
			if strings.EqualFold(name, column) {
				matches = append(matches, match)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return matches, nil
}

func filterSameValueMatches(ctx context.Context, db *sql.DB, dbType config.DBType, locations []sameValueMatch, value foreignKeyRowValue) ([]sameValueMatch, error) {
	matches := make([]sameValueMatch, 0, len(locations))
	for _, location := range locations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		predicate := resultFilterPredicate{column: location.column, operator: resultFilterEqual, value: cloneResultRawValue(value.value)}
		if value.isNull {
			predicate.operator = resultFilterIsNull
			predicate.value = nil
		}
		filter := newResultValueFilter(location.table, []resultFilterPredicate{predicate})
		clause, args := resultFilterSQL(dbType, filter)
		query := fmt.Sprintf("SELECT 1 FROM %s%s LIMIT 1", quoteIdentifier(dbType, location.table), clause)
		var one int
		err := db.QueryRowContext(ctx, query, args...).Scan(&one)
		switch {
		case err == nil:
			matches = append(matches, location)
		case errors.Is(err, sql.ErrNoRows):
			continue
		default:
			return nil, fmt.Errorf("search %s.%s: %w", location.table, location.column, err)
		}
	}
	return matches, nil
}

func (a *App) showSameValueMatches(sourceTable, column string, value foreignKeyRowValue, matches []sameValueMatch) {
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).
		SetTitle(fmt.Sprintf(" Same Value • %s = %s ", tview.Escape(column), tview.Escape(resultValuePreview(resultFilterValueString(value.value), 28)))).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text)
	list.SetSecondaryTextColor(subtext0)
	list.SetSelectedBackgroundColor(surface0)
	list.SetSelectedTextColor(green)
	for _, match := range matches {
		note := "value match only; not necessarily a relationship"
		if match.table == sourceTable {
			note = "current table"
		}
		list.AddItem(" "+tview.Escape(match.table), fmt.Sprintf(" %s.%s • %s", tview.Escape(match.table), tview.Escape(match.column), note), 0, nil)
	}
	closePicker := func() {
		a.pages.RemovePage(pageSameValueMatches)
		a.setFocusWithColor(a.results)
	}
	list.SetSelectedFunc(func(index int, _ string, _ string, _ rune) {
		if index < 0 || index >= len(matches) {
			return
		}
		match := matches[index]
		closePicker()
		operator := resultFilterEqual
		predicateValue := cloneResultRawValue(value.value)
		if value.isNull {
			operator = resultFilterIsNull
			predicateValue = nil
		}
		a.navigateToRelatedRows(match.table, []resultFilterPredicate{{column: match.column, operator: operator, value: predicateValue}})
	})
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			closePicker()
			return nil
		}
		return event
	})
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter).
		SetText(fmt.Sprintf(" [yellow]Enter[-] Open filtered table  │  [yellow]Esc[-] Close  │  [#6c7086]%d table(s)[-]", len(matches)))
	footer.SetBackgroundColor(crust)
	container := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(list, 0, 1, true).
		AddItem(footer, 1, 0, false)
	modalW, modalH := a.modalSize(68, 100, 12, 26)
	grid := tview.NewGrid().SetColumns(0, modalW, 0).SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageSameValueMatches, grid, true, true)
	a.app.SetFocus(list)
}
