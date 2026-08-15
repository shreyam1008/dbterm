package ui

import (
	"strings"
	"testing"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
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

func TestDashboardConnectionDetailShowsDatabaseInsideScope(t *testing.T) {
	detail := dashboardConnectionDetail(config.ConnectionConfig{
		Type: config.PostgreSQL, Host: "pg.example.com", Port: "5432", User: "alice", Database: "orders",
	})
	for _, want := range []string{"PostgreSQL server", "pg.example.com:5432", "database orders", "default", "user alice"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("dashboard detail %q does not contain %q", detail, want)
		}
	}

	local := dashboardConnectionDetail(config.ConnectionConfig{Type: config.SQLite, FilePath: "/data/local.db"})
	if !strings.Contains(local, "Local file") || !strings.Contains(local, "/data/local.db") {
		t.Fatalf("SQLite detail does not show its local scope: %q", local)
	}

	server := dashboardConnectionDetail(config.ConnectionConfig{
		Type: config.MySQL, Host: "mysql.example.com", Port: "3306", User: "alice",
	})
	if !strings.Contains(server, "choose database on connect") || strings.Contains(server, "(default)") {
		t.Fatalf("server-only detail does not explain database choice: %q", server)
	}
}
