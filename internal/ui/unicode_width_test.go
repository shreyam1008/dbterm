package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestIndicTextUsesOneCellPerGraphemeCluster(t *testing.T) {
	tests := map[string]int{
		"अखण्ड श्रेष्ठ":        6,
		"अच्युत थापा क्षेत्री": 9,
		"क्ष": 1,
		"श्र": 1,
	}

	for text, want := range tests {
		if got := tview.TaggedStringWidth(text); got != want {
			t.Errorf("TaggedStringWidth(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestUnicodeSymbolsKeepTheirTerminalWidths(t *testing.T) {
	tests := map[string]int{
		"👩‍💻":  2,
		"🏳️‍🌈": 2,
		"中":    2,
		"é":   1,
	}

	for text, want := range tests {
		if got := tview.TaggedStringWidth(text); got != want {
			t.Errorf("TaggedStringWidth(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestIndicTextOccupiesOneTerminalCell(t *testing.T) {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("initialize simulation screen: %v", err)
	}
	defer screen.Fini()
	screen.SetSize(8, 1)

	tview.Print(screen, "क्षA", 0, 0, 8, tview.AlignLeft, tcell.ColorWhite)
	screen.Show()
	cells, width, height := screen.GetContents()
	if width != 8 || height != 1 {
		t.Fatalf("simulation screen size = %dx%d, want 8x1", width, height)
	}
	if got := string(cells[0].Runes); got != "क्ष" {
		t.Fatalf("first cell contains %q, want %q", got, "क्ष")
	}
	if got := string(cells[1].Runes); got != "A" {
		t.Fatalf("second cell contains %q, want %q", got, "A")
	}
}

func TestTruncateForDisplayDoesNotSplitGraphemeClusters(t *testing.T) {
	value := strings.Repeat("क्ष", 6)
	if got := truncateForDisplay(value, 5); got != "क्षक्ष..." {
		t.Fatalf("truncateForDisplay(%q, 5) = %q, want %q", value, got, "क्षक्ष...")
	}
	if !resultCellDisplayIsTruncated(strings.Repeat("क्ष", maxCellPreviewRunes+1), "TEXT") {
		t.Fatal("resultCellDisplayIsTruncated did not count Indic grapheme clusters")
	}
}
