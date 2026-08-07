package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestLoadingModalOwnershipRejectsOlderCompletion(t *testing.T) {
	app := &App{pages: tview.NewPages()}
	oldToken := app.loadingGeneration.Add(1)
	newToken := app.loadingGeneration.Add(1)
	if app.finishLoadingModal(oldToken) {
		t.Fatal("older loading operation dismissed the newer overlay")
	}
	if !app.finishLoadingModal(newToken) {
		t.Fatal("current loading operation could not finish its overlay")
	}
	if app.finishLoadingModal(newToken) {
		t.Fatal("loading token finished more than once")
	}
}

func TestReplacementLoadingModalRestoresUnderlyingFocus(t *testing.T) {
	app := &App{
		app:        tview.NewApplication(),
		pages:      tview.NewPages(),
		tables:     tview.NewList(),
		queryInput: tview.NewTextArea(),
		results:    tview.NewTable(),
		statusBar:  tview.NewTextView(),
	}
	app.pages.AddPage("main", app.results, true, true)
	app.focusedPanel = app.results
	app.app.SetFocus(app.results)

	oldToken := app.showLoadingModal("first")
	newToken := app.showLoadingModal("second")
	if app.finishLoadingModal(oldToken) {
		t.Fatal("older loader dismissed its replacement")
	}
	if !app.finishLoadingModal(newToken) {
		t.Fatal("replacement loader did not finish")
	}
	if focus := app.app.GetFocus(); focus != app.results {
		t.Fatalf("focus = %T, want Results", focus)
	}
	if app.pages.HasPage("loading") {
		t.Fatal("loading page remained after current loader finished")
	}
}

func TestLoadingCompletionDoesNotStealFocusFromNewerModal(t *testing.T) {
	app := &App{
		app:        tview.NewApplication(),
		pages:      tview.NewPages(),
		tables:     tview.NewList(),
		queryInput: tview.NewTextArea(),
		results:    tview.NewTable(),
		statusBar:  tview.NewTextView(),
	}
	app.pages.AddPage("main", app.results, true, true)
	app.focusedPanel = app.results
	app.app.SetFocus(app.results)
	loadingToken := app.showLoadingModal("working")

	alert := tview.NewModal().SetText("newer alert")
	app.pages.AddPage("alert", alert, true, true)
	app.app.SetFocus(alert)
	newerFocus := app.app.GetFocus()
	if !app.finishLoadingModal(loadingToken) {
		t.Fatal("current loader did not finish")
	}
	if focus := app.app.GetFocus(); focus != newerFocus {
		t.Fatalf("focus = %T, want newer alert focus %T", focus, newerFocus)
	}
	if !app.pages.HasPage("alert") {
		t.Fatal("newer alert was removed with underlying loader")
	}
}

func TestLoadingCancelWaitsForWorkerOutcome(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	origin := tview.NewTextView()
	pages.AddPage("origin", origin, true, true)
	application.SetRoot(pages, true).SetFocus(origin)
	app := &App{app: application, pages: pages, statusBar: tview.NewTextView()}

	cancelCalls := 0
	token := app.showLoadingModal("working", withLoadingCancelOutcome("Esc cancels", func() { cancelCalls++ }))
	modal, ok := pages.GetPage("loading").(*tview.Modal)
	if !ok {
		t.Fatalf("loading page = %T, want modal", pages.GetPage("loading"))
	}
	modal.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})
	modal.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})

	if cancelCalls != 1 {
		t.Fatalf("cancel calls = %d, want exactly one", cancelCalls)
	}
	if !pages.HasPage("loading") {
		t.Fatal("cancel request dismissed progress before the worker reported its outcome")
	}
	if !app.finishLoadingModal(token) {
		t.Fatal("worker could not finish loader after cancellation")
	}
	if pages.HasPage("loading") {
		t.Fatal("loading page remained after worker completion")
	}
	if application.GetFocus() != origin {
		t.Fatalf("focus = %T, want original primitive", application.GetFocus())
	}
}

func TestGlobalCtrlCPassesToCancellableLoadingModal(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	origin := tview.NewTextView()
	pages.AddPage("origin", origin, true, true)
	application.SetRoot(pages, true).SetFocus(origin)
	app := &App{app: application, pages: pages, statusBar: tview.NewTextView()}
	app.setupKeyBindings()

	cancelCalls := 0
	token := app.showLoadingModal("working", withLoadingCancelOutcome("Esc or Ctrl+C cancels", func() { cancelCalls++ }))
	event := tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModCtrl)
	returned := application.GetInputCapture()(event)
	if returned == nil {
		t.Fatal("global Ctrl+C capture consumed the event before the loading modal")
	}
	modal := pages.GetPage("loading").(*tview.Modal)
	modal.InputHandler()(returned, func(primitive tview.Primitive) { application.SetFocus(primitive) })
	if cancelCalls != 1 {
		t.Fatalf("cancel calls = %d, want one", cancelCalls)
	}
	if !pages.HasPage("loading") {
		t.Fatal("Ctrl+C dismissed loader before worker completion")
	}
	if !app.finishLoadingModal(token) {
		t.Fatal("worker could not finish Ctrl+C-canceled loader")
	}
}

func TestDefaultLoadingCancelDismissesImmediately(t *testing.T) {
	application := tview.NewApplication()
	pages := tview.NewPages()
	origin := tview.NewTextView()
	pages.AddPage("origin", origin, true, true)
	application.SetRoot(pages, true).SetFocus(origin)
	app := &App{app: application, pages: pages, statusBar: tview.NewTextView()}

	cancelCalls := 0
	app.showLoadingModal("working", withLoadingCancel("Esc cancels", func() { cancelCalls++ }))
	modal := pages.GetPage("loading").(*tview.Modal)
	modal.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})
	if cancelCalls != 1 {
		t.Fatalf("cancel calls = %d, want one", cancelCalls)
	}
	if pages.HasPage("loading") {
		t.Fatal("default cancel did not dismiss loader immediately")
	}
	if application.GetFocus() != origin {
		t.Fatalf("focus = %T, want original primitive", application.GetFocus())
	}
}
