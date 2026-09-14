package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func styleBackupFormControls(form *tview.Form) {
	form.SetButtonActivatedStyle(tcell.StyleDefault.Foreground(crust).Background(green))
	for index := 0; index < form.GetFormItemCount(); index++ {
		switch field := form.GetFormItem(index).(type) {
		case *tview.InputField:
			// InputField's outer Box is independent of its inner text area.
			field.SetBackgroundColor(bg)
			// tview's fixed-width input can paint beyond its assigned rectangle.
			// Let long text fields use the space available after the form label.
			if field.GetFieldWidth() > 24 {
				field.SetFieldWidth(0)
			}
		case *tview.DropDown:
			field.SetUseStyleTags(false)
			field.SetFocusedStyle(tcell.StyleDefault.Foreground(blue).Background(surface0))
			field.SetListStyles(tcell.StyleDefault.Foreground(text).Background(mantle), tcell.StyleDefault.Foreground(green).Background(surface1))
		case *tview.Checkbox:
			field.SetUncheckedString("□").SetCheckedString("✓")
			field.SetActivatedStyle(tcell.StyleDefault.Foreground(green).Background(surface1))
		}
	}
}

// Section captions span the form instead of inheriting its aligned label
// column. Otherwise a one-line caption wraps and tview discards its beginning.
type backupFormSection struct{ *tview.TextView }

func (section *backupFormSection) SetFormAttributes(_ int, labelColor, bgColor, fieldTextColor, fieldBgColor tcell.Color) tview.FormItem {
	section.TextView.SetFormAttributes(0, labelColor, bgColor, fieldTextColor, fieldBgColor)
	return section
}

// Keep the form centered and its footer reachable after a terminal resize.
type backupFormModal struct {
	*tview.Grid
	preferredWidth, preferredHeight int
	footer                          *tview.TextView
	footerText                      func(int) string
	heightHint                      func() int
}

func newBackupFormModal(content tview.Primitive, width, height int, footer *tview.TextView, footerText func(int) string) *backupFormModal {
	return &backupFormModal{Grid: backupModalGrid(content, width, height), preferredWidth: width, preferredHeight: height, footer: footer, footerText: footerText}
}

func (modal *backupFormModal) Draw(screen tcell.Screen) {
	width, height := screen.Size()
	width = max(1, min(modal.preferredWidth, width-4))
	preferredHeight := modal.preferredHeight
	if modal.heightHint != nil {
		preferredHeight = modal.heightHint()
	}
	height = max(1, min(preferredHeight, height-2))
	modal.SetColumns(0, width, 0).SetRows(0, height, 0)
	modal.footer.SetText(modal.footerText(width))
	modal.Grid.Draw(screen)
}

func backupFormContentHeight(form *tview.Form, padding int) int {
	height := 7 // Border, inner padding, button row, and footer.
	for index := 0; index < form.GetFormItemCount(); index++ {
		height += form.GetFormItem(index).GetFieldHeight() + padding
	}
	return height
}
