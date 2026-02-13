package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

func (a *App) showHelp() {
	helpText := `[::b][#cba6f7]━━━ ` + iconHelp + ` dbterm Help ━━━[-][-]

[#f9e2af]KEYBOARD SHORTCUTS[-]

[#a6e3a1]Navigation ` + iconDashboard + `[-]
  Alt + T ........... Focus Tables list
  Alt + Q ........... Focus Query editor
  Alt + R ........... Focus Results view
  Alt + D ........... Back to Dashboard
  Alt + H ........... Toggle this Help
  Alt + S ........... Database Services
  B ................. Backup current DB (PostgreSQL/MySQL)
  Tab ............... Cycle: Tables → Query → Results
  Esc ............... Close / Go back
  Ctrl+C ............ Quit

[#a6e3a1]Query & Results ` + iconQuery + ` ` + iconResults + `[-]
  Alt + Enter ....... Execute query
  F5 ................ Refresh current table (keep sort/selection) ` + iconRefresh + `
  Ctrl + F5 ......... Refresh table list + current table ` + iconRefresh + `
  F ................. Toggle fullscreen results
  S ................. Sort by current column (in Results)
  Alt + = / - ....... Increase / decrease preview row limit
  Alt + 0 ........... Toggle preview limit (100 ↔ all rows)
  Status bar ........ Shows active sort + preview limit

[#a6e3a1]Dashboard ` + iconDashboard + `[-]
  Enter ............. Connect to selected ` + iconConnect + `
  N ................. New connection
  E ................. Edit connection
  D ................. Delete connection
  R ................. Re-check saved connection reachability ` + iconRefresh + `
  W / B / Esc ....... Back to workspace (when connected) ` + iconBack + `
  H ................. Open Help ` + iconHelp + `
  Q ................. Quit
  1-9 ............... Quick-select connection

[#a6e3a1]Services (Alt+S) ` + iconServices + `[-]
  1 ................. Toggle MySQL start/stop
  2 ................. Toggle PostgreSQL start/stop
  C / Enter ......... Connect to selected service ` + iconConnect + `
  R ................. Refresh service info ` + iconRefresh + `
  Esc ............... Go back ` + iconBack + `


`

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

	// Show the connected DB cheatsheet first
	var content string
	if a.db != nil {
		switch a.dbType {
		case config.MySQL:
			content = helpText + cheatMySQL + cheatPG + cheatSQLite
		case config.SQLite:
			content = helpText + cheatSQLite + cheatPG + cheatMySQL
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
		SetTitle(" " + iconHelp + " Help & Cheatsheets [yellow](Esc / Alt+H to close)[-] ").
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
