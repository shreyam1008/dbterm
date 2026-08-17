package ui

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func testSQLCompletionCatalog() sqlCompletionCatalog {
	return sqlCompletionCatalog{
		relations: []sqlCompletionRelation{
			{name: "public.orders", kind: sqlCompletionTable, columns: []string{"id", "user_id", "total"}},
			{name: "public.users", kind: sqlCompletionTable, columns: []string{"id", "name", "email"}},
			{name: "public.active_users", kind: sqlCompletionView, columns: []string{"id", "name"}},
		},
		schemas:   []string{"public"},
		databases: []string{"app", "analytics"},
	}
}

func TestSQLCompletionSuggestsSelectWhileTyping(t *testing.T) {
	result := completeSQL(sqlCompletionInput{
		text: "sel", cursor: 3, dbType: config.PostgreSQL,
		catalog: testSQLCompletionCatalog(), limit: 6,
	})
	if len(result.items) == 0 {
		t.Fatal("typing sel returned no SQL completions")
	}
	if got := result.items[0].insertText; got != "SELECT" {
		t.Fatalf("first completion = %q, want SELECT", got)
	}
	if result.replaceStart != 0 || result.replaceEnd != 3 {
		t.Fatalf("replacement range = (%d, %d), want (0, 3)", result.replaceStart, result.replaceEnd)
	}
}

func TestSQLCompletionPrioritizesRelationsAfterFrom(t *testing.T) {
	query := "SELECT * FROM us"
	result := completeSQL(sqlCompletionInput{
		text: query, cursor: len(query), dbType: config.PostgreSQL,
		catalog: testSQLCompletionCatalog(), limit: 6,
	})
	if len(result.items) == 0 {
		t.Fatal("FROM context returned no completions")
	}
	if got := result.items[0].label; got != "public.users" {
		t.Fatalf("first FROM completion = %q, want public.users", got)
	}
	if result.items[0].kind != sqlCompletionTable {
		t.Fatalf("first FROM completion kind = %d, want table", result.items[0].kind)
	}
}

func TestSQLCompletionClosesForCompleteRelation(t *testing.T) {
	query := "SELECT * FROM users"
	result := completeSQL(sqlCompletionInput{
		text: query, cursor: len(query), dbType: config.PostgreSQL,
		catalog: testSQLCompletionCatalog(), limit: 6,
	})
	if len(result.items) != 0 {
		t.Fatalf("complete table name kept suggestions open: %#v", result.items)
	}
}

func TestSQLCompletionUsesAliasesForColumns(t *testing.T) {
	query := "SELECT * FROM public.users AS u WHERE u.na"
	result := completeSQL(sqlCompletionInput{
		text: query, cursor: len(query), dbType: config.PostgreSQL,
		catalog: testSQLCompletionCatalog(), limit: 6,
	})
	if len(result.items) == 0 {
		t.Fatal("qualified alias returned no column completions")
	}
	if got := result.items[0].label; got != "u.name" {
		t.Fatalf("qualified completion label = %q, want u.name", got)
	}
	if got := result.items[0].insertText; got != "u.name" {
		t.Fatalf("qualified completion insertion = %q, want u.name", got)
	}
}

func TestSQLCompletionUsesDatabaseContext(t *testing.T) {
	query := "USE ana"
	result := completeSQL(sqlCompletionInput{
		text: query, cursor: len(query), dbType: config.MySQL,
		catalog: testSQLCompletionCatalog(), limit: 6,
	})
	if len(result.items) == 0 || result.items[0].label != "analytics" {
		t.Fatalf("database completions = %#v, want analytics first", result.items)
	}
	if result.items[0].kind != sqlCompletionDatabase {
		t.Fatalf("database completion kind = %d", result.items[0].kind)
	}
}

func TestSQLCompletionFinishesMultiwordKeywordWithoutDuplicatingPrefix(t *testing.T) {
	query := "INSERT in"
	result := completeSQL(sqlCompletionInput{
		text: query, cursor: len(query), dbType: config.PostgreSQL,
		catalog: testSQLCompletionCatalog(), limit: 6,
	})
	for _, item := range result.items {
		if item.label != "INSERT INTO" {
			continue
		}
		if item.insertText != "INTO" {
			t.Fatalf("INSERT INTO completion inserts %q, want only INTO", item.insertText)
		}
		return
	}
	t.Fatalf("INSERT context did not suggest INSERT INTO: %#v", result.items)
}

func TestSQLCompletionStaysOutOfStringsAndComments(t *testing.T) {
	queries := []string{
		"SELECT 'sel",
		"SELECT * -- fro",
		"SELECT * # fro",
		"SELECT /* fro",
		"SELECT $body$ fro",
	}
	for _, query := range queries {
		result := completeSQL(sqlCompletionInput{
			text: query, cursor: len(query), dbType: config.PostgreSQL,
			catalog: testSQLCompletionCatalog(), limit: 6,
		})
		if len(result.items) != 0 {
			t.Errorf("query %q returned completions inside a string/comment: %#v", query, result.items)
		}
	}
}

func TestAcceptSQLCompletionUsesUndoSafeReplacement(t *testing.T) {
	queryInput := tview.NewTextArea().SetText("sel", true)
	view := newSQLCompletionView()
	app := &App{
		queryInput:        queryInput,
		sqlCompletionView: view,
		rightFlex:         tview.NewFlex().SetDirection(tview.FlexRow).AddItem(queryInput, 3, 0, true).AddItem(view, 3, 0, false),
		sqlCompletionState: sqlCompletionState{
			visible: true,
			items: []sqlCompletionItem{{
				label: "SELECT", insertText: "SELECT", kind: sqlCompletionKeyword, appendSpace: true,
			}},
			replaceStart: 0,
			replaceEnd:   3,
		},
	}

	if !app.acceptSQLCompletion() {
		t.Fatal("completion was not accepted")
	}
	if got := queryInput.GetText(); got != "SELECT " {
		t.Fatalf("query text = %q, want %q", got, "SELECT ")
	}
	if app.sqlCompletionState.visible {
		t.Fatal("completion popup remained visible after insertion")
	}
}

func TestSQLCompletionTextAreaFlow(t *testing.T) {
	queryInput := tview.NewTextArea()
	app := &App{
		queryInput:           queryInput,
		sqlCompletionView:    newSQLCompletionView(),
		sqlCompletionCatalog: testSQLCompletionCatalog(),
		dbType:               config.PostgreSQL,
		focusedPanel:         queryInput,
	}
	queryInput.SetChangedFunc(func() { app.refreshSQLCompletions(false) })
	queryInput.SetMovedFunc(func() { app.refreshSQLCompletions(false) })
	queryInput.SetText("sel", true)

	if !app.sqlCompletionState.visible || len(app.sqlCompletionState.items) == 0 {
		t.Fatal("typing into TextArea did not open completions")
	}
	if got := app.sqlCompletionState.items[0].insertText; got != "SELECT" {
		t.Fatalf("TextArea completion = %q, want SELECT", got)
	}
	if !app.handleSQLCompletionKey(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)) {
		t.Fatal("Tab did not accept the visible completion")
	}
	if got := queryInput.GetText(); got != "SELECT " {
		t.Fatalf("TextArea after Tab = %q, want SELECT followed by a space", got)
	}
}

func TestLoadSQLCompletionCatalogFromSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, email TEXT);
		CREATE VIEW active_users AS SELECT id, name FROM users;
	`); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	catalog := loadSQLCompletionCatalog(t.Context(), db, config.SQLite, []string{"users"}, "test")
	users, ok := findSQLCompletionRelation(catalog, "users")
	if !ok {
		t.Fatal("users table missing from completion catalog")
	}
	if got := fmt.Sprint(users.columns); got != "[id name email]" {
		t.Fatalf("users columns = %s, want [id name email]", got)
	}
	view, ok := findSQLCompletionRelation(catalog, "active_users")
	if !ok || view.kind != sqlCompletionView {
		t.Fatalf("active_users view = %#v, want a view", view)
	}
}

func BenchmarkSQLCompletionLargeCatalog(b *testing.B) {
	catalog := sqlCompletionCatalog{relations: make([]sqlCompletionRelation, 5000)}
	for index := range catalog.relations {
		catalog.relations[index] = sqlCompletionRelation{
			name:    fmt.Sprintf("public.table_%04d", index),
			kind:    sqlCompletionTable,
			columns: []string{"id", "created_at", "updated_at", "display_name"},
		}
	}
	input := sqlCompletionInput{
		text: "SELECT * FROM tab", cursor: len("SELECT * FROM tab"),
		dbType: config.PostgreSQL, catalog: catalog, limit: 6,
	}
	b.ResetTimer()
	for range b.N {
		_ = completeSQL(input)
	}
}
