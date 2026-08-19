package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestPlainShortcutRuneRejectsGlobalModifiers(t *testing.T) {
	tests := []struct {
		name  string
		event *tcell.EventKey
		want  rune
		ok    bool
	}{
		{name: "lowercase", event: tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone), want: 'c', ok: true},
		{name: "shifted rune", event: tcell.NewEventKey(tcell.KeyRune, 'C', tcell.ModShift), want: 'c', ok: true},
		{name: "alt belongs global", event: tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModAlt), ok: false},
		{name: "ctrl belongs global", event: tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModCtrl), ok: false},
		{name: "navigation key", event: tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := plainShortcutRune(test.event)
			if ok != test.ok || got != test.want {
				t.Fatalf("plainShortcutRune() = (%q, %v), want (%q, %v)", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestAdaptiveShortcutFootersFitCommonWidths(t *testing.T) {
	footerCases := []struct {
		name  string
		width int
		text  string
	}{
		{name: "services narrow", width: 52, text: servicesFooterText(52)},
		{name: "services wide", width: 120, text: servicesFooterText(120)},
		{name: "profiler narrow", width: 52, text: changeProfilerFooterText(52)},
		{name: "profiler wide", width: 120, text: changeProfilerFooterText(120)},
		{name: "profiler table review", width: 76, text: profilerTableReviewFooterText(76)},
		{name: "profiler report", width: 64, text: profilerReportFooterText(64)},
		{name: "related rows", width: 76, text: relatedDataFooterText(76, 4)},
		{name: "same value", width: 68, text: sameValueMatchesFooterText(68, 3)},
		{name: "row detail", width: 48, text: rowDetailFooterText(48)},
		{name: "result filter", width: 72, text: resultFilterFooterText(72)},
		{name: "result export", width: 72, text: resultExportFooterText(72)},
		{name: "backup history", width: 52, text: backupHistoryFooterText(52)},
		{name: "backup agent logs", width: 64, text: backupAgentLogsFooterText(64)},
	}

	for _, test := range footerCases {
		t.Run(test.name, func(t *testing.T) {
			if got := tview.TaggedStringWidth(test.text); got > test.width {
				t.Fatalf("footer width = %d, available = %d: %s", got, test.width, test.text)
			}
			if test.text == "" {
				t.Fatal("adaptive footer is empty")
			}
		})
	}
}

func TestAdaptiveMultilineFootersFitCommonWidths(t *testing.T) {
	footerCases := []struct {
		name  string
		width int
		text  string
	}{
		{name: "instant backup narrow", width: 78, text: instantBackupFooterText(78)},
		{name: "instant backup wide", width: 116, text: instantBackupFooterText(116)},
		{name: "backup plan narrow", width: 68, text: backupPlanFormFooterText(68)},
		{name: "backup plan wide", width: 104, text: backupPlanFormFooterText(104)},
	}

	for _, test := range footerCases {
		t.Run(test.name, func(t *testing.T) {
			for lineNumber, line := range strings.Split(test.text, "\n") {
				if got := tview.TaggedStringWidth(line); got > test.width {
					t.Fatalf("footer line %d width = %d, available = %d: %s", lineNumber+1, got, test.width, line)
				}
			}
			if test.text == "" {
				t.Fatal("adaptive footer is empty")
			}
		})
	}
}

func TestFooterTextThatFitsKeepsRichestAvailableCandidate(t *testing.T) {
	full := "[yellow]Enter[-] Open  │  [yellow]C[-] Copy  │  [yellow]Esc[-] Back"
	short := "[yellow]C[-] Copy  │  [yellow]Esc[-] Back"
	if got := footerTextThatFits(80, full, short); got != full {
		t.Fatalf("wide footer = %q, want full candidate", got)
	}
	if got := footerTextThatFits(24, full, short); got != short {
		t.Fatalf("narrow footer = %q, want short candidate", got)
	}
}

func TestWorkspacePanelTitleUsesEffectiveShortcut(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Keymap[config.ActionFocusResults] = []string{"f9"}
	app := &App{settings: settings}

	if got := app.workspacePanelTitle(iconResults, "Results", actionFocusResults, " — ready"); !strings.Contains(got, "[yellow](F9)[-] — ready") {
		t.Fatalf("workspace panel title did not use configured shortcut: %q", got)
	}
	if got := replacePanelShortcut(" Results [yellow](Alt+R)[-] — 4 rows ", "Results", "F9"); got != " Results [yellow](F9)[-] — 4 rows " {
		t.Fatalf("replacePanelShortcut() = %q", got)
	}
}
