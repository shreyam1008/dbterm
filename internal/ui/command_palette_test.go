package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
	"github.com/shreyam1008/dbterm/internal/history"
)

func TestFuzzySubsequenceMatchIsCaseInsensitiveAndScoresCompactMatchesFirst(t *testing.T) {
	positions, _, ok := fuzzySubsequenceMatch("Inspect Schema", "isch")
	if !ok {
		t.Fatal("expected Inspect Schema to fuzzy-match isch")
	}
	wantPositions := []int{0, 2, 5, 10}
	if len(positions) != len(wantPositions) {
		t.Fatalf("positions = %#v, want %#v", positions, wantPositions)
	}
	for index := range wantPositions {
		if positions[index] != wantPositions[index] {
			t.Fatalf("positions = %#v, want %#v", positions, wantPositions)
		}
	}

	_, compactScore, ok := fuzzySubsequenceMatch("users", "usr")
	if !ok {
		t.Fatal("expected users to match usr")
	}
	_, scatteredScore, ok := fuzzySubsequenceMatch("user_account_records", "usr")
	if !ok {
		t.Fatal("expected user_account_records to match usr")
	}
	if compactScore >= scatteredScore {
		t.Fatalf("compact score = %d, scattered score = %d; compact match should rank first", compactScore, scatteredScore)
	}
}

func TestSearchCommandPaletteItemsSearchesAllFieldsAndHighlightsTitle(t *testing.T) {
	items := []commandPaletteItem{
		{
			kind:        commandPaletteAction,
			title:       "Inspect Schema",
			description: "Show columns and foreign keys.",
			keywords:    "metadata indexes",
			sortOrder:   0,
		},
		{
			kind:        commandPaletteTable,
			title:       "customer_orders",
			objectName:  "customer_orders",
			description: "Open table rows.",
			keywords:    "records data",
			sortOrder:   1,
		},
	}

	titleMatches := searchCommandPaletteItems(items, "isch")
	if len(titleMatches) != 1 || titleMatches[0].item.title != "Inspect Schema" {
		t.Fatalf("title matches = %#v, want Inspect Schema", titleMatches)
	}
	if len(titleMatches[0].titlePositions) != 4 {
		t.Fatalf("title highlight positions = %#v, want four matched characters", titleMatches[0].titlePositions)
	}

	descriptionMatches := searchCommandPaletteItems(items, "foreign")
	if len(descriptionMatches) != 1 || descriptionMatches[0].item.title != "Inspect Schema" {
		t.Fatalf("description matches = %#v, want Inspect Schema", descriptionMatches)
	}

	keywordMatches := searchCommandPaletteItems(items, "idx")
	if len(keywordMatches) != 1 || keywordMatches[0].item.title != "Inspect Schema" {
		t.Fatalf("keyword matches = %#v, want Inspect Schema", keywordMatches)
	}

	objectMatches := searchCommandPaletteItems(items, "cord")
	if len(objectMatches) != 1 || objectMatches[0].item.title != "customer_orders" {
		t.Fatalf("object-name matches = %#v, want customer_orders", objectMatches)
	}

	multiFieldMatches := searchCommandPaletteItems(items, "schema fk")
	if len(multiFieldMatches) != 1 || multiFieldMatches[0].item.title != "Inspect Schema" {
		t.Fatalf("multi-field matches = %#v, want Inspect Schema", multiFieldMatches)
	}
}

func TestHighlightCommandPaletteTitleEscapesTextAndMarksMatchedRunes(t *testing.T) {
	got := highlightCommandPaletteTitle("user[role]", []int{0, 2})
	if strings.Count(got, "[black:#f9e2af:b]") != 2 {
		t.Fatalf("highlighted title = %q, want two highlighted groups", got)
	}
	if strings.Contains(got, "[role]") {
		t.Fatalf("highlighted title = %q, raw tview tag-like content was not escaped", got)
	}
}

func TestBuildCommandPaletteItemsIncludesObjectsRecentQueriesAndEffectiveShortcut(t *testing.T) {
	historyManager, err := history.NewManagerAt(t.TempDir()+"/history.json", 20)
	if err != nil {
		t.Fatalf("NewManagerAt() error = %v", err)
	}
	settings := config.DefaultSettings()
	settings.Keymap[config.ActionFocusTables] = []string{"ctrl+g", "alt+t"}
	activeConnection := &config.ConnectionConfig{
		Name:     "test-db",
		Type:     config.SQLite,
		FilePath: "/tmp/test.db",
	}
	app := &App{
		settings:   settings,
		historyMgr: historyManager,
		activeConn: activeConnection,
		tableIdentifiers: map[int]string{
			0: "users",
			1: "orders",
		},
		databaseObjects: map[int]databaseObjectListItem{
			2: {objType: database.ObjViews, name: "active_users"},
			3: {objType: database.ObjFunctions, name: "normalize_email"},
			4: {objType: database.ObjStoredProcedures, name: "rebuild_totals"},
			5: {objType: database.ObjTriggers, name: "audit_orders"},
			6: {objType: database.ObjExtensions, name: "uuid-ossp"},
		},
	}
	connectionKey, ok := app.activeConnectionKey()
	if !ok {
		t.Fatal("activeConnectionKey() did not resolve the test connection")
	}
	if err := historyManager.Append(connectionKey, "SELECT * FROM users WHERE active = true"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	items := app.buildCommandPaletteItems()
	wantedKinds := map[commandPaletteItemKind]bool{
		commandPaletteAction:    false,
		commandPaletteTable:     false,
		commandPaletteView:      false,
		commandPaletteFunction:  false,
		commandPaletteProcedure: false,
		commandPaletteTrigger:   false,
		commandPaletteQuery:     false,
	}
	focusTablesShortcut := ""
	pinShortcut := ""
	copyTableShortcut := ""
	containsExtension := false
	for _, item := range items {
		if _, wanted := wantedKinds[item.kind]; wanted {
			wantedKinds[item.kind] = true
		}
		if item.action == actionFocusTables {
			focusTablesShortcut = item.shortcut
		}
		if item.action == paletteActionToggleTablePin {
			pinShortcut = item.shortcut
		}
		if item.action == paletteActionCopyTableName {
			copyTableShortcut = item.shortcut
		}
		if item.objectName == "uuid-ossp" {
			containsExtension = true
		}
	}
	for kind, found := range wantedKinds {
		if !found {
			t.Errorf("palette did not include kind %q", kind)
		}
	}
	if focusTablesShortcut != "Ctrl+G / Alt+T" {
		t.Fatalf("effective shortcut = %q, want %q", focusTablesShortcut, "Ctrl+G / Alt+T")
	}
	if pinShortcut != "Space (Tables)" {
		t.Fatalf("pin action shortcut = %q, want Space (Tables)", pinShortcut)
	}
	if copyTableShortcut != "Shift+C / Right-click (Tables)" {
		t.Fatalf("copy-table action shortcut = %q, want Shift+C / Right-click (Tables)", copyTableShortcut)
	}
	pinMatches := searchCommandPaletteItems(items, "favorite sidebar")
	if len(pinMatches) == 0 || pinMatches[0].item.action != paletteActionToggleTablePin {
		t.Fatalf("pin action search = %#v, want pin toggle first", pinMatches)
	}
	copyTableMatches := searchCommandPaletteItems(items, "copy table name")
	if len(copyTableMatches) == 0 || copyTableMatches[0].item.action != paletteActionCopyTableName {
		t.Fatalf("copy-table action search = %#v, want table-name copy first", copyTableMatches)
	}
	if containsExtension {
		t.Fatal("palette unexpectedly included an unsupported extension object")
	}

	recent := searchCommandPaletteItems(items, "active true")
	if len(recent) == 0 || recent[0].item.kind != commandPaletteQuery {
		t.Fatalf("recent-query search = %#v, want a recent query first", recent)
	}

	relationship := searchCommandPaletteItems(items, "relationship composite")
	foundFollow := false
	for _, match := range relationship {
		if match.item.action == paletteActionExploreRelationships {
			foundFollow = true
			if !strings.Contains(match.item.description, "every component") || !strings.Contains(match.item.description, "child rows") || match.item.shortcut != "F" {
				t.Fatalf("follow action details = %#v", match.item)
			}
		}
	}
	if !foundFollow {
		t.Fatal("palette search did not include related-row navigation action")
	}
}

func TestCommandPaletteDefaultShortcutResolvesCtrlP(t *testing.T) {
	resolver, err := newActionKeymap(config.DefaultSettings())
	if err != nil {
		t.Fatalf("newActionKeymap() error = %v", err)
	}
	event := tcell.NewEventKey(tcell.KeyCtrlP, 0, tcell.ModCtrl)
	action, ok := resolver.Resolve(event)
	if !ok || action != actionCommandPalette {
		t.Fatalf("Resolve(Ctrl+P) = (%q, %v), want (%q, true)", action, ok, actionCommandPalette)
	}
}

func TestBuildCommandPaletteItemsIncludesSavedConnectionBackupShortcut(t *testing.T) {
	app := &App{store: &config.Store{Connections: []config.ConnectionConfig{{
		ID: "conn-1", Name: "production", Type: config.PostgreSQL,
	}}}}
	items := app.buildCommandPaletteItems()
	for _, item := range items {
		if item.kind == commandPaletteBackupJob && item.objectName == "conn-1" {
			if item.shortcut != "Dashboard Ctrl+B" || !strings.Contains(item.title, "production") {
				t.Fatalf("backup palette item = %#v", item)
			}
			return
		}
	}
	t.Fatal("saved connection backup palette item was not added")
}
