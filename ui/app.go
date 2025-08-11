package ui

import (
	"database/sql"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/shreyam1008/dbterm/config"
	"github.com/rivo/tview"
)

// ── Catppuccin Mocha Palette ──────────────────────────────────────────
var (
	bg       = tcell.NewRGBColor(30, 30, 46)    // #1e1e2e  base
	mantle   = tcell.NewRGBColor(24, 24, 37)    // #181825  mantle
	crust    = tcell.NewRGBColor(17, 17, 27)    // #11111b  crust
	green    = tcell.NewRGBColor(166, 227, 161) // #a6e3a1  green
	surface0 = tcell.NewRGBColor(49, 50, 68)    // #313244  surface0
	surface1 = tcell.NewRGBColor(69, 71, 90)    // #45475a  surface1
	red      = tcell.NewRGBColor(243, 139, 168) // #f38ba8  red
	peach    = tcell.NewRGBColor(255, 180, 150) // #ffb496  peach
	blue     = tcell.NewRGBColor(137, 180, 250) // #89b4fa  blue
	mauve    = tcell.NewRGBColor(203, 166, 247) // #cba6f7  mauve
	yellow   = tcell.NewRGBColor(249, 226, 175) // #f9e2af  yellow
	teal     = tcell.NewRGBColor(148, 226, 213) // #94e2d5  teal
	text     = tcell.NewRGBColor(205, 214, 244) // #cdd6f4  text
	subtext0 = tcell.NewRGBColor(166, 173, 200) // #a6adc8  subtext0
	overlay0 = tcell.NewRGBColor(108, 112, 134) // #6c7086  overlay0
)

// App is the main application struct holding all TUI state
type App struct {
	app    *tview.Application
	db     *sql.DB
	pages  *tview.Pages
	store  *config.Store
	dbType config.DBType

	// Main UI components
	tables        *tview.List
	selectedTable string
	results       *tview.Table
	queryInput    *tview.TextArea
	statusBar     *tview.TextView
	queryStart    time.Time
}

func NewApp() *App {
	store, _ := config.LoadStore()
	if store == nil {
		store = &config.Store{}
	}
	return &App{
		app:   tview.NewApplication(),
		store: store,
	}
}

func (a *App) setupUI() {
	// Set global styles
	tview.Styles.PrimitiveBackgroundColor = bg
	tview.Styles.ContrastBackgroundColor = bg

	// ── Results Table ──
	a.results = tview.NewTable().
		SetBorders(true).
		SetSelectable(true, false).
		SetSelectedStyle(tcell.StyleDefault.Background(blue).Foreground(crust))
	a.results.SetBorder(true).
		SetTitle(" Results [yellow](Alt+R)[-] ").
		SetBorderColor(surface1).
		SetTitleColor(peach)

	// ── Tables List ──
	a.tables = tview.NewList().ShowSecondaryText(false)
	a.tables.SetBorder(true).
		SetTitle(" Tables [yellow](Alt+T)[-] ").
		SetBorderColor(surface1).
		SetTitleColor(peach)

	// ── Query Input ──
	a.queryInput = tview.NewTextArea().
		SetPlaceholder("  Type your SQL query here...  [Alt+Enter] to execute").
		SetPlaceholderStyle(tcell.StyleDefault.Foreground(overlay0))
	a.queryInput.SetBorder(true).
		SetTitle(" Query [yellow](Alt+Q)[-] ").
		SetBorderColor(surface1).
		SetTitleColor(peach)

	// ── Status Bar ──
	a.statusBar = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	a.statusBar.SetBackgroundColor(crust)
	a.updateStatusBar()

	// ── Layout ──
	rightFlex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.queryInput, 0, 1, false).
		AddItem(a.results, 0, 2, false)

	mainFlex := tview.NewFlex().
		AddItem(a.tables, 0, 1, true).
		AddItem(rightFlex, 0, 3, false)

	mainLayout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(mainFlex, 0, 1, true).
		AddItem(a.statusBar, 1, 0, false)

	a.pages = tview.NewPages()
	a.pages.AddPage("main", mainLayout, true, false)

	// Execute query on Alt+Enter
	a.queryInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter && event.Modifiers()&tcell.ModAlt != 0 {
			a.queryStart = time.Now()
			a.ExecuteQuery(a.queryInput.GetText())
			return nil
		}
		return event
	})
}

func (a *App) updateStatusBar() {
	if a.db == nil {
		a.statusBar.SetText("  [gray]Not connected[-]  │  [yellow]Alt+H[-] Help")
		return
	}
	dbLabel := string(a.dbType)
	switch a.dbType {
	case config.PostgreSQL:
		dbLabel = "[blue]PostgreSQL[-]"
	case config.MySQL:
		dbLabel = "[yellow]MySQL[-]"
	case config.SQLite:
		dbLabel = "[green]SQLite[-]"
	}
	a.statusBar.SetText("  " + dbLabel + "  │  [green]● Connected[-]  │  [yellow]Alt+H[-] Help  │  [yellow]Alt+D[-] Dashboard")
}

func (a *App) setupKeyBindings() {
	a.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		page, _ := a.pages.GetFrontPage()
		if event.Modifiers()&tcell.ModAlt != 0 {
			switch event.Rune() {
			case 'h', 'H':
				if page == "help" {
					a.pages.HidePage("help")
					a.pages.ShowPage("main")
				} else {
					a.showHelp()
				}
				return nil
			case 'd', 'D':
				if page == "main" || page == "help" {
					a.pages.HidePage(page)
					a.showDashboard()
				}
				return nil
			}
		}
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
	a.setupKeyBindings()
	a.showDashboard()

	return a.app.SetRoot(a.pages, true).
		EnableMouse(true).
		Run()
}
