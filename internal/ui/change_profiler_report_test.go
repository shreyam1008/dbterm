package ui

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	profiler "github.com/shreyam1008/dbterm/internal/changeprofiler"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestProfilerReportPagesAllChangesAndSwitchesFocus(t *testing.T) {
	ctx := context.Background()
	source, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	source.SetMaxOpenConns(1)
	count := 2*profilerReportPageSize + 25
	_, err = source.Exec(fmt.Sprintf(`CREATE TABLE records(id INTEGER PRIMARY KEY, note TEXT);
		WITH RECURSIVE series(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM series WHERE n<%d)
		INSERT INTO records SELECT n,'before' FROM series;
		CREATE TABLE z_other(id INTEGER PRIMARY KEY, note TEXT);
		INSERT INTO z_other VALUES(1,'before')`, count))
	if err != nil {
		t.Fatal(err)
	}
	store, err := profiler.OpenStore(filepath.Join(t.TempDir(), "profiler.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plans, err := profiler.Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := store.Start(ctx, source, profiler.StartRequest{Name: "Report review", ConnectionKey: "fixture", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec("UPDATE records SET note='after'; UPDATE z_other SET note='after'"); err != nil {
		t.Fatal(err)
	}
	anchor, err = store.Scan(ctx, source, anchor.ID, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := backupRoundTestApp(t)
	a.profilerStore = store
	a.lastScreenW, a.lastScreenH = 120, 30
	screen := tcell.NewSimulationScreen("UTF-8")
	a.app.SetScreen(screen)
	screen.SetSize(120, 30)
	t.Cleanup(screen.Fini)
	a.showProfilerReport(anchor)
	container := a.pages.GetPage(pageProfilerReport).(*tview.Flex)
	body := container.GetItem(0).(*tview.Flex)
	list, grid := body.GetItem(0).(*tview.List), body.GetItem(1).(*tview.Table)
	if grid.GetRowCount() != profilerReportPageSize+1 || !strings.Contains(grid.GetTitle(), "1–200 of 425") {
		t.Fatalf("first page: rows=%d title=%s", grid.GetRowCount(), grid.GetTitle())
	}
	seen := map[string]bool{}
	for page := 0; page < 3; page++ {
		for row := 1; row < grid.GetRowCount(); row++ {
			key := grid.GetCell(row, 1).Text
			if seen[key] {
				t.Fatalf("row repeated on report page %d: %s", page, key)
			}
			seen[key] = true
		}
		backupRoundKey(a, tcell.KeyRune, ']')
	}
	if len(seen) != count || grid.GetRowCount() != 26 || !strings.Contains(grid.GetTitle(), "401–425 of 425") {
		t.Fatalf("inaccessible changes: seen=%d rows=%d title=%s", len(seen), grid.GetRowCount(), grid.GetTitle())
	}
	backupRoundKey(a, tcell.KeyTab, 0)
	if a.app.GetFocus() != grid {
		t.Fatal("Tab did not focus the row grid")
	}
	backupRoundKey(a, tcell.KeyEnter, 0)
	if !a.pages.HasPage(pageProfilerDiffDetail) {
		t.Fatal("Enter did not open a row")
	}
	backupRoundKey(a, tcell.KeyEscape, 0)
	if a.app.GetFocus() != grid {
		t.Fatal("closing row details lost the grid focus")
	}
	backupRoundKey(a, tcell.KeyRune, '[')
	if !strings.Contains(grid.GetTitle(), "201–400 of 425") {
		t.Fatal("previous page did not load")
	}
	backupRoundKey(a, tcell.KeyBacktab, 0)
	if a.app.GetFocus() != list {
		t.Fatal("Shift+Tab did not return to table list")
	}
	list.SetCurrentItem(1)
	if !strings.Contains(grid.GetTitle(), "1–1 of 1") {
		t.Fatal("changing tables did not reset the report page")
	}
	for _, size := range [][2]int{{80, 24}, {120, 30}, {160, 45}} {
		screen.SetSize(size[0], size[1])
		a.app.ForceDraw()
		_, _, gridWidth, _ := grid.GetRect()
		if size[0] == 80 && gridWidth != 80 {
			t.Fatalf("small-terminal report grid width=%d, want full 80 columns", gridWidth)
		}
		exportBackupFormScreen(t, screen, fmt.Sprintf("profiler-report-%dx%d", size[0], size[1]))
	}
}

func TestProfilerDiffDetailKeepsLiteralValuesAndRestoresFocus(t *testing.T) {
	a := backupRoundTestApp(t)
	origin := a.app.GetFocus()
	full := "[red]literal[-]" + strings.Repeat("long value ", 50)
	diff := profiler.DiffRow{Kind: profiler.DiffUpdated,
		Before:         map[string]profiler.Value{"[blue]column": {Text: "", Null: true}, "empty": {Text: ""}},
		After:          map[string]profiler.Value{"[blue]column": {Text: full}},
		ChangedColumns: []string{"[blue]column"}}
	a.showProfilerDiffDetail(diff)
	table := a.app.GetFocus().(*tview.Table)
	if table.GetCell(1, 0).Text != tview.Escape("[blue]column") {
		t.Fatal("database column name interpreted as color markup")
	}
	if got := table.GetCell(1, 1).Text; got != "NULL (SQL)" {
		t.Fatalf("NULL lost its identity: %q", got)
	}
	if table.GetCell(1, 2).GetReference() != full || len(table.GetCell(1, 2).Text) >= len(full) {
		t.Fatal("detail preview lost full value or rendered the entire large value")
	}
	if table.GetCell(2, 1).Text != "\"\" (empty)" || table.GetCell(2, 2).Text != "— (absent)" {
		t.Fatal("empty text and absent value are indistinguishable")
	}
	if table.GetCell(2, 1).GetReference() != "" || table.GetCell(2, 2).GetReference() != nil || table.GetCell(1, 1).GetReference() != nil {
		t.Fatal("display markers replaced real cell values or made NULL/absent values into copyable text")
	}
	table.Select(1, 2)
	backupRoundKey(a, tcell.KeyEnter, 0)
	view, ok := a.app.GetFocus().(*tview.TextView)
	if !ok || !strings.HasPrefix(full, view.GetText(false)) || len(view.GetText(false)) >= len(full) {
		t.Fatal("cell view lost literal data or rendered the entire large value")
	}
	first := view.GetText(false)
	backupRoundKey(a, tcell.KeyRune, ']')
	second := view.GetText(false)
	backupRoundKey(a, tcell.KeyRune, ']')
	if got := first + second + view.GetText(false); got != full {
		t.Fatal("paging the cell skipped or repeated value bytes")
	}
	backupRoundKey(a, tcell.KeyRune, '[')
	if view.GetText(false) != second {
		t.Fatal("previous cell page did not restore its content")
	}
	backupRoundKey(a, tcell.KeyEscape, 0)
	if a.app.GetFocus() != table {
		t.Fatal("cell view did not return focus to the selected row")
	}
	backupRoundKey(a, tcell.KeyEscape, 0)
	if a.app.GetFocus() != origin {
		t.Fatal("row view did not restore its caller")
	}
}

func TestProfilerCellPagesBoundLargeValuesWithoutLosingBytes(t *testing.T) {
	for _, value := range []string{
		strings.Repeat("large text ", 100000),
		strings.Repeat("क्ष👩‍💻", 10000),
		"a" + strings.Repeat("\u0301", 10000),
		strings.Repeat("\xff\x80x", 2000),
	} {
		var restored strings.Builder
		for offset := 0; offset < len(value); {
			content, end := profilerCellPage(value, offset)
			if end <= offset || len(content) > 4096 {
				t.Fatalf("cell page failed to advance within its byte bound: %d..%d", offset, end)
			}
			restored.WriteString(content)
			offset = end
		}
		if restored.String() != value {
			t.Fatal("cell pages changed the original bytes")
		}
	}
}
