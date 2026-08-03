package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

func TestResolvedResultLimitGuardsWideTables(t *testing.T) {
	t.Run("requested limit respected when below guard", func(t *testing.T) {
		if got := resolvedResultLimit(100, 4); got != 100 {
			t.Fatalf("resolvedResultLimit(100, 4) = %d, want 100", got)
		}
	})

	t.Run("adaptive mode caps wide tables", func(t *testing.T) {
		got := resolvedResultLimit(adaptiveTablePreviewLimit, 80)
		if got <= 0 || got >= maxResultRows {
			t.Fatalf("resolvedResultLimit(auto, 80) = %d, expected a guarded value between 1 and %d", got, maxResultRows)
		}
	})

	t.Run("column count always yields at least one row", func(t *testing.T) {
		if got := resolvedResultLimit(adaptiveTablePreviewLimit, 5000); got < 1 || got > 2 {
			t.Fatalf("resolvedResultLimit(auto, 5000) = %d, want a minimal guarded value", got)
		}
	})
}

func TestResultAllColumnWidthActionsUseTerminalSafeKeys(t *testing.T) {
	tests := []struct {
		name      string
		event     *tcell.EventKey
		wantDelta int
		wantReset bool
		wantOK    bool
	}{
		{name: "wider", event: tcell.NewEventKey(tcell.KeyRune, '>', tcell.ModShift), wantDelta: 1, wantOK: true},
		{name: "narrower", event: tcell.NewEventKey(tcell.KeyRune, '<', tcell.ModShift), wantDelta: -1, wantOK: true},
		{name: "reset", event: tcell.NewEventKey(tcell.KeyRune, '0', tcell.ModNone), wantReset: true, wantOK: true},
		{name: "plain plus remains selected column", event: tcell.NewEventKey(tcell.KeyRune, '+', tcell.ModShift)},
		{name: "modified key ignored", event: tcell.NewEventKey(tcell.KeyRune, '>', tcell.ModCtrl|tcell.ModShift)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delta, reset, ok := resultAllColumnWidthAction(tt.event)
			if delta != tt.wantDelta || reset != tt.wantReset || ok != tt.wantOK {
				t.Fatalf("resultAllColumnWidthAction() = (%d,%v,%v), want (%d,%v,%v)", delta, reset, ok, tt.wantDelta, tt.wantReset, tt.wantOK)
			}
		})
	}
}

func TestQuoteIdentifierQualifiedNames(t *testing.T) {
	if got := quoteIdentifier(config.PostgreSQL, "public.users"); got != `"public"."users"` {
		t.Fatalf("postgres qualified quote = %q", got)
	}

	if got := quoteIdentifier(config.MySQL, "app.users"); got != "`app`.`users`" {
		t.Fatalf("mysql qualified quote = %q", got)
	}
}

func TestResultRowSelectionPreservesCellValueReference(t *testing.T) {
	app := &App{results: newResultTable()}
	app.results.SetCell(0, 0, tview.NewTableCell("ID").SetReference("id"))
	app.results.SetCell(1, 0, tview.NewTableCell("42").SetReference(resultCellReference{value: "42"}))

	if !app.setResultRowSelected(1, true) {
		t.Fatal("expected row selection state to change")
	}
	ref, ok := app.results.GetCell(1, 0).GetReference().(resultCellReference)
	if !ok {
		t.Fatal("cell reference type was not preserved")
	}
	if ref.value != "42" || !ref.rowSelected {
		t.Fatalf("cell reference = %#v, want value 42 and selected", ref)
	}
}
