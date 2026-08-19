package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

const (
	pageCommandPalette       = "commandPalette"
	commandPaletteQueryLimit = 24

	paletteActionRunQuery             keymapAction = "palette_run_query"
	paletteActionSQLSuggestions       keymapAction = "palette_sql_suggestions"
	paletteActionRefreshTable         keymapAction = "palette_refresh_table"
	paletteActionRefreshDatabase      keymapAction = "palette_refresh_database"
	paletteActionFilterColumn         keymapAction = "palette_filter_column"
	paletteActionFilterClipboard      keymapAction = "palette_filter_clipboard"
	paletteActionClearFilters         keymapAction = "palette_clear_filters"
	paletteActionCopyCell             keymapAction = "palette_copy_cell"
	paletteActionFindResultColumn     keymapAction = "palette_find_result_column"
	paletteActionCopyColumnName       keymapAction = "palette_copy_column_name"
	paletteActionExploreRelationships keymapAction = "palette_explore_relationships"
	paletteActionSortColumn           keymapAction = "palette_sort_column"
	paletteActionOpenRowDetail        keymapAction = "palette_open_row_detail"
	paletteActionNextPage             keymapAction = "palette_next_page"
	paletteActionPreviousPage         keymapAction = "palette_previous_page"
	paletteActionFirstPage            keymapAction = "palette_first_page"
	paletteActionLastPage             keymapAction = "palette_last_page"
	paletteActionToggleTablePin       keymapAction = "palette_toggle_table_pin"
	paletteActionCopyTableName        keymapAction = "palette_copy_table_name"
	paletteActionUpdates              keymapAction = "palette_updates"
)

type commandPaletteItemKind string

const (
	commandPaletteAction    commandPaletteItemKind = "action"
	commandPaletteTable     commandPaletteItemKind = "table"
	commandPaletteColumn    commandPaletteItemKind = "column"
	commandPaletteView      commandPaletteItemKind = "view"
	commandPaletteFunction  commandPaletteItemKind = "function"
	commandPaletteProcedure commandPaletteItemKind = "procedure"
	commandPaletteTrigger   commandPaletteItemKind = "trigger"
	commandPaletteQuery     commandPaletteItemKind = "recent query"
	commandPaletteBackupJob commandPaletteItemKind = "backup job"
)

type commandPaletteItem struct {
	id          string
	kind        commandPaletteItemKind
	title       string
	description string
	keywords    string
	objectName  string
	columnName  string
	shortcut    string
	action      keymapAction
	objectType  database.DBObjectType
	query       string
	sortOrder   int
}

type commandPaletteMatch struct {
	item           commandPaletteItem
	score          int
	titlePositions []int
	sourceIndex    int
}

type commandPaletteActionSpec struct {
	action      keymapAction
	title       string
	description string
	keywords    string
	shortcut    string
}

var commandPaletteActionSpecs = []commandPaletteActionSpec{
	{actionFocusTables, "Focus Tables & Schema", "Move focus to the searchable schema tree so you can expand tables and open columns.", "sidebar objects columns navigation browse", ""},
	{actionFocusQuery, "Focus Query Editor", "Move focus to the SQL editor and keep the current query text intact.", "sql statement editor write", ""},
	{actionFocusResults, "Focus Results", "Move focus to the result grid for cell navigation, filtering, and row actions.", "data grid cells rows navigation", ""},
	{actionDashboard, "Open Dashboard", "Open saved connections, connection health, and connection management.", "connections home back manage", ""},
	{actionHelp, "Open Help & SQL Cheatsheets", "Show dbterm keyboard workflows and database-specific SQL reference sheets.", "shortcuts keys documentation postgres mysql sqlite", ""},
	{actionServices, "Open Database Services", "Inspect and manage supported local MySQL and PostgreSQL services.", "system local mysql postgresql start stop status", ""},
	{actionBackupCenter, "Open Backup Center", "Create schedules, run or prune backups, restore artifacts, and manage the agent. N chooses a saved database or adds one; Ctrl+N adds a database from the plan form. Dashboard Ctrl+B starts preselected.", "new saved database connection scheduled automatic restore agent history retention encryption zstd zip ctrl n", ""},
	{actionChangeProfiler, "Open Change Profiler", "Create named anchors, scan for row and schema changes, and inspect saved before/after reports.", "diff snapshot anchor track changes inserted updated deleted audit", ""},
	{actionFullscreen, "Toggle Fullscreen Results", "Expand the result grid to the full workspace or restore the normal layout.", "maximize expand data grid", ""},
	{actionInspectSchema, "Inspect Selected Table Schema", "Show columns, keys, foreign keys, and indexes for the selected table.", "metadata structure columns constraints indexes foreign keys", ""},
	{actionHistory, "Open Query History", "Browse successful queries saved for the active connection and load one into the editor.", "recent sql previous statements", ""},
	{actionExportCSV, "Export Results to CSV", "Choose selected rows, the current page, or all table rows matching the active filters and stream them safely to CSV.", "download save spreadsheet comma separated all filtered matching stream", ""},
	{actionBackup, "Back Up Current Database", "From any workspace panel, create an engine-appropriate backup of the active database. F2 chooses a folder and F3 refreshes destination and staging capacity.", "dump snapshot save restore folder chooser destination staging capacity disk f2 f3", ""},
	{actionImportDump, "Import SQL Dump", "Import a supported PostgreSQL or MySQL dump into the active connection.", "restore upload sql file", ""},
	{actionSelectAll, "Select All Displayed Rows", "Select every currently displayed data row for a bulk result action.", "mark rows bulk csv", ""},
	{actionClearSelection, "Clear Result Row Selection", "Remove the selection marker from all currently displayed result rows.", "unselect deselect rows bulk", ""},
	{actionSettings, "Open Settings", "Configure effective keyboard shortcuts and dashboard health-check behavior.", "preferences keymap bindings configuration", ""},
	{paletteActionUpdates, "Version & Update", "Show the current build, check the latest GitHub release, and install it with checksum verification while preserving the user profile.", "about upgrade latest release current version", "U (Dashboard)"},
	{paletteActionSQLSuggestions, "Open Smart SQL Suggestions", "Focus Query and show context-ranked SQL, typo-tolerant tables, selected-table columns, and ready read-only query templates.", "autocomplete completion template preview count columns typo", "Ctrl+Space"},
	{paletteActionRunQuery, "Run Current SQL", "Execute the SQL currently in the Query editor against the active connection.", "execute statement editor", "Enter"},
	{paletteActionRefreshTable, "Refresh Current Table", "Reload the active table page without blocking the interface; Esc cancels safely.", "reload data rows", "F5"},
	{paletteActionRefreshDatabase, "Refresh Database Objects and Data", "Reload tables and objects, then reload the active table with cancellable progress.", "full reload schema sidebar", "Ctrl+F5"},
	{paletteActionToggleTablePin, "Pin / Unpin Selected Table", "Move the selected sidebar table into or out of the persistent pinned section for this database connection.", "favorite favourite top sidebar table", "Space (Tables)"},
	{paletteActionCopyTableName, "Copy Selected Table Name", "Copy the complete selected table identifier without opening it.", "clipboard relation identifier sidebar", "Shift+C / Right-click (Tables)"},
	{paletteActionFindResultColumn, "Find a Result Column", "Focus the selectable header row; type to find and highlight a column, then press Down or Enter for its data.", "header field name jump search highlight columns", "↑ from first data row"},
	{paletteActionCopyColumnName, "Copy Selected Column Name", "Copy the complete name of the selected result column.", "clipboard header field identifier", "Shift+C (Headers)"},
	{paletteActionFilterColumn, "Filter Selected Column", "Open the typed filter builder for the selected result column; Apply updates and Add AND composes.", "where search operator contains starts null", "/"},
	{paletteActionFilterClipboard, "Filter Column by Clipboard", "Apply or update equality on the selected column using the copied value; SQL NULL becomes IS NULL.", "paste value cross table lookup", "V"},
	{paletteActionClearFilters, "Clear All Active Filters", "Remove every active table predicate and reload the first page.", "reset where predicates", "Esc"},
	{paletteActionCopyCell, "Copy Selected Cell", "Copy the complete selected cell value, even when its visible preview is shortened.", "clipboard full raw value", "C"},
	{paletteActionExploreRelationships, "Explore Related Rows", "Open parent or child rows using every component of a declared key; repeat across a chain and use Backspace to return.", "relationship parent child join reference navigation composite chain", "F"},
	{paletteActionSortColumn, "Sort by Selected Column", "Toggle ascending or descending server-side sorting for the active table.", "order ascending descending", "S"},
	{paletteActionOpenRowDetail, "Open Selected Row Details", "Inspect every full value in the selected row in a vertical detail view.", "inspect record full json", "Enter"},
	{paletteActionNextPage, "Go to Next Result Page", "Load the next bounded page of the active table with cancellable progress.", "pagination forward", "PgDn / ]"},
	{paletteActionPreviousPage, "Go to Previous Result Page", "Return to the previous bounded page of the active table.", "pagination back", "PgUp / ["},
	{paletteActionFirstPage, "Go to First Result Page", "Jump to the first page of the active table.", "pagination beginning", "Home"},
	{paletteActionLastPage, "Go to Last Result Page", "Jump to the final page once the matching row count is known.", "pagination end", "End"},
}

func (a *App) showCommandPalette() {
	if a == nil || a.app == nil || a.pages == nil {
		return
	}
	if a.pages.HasPage(pageCommandPalette) {
		a.dismissCommandPalette(true)
		return
	}

	a.paletteReturnPage, _ = a.pages.GetFrontPage()
	a.paletteReturnFocus = a.app.GetFocus()
	a.ensureSidebarSearchIndex()
	items := a.buildCommandPaletteItems()
	paletteShortcut := a.effectiveActionShortcut(actionCommandPalette)
	if paletteShortcut == "" {
		paletteShortcut = "Ctrl+P"
	}

	searchInput := tview.NewInputField().
		SetLabel(" Search ").
		SetPlaceholder("commands, tables, columns, views, routines, or recent SQL...")
	searchInput.SetBorder(true).
		SetTitle(fmt.Sprintf(" Command & Object Palette [yellow](%s)[-] ", tview.Escape(paletteShortcut))).
		SetTitleColor(mauve).
		SetBorderColor(blue).
		SetBackgroundColor(bg)
	searchInput.SetFieldBackgroundColor(mantle).
		SetFieldTextColor(text).
		SetLabelColor(peach)

	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).
		SetTitle(" Matches ").
		SetTitleColor(mauve).
		SetBorderColor(surface1).
		SetBackgroundColor(bg)
	list.SetMainTextColor(text).
		SetSelectedBackgroundColor(surface0).
		SetSelectedTextColor(green)

	detail := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true).
		SetWordWrap(true).
		SetScrollable(true)
	detail.SetBorder(true).
		SetTitle(" Selected Item ").
		SetTitleColor(mauve).
		SetBorderColor(surface1).
		SetBackgroundColor(mantle)

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(" [yellow]Type[-] Search  │  [yellow]↑/↓[-] Navigate  │  [yellow]Enter[-] Open  │  [yellow]Esc[-] Close ")
	footer.SetBackgroundColor(crust)

	modalW, modalH := a.modalSize(68, 112, 15, 30)
	detailHeight := 6
	if modalH < 20 {
		detailHeight = 4
	}
	if modalH < 16 {
		detailHeight = 3
	}
	titleWidth := max(8, modalW-32)

	var matches []commandPaletteMatch
	updateDetail := func(index int) {
		if index < 0 || index >= len(matches) {
			detail.SetText(" [#6c7086]No matching command or object. Try a shorter search.[-]")
			return
		}
		item := matches[index].item
		shortcut := ""
		if item.shortcut != "" {
			shortcut = fmt.Sprintf("\n [#a6adc8]Shortcut:[-] [yellow]%s[-]", tview.Escape(item.shortcut))
		}
		detail.SetText(fmt.Sprintf(
			" [::b]%s  [#cdd6f4]%s[-][-]%s\n [#a6adc8]%s[-]",
			commandPaletteCategoryTag(item.kind),
			tview.Escape(item.title),
			shortcut,
			tview.Escape(item.description),
		))
		detail.ScrollToBeginning()
	}

	refreshMatches := func(query string) {
		matches = a.searchCommandPaletteItemsWithColumns(items, query, 80)
		list.Clear()
		list.SetTitle(fmt.Sprintf(" Matches (%d) ", len(matches)))
		if len(matches) == 0 {
			list.AddItem("  [#6c7086]No matches[-]", "", 0, nil)
			updateDetail(-1)
			return
		}
		for _, match := range matches {
			item := match.item
			displayTitle, positions := truncateCommandPaletteTitle(item.title, match.titlePositions, titleWidth)
			shortcut := ""
			if item.shortcut != "" {
				shortcut = "  [#a6adc8]" + tview.Escape(item.shortcut) + "[-]"
			}
			list.AddItem(fmt.Sprintf("  %s  %s%s",
				commandPaletteCategoryTag(item.kind),
				highlightCommandPaletteTitle(displayTitle, positions),
				shortcut,
			), "", 0, nil)
		}
		list.SetCurrentItem(0)
		updateDetail(0)
	}

	executeSelected := func() {
		index := list.GetCurrentItem()
		if index < 0 || index >= len(matches) {
			return
		}
		item := matches[index].item
		a.dismissCommandPalette(false)
		a.executeCommandPaletteItem(item)
	}

	moveSelection := func(delta int) {
		if len(matches) == 0 {
			return
		}
		index := clamp(list.GetCurrentItem()+delta, 0, len(matches)-1)
		list.SetCurrentItem(index)
		updateDetail(index)
	}

	searchInput.SetChangedFunc(refreshMatches)
	searchInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEscape:
			a.dismissCommandPalette(true)
			return nil
		case tcell.KeyUp:
			moveSelection(-1)
			return nil
		case tcell.KeyDown:
			moveSelection(1)
			return nil
		case tcell.KeyEnter:
			executeSelected()
			return nil
		}
		return event
	})
	list.SetChangedFunc(func(index int, _, _ string, _ rune) {
		updateDetail(index)
	})
	list.SetSelectedFunc(func(_ int, _, _ string, _ rune) {
		executeSelected()
	})
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.dismissCommandPalette(true)
			return nil
		}
		return event
	})

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(searchInput, 3, 0, true).
		AddItem(list, 0, 1, false).
		AddItem(detail, detailHeight, 0, false).
		AddItem(footer, 1, 0, false)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	refreshMatches("")
	a.pages.AddPage(pageCommandPalette, grid, true, true)
	a.app.SetFocus(searchInput)
}

func (a *App) dismissCommandPalette(restoreFocus bool) {
	if a == nil || a.pages == nil {
		return
	}
	returnPage := a.paletteReturnPage
	returnFocus := a.paletteReturnFocus
	a.paletteReturnPage = ""
	a.paletteReturnFocus = nil
	a.pages.RemovePage(pageCommandPalette)
	if !restoreFocus || a.app == nil {
		return
	}
	if returnPage != "" && a.pages.HasPage(returnPage) {
		a.pages.ShowPage(returnPage).SendToFront(returnPage)
	}
	if returnFocus != nil {
		a.app.SetFocus(returnFocus)
		return
	}
	if a.db != nil && a.tables != nil {
		a.setFocusWithColor(a.tables)
	}
}

func (a *App) buildCommandPaletteItems() []commandPaletteItem {
	connectionCount := 0
	if a.store != nil {
		connectionCount = len(a.store.Connections)
	}
	items := make([]commandPaletteItem, 0, len(commandPaletteActionSpecs)+connectionCount+len(a.tableIdentifiers)+len(a.databaseObjects)+commandPaletteQueryLimit)
	for index, spec := range commandPaletteActionSpecs {
		shortcut := spec.shortcut
		if shortcut == "" {
			shortcut = a.effectiveActionShortcut(spec.action)
		}
		items = append(items, commandPaletteItem{
			id:          "action:" + string(spec.action),
			kind:        commandPaletteAction,
			title:       spec.title,
			description: spec.description,
			keywords:    spec.keywords + " " + string(spec.action),
			shortcut:    shortcut,
			action:      spec.action,
			sortOrder:   index,
		})
	}
	if a.store != nil {
		for index, connection := range a.store.Connections {
			if strings.TrimSpace(connection.ID) == "" {
				continue
			}
			name := nonEmptyOr(strings.TrimSpace(connection.Name), string(connection.Type))
			items = append(items, commandPaletteItem{
				id:          "backup-connection:" + connection.ID,
				kind:        commandPaletteBackupJob,
				title:       "Schedule backup · " + name,
				description: fmt.Sprintf("Create a backup job with saved %s connection %s already selected. Safe defaults are ready to save or refine.", connection.TypeLabel(), name),
				keywords:    "saved connection schedule automatic backup destination retention",
				shortcut:    "Dashboard Ctrl+B",
				objectName:  connection.ID,
				sortOrder:   70 + index,
			})
		}
	}

	tableNames := make([]string, 0, len(a.tableIdentifiers))
	for _, name := range a.tableIdentifiers {
		if strings.TrimSpace(name) != "" {
			tableNames = append(tableNames, name)
		}
	}
	sort.Strings(tableNames)
	for index, name := range uniqueCommandPaletteStrings(tableNames) {
		items = append(items, commandPaletteItem{
			id:          "table:" + name,
			kind:        commandPaletteTable,
			title:       name,
			description: fmt.Sprintf("Open table %s and browse its rows in the Results panel.", name),
			keywords:    "database relation records rows data",
			objectName:  name,
			sortOrder:   100 + index,
		})
	}

	type paletteObject struct {
		kind    commandPaletteItemKind
		objType database.DBObjectType
		name    string
	}
	objects := make([]paletteObject, 0, len(a.databaseObjects))
	for _, object := range a.databaseObjects {
		kind, ok := commandPaletteKindForObjectType(object.objType)
		if !ok || strings.TrimSpace(object.name) == "" {
			continue
		}
		objects = append(objects, paletteObject{kind: kind, objType: object.objType, name: object.name})
	}
	sort.SliceStable(objects, func(i, j int) bool {
		if objects[i].kind == objects[j].kind {
			return objects[i].name < objects[j].name
		}
		return objects[i].kind < objects[j].kind
	})
	seenObjects := map[string]struct{}{}
	for index, object := range objects {
		id := string(object.kind) + ":" + object.name
		if _, seen := seenObjects[id]; seen {
			continue
		}
		seenObjects[id] = struct{}{}
		description := fmt.Sprintf("Inspect the read-only definition for %s %s.", object.kind, object.name)
		if object.kind == commandPaletteView {
			description = fmt.Sprintf("Open view %s and browse its rows in the Results panel.", object.name)
		}
		items = append(items, commandPaletteItem{
			id:          id,
			kind:        object.kind,
			title:       object.name,
			description: description,
			keywords:    "database object schema definition ddl metadata",
			objectName:  object.name,
			objectType:  object.objType,
			sortOrder:   200 + index,
		})
	}

	if a.historyMgr != nil {
		if connectionKey, ok := a.activeConnectionKey(); ok {
			entries := a.historyMgr.Entries(connectionKey)
			sort.SliceStable(entries, func(i, j int) bool {
				return entries[i].Timestamp.After(entries[j].Timestamp)
			})
			seenQueries := map[string]struct{}{}
			for _, entry := range entries {
				query := strings.TrimSpace(entry.SQL)
				if query == "" {
					continue
				}
				if _, seen := seenQueries[query]; seen {
					continue
				}
				seenQueries[query] = struct{}{}
				timestamp := "Recorded in query history"
				if !entry.Timestamp.IsZero() {
					timestamp = "Executed " + entry.Timestamp.Local().Format("2006-01-02 15:04:05")
				}
				items = append(items, commandPaletteItem{
					id:          fmt.Sprintf("query:%d", len(seenQueries)),
					kind:        commandPaletteQuery,
					title:       truncateForDisplay(compactSQL(query), 100),
					description: timestamp + ". Load this SQL into the Query editor:\n\n" + query,
					keywords:    "recent previous history sql statement",
					query:       query,
					sortOrder:   300 + len(seenQueries),
				})
				if len(seenQueries) >= commandPaletteQueryLimit {
					break
				}
			}
		}
	}

	return items
}

func (a *App) effectiveActionShortcut(action keymapAction) string {
	var bindings []string
	if a != nil && a.settings != nil {
		bindings = cleanBindings(a.settings.Keymap[string(action)])
	}
	if len(bindings) == 0 {
		bindings = cleanBindings(config.DefaultKeymapBindings()[string(action)])
	}
	if len(bindings) > 2 {
		bindings = bindings[:2]
	}
	formatted := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		formatted = append(formatted, formatCommandPaletteShortcut(binding))
	}
	return strings.Join(formatted, " / ")
}

func (a *App) commandPaletteShortcutHint() string {
	if shortcut := a.effectiveActionShortcut(actionCommandPalette); shortcut != "" {
		return shortcut
	}
	return "Ctrl+P"
}

func formatCommandPaletteShortcut(binding string) string {
	formatted := strings.ToUpper(strings.TrimSpace(binding))
	replacer := strings.NewReplacer(
		"CONTROL", "Ctrl",
		"CTRL", "Ctrl",
		"COMMAND", "Cmd",
		"CMD", "Cmd",
		"OPTION", "Alt",
		"ALT", "Alt",
		"SHIFT", "Shift",
	)
	return replacer.Replace(formatted)
}

func commandPaletteKindForObjectType(objectType database.DBObjectType) (commandPaletteItemKind, bool) {
	switch objectType {
	case database.ObjViews:
		return commandPaletteView, true
	case database.ObjFunctions:
		return commandPaletteFunction, true
	case database.ObjStoredProcedures:
		return commandPaletteProcedure, true
	case database.ObjTriggers:
		return commandPaletteTrigger, true
	default:
		return "", false
	}
}

func uniqueCommandPaletteStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	last := ""
	for _, value := range values {
		if len(result) > 0 && value == last {
			continue
		}
		result = append(result, value)
		last = value
	}
	return result
}

func searchCommandPaletteItems(items []commandPaletteItem, query string) []commandPaletteMatch {
	return searchCommandPaletteItemsLimit(items, query, 0)
}

func searchCommandPaletteItemsLimit(items []commandPaletteItem, query string, limit int) []commandPaletteMatch {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	capacity := len(items)
	if limit > 0 {
		capacity = min(capacity, limit)
	}
	matches := make([]commandPaletteMatch, 0, capacity)
	for sourceIndex, item := range items {
		match := commandPaletteMatch{item: item, sourceIndex: sourceIndex}
		if len(terms) == 0 {
			matches = appendLimitedCommandPaletteMatch(matches, match, limit)
			continue
		}

		fields := []struct {
			text   string
			weight int
		}{
			{text: item.title, weight: 0},
			{text: item.objectName, weight: 0},
			{text: item.keywords, weight: 80},
			{text: item.description, weight: 120},
			{text: item.shortcut, weight: 160},
			{text: string(item.kind), weight: 180},
		}

		matched := true
		for _, term := range terms {
			bestScore := int(^uint(0) >> 1)
			termMatched := false
			for _, field := range fields {
				score, ok := fuzzySubsequenceScore(field.text, term)
				if !ok {
					continue
				}
				termMatched = true
				if weightedScore := score + field.weight; weightedScore < bestScore {
					bestScore = weightedScore
				}
			}
			if !termMatched {
				matched = false
				break
			}
			match.score += bestScore
		}
		if !matched {
			continue
		}
		matches = appendLimitedCommandPaletteMatch(matches, match, limit)
	}

	sort.SliceStable(matches, func(i, j int) bool { return commandPaletteMatchLess(matches[i], matches[j]) })
	for index := range matches {
		positionSet := make(map[int]struct{})
		for _, term := range terms {
			if positions, _, ok := fuzzySubsequenceMatch(matches[index].item.title, term); ok {
				for _, position := range positions {
					positionSet[position] = struct{}{}
				}
			}
		}
		for position := range positionSet {
			matches[index].titlePositions = append(matches[index].titlePositions, position)
		}
		sort.Ints(matches[index].titlePositions)
	}
	return matches
}

func commandPaletteMatchLess(left, right commandPaletteMatch) bool {
	if left.score != right.score {
		return left.score < right.score
	}
	if left.item.sortOrder != right.item.sortOrder {
		return left.item.sortOrder < right.item.sortOrder
	}
	return left.sourceIndex < right.sourceIndex
}

func (a *App) searchCommandPaletteItemsWithColumns(items []commandPaletteItem, query string, limit int) []commandPaletteMatch {
	columnMatches := searchSidebarColumnPaletteWithLookup(a.sidebarSearchIndex, a.sidebarSearchLookup, query, limit)
	trimmedQuery := strings.TrimSpace(query)
	if len(columnMatches) > 0 && (strings.Contains(trimmedQuery, ".") || len(strings.Fields(trimmedQuery)) > 1) {
		return columnMatches
	}
	matches := searchCommandPaletteItemsLimit(items, query, limit)
	matches = append(matches, columnMatches...)
	sort.SliceStable(matches, func(i, j int) bool { return commandPaletteMatchLess(matches[i], matches[j]) })
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func searchSidebarColumnPalette(entries []sidebarSearchEntry, query string, limit int) []commandPaletteMatch {
	return searchSidebarColumnPaletteWithLookup(entries, sidebarSearchLookup{}, query, limit)
}

func searchSidebarColumnPaletteWithLookup(entries []sidebarSearchEntry, lookup sidebarSearchLookup, query string, limit int) []commandPaletteMatch {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(terms) == 0 || limit == 0 {
		return nil
	}
	if candidates := exactSidebarSearchCandidates(lookup, terms); len(candidates) > 0 {
		if matches := searchSidebarColumnPaletteMode(entries, candidates, terms, limit, false); len(matches) > 0 {
			return matches
		}
		if matches := searchSidebarColumnPaletteMode(entries, candidates, terms, limit, true); len(matches) > 0 {
			return matches
		}
	}
	matches := searchSidebarColumnPaletteMode(entries, nil, terms, limit, false)
	if len(matches) == 0 {
		matches = searchSidebarColumnPaletteMode(entries, nil, terms, limit, true)
	}
	return matches
}

func searchSidebarColumnPaletteMode(entries []sidebarSearchEntry, candidates []int, terms []string, limit int, allowFuzzy bool) []commandPaletteMatch {
	matches := make([]commandPaletteMatch, 0, min(limit, 32))
	visit := func(sourceIndex int, entry sidebarSearchEntry) {
		if entry.column == "" {
			return
		}
		score := 0
		matched := true
		for _, term := range terms {
			termScore, ok := sidebarColumnPaletteTermScore(entry, term, allowFuzzy)
			if !ok {
				matched = false
				break
			}
			score += termScore
		}
		if !matched {
			return
		}
		title := entry.table + "." + entry.column
		match := commandPaletteMatch{
			item: commandPaletteItem{
				id: "column:" + title, kind: commandPaletteColumn, title: title,
				description: fmt.Sprintf("Open table %s and select column %s in the Results header.", entry.table, entry.column),
				keywords:    "field schema table column", objectName: entry.table, columnName: entry.column,
				sortOrder: 150 + entry.order,
			},
			score: score, sourceIndex: sourceIndex + 1_000_000,
		}
		matches = appendLimitedCommandPaletteMatch(matches, match, limit)
	}
	if len(candidates) > 0 {
		for _, sourceIndex := range candidates {
			if sourceIndex >= 0 && sourceIndex < len(entries) {
				visit(sourceIndex, entries[sourceIndex])
			}
		}
	} else {
		for sourceIndex, entry := range entries {
			visit(sourceIndex, entry)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return commandPaletteMatchLess(matches[i], matches[j]) })
	for index := range matches {
		positionSet := make(map[int]struct{})
		for _, term := range terms {
			if positions, _, ok := fuzzySubsequenceMatch(matches[index].item.title, term); ok {
				for _, position := range positions {
					positionSet[position] = struct{}{}
				}
			}
		}
		for position := range positionSet {
			matches[index].titlePositions = append(matches[index].titlePositions, position)
		}
		sort.Ints(matches[index].titlePositions)
	}
	return matches
}

func sidebarColumnPaletteTermScore(entry sidebarSearchEntry, term string, allowFuzzy bool) (int, bool) {
	if strings.Contains(term, ".") {
		return sidebarSearchEntryScore(entry, term, allowFuzzy)
	}
	best := int(^uint(0) >> 1)
	if strings.Contains(entry.columnFolded, term) {
		best = sidebarTextMatchScore(entry.columnFolded, term)
	} else if allowFuzzy {
		if score, ok := foldedSubsequenceScore(entry.columnFolded, term); ok {
			best = 80 + score
		}
	}
	if strings.Contains(entry.tableFolded, term) {
		best = min(best, 50+sidebarTextMatchScore(entry.tableFolded, term))
	} else if allowFuzzy {
		if score, ok := foldedSubsequenceScore(entry.tableFolded, term); ok {
			best = min(best, 130+score)
		}
	}
	return best, best != int(^uint(0)>>1)
}

func appendLimitedCommandPaletteMatch(matches []commandPaletteMatch, match commandPaletteMatch, limit int) []commandPaletteMatch {
	if limit <= 0 || len(matches) < limit {
		return append(matches, match)
	}
	worst := 0
	for index := 1; index < len(matches); index++ {
		if commandPaletteMatchLess(matches[worst], matches[index]) {
			worst = index
		}
	}
	if !commandPaletteMatchLess(match, matches[worst]) {
		return matches
	}
	matches[worst] = match
	return matches
}

func fuzzySubsequenceScore(candidate, query string) (int, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return 0, true
	}
	if !isASCIIString(candidate) || !isASCIIString(query) {
		_, score, ok := fuzzySubsequenceMatch(candidate, query)
		return score, ok
	}
	if len(candidate) == 0 || len(query) > len(candidate) {
		return 0, false
	}

	queryIndex := 0
	start := -1
	previous := -2
	boundaryBonus := 0
	contiguousBonus := 0
	last := -1
	for candidateIndex := 0; candidateIndex < len(candidate) && queryIndex < len(query); candidateIndex++ {
		if asciiLower(candidate[candidateIndex]) != asciiLower(query[queryIndex]) {
			continue
		}
		if start < 0 {
			start = candidateIndex
		}
		if candidateIndex == previous+1 {
			contiguousBonus += 3
		}
		if candidateIndex == 0 || isCommandPaletteBoundary(rune(candidate[candidateIndex-1])) {
			boundaryBonus += 4
		}
		previous = candidateIndex
		last = candidateIndex
		queryIndex++
	}
	if queryIndex != len(query) {
		return 0, false
	}
	span := last - start + 1
	gaps := span - len(query)
	score := start*6 + gaps*5 + len(candidate) - len(query) - contiguousBonus - boundaryBonus
	if asciiEqualFold(candidate, query) {
		score -= 200
	} else if asciiHasPrefixFold(candidate, query) {
		score -= 100
	} else if asciiIndexFold(candidate, query) >= 0 {
		score -= 50
	}
	return score, true
}

func isASCIIString(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func asciiLower(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func asciiEqualFold(left, right string) bool {
	return len(left) == len(right) && asciiHasPrefixFold(left, right)
}

func asciiHasPrefixFold(candidate, prefix string) bool {
	if len(prefix) > len(candidate) {
		return false
	}
	for index := range len(prefix) {
		if asciiLower(candidate[index]) != asciiLower(prefix[index]) {
			return false
		}
	}
	return true
}

func asciiIndexFold(candidate, query string) int {
	if query == "" {
		return 0
	}
	for start := 0; start+len(query) <= len(candidate); start++ {
		if asciiHasPrefixFold(candidate[start:], query) {
			return start
		}
	}
	return -1
}

func fuzzySubsequenceMatch(candidate, query string) ([]int, int, bool) {
	candidateRunes := []rune(candidate)
	queryRunes := []rune(strings.TrimSpace(query))
	if len(queryRunes) == 0 {
		return nil, 0, true
	}
	if len(candidateRunes) == 0 || len(queryRunes) > len(candidateRunes) {
		return nil, 0, false
	}

	positions := make([]int, 0, len(queryRunes))
	queryIndex := 0
	for candidateIndex, candidateRune := range candidateRunes {
		if unicode.ToLower(candidateRune) != unicode.ToLower(queryRunes[queryIndex]) {
			continue
		}
		positions = append(positions, candidateIndex)
		queryIndex++
		if queryIndex == len(queryRunes) {
			break
		}
	}
	if queryIndex != len(queryRunes) {
		return nil, 0, false
	}

	start := positions[0]
	span := positions[len(positions)-1] - start + 1
	gaps := span - len(positions)
	score := start*6 + gaps*5 + len(candidateRunes) - len(queryRunes)
	for index, position := range positions {
		if index > 0 && position == positions[index-1]+1 {
			score -= 3
		}
		if position == 0 || isCommandPaletteBoundary(candidateRunes[position-1]) {
			score -= 4
		}
	}
	lowerCandidate := strings.ToLower(candidate)
	lowerQuery := strings.ToLower(string(queryRunes))
	if lowerCandidate == lowerQuery {
		score -= 200
	} else if strings.HasPrefix(lowerCandidate, lowerQuery) {
		score -= 100
	} else if strings.Contains(lowerCandidate, lowerQuery) {
		score -= 50
	}
	return positions, score, true
}

func isCommandPaletteBoundary(r rune) bool {
	return unicode.IsSpace(r) || r == '_' || r == '-' || r == '.' || r == '/' || r == ':'
}

func highlightCommandPaletteTitle(title string, positions []int) string {
	if len(positions) == 0 {
		return tview.Escape(title)
	}
	positionSet := make(map[int]struct{}, len(positions))
	for _, position := range positions {
		positionSet[position] = struct{}{}
	}

	var output, segment strings.Builder
	segmentHighlighted := false
	haveSegment := false
	flush := func() {
		if segment.Len() == 0 {
			return
		}
		if segmentHighlighted {
			output.WriteString("[black:#f9e2af:b]")
		}
		output.WriteString(tview.Escape(segment.String()))
		if segmentHighlighted {
			output.WriteString("[-:-:-]")
		}
		segment.Reset()
	}
	for index, r := range []rune(title) {
		_, shouldHighlight := positionSet[index]
		if haveSegment && shouldHighlight != segmentHighlighted {
			flush()
		}
		segmentHighlighted = shouldHighlight
		haveSegment = true
		segment.WriteRune(r)
	}
	flush()
	return output.String()
}

func truncateCommandPaletteTitle(title string, positions []int, maxRunes int) (string, []int) {
	runes := []rune(title)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return title, positions
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes]), commandPalettePositionsBefore(positions, maxRunes)
	}
	visible := maxRunes - 3
	return string(runes[:visible]) + "...", commandPalettePositionsBefore(positions, visible)
}

func commandPalettePositionsBefore(positions []int, limit int) []int {
	filtered := make([]int, 0, len(positions))
	for _, position := range positions {
		if position >= 0 && position < limit {
			filtered = append(filtered, position)
		}
	}
	return filtered
}

func commandPaletteCategoryTag(kind commandPaletteItemKind) string {
	switch kind {
	case commandPaletteAction:
		return "[#cba6f7]ACTION[-]"
	case commandPaletteTable:
		return "[#89b4fa]TABLE[-]"
	case commandPaletteColumn:
		return "[#74c7ec]COLUMN[-]"
	case commandPaletteView:
		return "[#94e2d5]VIEW[-]"
	case commandPaletteFunction:
		return "[#a6e3a1]FUNCTION[-]"
	case commandPaletteProcedure:
		return "[#f9e2af]PROCEDURE[-]"
	case commandPaletteTrigger:
		return "[#ffb496]TRIGGER[-]"
	case commandPaletteQuery:
		return "[#a6adc8]RECENT SQL[-]"
	case commandPaletteBackupJob:
		return "[#a6e3a1]BACKUP[-]"
	default:
		return "[#a6adc8]ITEM[-]"
	}
}

func (a *App) executeCommandPaletteItem(item commandPaletteItem) {
	switch item.kind {
	case commandPaletteAction:
		a.executeCommandPaletteAction(item.action, item.title)
	case commandPaletteTable:
		a.openCommandPaletteTable(item.objectName)
	case commandPaletteColumn:
		a.openCommandPaletteColumn(item.objectName, item.columnName)
	case commandPaletteView, commandPaletteFunction, commandPaletteProcedure, commandPaletteTrigger:
		a.openCommandPaletteDatabaseObject(item.objectType, item.objectName)
	case commandPaletteQuery:
		a.loadCommandPaletteQuery(item.query)
	case commandPaletteBackupJob:
		a.showBackupCenter()
		if a.pages.HasPage(pageBackupCenter) {
			a.showBackupJobFormForConnection(nil, item.objectName)
		}
	}
}

func (a *App) executeCommandPaletteAction(action keymapAction, title string) {
	if commandPaletteActionNeedsConnection(action) && !a.commandPaletteRequireConnection(title) {
		return
	}

	switch action {
	case actionFocusTables:
		a.showCommandPaletteWorkspace(a.tables)
	case actionFocusQuery:
		a.showCommandPaletteWorkspace(a.queryInput)
	case actionFocusResults:
		a.showCommandPaletteWorkspace(a.results)
	case actionDashboard:
		a.pages.HidePage("main")
		a.pages.RemovePage("help")
		a.showDashboard()
	case actionHelp:
		a.showHelp()
	case actionServices:
		a.showServiceDashboard()
	case actionBackupCenter:
		a.showBackupCenter()
	case actionChangeProfiler:
		a.showChangeProfiler()
	case actionFullscreen:
		a.pages.SwitchToPage("main")
		a.toggleExpandResults()
	case actionBackup:
		a.pages.SwitchToPage("main")
		a.showBackupModal()
	case actionExportCSV:
		a.pages.SwitchToPage("main")
		a.exportCurrentResultsToCSV()
	case actionHistory:
		a.pages.SwitchToPage("main")
		a.showHistoryModal()
	case actionSettings:
		a.showSettings()
	case paletteActionUpdates:
		a.showUpdates()
	case actionImportDump:
		a.pages.SwitchToPage("main")
		a.showImportModal()
	case actionInspectSchema:
		a.pages.SwitchToPage("main")
		a.showSelectedTableMetadata()
	case actionSelectAll:
		a.pages.SwitchToPage("main")
		a.selectAllResultRows()
		a.setFocusWithColor(a.results)
	case actionClearSelection:
		a.pages.SwitchToPage("main")
		a.clearResultRowSelection()
		a.setFocusWithColor(a.results)
	case paletteActionRunQuery:
		a.pages.SwitchToPage("main")
		query := strings.TrimSpace(a.queryInput.GetText())
		if query == "" {
			a.ShowAlert(fmt.Sprintf("%s No query to execute.\n\nType SQL in the Query panel first.", iconInfo), "main")
			return
		}
		a.ExecuteQuery(query)
	case paletteActionSQLSuggestions:
		a.showCommandPaletteWorkspace(a.queryInput)
		a.refreshSQLCompletions(true)
	case paletteActionRefreshTable:
		a.showCommandPaletteWorkspace(a.results)
		a.refreshCurrentTableAsync()
	case paletteActionRefreshDatabase:
		a.pages.SwitchToPage("main")
		a.refreshDataAsync()
	case paletteActionToggleTablePin:
		a.showCommandPaletteWorkspace(a.tables)
		a.toggleSelectedTablePin()
	case paletteActionCopyTableName:
		a.showCommandPaletteWorkspace(a.tables)
		a.copySelectedTableName()
	case paletteActionFindResultColumn:
		a.showCommandPaletteWorkspace(a.results)
		a.focusResultColumnHeader()
	case paletteActionCopyColumnName:
		a.showCommandPaletteWorkspace(a.results)
		a.copySelectedResultColumnName()
	case paletteActionFilterColumn:
		a.showCommandPaletteWorkspace(a.results)
		a.showResultFilterModal()
	case paletteActionFilterClipboard:
		a.showCommandPaletteWorkspace(a.results)
		a.filterSelectedResultColumnByClipboard()
	case paletteActionClearFilters:
		a.showCommandPaletteWorkspace(a.results)
		a.clearResultFilterAndReload()
	case paletteActionCopyCell:
		a.showCommandPaletteWorkspace(a.results)
		a.copyCurrentResultCell()
	case paletteActionExploreRelationships:
		a.showCommandPaletteWorkspace(a.results)
		a.exploreSelectedRelationships()
	case paletteActionSortColumn:
		a.showCommandPaletteWorkspace(a.results)
		_, column := a.results.GetSelection()
		a.toggleSort(column)
	case paletteActionOpenRowDetail:
		a.showCommandPaletteWorkspace(a.results)
		row, _ := a.results.GetSelection()
		if row <= 0 {
			a.flashStatus("[yellow]Select a data row to inspect[-]", a.currentResultRowCount(), 1600*time.Millisecond)
			return
		}
		a.showRowDetail(row)
	case paletteActionNextPage:
		a.showCommandPaletteWorkspace(a.results)
		a.nextPage()
	case paletteActionPreviousPage:
		a.showCommandPaletteWorkspace(a.results)
		a.prevPage()
	case paletteActionFirstPage:
		a.showCommandPaletteWorkspace(a.results)
		a.firstPage()
	case paletteActionLastPage:
		a.showCommandPaletteWorkspace(a.results)
		a.lastPage()
	}
}

func commandPaletteActionNeedsConnection(action keymapAction) bool {
	switch action {
	case actionFocusTables, actionFocusQuery, actionFocusResults, actionFullscreen,
		actionBackup, actionExportCSV, actionHistory, actionImportDump,
		actionInspectSchema, actionSelectAll, actionClearSelection,
		paletteActionRunQuery, paletteActionSQLSuggestions, paletteActionRefreshTable, paletteActionRefreshDatabase,
		paletteActionToggleTablePin, paletteActionCopyTableName,
		paletteActionFindResultColumn, paletteActionCopyColumnName,
		paletteActionFilterColumn, paletteActionFilterClipboard, paletteActionClearFilters,
		paletteActionCopyCell, paletteActionExploreRelationships, paletteActionSortColumn,
		paletteActionOpenRowDetail, paletteActionNextPage, paletteActionPreviousPage,
		paletteActionFirstPage, paletteActionLastPage:
		return true
	default:
		return false
	}
}

func (a *App) commandPaletteRequireConnection(actionTitle string) bool {
	if a != nil && a.db != nil {
		return true
	}
	returnPage, _ := a.pages.GetFrontPage()
	if returnPage == "" {
		a.showDashboard()
		returnPage = "dashboard"
	}
	a.ShowAlert(fmt.Sprintf("%s %s requires an active database connection.\n\nConnect from the Dashboard and try again.", iconInfo, actionTitle), returnPage)
	return false
}

func (a *App) showCommandPaletteWorkspace(target tview.Primitive) {
	if a == nil || target == nil {
		return
	}
	a.pages.SwitchToPage("main")
	a.setFocusWithColor(target)
}

func (a *App) openCommandPaletteTable(tableName string) {
	if strings.TrimSpace(tableName) == "" || !a.commandPaletteRequireConnection("Opening a table") {
		return
	}
	a.pages.SwitchToPage("main")
	for index, identifier := range a.tableIdentifiers {
		if identifier == tableName {
			a.tables.SetCurrentItem(index)
			break
		}
	}
	a.setFocusWithColor(a.tables)
	a.openSidebarTable(tableName, nil)
}

func (a *App) openCommandPaletteColumn(tableName, columnName string) {
	if strings.TrimSpace(tableName) == "" || strings.TrimSpace(columnName) == "" || !a.commandPaletteRequireConnection("Finding a column") {
		return
	}
	a.pages.SwitchToPage("main")
	a.revealSidebarColumn(tableName, columnName)
	a.flashStatus(fmt.Sprintf("[teal]Opening %s.%s…[-]", tview.Escape(tableName), tview.Escape(columnName)), a.currentResultRowCount(), 1800*time.Millisecond)
	a.openSidebarColumn(sidebarColumnRef{table: tableName, column: columnName})
}

func (a *App) openCommandPaletteDatabaseObject(objectType database.DBObjectType, objectName string) {
	if strings.TrimSpace(objectName) == "" || !a.commandPaletteRequireConnection("Opening a database object") {
		return
	}
	a.pages.SwitchToPage("main")
	a.clearResultNavigation()
	for index, object := range a.databaseObjects {
		if object.objType == objectType && object.name == objectName {
			a.tables.SetCurrentItem(index)
			break
		}
	}
	a.setFocusWithColor(a.tables)
	a.onDatabaseObjectSelected(objectType, objectName)
}

func (a *App) loadCommandPaletteQuery(query string) {
	if strings.TrimSpace(query) == "" || !a.commandPaletteRequireConnection("Loading a recent query") {
		return
	}
	a.pages.SwitchToPage("main")
	a.queryInput.SetText(query, true)
	a.setFocusWithColor(a.queryInput)
	a.flashStatus(fmt.Sprintf("[green]%s Recent query loaded[-]", iconSuccess), a.currentResultRowCount(), 1400*time.Millisecond)
}
