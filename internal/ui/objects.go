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
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

type databaseObjectListItem struct {
	objType database.DBObjectType
	name    string
}

// loadDatabaseObjects fetches views, functions, triggers, stored procedures,
// and extensions for the current connection and appends them to the tables list.
// All items are read-only display entries (selecting one runs SELECT * for views,
// or shows a read-only info alert for other object types).
func (a *App) loadDatabaseObjects() {
	db := a.db
	dbType := a.dbType
	dbName := a.dbName
	generation := a.objectGeneration.Add(1)
	if db == nil {
		return
	}

	objTypes := database.SupportedObjectTypes(dbType)
	if len(objTypes) == 0 {
		return
	}

	go func() {
		type objGroup struct {
			objType database.DBObjectType
			names   []string
		}
		var groups []objGroup

		for _, ot := range objTypes {
			query := database.ListObjectsQuery(dbType, ot)
			if query == "" {
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			rows, err := db.QueryContext(ctx, query)
			if err != nil {
				cancel()
				continue
			}

			var names []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					break
				}
				names = append(names, name)
			}
			rows.Close()
			cancel()

			if len(names) > 0 {
				groups = append(groups, objGroup{objType: ot, names: names})
			}
		}

		if len(groups) == 0 {
			return
		}

		a.app.QueueUpdateDraw(func() {
			if a.db != db || a.dbType != dbType || a.dbName != dbName || a.objectGeneration.Load() != generation {
				return
			}
			for _, g := range groups {
				icon := objectTypeIcon(g.objType)
				// Section header (non-selectable styled text)
				a.tables.AddItem(
					fmt.Sprintf("[#6c7086]── %s %s (%d) ──[-]", icon, g.objType, len(g.names)),
					"", 0, nil,
				)
				for _, name := range g.names {
					objName := name
					objType := g.objType
					itemIndex := a.tables.GetItemCount()
					a.tables.AddItem(
						fmt.Sprintf("  [#a6adc8]%s[-] %s", icon, objName),
						"", 0, nil,
					)
					a.databaseObjects[itemIndex] = databaseObjectListItem{
						objType: objType,
						name:    objName,
					}
				}
			}

			// Update title to include object counts
			totalObjects := 0
			for _, g := range groups {
				totalObjects += len(g.names)
			}
			a.databaseObjectCount = totalObjects
			a.updateTableListTitle()
			a.sqlCompletionRoutines = sqlCompletionRoutinesFromObjects(a.databaseObjects)
			if a.focusedPanel == a.queryInput {
				a.refreshSQLCompletions(false)
			}
		})
	}()
}

// onDatabaseObjectSelected handles selection of a database object from the sidebar.
func (a *App) onDatabaseObjectSelected(objType database.DBObjectType, name string) {
	switch objType {
	case database.ObjViews:
		// Views can be queried like tables
		previous := a.captureResultNavigationState()
		previousStack := append([]resultNavigationState(nil), a.resultNavStack...)
		a.clearResultNavigation()
		a.selectTableWithRememberedFilter(name)
		a.resetSort()
		a.resetPagination()
		a.loadCurrentTableAsync(tableLoadOptions{
			loadingText:  fmt.Sprintf("Loading view %s...", name),
			cancelText:   "Press Esc to cancel opening this view.",
			canceledText: "View loading canceled",
			errorText:    fmt.Sprintf("Could not load view %q", name),
			rollback: func() {
				a.restoreResultNavigationState(previous)
				a.resultNavStack = previousStack
				a.selectTableListIdentifier(previous.table)
			},
		})
	default:
		// Show read-only info for functions, triggers, procedures, extensions
		a.showObjectInfo(objType, name)
	}
}

// showObjectInfo displays a read-only modal with details about a database object.
func (a *App) showObjectInfo(objType database.DBObjectType, name string) {
	var query string
	namespace, objectName := splitQualifiedIdentifier(name)
	namespace = a.defaultObjectNamespace(namespace)
	switch a.dbType {
	case "postgresql":
		switch objType {
		case database.ObjFunctions:
			query = fmt.Sprintf(`SELECT pg_get_functiondef(p.oid)
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
WHERE n.nspname = '%s' AND p.proname = '%s'
LIMIT 1`, escapeSQLString(namespace), escapeSQLString(objectName))
		case database.ObjTriggers:
			query = fmt.Sprintf(`SELECT pg_get_triggerdef(t.oid)
FROM pg_trigger t
JOIN pg_class c ON t.tgrelid = c.oid
JOIN pg_namespace n ON c.relnamespace = n.oid
WHERE n.nspname = '%s' AND t.tgname = '%s'
LIMIT 1`, escapeSQLString(namespace), escapeSQLString(objectName))
		case database.ObjStoredProcedures:
			query = fmt.Sprintf(`SELECT pg_get_functiondef(p.oid)
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
WHERE n.nspname = '%s' AND p.proname = '%s'
LIMIT 1`, escapeSQLString(namespace), escapeSQLString(objectName))
		case database.ObjExtensions:
			query = fmt.Sprintf(`SELECT e.extname, e.extversion, n.nspname AS schema, d.description
FROM pg_extension e
LEFT JOIN pg_namespace n ON e.extnamespace = n.oid
LEFT JOIN pg_description d ON e.oid = d.objoid
WHERE e.extname = '%s'`, escapeSQLString(objectName))
		}
	case "mysql":
		switch objType {
		case database.ObjFunctions:
			query = fmt.Sprintf(`SHOW CREATE FUNCTION %s`, quoteIdentifier(a.dbType, qualifiedIdentifier(namespace, objectName)))
		case database.ObjTriggers:
			query = fmt.Sprintf(`SHOW CREATE TRIGGER %s`, quoteIdentifier(a.dbType, qualifiedIdentifier(namespace, objectName)))
		case database.ObjStoredProcedures:
			query = fmt.Sprintf(`SHOW CREATE PROCEDURE %s`, quoteIdentifier(a.dbType, qualifiedIdentifier(namespace, objectName)))
		}
	}

	if query == "" {
		a.ShowAlert(fmt.Sprintf("%s %s: %s\n\n[#a6adc8]Type:[-] %s\n[#a6adc8]Read-only object[-]", iconInfo, objType, name, objType), "main")
		return
	}

	db := a.db
	dbType := a.dbType
	generation := a.objectGeneration.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	var canceled atomic.Bool
	loadingToken := a.showLoadingModal(fmt.Sprintf("Loading %s %s...", objType, name), withLoadingCancel("Press Esc to cancel object inspection.", func() {
		canceled.Store(true)
		cancel()
		a.setFocusWithColor(a.tables)
		a.flashStatus("[yellow]Object inspection canceled[-]", a.currentResultRowCount(), 1500*time.Millisecond)
	}))

	go func() {
		defer cancel()
		summary, err := loadDatabaseObjectInfo(ctx, db, query, objType, name)
		a.queueUpdateDraw(func() {
			if canceled.Load() || a.db != db || a.dbType != dbType || a.objectGeneration.Load() != generation {
				return
			}
			if !a.finishLoadingModal(loadingToken) {
				return
			}
			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not fetch %s \"%s\":\n\n%v", iconWarn, objType, name, err), "main")
				return
			}
			a.showDatabaseObjectInfoModal(objType, name, summary)
		})
	}()
}

func loadDatabaseObjectInfo(ctx context.Context, db *sql.DB, query string, objType database.DBObjectType, name string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("not connected")
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("no definition is available")
	}
	databaseTypes := resultDatabaseTypes(rows, len(cols))

	var summary strings.Builder
	summary.WriteString(fmt.Sprintf("[::b]%s %s: %s[-]\n\n", objectTypeIcon(objType), tview.Escape(string(objType)), tview.Escape(name)))
	rowCount := 0
	for rows.Next() {
		values := make([]any, len(cols))
		valuePointers := make([]any, len(cols))
		for index := range values {
			valuePointers[index] = &values[index]
		}
		if err := rows.Scan(valuePointers...); err != nil {
			return "", err
		}
		rowCount++
		for index, column := range cols {
			summary.WriteString(fmt.Sprintf("[#a6adc8]%s:[-] %s\n", tview.Escape(column), tview.Escape(fullCellValueForDatabaseType(values[index], databaseTypes[index]))))
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if rowCount == 0 {
		return "", fmt.Errorf("no definition is available")
	}
	return summary.String(), nil
}

func (a *App) showDatabaseObjectInfoModal(objType database.DBObjectType, name, summary string) {
	// Show in a scrollable modal.
	detailView := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetText(summary)
	detailView.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s %s: %s (read-only) [yellow](Esc/Enter close)[-] ", objectTypeIcon(objType), objType, name)).
		SetBorderColor(surface1).
		SetTitleColor(mauve).
		SetBackgroundColor(mantle)

	detailView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyEnter {
			a.pages.RemovePage("objectInfo")
			a.setFocusWithColor(a.tables)
			return nil
		}
		return event
	})

	frame := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(detailView, 0, 3, true).
			AddItem(nil, 0, 1, false),
			0, 3, true).
		AddItem(nil, 0, 1, false)

	a.pages.AddPage("objectInfo", frame, true, true)
	a.app.SetFocus(detailView)
}

func objectTypeIcon(objType database.DBObjectType) string {
	switch objType {
	case database.ObjViews:
		return "👁"
	case database.ObjFunctions:
		return "ƒ"
	case database.ObjTriggers:
		return "⚡"
	case database.ObjStoredProcedures:
		return "⚙"
	case database.ObjExtensions:
		return "🧩"
	default:
		return "•"
	}
}

func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func splitQualifiedIdentifier(identifier string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(identifier), ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", strings.TrimSpace(identifier)
}

func qualifiedIdentifier(namespace, name string) string {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" {
		return name
	}
	if name == "" {
		return namespace
	}
	return namespace + "." + name
}

func (a *App) defaultObjectNamespace(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace != "" {
		return namespace
	}
	switch a.dbType {
	case config.PostgreSQL:
		return "public"
	case config.MySQL:
		if cfg := a.currentConnectionConfig(); cfg != nil {
			return strings.TrimSpace(cfg.Database)
		}
		return strings.TrimSpace(a.dbName)
	default:
		return ""
	}
}
