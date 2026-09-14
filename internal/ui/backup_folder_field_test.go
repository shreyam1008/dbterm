package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

func TestBackupFolderFieldKeyboardMouseAndPaste(t *testing.T) {
	application := tview.NewApplication()
	form := tview.NewForm()
	path := filepath.Join(t.TempDir(), "folder with spaces")
	var selectedFrom []string
	field := newBackupFolderField("Save to", path, 48, nil, nil)
	field.browse.SetSelectedFunc(func() { selectedFrom = append(selectedFrom, field.GetText()) })
	next := tview.NewInputField().SetLabel("File name")
	form.AddFormItem(field).AddFormItem(next)
	application.SetRoot(form, true)
	screen := tcell.NewSimulationScreen("UTF-8")
	application.SetScreen(screen)
	screen.SetSize(80, 24)
	t.Cleanup(screen.Fini)
	application.ForceDraw()
	setFocus := func(p tview.Primitive) { application.SetFocus(p) }
	press := func(key tcell.Key) { form.InputHandler()(tcell.NewEventKey(key, 0, tcell.ModNone), setFocus) }
	assertFocus := func(want tview.Primitive) {
		t.Helper()
		if application.GetFocus() != want {
			t.Fatalf("focus is %T, want %T", application.GetFocus(), want)
		}
	}
	assertFocus(field.InputField)
	press(tcell.KeyTab)
	assertFocus(field.browse)
	// Pasting into Browse must not change the path behind it.
	form.PasteHandler()("unintended path", setFocus)
	press(tcell.KeyEnter)
	if len(selectedFrom) != 1 || selectedFrom[0] != path {
		t.Fatalf("Browse did not use the current path: %v", selectedFrom)
	}
	press(tcell.KeyBacktab)
	assertFocus(field.InputField)
	press(tcell.KeyCtrlA)
	press(tcell.KeyCtrlK)
	form.PasteHandler()(path+" edited", setFocus)
	if field.GetText() != path+" edited" {
		t.Fatalf("typed-path fallback lost pasted text: %q", field.GetText())
	}
	press(tcell.KeyTab)
	press(tcell.KeyTab)
	assertFocus(next)
	application.ForceDraw()
	// Click the visible inline action while a different field owns focus.
	x, y, width, _ := field.browse.GetRect()
	event := tcell.NewEventMouse(x+width/2, y, tcell.Button1, tcell.ModNone)
	for _, action := range []tview.MouseAction{tview.MouseLeftDown, tview.MouseLeftClick} {
		if consumed, _ := form.MouseHandler()(action, event, setFocus); !consumed {
			t.Fatalf("Browse did not consume mouse action %v", action)
		}
	}
	assertFocus(field.browse)
	if len(selectedFrom) != 2 || selectedFrom[1] != path+" edited" {
		t.Fatalf("mouse Browse used stale text: %v", selectedFrom)
	}
	press(tcell.KeyTab)
	assertFocus(next)
	// Resizing must update the button's hit area as well as its appearance.
	screen.SetSize(42, 16)
	application.ForceDraw()
	x, _, width, _ = field.browse.GetRect()
	if x+width > 42 || !strings.Contains(backupSimulationScreenText(screen), "Browse…") {
		t.Fatal("inline Browse is clipped after a resize")
	}
}

func TestBackupCopyFolderEditsSurviveRouteChangesAndSave(t *testing.T) {
	a := backupRoundTestApp(t)
	a.pages.AddAndSwitchToPage(pageBackupCopies, tview.NewList(), true)
	form := a.showBackupCopyForm(nil)
	source, destination := t.TempDir(), t.TempDir()
	form.GetFormItemByLabel("Source Folder").(*backupFolderField).SetText(source)
	form.GetFormItemByLabel("Destination Folder").(*backupFolderField).SetText(destination)
	form.GetFormItemByLabel("Route").(*tview.DropDown).SetCurrentOption(copyTopologyLocalSFTP)
	// Changing the route rebuilds the form and focuses its route selector.
	route := a.app.GetFocus().(*tview.DropDown)
	route.SetCurrentOption(copyTopologyLocalLocal)
	root := a.pages.GetPage(pageBackupCopyForm)
	press := func(key tcell.Key) {
		root.InputHandler()(tcell.NewEventKey(key, 0, tcell.ModNone), func(p tview.Primitive) { a.app.SetFocus(p) })
	}
	press(tcell.KeyTab)
	if field, ok := a.app.GetFocus().(*tview.InputField); !ok || field.GetLabel() != "Source Folder" || field.GetText() != source {
		t.Fatal("route change lost local source edit")
	}
	press(tcell.KeyTab) // Inline Browse.
	press(tcell.KeyTab)
	if field, ok := a.app.GetFocus().(*tview.InputField); !ok || field.GetLabel() != "Destination Folder" || field.GetText() != destination {
		t.Fatal("route change lost local destination edit")
	}
	saveReached := false
	for step := 0; step < 60; step++ {
		if button, ok := a.app.GetFocus().(*tview.Button); ok && button.GetLabel() == "Save Copy" {
			saveReached = true
			press(tcell.KeyEnter)
			break
		}
		press(tcell.KeyTab)
	}
	if !saveReached {
		t.Fatal("could not reach Save Copy from folder controls")
	}
	jobs, err := a.backupStore.ListCopyJobs(context.Background())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("save copy jobs=%d error=%v", len(jobs), err)
	}
	if jobs[0].Source.Location != source || jobs[0].Destination.Location != destination || jobs[0].Source.Kind != backupcore.CopyEndpointLocal {
		t.Fatalf("saved copy has wrong local folders: %#v", jobs[0])
	}
}
