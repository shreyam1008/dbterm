package ui

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"
	profiler "github.com/shreyam1008/dbterm/internal/changeprofiler"
	"github.com/shreyam1008/dbterm/internal/config"
	_ "modernc.org/sqlite"
)

func TestProfilerMarkersRemainReadableWithoutColor(t *testing.T) {
	tests := []struct {
		summary profiler.TableSummary
		marker  string
	}{
		{summary: profiler.TableSummary{Inserted: 1}, marker: "+"},
		{summary: profiler.TableSummary{Updated: 1}, marker: "~"},
		{summary: profiler.TableSummary{Deleted: 1}, marker: "-"},
		{summary: profiler.TableSummary{SchemaChanged: true}, marker: "Δ"},
	}
	for _, test := range tests {
		if got := profilerTableMarker(test.summary); !strings.Contains(got, test.marker) {
			t.Fatalf("marker %q does not contain %q", got, test.marker)
		}
	}
}

func TestProfilerHighlightsVisibleUpdatedAndInsertedRows(t *testing.T) {
	ctx := context.Background()
	source, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	for _, statement := range []string{
		`CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO users VALUES(1,'before')`,
	} {
		if _, err := source.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	store, err := profiler.OpenStore(filepath.Join(t.TempDir(), "profiler.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	plans, err := profiler.Preflight(ctx, source, config.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	plans[0].Included = true
	anchor, err := store.Start(ctx, source, profiler.StartRequest{Name: "visual", ConnectionKey: "visual-db", Engine: config.SQLite, Tables: plans}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`UPDATE users SET name='after' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO users VALUES(2,'new')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Scan(ctx, source, anchor.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	summaries, err := store.TableSummaries(ctx, anchor.ID)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries=%#v err=%v", summaries, err)
	}

	results := newResultTable()
	results.SetCell(0, 0, tview.NewTableCell("ID").SetReference("id"))
	results.SetCell(0, 1, tview.NewTableCell("NAME").SetReference("name"))
	results.SetCell(1, 0, tview.NewTableCell("1").SetReference(newResultCellReference(int64(1), "1")))
	results.SetCell(1, 1, tview.NewTableCell("after").SetReference(newResultCellReference("after", "after")))
	results.SetCell(2, 0, tview.NewTableCell("2").SetReference(newResultCellReference(int64(2), "2")))
	results.SetCell(2, 1, tview.NewTableCell("new").SetReference(newResultCellReference("new", "new")))
	app := &App{results: results, profilerStore: store, profilerAnchorID: anchor.ID,
		profilerTableChanges: map[string]profiler.TableSummary{"users": summaries[0]}}
	app.applyProfilerHighlightsToResults("users")

	_, updatedIDBackground, _ := results.GetCell(1, 0).Style.Decompose()
	_, updatedNameBackground, _ := results.GetCell(1, 1).Style.Decompose()
	_, insertedBackground, _ := results.GetCell(2, 0).Style.Decompose()
	if updatedIDBackground != updateRowBG {
		t.Fatalf("updated row background = %v", updatedIDBackground)
	}
	if updatedNameBackground != updateCellBG {
		t.Fatalf("changed cell background = %v", updatedNameBackground)
	}
	if insertedBackground != insertRowBG {
		t.Fatalf("inserted row background = %v", insertedBackground)
	}

	if !app.setResultRowSelected(1, true) || !app.setResultRowSelected(1, false) {
		t.Fatal("row selection did not toggle")
	}
	_, restoredBackground, _ := results.GetCell(1, 1).Style.Decompose()
	if restoredBackground != updateCellBG {
		t.Fatalf("deselect lost profiler background: %v", restoredBackground)
	}
	label := app.tableSidebarLabel("users", "users")
	if !strings.Contains(label, "~") {
		t.Fatalf("changed table label = %q", label)
	}
	if !isSelectableTableLabel(label) {
		t.Fatalf("changed table became non-selectable: %q", label)
	}
}

func TestProfilerProgressRendersPercentAndPosition(t *testing.T) {
	text := renderProfilerProgress(profiler.Progress{
		Phase: "capturing", Table: "registrations", TableIndex: 2, TableCount: 5,
		Rows: 500, EstimatedRows: 1000, Bytes: 4096, Percent: 40, Approximate: true,
	}, 2*time.Second, 20)
	for _, wanted := range []string{"CAPTURING", "table 2/5", "registrations", "~40%", "500 / ~1000 rows", "4.0 KB"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("progress missing %q:\n%s", wanted, text)
		}
	}
}
