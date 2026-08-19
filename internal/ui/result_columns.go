package ui

import (
	"strings"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// handleResultColumnInput turns the fixed result header into a keyboard
// destination. Data-row shortcuts remain unchanged; plain typing searches
// columns only while row zero is selected.
func (a *App) handleResultColumnInput(event *tcell.EventKey) bool {
	if a == nil || a.results == nil || event == nil || a.results.GetColumnCount() == 0 {
		return false
	}
	row, col := a.results.GetSelection()
	if row == 1 && event.Key() == tcell.KeyUp {
		a.results.Select(0, col)
		a.refreshResultColumnStatus()
		return true
	}
	if row != 0 {
		return false
	}

	switch event.Key() {
	case tcell.KeyUp:
		return true
	case tcell.KeyDown, tcell.KeyEnter:
		a.clearResultColumnSearch()
		if a.currentResultRowCount() > 0 {
			a.results.Select(1, col)
		}
		a.refreshResultColumnStatus()
		return true
	case tcell.KeyLeft, tcell.KeyRight:
		if a.hasActiveResultColumnSearch() {
			a.clearResultColumnSearch()
		}
		return false
	case tcell.KeyHome:
		a.clearResultColumnSearch()
		a.results.Select(0, 0)
		a.refreshResultColumnStatus()
		return true
	case tcell.KeyEnd:
		a.clearResultColumnSearch()
		a.results.Select(0, a.results.GetColumnCount()-1)
		a.refreshResultColumnStatus()
		return true
	case tcell.KeyEscape:
		if a.hasActiveResultColumnSearch() {
			a.clearResultColumnSearch()
			return true
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if a.hasActiveResultColumnSearch() {
			runes := []rune(a.resultColumnSearch)
			a.resultColumnSearch = string(runes[:len(runes)-1])
			a.applyResultColumnSearch()
			return true
		}
	case tcell.KeyRune:
		// Mirror the Tables convention: uppercase C copies while lowercase
		// letters remain available for type-to-find.
		if !a.hasActiveResultColumnSearch() && isTableNameCopyKey(event) {
			a.copySelectedResultColumnName()
			return true
		}
		if event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) == 0 && unicode.IsPrint(event.Rune()) {
			a.resultColumnSearch += string(event.Rune())
			a.applyResultColumnSearch()
			return true
		}
	}
	return false
}

func (a *App) hasActiveResultColumnSearch() bool {
	return a != nil && a.resultColumnSearch != ""
}

func (a *App) resultHeaderSelected() bool {
	if a == nil || a.results == nil || a.results.GetColumnCount() == 0 {
		return false
	}
	row, _ := a.results.GetSelection()
	return row == 0
}

func (a *App) focusResultColumnHeader() {
	if a == nil || a.results == nil || a.results.GetColumnCount() == 0 {
		a.flashStatus("[yellow]No result columns are available[-]", a.currentResultRowCount(), 1600*time.Millisecond)
		return
	}
	_, col := a.results.GetSelection()
	col = clamp(col, 0, a.results.GetColumnCount()-1)
	a.results.Select(0, col)
	a.refreshResultColumnStatus()
}

func (a *App) clearResultColumnSearch() {
	if a == nil || a.resultColumnSearch == "" {
		return
	}
	a.resultColumnSearch = ""
	a.refreshResultColumnHeaders()
	a.refreshResultColumnStatus()
}

func (a *App) applyResultColumnSearch() {
	if a == nil || a.results == nil {
		return
	}
	firstMatch := -1
	for col := 0; col < a.results.GetColumnCount(); col++ {
		if _, _, matched := tableSearchMatchRange(a.resultColumnName(col), a.resultColumnSearch); matched {
			firstMatch = col
			break
		}
	}
	if firstMatch >= 0 {
		a.results.Select(0, firstMatch)
	}
	a.refreshResultColumnHeaders()
	a.refreshResultColumnStatus()
}

func (a *App) refreshResultColumnHeaders() {
	if a == nil || a.results == nil {
		return
	}
	for col := 0; col < a.results.GetColumnCount(); col++ {
		header := a.results.GetCell(0, col)
		if header == nil {
			continue
		}
		name := a.resultColumnName(col)
		if name == "" {
			continue
		}
		displayName := strings.ToUpper(name)
		label := tview.Escape(displayName)
		if a.resultColumnSearch != "" {
			if highlighted, matched := highlightTableSearchMatch(displayName, a.resultColumnSearch); matched {
				label = highlighted
			}
		}
		if col == a.sortColumn {
			if a.sortAsc {
				label += " ▲"
			} else {
				label += " ▼"
			}
		}
		if width := tview.TaggedStringWidth(label); header.MaxWidth > width {
			label += strings.Repeat(" ", header.MaxWidth-width)
		}
		header.SetText(label).
			SetSelectable(true).
			SetTextColor(peach).
			SetBackgroundColor(mantle)
	}
}

func (a *App) copySelectedResultColumnName() {
	if a == nil || a.results == nil {
		return
	}
	_, col := a.results.GetSelection()
	column := a.resultColumnName(col)
	if column == "" {
		a.flashStatus("[yellow]Select a result column to copy[-]", a.currentResultRowCount(), 1600*time.Millisecond)
		return
	}
	a.copyColumnName(column)
}

func (a *App) focusResultColumnByName(column string) bool {
	if a == nil || a.results == nil || strings.TrimSpace(column) == "" {
		return false
	}
	for index := 0; index < a.results.GetColumnCount(); index++ {
		if !strings.EqualFold(a.resultColumnName(index), column) {
			continue
		}
		a.results.Select(0, index)
		if a.app != nil {
			a.setFocusWithColor(a.results)
		}
		a.refreshResultColumnStatus()
		return true
	}
	return false
}

func (a *App) refreshResultColumnStatus() {
	if a != nil && a.statusBar != nil {
		a.updateStatusBar("", a.currentResultRowCount())
	}
}
