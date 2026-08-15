package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func keyboardHelpText() string {
	return `[::b][#cba6f7]━━━ ` + iconHelp + ` dbterm Help ━━━[-][-]

[#f9e2af]START HERE — COMMON WORKFLOWS[-]
  [#89b4fa]Find a table[-]       [yellow]Alt+T[-] → type its name → [yellow]Enter[-]
  [#89b4fa]Cross-table lookup[-] Select a cell → [yellow]C[-] → open another table/column → [yellow]V[-]
  [#89b4fa]Filter a column[-]    Select column → [yellow]/[-] → choose operator/value → [yellow]Enter[-]; Add AND composes
  [#89b4fa]Follow a relation[-]  Select a declared FK cell → [yellow]F[-]; [yellow]Backspace[-] returns
  [#89b4fa]Clear a filter[-]     Press [yellow]Esc[-] once; press it again to return to the Dashboard
  [#89b4fa]Resize results[-]     [yellow]+ / -[-] selected column  │  [yellow]Ctrl++ / Ctrl+-[-] all columns  │  [yellow]Alt++ / Alt+-[-] rows

[#a6e3a1]TABLES[-]
  [#6c7086]Markers[-]          [#a6e3a1]▶[-] currently shown  [#6c7086]•[-] opened this connection  [#cba6f7]/[-] remembered filter  [#f9e2af]` + iconPin + `[-] pinned
  [yellow]Space[-]            Pin/unpin the selected table at the top (saved per database connection)
  [yellow]Type[-]             Jump to the first matching table and highlight the match
  [yellow]Backspace[-]        Edit the table search
  [yellow]Enter[-]            Open the match and clear the search
  [yellow]Esc[-]              Clear an active search; press again for Dashboard
  [yellow]Alt+M[-]            Inspect the selected table schema

[#a6e3a1]RESULTS — CELLS & FILTERS[-]
  [yellow]C[-]                Copy only the selected cell (full value, even if preview is shortened)
  [yellow]V[-]                Apply/update equality from the clipboard (real NULL uses IS NULL)
  [yellow]/[-]                Open filters; Enter applies, Add AND composes, Tab / Shift+Tab moves; remembered per table
  [yellow]F[-]                Follow the selected column's declared foreign key
  [yellow]Backspace[-]        Return after following a foreign key
  [yellow]Esc[-]              Clear all filters first; press again to return to the Dashboard
  [yellow]Enter[-]            Open row details; C copies the selected detail cell
  [yellow]Space[-]            Toggle current row selection
  [yellow]Alt+A / Alt+C[-]    Select all / clear selected rows
  [yellow]Alt+E[-]            Export selected, current-page, or all matching rows to CSV

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
  [yellow]Alt+F[-]            Toggle fullscreen results

[#a6e3a1]QUERY[-]
  [yellow]Enter[-]            Execute SQL             [yellow]Shift+Enter[-] Insert newline
  [yellow]Alt+Y[-]            Query history
  [yellow]Alt+I[-]            Import SQL dump          [yellow]Esc[-] Cancel a running import

[#a6e3a1]NAVIGATION & APP[-]
  [yellow]Ctrl+P (default)[-] Search documented actions, database objects, and recent queries
  [yellow]Alt+T / Q / R[-]    Focus Tables / Query / Results
  [yellow]Tab[-]              Cycle Tables → Query → Results
  [yellow]Alt+B[-]            Instant backup from any workspace panel
  [yellow]Alt+D[-]            Dashboard                [yellow]Alt+K[-] Backup Center
  [yellow]Alt+S[-]            Database services
  [yellow]Alt+, / Alt+G[-]    Settings                 [yellow]Alt+H[-] This help
  [yellow]Esc / Backspace[-]  Go back                  [yellow]Ctrl+C[-] Cancel active work / quit

[#a6e3a1]DASHBOARD ` + iconDashboard + `[-]
  [yellow]Enter[-] Connect/default DB   [yellow]A[-] All DBs on selected server   [yellow]N[-] New   [yellow]E[-] Edit   [yellow]D[-] Delete
  [yellow]R[-] Health check
  [yellow]Ctrl+B[-] New backup job for highlighted connection   [yellow]B[-] Backup Center   [yellow]I[-] Import
  [yellow]G[-] Settings   [yellow]H[-] Help   [yellow]W / Esc[-] Workspace   [yellow]Q[-] Quit
  [yellow]1–9 / 0[-] Quick-select the first ten connections

[#a6e3a1]BACKUP CENTER (Alt+K) ` + iconBackup + `[-]
  [yellow]N[-]               Choose saved/new database for a new plan
  [yellow]Enter[-]           Edit the highlighted plan
  [yellow]R / Space[-]       Run now / toggle a timed schedule (manual stays on demand)
  [yellow]P[-]               Apply retention now; newest verified artifact is always kept
  [yellow]H / I[-]           Run history / inspect and restore a backup by content
  [yellow]A[-]               Desktop/user or Server/system agent: status, startup, PID/RAM/uptime, controls, and logs
  [yellow]G[-]               Generate an age identity and copy its public recipient
  [yellow]D[-]               Delete the job only; history and backup files stay untouched
  [yellow]F2 / F3[-]         Choose folder / refresh destination + staging capacity
  [yellow]Ctrl+N[-]          Add a database from inside the plan form
  [#6c7086]Filename tokens[-] {job} {connection} {database} {engine} {date} {time} {timestamp} {run}

[#a6e3a1]SERVICES (Alt+S) ` + iconServices + `[-]
  [yellow]1 / 2[-] Toggle MySQL / PostgreSQL    [yellow]C / Enter[-] Connect
  [yellow]Database optional[-] Leave it blank to browse every database visible to the selected DB login
  [yellow]R[-] Refresh service info             [yellow]Esc[-] Go back

[#a6e3a1]CLI (run in your terminal)[-]
  dbterm --help        Command help       dbterm --version     Version/build
  dbterm --info        Runtime info       dbterm --update      Install an update
  dbterm --uninstall   Remove dbterm      add --purge to remove dbterm-owned data
  dbterm backup --help Jobs, instant backup, agent, inspection, restore, keys, and paths


`
}

func (a *App) showHelp() {
	a.helpReturnPage, _ = a.pages.GetFrontPage()
	a.helpReturnFocus = a.app.GetFocus()
	helpText := keyboardHelpText()

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

	// Show the connected DB cheatsheet first
	var content string
	if a.db != nil {
		switch a.dbType {
		case config.MySQL:
			content = helpText + cheatMySQL + cheatPG + cheatSQLite
		case config.SQLite:
			content = helpText + cheatSQLite + cheatPG + cheatMySQL
		case config.Turso:
			content = helpText + cheatTurso + cheatPG + cheatMySQL
		case config.CloudflareD1:
			content = helpText + cheatD1 + cheatPG + cheatMySQL
		default:
			content = helpText + cheatPG + cheatMySQL + cheatSQLite
		}
	} else {
		content = helpText + cheatPG + cheatMySQL + cheatSQLite
	}

	helpView := tview.NewTextView().
		SetDynamicColors(true).
		SetText(content).
		SetScrollable(true)
	helpView.SetBorder(true).
		SetTitle(" " + iconHelp + " Help & Cheatsheets [yellow](↑/↓ scroll • Esc/Alt+H close)[-] ").
		SetBorderColor(surface1).
		SetTitleColor(mauve)

	helpView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.closeHelp()
			return nil
		}
		return event
	})

	a.pages.AddAndSwitchToPage("help", helpView, true)
	a.app.SetFocus(helpView)
}

func (a *App) closeHelp() {
	returnState := loadingReturnState{page: a.helpReturnPage, focus: a.helpReturnFocus}
	a.helpReturnPage = ""
	a.helpReturnFocus = nil
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
