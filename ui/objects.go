package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/utils"
)

// loadDatabaseObjects fetches views, functions, triggers, stored procedures,
// and extensions for the current connection and appends them to the tables list.
// All items are read-only display entries (selecting one runs SELECT * for views,
// or shows a read-only info alert for other object types).
func (a *App) loadDatabaseObjects() {
	db := a.db
	dbType := a.dbType
	dbName := a.dbName
	generation := a.currentDataGeneration()
	if db == nil {
		return
	}

	objTypes := utils.SupportedObjectTypes(dbType)
	if len(objTypes) == 0 {
		return
	}

	go func() {
		type objGroup struct {
			objType utils.DBObjectType
			names   []string
		}
		var groups []objGroup

		for _, ot := range objTypes {
			query := utils.ListObjectsQuery(dbType, ot)
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
			if a.db != db || a.dbType != dbType || a.dbName != dbName || !a.isDataGenerationCurrent(generation) {
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
					a.tables.AddItem(
						fmt.Sprintf("  [#a6adc8]%s[-] %s", icon, objName),
						"", 0,
						func() {
							a.onDatabaseObjectSelected(objType, objName)
						},
					)
				}
			}

			// Update title to include object counts
			totalObjects := 0
			for _, g := range groups {
				totalObjects += len(g.names)
			}
			currentTitle := a.tables.GetTitle()
			if totalObjects > 0 && !strings.Contains(currentTitle, "obj") {
				a.tables.SetTitle(fmt.Sprintf(" %s %s Tables (%d) + %d obj [yellow](Alt+T)[-] ", iconTables, a.connectionScopeLabel(), a.tableCount, totalObjects))
			}
		})
	}()
}

// onDatabaseObjectSelected handles selection of a database object from the sidebar.
func (a *App) onDatabaseObjectSelected(objType utils.DBObjectType, name string) {
	switch objType {
	case utils.ObjViews:
		// Views can be queried like tables
		a.selectedTable = name
		a.resetPagination()
		if err := a.LoadResults(); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not load view \"%s\":\n\n%v", iconWarn, name, err), "main")
		}
	default:
		// Show read-only info for functions, triggers, procedures, extensions
		a.showObjectInfo(objType, name)
	}
}

// showObjectInfo displays a read-only modal with details about a database object.
func (a *App) showObjectInfo(objType utils.DBObjectType, name string) {
	var query string
	namespace, objectName := splitQualifiedIdentifier(name)
	namespace = a.defaultObjectNamespace(namespace)
	switch a.dbType {
	case "postgresql":
		switch objType {
		case utils.ObjFunctions:
			query = fmt.Sprintf(`SELECT pg_get_functiondef(p.oid)
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
WHERE n.nspname = '%s' AND p.proname = '%s'
LIMIT 1`, escapeSQLString(namespace), escapeSQLString(objectName))
		case utils.ObjTriggers:
			query = fmt.Sprintf(`SELECT pg_get_triggerdef(t.oid)
FROM pg_trigger t
JOIN pg_class c ON t.tgrelid = c.oid
JOIN pg_namespace n ON c.relnamespace = n.oid
WHERE n.nspname = '%s' AND t.tgname = '%s'
LIMIT 1`, escapeSQLString(namespace), escapeSQLString(objectName))
		case utils.ObjStoredProcedures:
			query = fmt.Sprintf(`SELECT pg_get_functiondef(p.oid)
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
WHERE n.nspname = '%s' AND p.proname = '%s'
LIMIT 1`, escapeSQLString(namespace), escapeSQLString(objectName))
		case utils.ObjExtensions:
			query = fmt.Sprintf(`SELECT e.extname, e.extversion, n.nspname AS schema, d.description
FROM pg_extension e
LEFT JOIN pg_namespace n ON e.extnamespace = n.oid
LEFT JOIN pg_description d ON e.oid = d.objoid
WHERE e.extname = '%s'`, escapeSQLString(objectName))
		}
	case "mysql":
		switch objType {
		case utils.ObjFunctions:
			query = fmt.Sprintf(`SHOW CREATE FUNCTION %s`, quoteIdentifier(a.dbType, qualifiedIdentifier(namespace, objectName)))
		case utils.ObjTriggers:
			query = fmt.Sprintf(`SHOW CREATE TRIGGER %s`, quoteIdentifier(a.dbType, qualifiedIdentifier(namespace, objectName)))
		case utils.ObjStoredProcedures:
			query = fmt.Sprintf(`SHOW CREATE PROCEDURE %s`, quoteIdentifier(a.dbType, qualifiedIdentifier(namespace, objectName)))
		}
	}

	if query == "" {
		a.ShowAlert(fmt.Sprintf("%s %s: %s\n\n[#a6adc8]Type:[-] %s\n[#a6adc8]Read-only object[-]", iconInfo, objType, name, objType), "main")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := a.db.QueryContext(ctx, query)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not fetch %s \"%s\":\n\n%v", iconWarn, objType, name, err), "main")
		return
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	if len(cols) == 0 {
		a.ShowAlert(fmt.Sprintf("%s %s: %s\n\n[#a6adc8]No definition available[-]", iconInfo, objType, name), "main")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[::b]%s %s: %s[-]\n\n", objectTypeIcon(objType), objType, name))

	for rows.Next() {
		values := make([]any, len(cols))
		valuePtrs := make([]any, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}
		if err := rows.Scan(valuePtrs...); err != nil {
			continue
		}
		for i, col := range cols {
			val := "NULL"
			if values[i] != nil {
				switch v := values[i].(type) {
				case []byte:
					val = string(v)
				case string:
					val = v
				default:
					val = fmt.Sprintf("%v", v)
				}
			}
			sb.WriteString(fmt.Sprintf("[#a6adc8]%s:[-] %s\n", col, val))
		}
	}

	// Show in a scrollable modal
	detailView := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetText(sb.String())
	detailView.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s %s: %s (read-only) ", objectTypeIcon(objType), objType, name)).
		SetBorderColor(surface1).
		SetTitleColor(mauve).
		SetBackgroundColor(mantle)

	detailView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyEnter {
			a.pages.RemovePage("objectInfo")
			a.app.SetFocus(a.tables)
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

func objectTypeIcon(objType utils.DBObjectType) string {
	switch objType {
	case utils.ObjViews:
		return "👁"
	case utils.ObjFunctions:
		return "ƒ"
	case utils.ObjTriggers:
		return "⚡"
	case utils.ObjStoredProcedures:
		return "⚙"
	case utils.ObjExtensions:
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
