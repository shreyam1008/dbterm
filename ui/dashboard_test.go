package ui

import (
	"testing"

	"github.com/rivo/tview"
)

func TestDashboardFooterChoosesTextThatFits(t *testing.T) {
	for _, test := range []struct {
		name           string
		hasConnections bool
		hasWorkspace   bool
		width          int
		palette        string
	}{
		{name: "connected medium", hasConnections: true, hasWorkspace: true, width: 128, palette: "Ctrl+P"},
		{name: "connected long custom palette", hasConnections: true, width: 152, palette: "Ctrl+Shift+F12 / Alt+P"},
		{name: "empty dashboard", width: 104, palette: "Ctrl+P"},
	} {
		t.Run(test.name, func(t *testing.T) {
			footer := dashboardFooterText(test.hasConnections, test.hasWorkspace, test.width, test.palette)
			if visibleWidth := tview.TaggedStringWidth(footer); visibleWidth > test.width {
				t.Fatalf("footer width = %d, terminal width = %d\n%s", visibleWidth, test.width, footer)
			}
		})
	}
}
