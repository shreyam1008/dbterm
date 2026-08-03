package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

func keyboardHelpText() string {
	return `[::b][#cba6f7]━━━ ` + iconHelp + ` dbterm Help ━━━[-][-]

[#f9e2af]START HERE — COMMON WORKFLOWS[-]
  [#89b4fa]Find a table[-]       [yellow]Alt+T[-] → type its name → [yellow]Enter[-]
  [#89b4fa]Cross-table lookup[-] Select a cell → [yellow]C[-] → open another table/column → [yellow]V[-]
  [#89b4fa]Filter a column[-]    Select the column → [yellow]/[-] → type an exact value → [yellow]Enter[-]
  [#89b4fa]Clear a filter[-]     Press [yellow]Esc[-] once; press it again to return to the Dashboard
  [#89b4fa]Resize results[-]     [yellow]+ / -[-] selected column  │  [yellow]Ctrl++ / Ctrl+-[-] all columns  │  [yellow]Alt++ / Alt+-[-] rows

[#a6e3a1]TABLES[-]
  [yellow]Type[-]             Jump to the first matching table and highlight the match
  [yellow]Backspace[-]        Edit the table search
  [yellow]Enter[-]            Open the match and clear the search
  [yellow]Esc[-]              Clear an active search; press again for Dashboard
  [yellow]Alt+M[-]            Inspect the selected table schema

[#a6e3a1]RESULTS — CELLS & FILTERS[-]
  [yellow]C[-]                Copy only the selected cell (full value, even if preview is shortened)
  [yellow]V[-]                Exact-filter selected column using the clipboard value
  [yellow]/[-]                Open exact filter; Enter searches and Tab / Shift+Tab moves between controls
  [yellow]Esc[-]              Clear an active filter first; press again to return to the Dashboard
  [yellow]Enter[-]            Open row details; C copies the selected detail cell
  [yellow]Space[-]            Toggle current row selection
  [yellow]Alt+A / Alt+C[-]    Select all / clear selected rows
  [yellow]Alt+E[-]            Export selected rows, or all displayed rows, to CSV

[#a6e3a1]RESULTS — SIZE, SORT & PAGES[-]
  [yellow]+ / -[-]            Widen / narrow only the selected column
  [yellow]Ctrl++ / Ctrl+-[-]  Zoom all result columns in / out
  [yellow]> / <[-]            Same all-column zoom (fallback when a terminal drops Ctrl)
  [yellow]0 / Ctrl+0[-]       Reset all column widths
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
  [yellow]Alt+Y[-]            Query history            [yellow]Alt+B[-] Backup current database
  [yellow]Alt+I[-]            Import SQL dump          [yellow]Esc[-] Cancel a running import

[#a6e3a1]NAVIGATION & APP[-]
  [yellow]Alt+T / Q / R[-]    Focus Tables / Query / Results
  [yellow]Tab[-]              Cycle Tables → Query → Results
  [yellow]Alt+D[-]            Dashboard                [yellow]Alt+S[-] Services
  [yellow]Alt+, / Alt+G[-]    Settings                 [yellow]Alt+H[-] This help
  [yellow]Esc / Backspace[-]  Go back                  [yellow]Ctrl+C[-] Quit

[#a6e3a1]DASHBOARD ` + iconDashboard + `[-]
  [yellow]Enter[-] Connect   [yellow]N[-] New   [yellow]E[-] Edit   [yellow]D[-] Delete   [yellow]R[-] Health check
  [yellow]I[-] Import       [yellow]G[-] Settings   [yellow]H[-] Help   [yellow]W / B / Esc[-] Workspace   [yellow]Q[-] Quit
  [yellow]1–9 / 0[-] Quick-select the first ten connections

[#a6e3a1]SERVICES (Alt+S) ` + iconServices + `[-]
  [yellow]1 / 2[-] Toggle MySQL / PostgreSQL    [yellow]C / Enter[-] Connect
  [yellow]R[-] Refresh service info             [yellow]Esc[-] Go back

[#a6e3a1]CLI (run in your terminal)[-]
  dbterm --help        Command help       dbterm --version     Version/build
  dbterm --info        Runtime info       dbterm --update      Install an update
  dbterm --uninstall   Remove dbterm      add --purge to remove saved connections


`
}

func (a *App) showHelp() {
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
			a.pages.RemovePage("help")
			front, _ := a.pages.GetFrontPage()
			if front == "" {
				if a.db != nil {
					a.pages.ShowPage("main")
				} else {
					a.showDashboard()
				}
			}
			return nil
		}
		return event
	})

	a.pages.AddAndSwitchToPage("help", helpView, true)
	a.app.SetFocus(helpView)
}
