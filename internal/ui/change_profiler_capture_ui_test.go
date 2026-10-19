package ui

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	profiler "github.com/shreyam1008/dbterm/internal/changeprofiler"
	"github.com/shreyam1008/dbterm/internal/config"
)

func checkpointCaptureTestApp(width, height int, engine config.DBType, readOnly bool) *App {
	a := &App{
		app: tview.NewApplication(), pages: tview.NewPages(), db: &sql.DB{}, dbType: engine,
		activeConn:  &config.ConnectionConfig{Type: engine, ReadOnly: readOnly},
		lastScreenW: width, lastScreenH: height,
	}
	a.app.SetRoot(a.pages, true)
	a.pages.SetRect(0, 0, width, height)
	return a
}

// Dispatch through the actual page hierarchy: consent shortcuts live on the
// parent panel, so calling the focused child's handler would miss them.
func checkpointCaptureKey(t *testing.T, a *App, key tcell.Key) {
	checkpointCaptureEvent(t, a, tcell.NewEventKey(key, 0, tcell.ModNone))
}
func checkpointCaptureEvent(t *testing.T, a *App, event *tcell.EventKey) {
	t.Helper()
	// Grid records its visible children during drawing, as in the app event loop.
	checkpointCaptureScreen(t, a)
	a.pages.InputHandler()(event, func(p tview.Primitive) { a.app.SetFocus(p) })
}

func checkpointCaptureScreen(t *testing.T, a *App) string {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(a.lastScreenW, a.lastScreenH)
	a.pages.Draw(screen)
	screen.Show()
	cells, width, height := screen.GetContents()
	var rendered strings.Builder
	for row := 0; row < height; row++ {
		for column := 0; column < width; column++ {
			cell := cells[row*width+column]
			if len(cell.Runes) == 0 {
				rendered.WriteByte(' ')
			} else {
				rendered.WriteString(string(cell.Runes))
			}
		}
		rendered.WriteByte('\n')
	}
	return rendered.String()
}

func TestCheckpointCaptureModeDefaultsRespectEngineAndReadOnly(t *testing.T) {
	tests := []struct {
		engine        config.DBType
		readOnly      bool
		wantIndex     int
		wantModeCount int
	}{
		{config.SQLite, false, 0, 2},
		{config.Turso, false, 0, 2},
		{config.MySQL, false, 0, 2},
		{config.PostgreSQL, false, 0, 2},
		{config.CloudflareD1, false, 0, 1},
		{config.PostgreSQL, true, 1, 2},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/readonly=%v", tt.engine, tt.readOnly), func(t *testing.T) {
			a := checkpointCaptureTestApp(80, 24, tt.engine, tt.readOnly)
			a.showProfilerNameForm("")
			checkpointCaptureKey(t, a, tcell.KeyTab)
			capture, ok := a.app.GetFocus().(*tview.DropDown)
			if !ok {
				t.Fatalf("Tab from Name did not focus Capture: %T", a.app.GetFocus())
			}
			index, _ := capture.GetCurrentOption()
			if index != tt.wantIndex || capture.GetOptionCount() != tt.wantModeCount {
				t.Fatalf("default capture index/options = %d/%d, want %d/%d", index, capture.GetOptionCount(), tt.wantIndex, tt.wantModeCount)
			}
		})
	}
}

func TestCheckpointJournalConsentFitsAndRequiresExplicitAction(t *testing.T) {
	for _, dimensions := range [][2]int{{80, 24}, {40, 12}} {
		t.Run(fmt.Sprintf("%dx%d", dimensions[0], dimensions[1]), func(t *testing.T) {
			a := checkpointCaptureTestApp(dimensions[0], dimensions[1], config.PostgreSQL, false)
			started, canceled := 0, 0
			open := func() {
				a.showProfilerJournalConsent(profilerJournalConsentText(config.PostgreSQL), func() { started++ }, func() { canceled++ })
			}
			open()
			body, ok := a.app.GetFocus().(*tview.TextView)
			if !ok {
				t.Fatalf("consent should focus its explanation first, got %T", a.app.GetFocus())
			}
			rendered := checkpointCaptureScreen(t, a)
			for _, control := range []string{"Start journal", "Cancel", "Tab actions"} {
				if !strings.Contains(rendered, control) {
					t.Fatalf("essential consent control %q is hidden:\n%s", control, rendered)
				}
			}
			if !strings.ContainsAny(rendered, "┌╔") || !strings.ContainsAny(rendered, "└╚") {
				t.Fatalf("consent extends beyond the terminal:\n%s", rendered)
			}
			checkpointCaptureKey(t, a, tcell.KeyEnter)
			if started != 0 || canceled != 0 {
				t.Fatal("Enter on the explanation performed an action")
			}
			if dimensions[0] == 40 {
				checkpointCaptureKey(t, a, tcell.KeyPgDn)
				checkpointCaptureScreen(t, a)
				row, _ := body.GetScrollOffset()
				if row == 0 {
					t.Fatal("narrow consent cannot scroll through the explanation")
				}
			}
			checkpointCaptureKey(t, a, tcell.KeyTab)
			if _, ok := a.app.GetFocus().(*tview.Button); !ok {
				t.Fatalf("Tab did not reach consent actions: %T", a.app.GetFocus())
			}
			checkpointCaptureKey(t, a, tcell.KeyBacktab)
			if a.app.GetFocus() != body {
				t.Fatal("Shift+Tab did not return to the explanation")
			}
			checkpointCaptureKey(t, a, tcell.KeyEscape)
			if started != 0 || canceled != 1 || a.pages.HasPage("profilerJournalConsent") {
				t.Fatal("Esc did not cancel without starting capture")
			}
			open()
			checkpointCaptureKey(t, a, tcell.KeyTab)
			checkpointCaptureKey(t, a, tcell.KeyTab)
			checkpointCaptureKey(t, a, tcell.KeyEnter)
			if started != 0 || canceled != 2 {
				t.Fatal("Cancel button did not cancel without starting capture")
			}
			open()
			checkpointCaptureKey(t, a, tcell.KeyTab)
			checkpointCaptureKey(t, a, tcell.KeyEnter)
			if started != 1 || canceled != 2 || a.pages.HasPage("profilerJournalConsent") {
				t.Fatal("explicit Start action did not start once and close consent")
			}
		})
	}
}

func TestCheckpointJournalReviewCancelPreservesSelection(t *testing.T) {
	a := checkpointCaptureTestApp(80, 24, config.SQLite, false)
	plans := []profiler.TablePlan{
		{Name: "customers", KeyKind: profiler.KeyPrimary, KeyColumns: []string{"id"}, Included: true},
		{Name: "legacy", KeyKind: profiler.KeyFullRow, Included: false},
	}
	a.showProfilerTableReview("Review fixture", plans, 0, profiler.CaptureJournal)
	checkpointCaptureKey(t, a, tcell.KeyEnter)
	if page, _ := a.pages.GetFrontPage(); page != "profilerJournalConsent" {
		t.Fatalf("table review did not require source-object consent: %s", page)
	}
	checkpointCaptureKey(t, a, tcell.KeyEscape)
	if page, _ := a.pages.GetFrontPage(); page != pageProfilerTableReview {
		t.Fatalf("cancel did not return to table selection: %s", page)
	}
	list, ok := a.app.GetFocus().(*tview.List)
	if !ok || list.GetItemCount() != 2 || !plans[0].Included || plans[1].Included {
		t.Fatal("cancel lost the table scope or list focus")
	}
}
