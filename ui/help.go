package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/shreyam1008/dbterm/config"
	"github.com/rivo/tview"
)

func (a *App) showHelp() {
	helpText := `[::b][#cba6f7]dbterm — Multi-Database Terminal Client[-][-]

[yellow]━━━ Keyboard Shortcuts ━━━[-]

[green]Navigation[-]
  Alt + T       Focus Tables list
  Alt + Q       Focus Query editor
  Alt + R       Focus Results view
  Alt + D       Go to Dashboard
  Alt + H       Toggle this Help
  Tab           Navigate forward
  Shift+Tab     Navigate backward
  Esc           Close modal / Go back

[green]Query[-]
  Alt + Enter   Execute query

[green]Dashboard[-]
  Enter         Connect to selected
  N             New connection
  E             Edit connection
  D             Delete connection
  Q             Quit


`

	cheatPG := `[yellow]━━━ PostgreSQL Cheatsheet ━━━[-]

[green]Tables & Schema[-]
  \d                    List tables
  \d table_name         Describe table
  SELECT * FROM pg_tables WHERE schemaname='public';

[green]Common Queries[-]
  SELECT version();
  SELECT current_database();
  SELECT pg_size_pretty(pg_database_size(current_database()));
  SELECT tablename FROM pg_tables WHERE schemaname='public';
  SELECT column_name, data_type FROM information_schema.columns
    WHERE table_name = 'your_table';

[green]Data[-]
  SELECT * FROM table LIMIT 100;
  SELECT COUNT(*) FROM table;
  INSERT INTO table (col1, col2) VALUES ('val1', 'val2');
  UPDATE table SET col1 = 'new' WHERE id = 1;
  DELETE FROM table WHERE id = 1;

[green]Index & Performance[-]
  CREATE INDEX idx_name ON table(column);
  EXPLAIN ANALYZE SELECT * FROM table WHERE col = 'val';
  SELECT pg_size_pretty(pg_total_relation_size('table'));


`

	cheatMySQL := `[yellow]━━━ MySQL Cheatsheet ━━━[-]

[green]Tables & Schema[-]
  SHOW TABLES;
  DESCRIBE table_name;
  SHOW CREATE TABLE table_name;

[green]Common Queries[-]
  SELECT VERSION();
  SELECT DATABASE();
  SHOW DATABASES;
  SELECT table_name, engine FROM information_schema.tables
    WHERE table_schema = DATABASE();

[green]Data[-]
  SELECT * FROM table LIMIT 100;
  SELECT COUNT(*) FROM table;
  INSERT INTO table (col1, col2) VALUES ('val1', 'val2');
  UPDATE table SET col1 = 'new' WHERE id = 1;
  DELETE FROM table WHERE id = 1;

[green]Index & Performance[-]
  CREATE INDEX idx_name ON table(column);
  EXPLAIN SELECT * FROM table WHERE col = 'val';
  SHOW TABLE STATUS;


`

	cheatSQLite := `[yellow]━━━ SQLite Cheatsheet ━━━[-]

[green]Tables & Schema[-]
  .tables
  PRAGMA table_info(table_name);
  SELECT sql FROM sqlite_master WHERE name='table_name';

[green]Common Queries[-]
  SELECT sqlite_version();
  SELECT name FROM sqlite_master WHERE type='table';
  PRAGMA database_list;
  PRAGMA page_count;

[green]Data[-]
  SELECT * FROM table LIMIT 100;
  SELECT COUNT(*) FROM table;
  INSERT INTO table (col1, col2) VALUES ('val1', 'val2');
  UPDATE table SET col1 = 'new' WHERE id = 1;
  DELETE FROM table WHERE id = 1;

[green]Index & Performance[-]
  CREATE INDEX idx_name ON table(column);
  EXPLAIN QUERY PLAN SELECT * FROM table WHERE col = 'val';
  PRAGMA integrity_check;

`

	content := helpText + cheatPG + cheatMySQL + cheatSQLite

	// Mark the active DB cheatsheet when connected
	if a.db != nil {
		switch a.dbType {
		case config.PostgreSQL:
			content = helpText + cheatPG + cheatMySQL + cheatSQLite
		case config.MySQL:
			content = helpText + cheatMySQL + cheatPG + cheatSQLite
		case config.SQLite:
			content = helpText + cheatSQLite + cheatPG + cheatMySQL
		}
	}

	helpView := tview.NewTextView().
		SetDynamicColors(true).
		SetText(content).
		SetScrollable(true)
	helpView.SetBorder(true).
		SetTitle(" Help & Cheatsheets [yellow](Esc to close)[-] ").
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
