package ui

import (
	"testing"

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
