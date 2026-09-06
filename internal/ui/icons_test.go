package ui

import (
	"testing"
	"unicode/utf8"

	"github.com/rivo/tview"
)

func TestSharedIconsArePortableSingleCellGlyphs(t *testing.T) {
	icons := map[string]string{
		"dashboard": iconDashboard,
		"connect":   iconConnect,
		"help":      iconHelp,
		"services":  iconServices,
		"database":  iconDatabase,
		"tables":    iconTables,
		"query":     iconQuery,
		"results":   iconResults,
		"backup":    iconBackup,
		"pin":       iconPin,
		"back":      iconBack,
		"refresh":   iconRefresh,
		"dropdown":  iconDropdown,
		"info":      iconInfo,
		"warn":      iconWarn,
		"success":   iconSuccess,
		"fail":      iconFail,
	}

	for name, icon := range icons {
		t.Run(name, func(t *testing.T) {
			if utf8.RuneCountInString(icon) != 1 {
				t.Fatalf("icon %q contains %d runes, want 1", icon, utf8.RuneCountInString(icon))
			}
			if icon[0] < 0x21 || icon[0] > 0x7e {
				t.Fatalf("icon %q is not portable printable ASCII", icon)
			}
			if width := tview.TaggedStringWidth(icon); width != 1 {
				t.Fatalf("icon %q occupies %d terminal cells, want 1", icon, width)
			}
		})
	}
}
