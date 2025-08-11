package ui

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

// ── Catppuccin Mocha ──────────────────────────────────────────────────
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

// App holds all TUI state for the dbterm application
type App struct {
	app    *tview.Application
	db     *sql.DB
	pages  *tview.Pages
	store  *config.Store
	dbType config.DBType
	dbName string // name of current connection (from config)

	// Main UI components
	tables        *tview.List
	selectedTable string
	results       *tview.Table
	queryInput    *tview.TextArea
	statusBar     *tview.TextView
	tableCount    int
	queryStart    time.Time
}

// NewApp creates a new dbterm application instance
func NewApp() *App {
	store, err := config.LoadStore()
	if store == nil {
		store = &config.Store{}
		if err != nil {
			fmt.Printf("⚠ Warning: could not load saved connections: %v\n", err)
		}
	}
	return &App{
		app:   tview.NewApplication(),
		store: store,
	}
}

func (a *App) setupUI() {
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
		SetPlaceholder("  Write SQL here — Alt+Enter to execute").
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
	a.updateStatusBar("", 0)

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
			query := a.queryInput.GetText()
			if query == "" {
				a.ShowAlert("No query to execute.\n\nType a SQL query and press Alt+Enter.", "main")
				return nil
			}
			a.queryStart = time.Now()
			a.ExecuteQuery(query)
			return nil
		}
		return event
	})
}

// updateStatusBar refreshes the bottom status bar with current state
func (a *App) updateStatusBar(extra string, rowCount int) {
	if a.db == nil {
		a.statusBar.SetText("  [gray]● Disconnected[-]  │  [yellow]Alt+H[-] Help  │  [yellow]Q[-] Quit")
		return
	}

	var dbIcon string
	switch a.dbType {
	case config.PostgreSQL:
		dbIcon = "[#89b4fa]⬢ PostgreSQL[-]"
	case config.MySQL:
		dbIcon = "[#f9e2af]⬡ MySQL[-]"
	case config.SQLite:
		dbIcon = "[#a6e3a1]◆ SQLite[-]"
	}

	info := fmt.Sprintf("  %s  │  [green]●[-] [white]%s[-]  │  [gray]%d tables[-]",
		dbIcon, a.dbName, a.tableCount)

	if rowCount > 0 {
		info += fmt.Sprintf("  │  [teal]%d rows[-]", rowCount)
	}
	if extra != "" {
		info += "  │  " + extra
	}

	info += "  │  [yellow]Alt+H[-] Help  │  [yellow]Alt+D[-] Dashboard"
	a.statusBar.SetText(info)
}

func (a *App) setupKeyBindings() {
	a.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		page, _ := a.pages.GetFrontPage()

		// Ctrl+C always quits
		if event.Key() == tcell.KeyCtrlC {
			a.cleanup()
			a.app.Stop()
			return nil
		}

		if event.Modifiers()&tcell.ModAlt != 0 {
			switch event.Rune() {
			case 'h', 'H':
				if page == "help" {
					a.pages.RemovePage("help")
					if a.db != nil {
						a.pages.ShowPage("main")
					} else {
						a.showDashboard()
					}
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

// cleanup gracefully closes the database connection
func (a *App) cleanup() {
	if a.db != nil {
		a.db.Close()
		a.db = nil
	}
}

// Run starts the application
func (a *App) Run() error {
	a.setupUI()
	a.setupKeyBindings()
	a.showDashboard()

	return a.app.SetRoot(a.pages, true).
		EnableMouse(true).
		Run()
}
