package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func keyboardHelpText() string {
	return keyboardHelpTextFor(nil)
}

func keyboardHelpTextFor(a *App) string {
	template := `[::b][#cba6f7]━━━ ` + iconHelp + ` dbterm Guide & SQL Reference ━━━[-][-]

[#f9e2af]START HERE — COMMON WORKFLOWS[-]
  [#89b4fa]Find a table[-]       [yellow]{{focus_tables}}[-] → type its name → [yellow]Enter[-]
  [#89b4fa]Find a column[-]      In Results press [yellow]↑[-] from the first row → type its name → [yellow]↓/Enter[-]
  [#89b4fa]Cross-table lookup[-] Select a cell → [yellow]C[-] → open another table/column → [yellow]V[-]
  [#89b4fa]Filter a column[-]    Select column → [yellow]/[-] → choose operator/value → [yellow]Enter[-]; Add AND composes
  [#89b4fa]Follow related rows[-] Select a key cell → [yellow]F[-] → choose [#a6e3a1]→ parent[-] or [#89b4fa]← children[-]; repeat for a chain
  [#89b4fa]Find the same value[-] In Related Data press [yellow]V[-] to check same-named columns across tables
  [#89b4fa]Clear a filter[-]     Press [yellow]Esc[-] once to clear and reset position; press again for Dashboard
  [#89b4fa]Resize results[-]     [yellow]+ / -[-] selected column  │  [yellow]Ctrl++ / Ctrl+-[-] all columns  │  [yellow]Alt++ / Alt+-[-] rows
  [#89b4fa]Track DB changes[-]   [yellow]{{change_profiler}}[-] → [yellow]N[-] name anchor → make changes → [yellow]S[-] scan or [yellow]F[-] finish

[#a6e3a1]SHORTCUT CONVENTIONS[-]
  [yellow]Enter[-]            Open or run the focused item     [yellow]Esc[-] Back, close, or safely cancel
  [yellow]↑/↓ and Tab[-]      Navigate lists / move through form fields; [yellow]Shift+Tab[-] reverses panel focus
  [yellow]C[-]                Copy in views that offer copying; the focused page's footer names the exact target
  [yellow]Plain letters[-]    Act only on the focused page     [yellow]Alt/Ctrl keys[-] Global actions from Settings
  [yellow]{{command_palette}}[-] Search global/workspace actions; local modal actions remain visible in their footer or title

[#a6e3a1]TABLES[-]
  [#6c7086]Markers[-]          [#a6e3a1]▶[-] currently shown  [#6c7086]•[-] opened this connection  [#cba6f7]/[-] remembered filter  [#f9e2af]` + iconPin + `[-] pinned
  [yellow]→ / ←[-]            Expand or enter children / return to parent or collapse it; one table stays expanded
  [yellow]Click ▸ / ▾[-]      Mouse-expand or collapse without opening table rows
  [#6c7086]Column badges[-]    [#f9e2af]PK[-] primary key  [#cba6f7]FK[-] foreign key  [#89b4fa]NN[-] not null; types load lazily without blocking input
  [yellow]Space[-]            Pin/unpin the selected table at the top (saved per database connection)
  [yellow]Type[-]             Search tables, views, and other objects; [yellow]↑/↓[-] moves through matches
  [yellow]Drag right border[-] Resize the Tables sidebar on wide layouts
  [yellow]Backspace[-]        Edit the table search
  [yellow]Enter[-]            Open a table, or open a child column directly in the Results header
  [yellow]Shift+C / Right-click[-] Copy the selected table or column name (lowercase letters remain type-to-find)
  [yellow]Esc[-]              Clear an active search; press again for Dashboard
  [yellow]{{inspect_schema}}[-]            Inspect the selected table schema

[#a6e3a1]RESULTS — CELLS & FILTERS[-]
  [yellow]↑ from first row[-] Enter the selectable column-header row without losing the current column
  [yellow]Type (headers)[-]   Jump to and highlight the first matching column; Backspace edits, Esc clears
  [yellow]←/→, ↓/Enter[-]     Move across headers / return to the same column's data
  [yellow]Shift+C (headers)[-] Copy the complete selected column name
  [yellow]Tab / Shift+Tab[-]  Hop between column search and table search while both retain their position/text
  [yellow]C[-]                Copy only the selected cell (full value, even if preview is shortened)
  [yellow]V[-]                Apply/update equality from the clipboard (real NULL uses IS NULL)
  [yellow]/[-]                Open filters; Enter applies, Add AND composes, Tab / Shift+Tab moves; remembered per table
  [yellow]F[-]                Explore declared relationships in both directions; Enter opens related rows
  [yellow]V (Related Data)[-] Find the exact value in same-named columns across tables
  [yellow]Backspace[-]        Return one step through a Person → Visit → Payment-style chain
  [yellow]Esc[-]              Clear filters/reset position first; press again for Dashboard
  [yellow]Enter[-]            Open row details; C copies the selected detail cell
  [yellow]Space[-]            Toggle current row selection
  [yellow]{{select_all}} / {{clear_selection}}[-]    Select all / clear selected rows
  [yellow]{{export_csv}}[-]            Export selected, current-page, or all matching rows to CSV

[#a6e3a1]RESULTS — SIZE, SORT & PAGES[-]
  [yellow]+ / -[-]            Widen / narrow selected column (remembered per table)
  [yellow]Ctrl++ / Ctrl+-[-]  Widen / narrow all columns (remembered per table)
  [yellow]> / <[-]            Same all-column resize (fallback when a terminal drops Ctrl)
  [yellow]0 / Ctrl+0[-]       Reset this table's column widths
  [#6c7086]Keyboard note[-]    The + character is already Shift+=, so Shift+plus is not a separate terminal key
  [yellow]S[-]                Sort by the selected column
  [yellow]PgDn / ][-]         Next page        [yellow]PgUp / Left bracket[-] Previous page
  [yellow]Home / End[-]       First / last page
  [yellow]Alt++ / Alt+-[-]    Increase / decrease preview rows per page
  [yellow]Alt+0[-]            Toggle preview limit between 100 and safe maximum
  [yellow]F5 / Ctrl+F5[-]     Refresh current table / refresh tables and current data
  [yellow]{{fullscreen}}[-]            Toggle fullscreen results

[#a6e3a1]QUERY[-]
  [yellow]Enter[-]            Execute SQL             [yellow]Shift+Enter[-] Insert newline
  [yellow]Ctrl+Space[-]        Smart local suggestions plus ready read-only queries for the selected table
  [yellow]↑ / ↓, Tab/Enter[-]  Choose / insert; context ranks typo fixes, tables, columns, clauses, functions, and routines
  [yellow]Esc[-]               Close suggestions without leaving Query; Enter runs when suggestions are closed
  [yellow]{{history}}[-]            Query history
  [yellow]{{import_dump}}[-]            Import SQL dump          [yellow]Esc[-] Cancel a running import

[#a6e3a1]NAVIGATION & APP[-]
  [yellow]{{command_palette}}[-] Search documented actions, tables, collapsed columns, database objects, and recent queries
  [yellow]{{focus_tables}} / {{focus_query}} / {{focus_results}}[-]    Focus Tables / Query / Results
  [yellow]Tab / Shift+Tab[-]  Cycle Tables → Query → Results forward / backward
  [yellow]{{backup}}[-]            Instant backup from any workspace panel
  [yellow]{{dashboard}}[-]            Dashboard                [yellow]{{backup_center}}[-] Backup Center
  [yellow]{{change_profiler}}[-]            Change Profiler: named before/after anchors and saved reports
  [yellow]{{services}}[-]            Database services
  [yellow]{{settings}}[-]    Settings                 [yellow]{{help}}[-] This guide
  [yellow]Esc[-]              Back/close/cancel        [yellow]Backspace[-] Back in supported lists/results, edit in fields
  [yellow]Ctrl+C[-]           Cancel active work / quit

[#a6e3a1]DASHBOARD ` + iconDashboard + `[-]
  [yellow]Enter[-] Connect/default DB   [yellow]A[-] All DBs on selected server   [yellow]N[-] New   [yellow]E[-] Edit   [yellow]D[-] Delete
  [yellow]R[-] Health check
  [yellow]Ctrl+B[-] New backup job for highlighted connection   [yellow]B[-] Backup Center   [yellow]I[-] Import
  [yellow]G[-] Settings   [yellow]H[-] Guide   [yellow]W / Esc[-] Workspace   [yellow]Q[-] Quit
  [yellow]1–9 / 0[-] Quick-select the first ten connections

[#a6e3a1]BACKUP CENTER ({{backup_center}}) ` + iconBackup + `[-]
  [yellow]N[-]               Choose saved/new database for a new plan
  [yellow]Enter[-]           Open actions for the highlighted plan
  [yellow]C[-]               Open independent local, pinned-SFTP, and rclone-pull copy jobs
  [yellow]R / Space[-]       Run now / toggle a timed schedule (manual stays on demand)
  [yellow]P[-]               Apply retention now; newest verified artifact is always kept
  [yellow]H / I[-]           Run history / inspect and restore a backup by content
  [yellow]A[-]               Desktop/user or Server/system agent: status, startup, PID/RAM/uptime, controls, and logs
  [yellow]G[-]               Generate an age identity and copy its public recipient
  [yellow]D[-]               Delete the job only; history and backup files stay untouched
  [yellow]F2 / F3[-]         Choose folder / refresh destination + staging capacity
  [yellow]Ctrl+N[-]          Add a database from inside the plan form
  [#6c7086]Filename tokens[-] {job} {connection} {database} {engine} {date} {time} {timestamp} {run}

[#a6e3a1]CHANGE PROFILER ({{change_profiler}})[-]
  [yellow]N[-]               Create a named anchor on the connected database; risky/keyless tables require opt-in
  [yellow]Space / A[-]       Toggle one table / explicitly include or exclude the whole database
  [yellow]S / F[-]           Scan without stopping / run the final scan and finish the anchor
  [yellow]Enter[-]           Inspect changed tables, rows, columns, and complete before/after values
  [yellow]E / D[-]           Rename / permanently delete the local anchor report
  [#6c7086]Large databases[-] Loaders show phase, table, rows, bytes, rate, percent, and ETA; baselines are compressed locally
  [#6c7086]Attribution[-]     Observed connection and dbterm writes are evidence; writer remains Unknown without an audit trail

[#a6e3a1]SERVICES ({{services}}) ` + iconServices + `[-]
  [yellow]1 / 2[-] Toggle MySQL / PostgreSQL    [yellow]C / Enter[-] Connect
  [yellow]Database optional[-] Leave it blank to browse every database visible to the selected DB login
  [yellow]R[-] Refresh service info             [yellow]Esc[-] Go back

[#a6e3a1]CLI (run in your terminal)[-]
  dbterm --help        Command help       dbterm --version     Version/build
  dbterm --info        Runtime info       dbterm --update      Install an update
  dbterm --uninstall   Remove dbterm      add --purge to remove dbterm-owned data
  dbterm backup --help Jobs, instant backup, agent, inspection, restore, keys, and paths


`
	shortcut := func(action keymapAction) string {
		return tview.Escape(a.effectiveActionShortcut(action))
	}
	return strings.NewReplacer(
		"{{focus_tables}}", shortcut(actionFocusTables),
		"{{focus_query}}", shortcut(actionFocusQuery),
		"{{focus_results}}", shortcut(actionFocusResults),
		"{{dashboard}}", shortcut(actionDashboard),
		"{{help}}", shortcut(actionHelp),
		"{{services}}", shortcut(actionServices),
		"{{fullscreen}}", shortcut(actionFullscreen),
		"{{backup}}", shortcut(actionBackup),
		"{{backup_center}}", shortcut(actionBackupCenter),
		"{{change_profiler}}", shortcut(actionChangeProfiler),
		"{{export_csv}}", shortcut(actionExportCSV),
		"{{history}}", shortcut(actionHistory),
		"{{settings}}", shortcut(actionSettings),
		"{{import_dump}}", shortcut(actionImportDump),
		"{{inspect_schema}}", shortcut(actionInspectSchema),
		"{{select_all}}", shortcut(actionSelectAll),
		"{{clear_selection}}", shortcut(actionClearSelection),
		"{{command_palette}}", shortcut(actionCommandPalette),
	).Replace(template)
}

func (a *App) showHelp() {
	a.helpReturnPage, _ = a.pages.GetFrontPage()
	a.helpReturnFocus = a.app.GetFocus()
	helpText := keyboardHelpTextFor(a)

	cheatPG := `[::b][#89b4fa]━━━ PostgreSQL Cheatsheet ━━━[-][-]

[#a6e3a1]Inspect Schema[-]
  SELECT table_name FROM information_schema.tables
    WHERE table_schema = 'public';
  SELECT column_name, data_type, is_nullable
    FROM information_schema.columns WHERE table_name = 'TABLE';
  SELECT indexname, indexdef FROM pg_indexes
    WHERE tablename = 'TABLE';

[#a6e3a1]Server Info[-]
  SELECT version();
  SELECT current_database();
  SELECT current_user;
  SELECT pg_size_pretty(pg_database_size(current_database()));

[#a6e3a1]Common Operations[-]
  SELECT * FROM table LIMIT 100;
  SELECT COUNT(*) FROM table;
  INSERT INTO t (c1, c2) VALUES ('v1', 'v2');
  UPDATE t SET c1 = 'new' WHERE id = 1;
  DELETE FROM t WHERE id = 1;

[#a6e3a1]Performance[-]
  EXPLAIN ANALYZE SELECT ...;
  SELECT pg_size_pretty(pg_total_relation_size('table'));
  SELECT * FROM pg_stat_activity;


`

	cheatMySQL := `[::b][#f9e2af]━━━ MySQL Cheatsheet ━━━[-][-]

[#a6e3a1]Inspect Schema[-]
  SHOW TABLES;
  DESCRIBE table_name;
  SHOW CREATE TABLE table_name;
  SHOW INDEX FROM table_name;

[#a6e3a1]Server Info[-]
  SELECT VERSION();
  SELECT DATABASE();
  SELECT USER();
  SHOW DATABASES;
  SELECT table_name, engine, table_rows
    FROM information_schema.tables WHERE table_schema = DATABASE();

[#a6e3a1]Common Operations[-]
  SELECT * FROM table LIMIT 100;
  SELECT COUNT(*) FROM table;
  INSERT INTO t (c1, c2) VALUES ('v1', 'v2');
  UPDATE t SET c1 = 'new' WHERE id = 1;
  DELETE FROM t WHERE id = 1;

[#a6e3a1]Performance[-]
  EXPLAIN SELECT ...;
  SHOW TABLE STATUS;
  SHOW PROCESSLIST;


`

	cheatSQLite := `[::b][#a6e3a1]━━━ SQLite Cheatsheet ━━━[-][-]

[#a6e3a1]Inspect Schema[-]
  SELECT name FROM sqlite_master WHERE type='table';
  PRAGMA table_info(table_name);
  SELECT sql FROM sqlite_master WHERE name = 'TABLE';

[#a6e3a1]Database Info[-]
  SELECT sqlite_version();
  PRAGMA database_list;
  PRAGMA page_count;
  PRAGMA page_size;
  PRAGMA integrity_check;

[#a6e3a1]Common Operations[-]
  SELECT * FROM table LIMIT 100;
  SELECT COUNT(*) FROM table;
  INSERT INTO t (c1, c2) VALUES ('v1', 'v2');
  UPDATE t SET c1 = 'new' WHERE id = 1;
  DELETE FROM t WHERE id = 1;

[#a6e3a1]Performance[-]
  EXPLAIN QUERY PLAN SELECT ...;
  PRAGMA optimize;
  ANALYZE;

`

	cheatTurso := `[::b][#a6e3a1]━━━ Turso (LibSQL) Cheatsheet ━━━[-][-]

[#a6e3a1]Inspect Schema[-]
  SELECT name FROM sqlite_master WHERE type='table';
  PRAGMA table_info(table_name);
  SELECT sql FROM sqlite_master WHERE name = 'TABLE';

[#a6e3a1]Database Info[-]
  SELECT sqlite_version();
  PRAGMA database_list;
  PRAGMA page_count;

[#a6e3a1]Common Operations[-]
  SELECT * FROM table LIMIT 100;
  SELECT COUNT(*) FROM table;
  INSERT INTO t (c1, c2) VALUES ('v1', 'v2');
  UPDATE t SET c1 = 'new' WHERE id = 1;

`

	cheatD1 := `[::b][#a6e3a1]━━━ Cloudflare D1 Cheatsheet ━━━[-][-]

[#a6e3a1]Inspect Schema[-]
  SELECT name FROM sqlite_master WHERE type='table';
  PRAGMA table_info(table_name);
  SELECT sql FROM sqlite_master WHERE name = 'TABLE';

[#a6e3a1]Database Info[-]
  SELECT sqlite_version();
  PRAGMA database_list;

[#a6e3a1]Common Operations[-]
  SELECT * FROM table LIMIT 100;
  SELECT COUNT(*) FROM table;
  INSERT INTO t (c1, c2) VALUES ('v1', 'v2');

`

	sections := manualGuideSections(a)
	// keyboardHelpTextFor is already the first manual section.
	sections[0].body = helpText

	sqlSections := []guideSection{
		{title: "PostgreSQL SQL reference", summary: "Schema, server, CRUD, and performance queries", body: cheatPG},
		{title: "MySQL SQL reference", summary: "Schema, server, CRUD, and performance queries", body: cheatMySQL},
		{title: "SQLite SQL reference", summary: "Schema, database, CRUD, and performance queries", body: cheatSQLite},
		{title: "Turso / LibSQL SQL reference", summary: "Schema, database, and common queries", body: cheatTurso},
		{title: "Cloudflare D1 SQL reference", summary: "Schema, database, and common queries", body: cheatD1},
	}
	if a.db != nil {
		preferred := 0
		switch a.dbType {
		case config.MySQL:
			preferred = 1
		case config.SQLite:
			preferred = 2
		case config.Turso:
			preferred = 3
		case config.CloudflareD1:
			preferred = 4
		}
		sqlSections[0], sqlSections[preferred] = sqlSections[preferred], sqlSections[0]
	}
	sections = append(sections, sqlSections...)

	sectionList := tview.NewList().ShowSecondaryText(true)
	sectionList.SetBorder(true).
		SetTitle(" Contents ").
		SetBorderColor(surface1).
		SetTitleColor(mauve)
	sectionList.SetMainTextColor(text).
		SetSecondaryTextColor(overlay0).
		SetSelectedTextColor(crust).
		SetSelectedBackgroundColor(mauve)

	article := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetWordWrap(true)
	article.SetBorder(true).
		SetBorderColor(surface1).
		SetTitleColor(mauve)

	body := tview.NewFlex()
	screenWidth, _ := a.getScreenSize()
	currentWidth := screenWidth
	narrowLayout := false
	readingArticle := false
	updateFooter := func() {}
	var rebuildBody func(width int)
	showContents := func() {
		readingArticle = false
		rebuildBody(currentWidth)
		a.app.SetFocus(sectionList)
	}
	showArticle := func() {
		readingArticle = true
		rebuildBody(currentWidth)
		a.app.SetFocus(article)
	}

	showSection := func(index int) {
		if index < 0 || index >= len(sections) {
			return
		}
		article.SetTitle(" " + tview.Escape(sections[index].title) + " ")
		article.SetText(sections[index].body)
		article.ScrollToBeginning()
	}
	for _, section := range sections {
		sectionList.AddItem(section.title, section.summary, 0, nil)
	}
	sectionList.SetChangedFunc(func(index int, _, _ string, _ rune) {
		showSection(index)
	})
	sectionList.SetSelectedFunc(func(index int, _, _ string, _ rune) {
		showSection(index)
		showArticle()
	})
	showSection(0)

	sectionList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEscape:
			a.closeHelp()
			return nil
		case tcell.KeyRight, tcell.KeyTab:
			showSection(sectionList.GetCurrentItem())
			showArticle()
			return nil
		}
		return event
	})
	article.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEscape:
			a.closeHelp()
			return nil
		case tcell.KeyLeft, tcell.KeyBackspace, tcell.KeyBackspace2, tcell.KeyTab, tcell.KeyBacktab:
			showContents()
			return nil
		}
		return event
	})

	version := strings.TrimSpace(a.buildInfo.Version)
	if version == "" {
		version = "dev"
	}
	header := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText("[::b][#cba6f7]" + iconHelp + " dbterm Guide & SQL Reference[-][-]  [#6c7086]v" + tview.Escape(version) + " · offline full manual[-]")
	header.SetBackgroundColor(bg)

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	updateFooter = func() {
		footer.SetText(guideFooterText(currentWidth, a.effectiveActionShortcut(actionHelp), readingArticle))
	}
	rebuildBody = func(width int) {
		if width <= 0 {
			return
		}
		currentWidth = width
		narrowLayout = width < 80
		body.Clear()
		if narrowLayout {
			if readingArticle {
				body.AddItem(article, 0, 1, true)
			} else {
				body.AddItem(sectionList, 0, 1, true)
			}
		} else {
			body.AddItem(sectionList, 32, 0, !readingArticle).
				AddItem(article, 0, 1, readingArticle)
		}
		updateFooter()
	}
	sectionList.SetFocusFunc(func() {
		readingArticle = false
		updateFooter()
	})
	article.SetFocusFunc(func() {
		readingArticle = true
		updateFooter()
	})
	a.guideResize = rebuildBody
	rebuildBody(screenWidth)

	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(body, 0, 1, true).
		AddItem(footer, 1, 0, false)

	a.pages.AddAndSwitchToPage("help", layout, true)
	a.app.SetFocus(sectionList)
}

func (a *App) closeHelp() {
	returnState := loadingReturnState{page: a.helpReturnPage, focus: a.helpReturnFocus}
	a.helpReturnPage = ""
	a.helpReturnFocus = nil
	a.guideResize = nil
	a.pages.RemovePage("help")
	if returnState.page != "" && a.pages.HasPage(returnState.page) {
		a.pages.ShowPage(returnState.page)
		a.restoreLoadingReturnState(returnState)
		return
	}
	if a.db != nil && a.pages.HasPage("main") {
		a.pages.ShowPage("main")
		a.restoreLoadingReturnState(loadingReturnState{page: "main", focus: a.focusedPanel})
		return
	}
	a.showDashboard()
}

func guideFooterText(width int, helpShortcut string, readingArticle bool) string {
	closeWithShortcut := "[yellow]Esc/" + tview.Escape(helpShortcut) + "[-] Close"
	if readingArticle {
		return footerTextThatFits(width,
			" [yellow]↑/↓ · PgUp/PgDn · Home/End[-] Scroll  │  [yellow]←/Tab[-] Contents  │  "+closeWithShortcut+" ",
			" [yellow]PgUp/PgDn[-] Scroll  │  [yellow]←/Tab[-] Contents  │  "+closeWithShortcut+" ",
			" [yellow]PgUp/PgDn[-] Scroll  │  [yellow]←[-] Contents  │  [yellow]Esc[-] Close ",
			" [yellow]Esc[-] Close ",
			"[yellow]Esc[-]",
			"",
		)
	}
	return footerTextThatFits(width,
		" [yellow]↑/↓[-] Section  │  [yellow]Enter/→/Tab[-] Read  │  "+closeWithShortcut+" ",
		" [yellow]↑/↓[-] Choose  │  [yellow]Enter/→[-] Read  │  "+closeWithShortcut+" ",
		" [yellow]↑/↓[-] Choose  │  [yellow]Enter[-] Read  │  [yellow]Esc[-] Close ",
		" [yellow]Esc[-] Close ",
		"[yellow]Esc[-]",
		"",
	)
}
