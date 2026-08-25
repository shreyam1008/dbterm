package ui

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

type sidebarColumnRef struct {
	table  string
	column string
	last   bool
}

type sidebarColumnMeta struct {
	name       string
	dataType   string
	notNull    bool
	primaryKey bool
	foreignKey bool
	target     string
}

type sidebarSearchEntry struct {
	table        string
	column       string
	tableFolded  string
	columnFolded string
	order        int
}

type sidebarSearchLookup struct {
	tables  map[string][]int
	columns map[string][]int
}

type sidebarSelection struct {
	table   string
	column  string
	index   int
	indexed bool
}

func buildSidebarSearchIndex(tableOrder []string, catalog sqlCompletionCatalog, metadata map[string][]sidebarColumnMeta) []sidebarSearchEntry {
	relations := make(map[string]sqlCompletionRelation, len(catalog.relations))
	for _, relation := range catalog.relations {
		relations[strings.ToLower(relation.name)] = relation
	}
	entries := make([]sidebarSearchEntry, 0, len(tableOrder)*4)
	order := 0
	for _, table := range tableOrder {
		table = strings.TrimSpace(table)
		if table == "" {
			continue
		}
		tableFolded := strings.ToLower(table)
		entries = append(entries, sidebarSearchEntry{table: table, tableFolded: tableFolded, order: order})
		order++

		columns := make([]string, 0)
		if cached, ok := metadata[table]; ok {
			columns = make([]string, 0, len(cached))
			for _, column := range cached {
				columns = append(columns, column.name)
			}
		} else if relation, ok := relations[tableFolded]; ok {
			columns = relation.columns
		}
		for _, column := range columns {
			column = strings.TrimSpace(column)
			if column == "" {
				continue
			}
			entries = append(entries, sidebarSearchEntry{
				table: table, column: column, tableFolded: tableFolded,
				columnFolded: strings.ToLower(column), order: order,
			})
			order++
		}
	}
	return entries
}

func buildSidebarSearchLookup(entries []sidebarSearchEntry) sidebarSearchLookup {
	lookup := sidebarSearchLookup{
		tables:  make(map[string][]int),
		columns: make(map[string][]int),
	}
	for index, entry := range entries {
		lookup.tables[entry.tableFolded] = append(lookup.tables[entry.tableFolded], index)
		if entry.columnFolded != "" {
			lookup.columns[entry.columnFolded] = append(lookup.columns[entry.columnFolded], index)
		}
	}
	return lookup
}

func bestSidebarSearchMatch(entries []sidebarSearchEntry, query string) (sidebarSearchEntry, bool) {
	return bestSidebarSearchMatchWithLookup(entries, sidebarSearchLookup{}, query)
}

func bestSidebarSearchMatchWithLookup(entries []sidebarSearchEntry, lookup sidebarSearchLookup, query string) (sidebarSearchEntry, bool) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return sidebarSearchEntry{}, false
	}
	if candidates := exactSidebarSearchCandidates(lookup, strings.Fields(query)); len(candidates) > 0 {
		if best, found := bestSidebarSearchMatchInCandidates(entries, candidates, query, false); found {
			return best, true
		}
		if best, found := bestSidebarSearchMatchInCandidates(entries, candidates, query, true); found {
			return best, true
		}
	}
	if best, found := bestSidebarSearchMatchInCandidates(entries, nil, query, false); found {
		return best, true
	}
	return bestSidebarSearchMatchInCandidates(entries, nil, query, true)
}

func bestSidebarSearchMatchInCandidates(entries []sidebarSearchEntry, candidates []int, query string, fuzzy bool) (sidebarSearchEntry, bool) {
	bestScore := int(^uint(0) >> 1)
	var best sidebarSearchEntry
	found := false
	if len(candidates) > 0 {
		for _, index := range candidates {
			if index < 0 || index >= len(entries) {
				continue
			}
			entry := entries[index]
			score, ok := sidebarSearchEntryScore(entry, query, fuzzy)
			if ok && score < bestScore {
				best, bestScore, found = entry, score, true
			}
		}
		return best, found
	}
	for _, entry := range entries {
		score, ok := sidebarSearchEntryScore(entry, query, fuzzy)
		if !ok || score >= bestScore {
			continue
		}
		best, bestScore, found = entry, score, true
	}
	return best, found
}

func exactSidebarSearchCandidates(lookup sidebarSearchLookup, terms []string) []int {
	var best []int
	for _, term := range terms {
		term = strings.TrimSpace(strings.ToLower(term))
		if dot := strings.LastIndex(term, "."); dot >= 0 {
			term = term[:dot]
		}
		for _, candidates := range [][]int{lookup.tables[term], lookup.columns[term]} {
			if len(candidates) > 0 && (len(best) == 0 || len(candidates) < len(best)) {
				best = candidates
			}
		}
	}
	return best
}

func sidebarSearchEntryScore(entry sidebarSearchEntry, query string, fuzzy bool) (int, bool) {
	if strings.Contains(query, ".") && entry.column != "" {
		dot := strings.LastIndex(query, ".")
		tableQuery, columnQuery := query[:dot], query[dot+1:]
		if tableQuery == "" || columnQuery == "" || !strings.Contains(entry.tableFolded, tableQuery) || !strings.Contains(entry.columnFolded, columnQuery) {
			return 0, false
		}
		return sidebarTextMatchScore(entry.tableFolded, tableQuery) + sidebarTextMatchScore(entry.columnFolded, columnQuery), true
	}
	target := entry.tableFolded
	kindPenalty := 0
	if entry.column != "" {
		target = entry.columnFolded
		kindPenalty = 4
	}
	if strings.Contains(target, query) {
		return sidebarTextMatchScore(target, query) + kindPenalty, true
	}
	if fuzzy {
		if score, ok := foldedSubsequenceScore(target, query); ok {
			return 80 + score + kindPenalty, true
		}
	}
	return 0, false
}

func sidebarTextMatchScore(target, query string) int {
	switch {
	case target == query:
		return 0
	case strings.HasPrefix(target, query):
		return 10 + len(target) - len(query)
	default:
		return 30 + strings.Index(target, query)
	}
}

func foldedSubsequenceScore(target, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	matched := 0
	gaps := 0
	started := false
	for _, candidate := range target {
		if matched >= len(query) {
			break
		}
		wanted, size := utf8.DecodeRuneInString(query[matched:])
		if unicode.ToLower(candidate) == unicode.ToLower(wanted) {
			matched += size
			started = true
		} else if started {
			gaps++
		}
	}
	if matched != len(query) {
		return 0, false
	}
	return gaps + len(target) - len(query), true
}

func (a *App) updateSidebarSearchIndex(catalog sqlCompletionCatalog) {
	if a == nil {
		return
	}
	index := buildSidebarSearchIndex(a.tableOrder, catalog, a.sidebarColumnMetadata)
	a.applySidebarSearchState(index, buildSidebarSearchLookup(index))
}

func (a *App) applySidebarSearchState(index []sidebarSearchEntry, lookup sidebarSearchLookup) {
	if a == nil {
		return
	}
	a.sidebarSearchIndex = index
	a.sidebarSearchLookup = lookup
	if a.tables == nil {
		return
	}
	selection := a.currentSidebarSelection()
	if a.expandedSidebarTable != "" {
		a.rebuildTableSidebar(selection)
	} else if a.tableSearch != "" {
		a.applyTableSearch()
	}
}

func (a *App) ensureSidebarSearchIndex() {
	if a == nil {
		return
	}
	if len(a.sidebarSearchIndex) > 0 {
		if a.sidebarSearchLookup.tables == nil {
			a.sidebarSearchLookup = buildSidebarSearchLookup(a.sidebarSearchIndex)
		}
		return
	}
	tables := append([]string(nil), a.tableOrder...)
	if len(tables) == 0 && len(a.tableIdentifiers) > 0 {
		indexes := make([]int, 0, len(a.tableIdentifiers))
		for index := range a.tableIdentifiers {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			tables = append(tables, a.tableIdentifiers[index])
		}
	}
	a.sidebarSearchIndex = buildSidebarSearchIndex(tables, a.sqlCompletionCatalog, a.sidebarColumnMetadata)
	a.sidebarSearchLookup = buildSidebarSearchLookup(a.sidebarSearchIndex)
}

func (a *App) bestSidebarSearchMatch(query string) (sidebarSearchEntry, bool) {
	if a == nil {
		return sidebarSearchEntry{}, false
	}
	return bestSidebarSearchMatchWithLookup(a.sidebarSearchIndex, a.sidebarSearchLookup, query)
}

func (a *App) currentSidebarSelection() sidebarSelection {
	if a == nil || a.tables == nil {
		return sidebarSelection{}
	}
	index := a.tables.GetCurrentItem()
	if column, ok := a.tableColumnItems[index]; ok {
		return sidebarSelection{table: column.table, column: column.column}
	}
	if table, ok := a.tableIdentifiers[index]; ok {
		return sidebarSelection{table: table}
	}
	return sidebarSelection{}
}

func (a *App) sidebarColumnsForTable(table string) []sidebarColumnMeta {
	if a == nil || table == "" {
		return nil
	}
	if columns, ok := a.sidebarColumnMetadata[table]; ok {
		return append([]sidebarColumnMeta(nil), columns...)
	}
	relation, ok := findSQLCompletionRelation(a.sqlCompletionCatalog, table)
	if !ok {
		return nil
	}
	columns := make([]sidebarColumnMeta, 0, len(relation.columns))
	for _, name := range relation.columns {
		columns = append(columns, sidebarColumnMeta{name: name})
	}
	return columns
}

func (a *App) appendExpandedSidebarColumns(items []tableListSnapshotItem, table string) []tableListSnapshotItem {
	if a == nil || table == "" || a.expandedSidebarTable != table {
		return items
	}
	columns := a.sidebarColumnsForTable(table)
	if len(columns) == 0 {
		return append(items, tableListSnapshotItem{
			label: fmt.Sprintf("      [#6c7086]%s loading columns…[-]", iconRefresh), parent: table,
		})
	}
	for index, column := range columns {
		items = append(items, tableListSnapshotItem{
			parent: table, column: column.name, lastColumn: index == len(columns)-1,
		})
	}
	return items
}

func (a *App) rebuildTableSidebar(selection sidebarSelection) {
	a.rebuildTableSidebarItems(selection)
	a.renderTableSidebarSearch()
}

func (a *App) rebuildTableSidebarItems(selection sidebarSelection) {
	if a == nil || a.tables == nil {
		return
	}
	preserved := make([]preservedSidebarItem, 0, max(0, a.tables.GetItemCount()-a.tableSidebarItems))
	for index := a.tableSidebarItems; index < a.tables.GetItemCount(); index++ {
		mainText, secondaryText := a.tables.GetItemText(index)
		item := preservedSidebarItem{mainText: mainText, secondaryText: secondaryText}
		if object, ok := a.databaseObjects[index]; ok {
			objectCopy := object
			item.object = &objectCopy
		}
		preserved = append(preserved, item)
	}

	a.tables.Clear()
	a.sidebarRenderedSearch = sidebarSelection{}
	a.tableIdentifiers = map[int]string{}
	a.tableColumnItems = map[int]sidebarColumnRef{}
	a.databaseObjects = map[int]databaseObjectListItem{}
	for index, item := range a.orderedTableSidebarItems() {
		a.addTableSidebarItem(index, item)
	}
	a.tableSidebarItems = a.tables.GetItemCount()
	for _, item := range preserved {
		index := a.tables.GetItemCount()
		a.tables.AddItem(item.mainText, item.secondaryText, 0, nil)
		if item.object != nil {
			a.databaseObjects[index] = *item.object
		}
	}
	a.selectSidebarSelection(selection)
}

func (a *App) addTableSidebarItem(index int, item tableListSnapshotItem) {
	label := item.label
	if item.identifier != "" {
		label = a.tableSidebarLabel(item.identifier, tview.Escape(item.identifier))
	} else if item.column != "" {
		label = a.sidebarColumnLabel(sidebarColumnRef{table: item.parent, column: item.column, last: item.lastColumn}, item.lastColumn, "")
	}
	a.tables.AddItem(label, "", 0, nil)
	if item.identifier != "" {
		a.tableIdentifiers[index] = item.identifier
	}
	if item.parent != "" && item.column != "" {
		a.tableColumnItems[index] = sidebarColumnRef{table: item.parent, column: item.column, last: item.lastColumn}
	}
}

func (a *App) selectSidebarSelection(selection sidebarSelection) {
	if a == nil || a.tables == nil {
		return
	}
	for index, column := range a.tableColumnItems {
		if selection.column != "" && column.table == selection.table && strings.EqualFold(column.column, selection.column) {
			a.tables.SetCurrentItem(index)
			return
		}
	}
	if selection.table != "" {
		a.selectTableListIdentifier(selection.table)
	}
}

func (a *App) sidebarColumnLabel(ref sidebarColumnRef, last bool, search string) string {
	branch := "├─"
	if last {
		branch = "└─"
	}
	nameLabel := tview.Escape(ref.column)
	if search != "" {
		if highlighted, matched := highlightTableSearchMatch(ref.column, search); matched {
			nameLabel = highlighted
		}
	}
	meta := a.sidebarColumnMeta(ref)
	badges := ""
	if meta.primaryKey {
		badges += " [#f9e2af]PK[-]"
	}
	if meta.foreignKey {
		badges += " [#cba6f7]FK[-]"
	}
	if meta.notNull && !meta.primaryKey {
		badges += " [#89b4fa]NN[-]"
	}
	typeLabel := ""
	if meta.dataType != "" {
		typeLabel = " [#6c7086]" + tview.Escape(strings.ToLower(meta.dataType)) + "[-]"
	}
	return fmt.Sprintf("      [#6c7086]%s[-] %s%s%s", branch, nameLabel, badges, typeLabel)
}

func (a *App) sidebarColumnMeta(ref sidebarColumnRef) sidebarColumnMeta {
	for _, column := range a.sidebarColumnMetadata[ref.table] {
		if strings.EqualFold(column.name, ref.column) {
			return column
		}
	}
	return sidebarColumnMeta{name: ref.column}
}

func (a *App) toggleSidebarTable(table string) {
	if a == nil || strings.TrimSpace(table) == "" {
		return
	}
	if a.expandedSidebarTable == table {
		a.expandedSidebarTable = ""
	} else {
		a.expandedSidebarTable = table
	}
	a.rebuildTableSidebar(sidebarSelection{table: table})
	if a.expandedSidebarTable == table {
		a.loadSidebarColumnMetadata(table)
	}
}

func (a *App) focusSidebarColumnParent(ref sidebarColumnRef) {
	if a == nil || ref.table == "" {
		return
	}
	a.selectTableListIdentifier(ref.table)
}

func (a *App) selectFirstSidebarColumn(table string) bool {
	if a == nil || a.tables == nil || table == "" {
		return false
	}
	bestIndex := a.tables.GetItemCount()
	for index, column := range a.tableColumnItems {
		if column.table == table && index < bestIndex {
			bestIndex = index
		}
	}
	if bestIndex >= a.tables.GetItemCount() {
		return false
	}
	a.tables.SetCurrentItem(bestIndex)
	return true
}

func (a *App) revealSidebarColumn(table, column string) {
	if a == nil || table == "" || column == "" {
		return
	}
	a.expandedSidebarTable = table
	a.rebuildTableSidebar(sidebarSelection{table: table, column: column})
	a.loadSidebarColumnMetadata(table)
	if a.app != nil {
		a.setFocusWithColor(a.tables)
	}
}

func (a *App) loadSidebarColumnMetadata(table string) {
	if a == nil || a.db == nil || table == "" {
		return
	}
	if _, loaded := a.sidebarColumnMetadata[table]; loaded {
		return
	}
	if a.sidebarMetadataLoads == nil {
		a.sidebarMetadataLoads = make(map[string]bool)
	}
	if a.sidebarMetadataLoads[table] {
		return
	}
	a.sidebarMetadataLoads[table] = true
	db, dbType := a.db, a.dbType
	generation := a.sqlCompletionGeneration.Load()
	defaultNamespace := a.defaultObjectNamespace("")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		columns, err := loadSidebarColumnMetadata(ctx, db, dbType, table, defaultNamespace)
		a.queueUpdateDraw(func() {
			if a.db != db || a.dbType != dbType || a.sqlCompletionGeneration.Load() != generation {
				return
			}
			delete(a.sidebarMetadataLoads, table)
			if err != nil {
				return
			}
			a.applySidebarColumnMetadata(table, columns)
		})
	}()
}

func (a *App) applySidebarColumnMetadata(table string, columns []sidebarColumnMeta) {
	if a == nil || table == "" {
		return
	}
	if a.sidebarColumnMetadata == nil {
		a.sidebarColumnMetadata = make(map[string][]sidebarColumnMeta)
	}
	a.sidebarColumnMetadata[table] = columns
	if a.expandedSidebarTable != table {
		return
	}
	if a.refreshExpandedSidebarColumnLabels(table, columns) {
		return
	}
	selection := a.currentSidebarSelection()
	if selection.table != table {
		selection = sidebarSelection{table: table}
	}
	// Metadata enriches labels only. The completion catalog already owns the
	// searchable column list, so rebuilding the full schema index here would
	// block the UI on every expansion in large databases.
	a.rebuildTableSidebar(selection)
}

func (a *App) refreshExpandedSidebarColumnLabels(table string, columns []sidebarColumnMeta) bool {
	if a == nil || a.tables == nil || table == "" || len(columns) == 0 {
		return false
	}
	type visibleColumn struct {
		index int
		ref   sidebarColumnRef
	}
	visible := make([]visibleColumn, 0, len(columns))
	for index, ref := range a.tableColumnItems {
		if ref.table == table {
			visible = append(visible, visibleColumn{index: index, ref: ref})
		}
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].index < visible[j].index })
	if len(visible) != len(columns) {
		return false
	}
	for index := range visible {
		if !strings.EqualFold(visible[index].ref.column, columns[index].name) {
			return false
		}
	}
	for _, column := range visible {
		a.tables.SetItemText(column.index, a.sidebarColumnLabel(column.ref, column.ref.last, ""), "")
	}
	a.renderTableSidebarSearch()
	return true
}

func (a *App) scheduleSidebarMetadataLoad(table string) {
	if a == nil || table == "" {
		return
	}
	generation := a.sidebarSearchGeneration.Add(1)
	go func() {
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		a.queueUpdateDraw(func() {
			if a.sidebarSearchGeneration.Load() != generation || a.expandedSidebarTable != table {
				return
			}
			a.loadSidebarColumnMetadata(table)
		})
	}()
}

func loadSidebarColumnMetadata(ctx context.Context, db *sql.DB, dbType config.DBType, tableName, defaultNamespace string) ([]sidebarColumnMeta, error) {
	if db == nil {
		return nil, fmt.Errorf("not connected")
	}
	namespace, tableOnly := splitQualifiedIdentifier(tableName)
	if namespace == "" {
		namespace = defaultNamespace
	}
	columns := make([]sidebarColumnMeta, 0)
	switch dbType {
	case config.PostgreSQL:
		rows, err := db.QueryContext(ctx, `SELECT column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2
ORDER BY ordinal_position`, namespace, tableOnly)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, dataType, nullable string
			if err := rows.Scan(&name, &dataType, &nullable); err != nil {
				rows.Close()
				return nil, err
			}
			columns = append(columns, sidebarColumnMeta{name: name, dataType: dataType, notNull: strings.EqualFold(nullable, "NO")})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	case config.MySQL:
		rows, err := db.QueryContext(ctx, `SELECT column_name, column_type, is_nullable
FROM information_schema.columns
WHERE table_schema = ? AND table_name = ?
ORDER BY ordinal_position`, namespace, tableOnly)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, dataType, nullable string
			if err := rows.Scan(&name, &dataType, &nullable); err != nil {
				rows.Close()
				return nil, err
			}
			columns = append(columns, sidebarColumnMeta{name: name, dataType: dataType, notNull: strings.EqualFold(nullable, "NO")})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	case config.SQLite, config.Turso, config.CloudflareD1:
		rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(dbType, tableOnly)))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid, notNull, primary int
			var name, dataType string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primary); err != nil {
				rows.Close()
				return nil, err
			}
			columns = append(columns, sidebarColumnMeta{name: name, dataType: dataType, notNull: notNull == 1, primaryKey: primary > 0})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("column metadata is not supported for %s", dbType)
	}

	if dbType == config.PostgreSQL || dbType == config.MySQL {
		primary, err := loadSidebarPrimaryKeyColumns(ctx, db, dbType, namespace, tableOnly)
		if err == nil {
			for index := range columns {
				columns[index].primaryKey = primary[strings.ToLower(columns[index].name)]
			}
		}
	}
	if refs, err := loadForeignKeyReferences(ctx, db, dbType, tableName, defaultNamespace); err == nil {
		for _, ref := range refs {
			for _, component := range ref.columns {
				for index := range columns {
					if strings.EqualFold(columns[index].name, component.localColumn) {
						columns[index].foreignKey = true
						columns[index].target = qualifiedIdentifier(ref.targetTable, component.targetColumn)
					}
				}
			}
		}
	}
	return columns, nil
}

func loadSidebarPrimaryKeyColumns(ctx context.Context, db *sql.DB, dbType config.DBType, namespace, table string) (map[string]bool, error) {
	result := make(map[string]bool)
	var rows *sql.Rows
	var err error
	switch dbType {
	case config.PostgreSQL:
		rows, err = db.QueryContext(ctx, `SELECT attribute.attname
FROM pg_catalog.pg_constraint constraint_row
JOIN pg_catalog.pg_class table_row ON table_row.oid = constraint_row.conrelid
JOIN pg_catalog.pg_namespace namespace_row ON namespace_row.oid = table_row.relnamespace
JOIN pg_catalog.pg_attribute attribute
  ON attribute.attrelid = table_row.oid AND attribute.attnum = ANY(constraint_row.conkey)
WHERE constraint_row.contype = 'p' AND namespace_row.nspname = $1 AND table_row.relname = $2`, namespace, table)
	case config.MySQL:
		rows, err = db.QueryContext(ctx, `SELECT column_name
FROM information_schema.key_column_usage
WHERE table_schema = ? AND table_name = ? AND constraint_name = 'PRIMARY'
ORDER BY ordinal_position`, namespace, table)
	default:
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		result[strings.ToLower(column)] = true
	}
	return result, rows.Err()
}

func sortedSidebarColumnMetadata(columns []sidebarColumnMeta) []sidebarColumnMeta {
	copyColumns := append([]sidebarColumnMeta(nil), columns...)
	sort.SliceStable(copyColumns, func(i, j int) bool { return copyColumns[i].name < copyColumns[j].name })
	return copyColumns
}
