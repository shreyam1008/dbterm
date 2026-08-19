package ui

import (
	"strings"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// plainShortcutRune returns a normalized rune only for unmodified, contextual
// shortcuts. Shift is represented by the rune itself and remains allowed;
// Ctrl/Alt/Meta belong to the global keymap and must never leak into a page's
// single-letter actions.
func plainShortcutRune(event *tcell.EventKey) (rune, bool) {
	if event == nil || event.Key() != tcell.KeyRune || event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) != 0 {
		return 0, false
	}
	return unicode.ToLower(event.Rune()), true
}

func matchesPlainShortcut(event *tcell.EventKey, shortcuts ...rune) bool {
	r, ok := plainShortcutRune(event)
	if !ok {
		return false
	}
	for _, shortcut := range shortcuts {
		if r == unicode.ToLower(shortcut) {
			return true
		}
	}
	return false
}

// footerTextThatFits chooses the richest hint set that fits the available
// cells. Callers provide candidates from most to least detailed.
func footerTextThatFits(width int, candidates ...string) string {
	for _, candidate := range candidates {
		if width <= 0 || tview.TaggedStringWidth(candidate) <= width {
			return candidate
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[len(candidates)-1]
}

func (a *App) escapedActionShortcut(action keymapAction) string {
	return tview.Escape(a.effectiveActionShortcut(action))
}

func (a *App) taggedActionShortcut(action keymapAction) string {
	return "[yellow]" + a.escapedActionShortcut(action) + "[-]"
}

func (a *App) workspacePanelTitle(icon, label string, action keymapAction, suffix string) string {
	return " " + icon + " " + label + " [yellow](" + a.escapedActionShortcut(action) + ")[-]" + suffix + " "
}

// refreshWorkspaceShortcutLabels updates persistent panel chrome after the
// keymap is saved. Result titles may contain row counts or timing, so only the
// shortcut segment is replaced there.
func (a *App) refreshWorkspaceShortcutLabels() {
	if a == nil {
		return
	}
	if a.tables != nil {
		a.updateTableListTitle()
	}
	if a.queryInput != nil {
		a.queryInput.SetTitle(a.workspacePanelTitle(iconQuery, "Query", actionFocusQuery, ""))
	}
	if a.results != nil {
		a.results.SetTitle(replacePanelShortcut(a.results.GetTitle(), "Results", a.escapedActionShortcut(actionFocusResults)))
	}
}

func replacePanelShortcut(title, label, shortcut string) string {
	marker := label + " [yellow]("
	start := strings.Index(title, marker)
	if start < 0 {
		return title
	}
	shortcutStart := start + len(marker)
	endOffset := strings.Index(title[shortcutStart:], ")[-]")
	if endOffset < 0 {
		return title
	}
	return title[:shortcutStart] + shortcut + title[shortcutStart+endOffset:]
}
