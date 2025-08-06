package ui

import (
	"database/sql"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

var (
	bg       = tcell.NewRGBColor(30, 30, 46)    //#1e1e2e
	mantle   = tcell.NewRGBColor(24, 24, 37)    //#181825
	green    = tcell.NewRGBColor(166, 227, 161) //#a6e3a1
	surface1 = tcell.NewRGBColor(69, 71, 90)    //#45475a
	red      = tcell.NewRGBColor(243, 139, 168) //#f38ba8
	peach    = tcell.NewRGBColor(255, 180, 150) //#ffb4a6
	blue     = tcell.NewRGBColor(137, 180, 250) //#89b4fa
)

type App struct {
	app           *tview.Application
	db            *sql.DB
	pages         *tview.Pages
	tables        *tview.List
	selectedTable string
	results       *tview.Table
	queryInput    *tview.TextArea
}

func NewApp() *App {
	return &App{
		app: tview.NewApplication(),
	}
}

func (a *App) setupUI() {
	// Set global styles
	tview.Styles.PrimitiveBackgroundColor = bg
	tview.Styles.ContrastBackgroundColor = bg
	a.results = tview.NewTable().SetBorders(true)
	a.results.SetBorder(true).SetTitle("Results (alt + r)")
	a.tables = tview.NewList().ShowSecondaryText(false)
	a.tables.SetBorder(true).SetTitle("Tables (alt + t)")
	a.queryInput = tview.NewTextArea().
		SetPlaceholder("Type your query here, [alt + enter] to execute").SetPlaceholderStyle(tcell.StyleDefault.Foreground(blue))
	a.queryInput.SetBorder(true).SetTitle("Query (alt + q)")

	// Right side: query on top, results below
	rightFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.queryInput, 0, 1, false).
		AddItem(a.results, 0, 2, false)

	// Main layout
	flex := tview.NewFlex().
		AddItem(a.tables, 0, 1, true).
		AddItem(rightFlex, 0, 3, false)

	// Page Manager
	a.pages = tview.NewPages()
	a.pages.AddPage("main", flex, true, false)

	// Setup query execution
	a.queryInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter && event.Modifiers()&tcell.ModAlt != 0 {
			a.ExecuteQuery(a.queryInput.GetText())
			return nil
		}
		return event
	})
}

func (a *App) setupKeyBindings() {
	a.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		page, _ := a.pages.GetFrontPage()
		if page != "main" {
			return event
		}
		if event.Modifiers()&tcell.ModAlt != 0 {
			switch event.Rune() {
			case 't', 'T':
				a.app.SetFocus(a.tables)
				return nil
			case 'q', 'Q':
				a.app.SetFocus(a.queryInput)
				return nil
			case 'r', 'R':
				a.app.SetFocus(a.results)
				return nil
			}
		}
		return event
	})
}

func (a *App) Run() error {
	a.setupUI()
	form := a.showConnectionForm()
	a.setupKeyBindings()

	return a.app.SetRoot(a.pages, true).
		SetFocus(form).
		EnableMouse(true).
		Run()
}
