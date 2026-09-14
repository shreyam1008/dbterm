package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// A folder path stays editable while its chooser is visible on the same row.
// Focus still belongs to the input or button so text editing and mouse selection
// use tview's normal controls.
type backupFolderField struct {
	*tview.InputField
	browse                 *tview.Button
	fieldWidth, labelWidth int
	x, y, width, height    int
	delegate               func(tview.Primitive)
}

func newBackupFolderField(label, path string, width int, changed func(string), browse func()) *backupFolderField {
	field := &backupFolderField{
		InputField: tview.NewInputField().SetLabel(label).SetText(path).SetChangedFunc(changed),
		browse:     tview.NewButton("Browse…").SetSelectedFunc(browse),
		fieldWidth: width,
	}
	field.SetBackgroundColor(bg)
	field.browse.SetStyle(tcell.StyleDefault.Foreground(green).Background(surface1)).
		SetActivatedStyle(tcell.StyleDefault.Foreground(crust).Background(green))
	return field
}

func (field *backupFolderField) GetFieldWidth() int { return field.fieldWidth }

func (field *backupFolderField) SetFormAttributes(labelWidth int, labelColor, bgColor, fieldTextColor, fieldBgColor tcell.Color) tview.FormItem {
	field.labelWidth = labelWidth
	field.InputField.SetFormAttributes(labelWidth, labelColor, bgColor, fieldTextColor, fieldBgColor)
	field.SetBackgroundColor(bgColor)
	return field
}

func (field *backupFolderField) SetRect(x, y, width, height int) {
	field.x, field.y, field.width, field.height = x, y, width, height
	available := max(0, width-field.labelWidth)
	if field.fieldWidth > 0 {
		available = min(available, field.fieldWidth)
	}
	buttonWidth := min(11, max(0, available-2))
	pathWidth := max(1, available-buttonWidth-1)
	field.InputField.SetFieldWidth(pathWidth)
	field.InputField.SetRect(x, y, min(width, field.labelWidth+pathWidth), height)
	field.browse.SetRect(x+field.labelWidth+pathWidth+1, y, buttonWidth, height)
}

func (field *backupFolderField) GetRect() (int, int, int, int) {
	return field.x, field.y, field.width, field.height
}

func (field *backupFolderField) Draw(screen tcell.Screen) {
	field.InputField.Draw(screen)
	field.browse.Draw(screen)
}

func (field *backupFolderField) Focus(delegate func(tview.Primitive)) {
	field.delegate = delegate
	delegate(field.InputField)
}

func (field *backupFolderField) HasFocus() bool {
	return field.InputField.HasFocus() || field.browse.HasFocus()
}

func (field *backupFolderField) Blur() {
	field.InputField.Blur()
	field.browse.Blur()
}

func (field *backupFolderField) SetFinishedFunc(finished func(tcell.Key)) tview.FormItem {
	field.InputField.SetFinishedFunc(func(key tcell.Key) {
		if key == tcell.KeyTab && field.delegate != nil {
			field.delegate(field.browse)
		} else if finished != nil {
			finished(key)
		}
	})
	field.browse.SetExitFunc(func(key tcell.Key) {
		if key == tcell.KeyBacktab && field.delegate != nil {
			field.delegate(field.InputField)
		} else if finished != nil {
			finished(key)
		}
	})
	return field
}

func (field *backupFolderField) SetDisabled(disabled bool) tview.FormItem {
	field.browse.SetDisabled(disabled)
	field.InputField.SetDisabled(disabled)
	return field
}

func (field *backupFolderField) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
		if field.browse.HasFocus() {
			field.browse.InputHandler()(event, setFocus)
		} else {
			field.InputField.InputHandler()(event, setFocus)
		}
	}
}

func (field *backupFolderField) PasteHandler() func(string, func(tview.Primitive)) {
	return func(value string, setFocus func(tview.Primitive)) {
		if field.InputField.HasFocus() {
			field.InputField.PasteHandler()(value, setFocus)
		}
	}
}

func (field *backupFolderField) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		field.delegate = setFocus
		if consumed, capture := field.browse.MouseHandler()(action, event, setFocus); consumed {
			return consumed, capture
		}
		return field.InputField.MouseHandler()(action, event, setFocus)
	}
}
