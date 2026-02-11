package ui

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

	// Layout components for scaling
	rightFlex *tview.Flex
	mainFlex  *tview.Flex

	// Sorting state
	sortColumn int  // current sort column index (-1 = none)
	sortAsc    bool // true = ascending

	// UI state
	tableExpanded bool // results fullscreen mode
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

	// init sorting state
	a.sortColumn = -1
	a.sortAsc = true

	// ── Results Table ──
	a.results = tview.NewTable().
		SetBorders(true).
		SetSelectable(true, true).
		SetFixed(1, 0). // ★ Freeze header row
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
	a.rightFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.queryInput, 0, 1, false).
		AddItem(a.results, 0, 2, false)

	a.mainFlex = tview.NewFlex().
		AddItem(a.tables, 0, 1, true).
		AddItem(a.rightFlex, 0, 3, false)

	mainLayout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.mainFlex, 0, 1, true).
		AddItem(a.statusBar, 1, 0, false)

	a.pages = tview.NewPages()
	a.pages.AddPage("main", mainLayout, true, false)

	// ── Results table input: sort on 's', key navigation ──
	a.results.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case 's', 'S':
			// Sort by current column
			_, col := a.results.GetSelection()
			a.toggleSort(col)
			a.results.Select(1, col) // Ensure selection stays visible
			return nil
		}
		return event
	})

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

	info += "  │  [yellow]F5[-] Refresh  │  [yellow]Alt+H[-] Help  │  [yellow]Alt+D[-] Dashboard  │  [yellow]Alt+S[-] Services"
	a.statusBar.SetText(info)
}

// setFocusWithColor sets focus to a panel and updates border colors to indicate active panel
func (a *App) setFocusWithColor(target tview.Primitive) {
	// Reset all panel borders to inactive color
	a.tables.SetBorderColor(surface1)
	a.queryInput.SetBorderColor(surface1)
	a.results.SetBorderColor(surface1)

	// Set the focused panel border to its accent color
	switch target {
	case a.tables:
		a.tables.SetBorderColor(mauve)
	case a.queryInput:
		a.queryInput.SetBorderColor(blue)
	case a.results:
		a.results.SetBorderColor(green)
	}

	a.app.SetFocus(target)
}

// cycleFocus cycles Tab focus: Tables → Query → Results → Tables
func (a *App) cycleFocus() {
	current := a.app.GetFocus()
	switch current {
	case a.tables:
		a.setFocusWithColor(a.queryInput)
	case a.queryInput:
		a.setFocusWithColor(a.results)
	default:
		a.setFocusWithColor(a.tables)
	}
}

// toggleExpandResults toggles between fullscreen results and normal layout
func (a *App) toggleExpandResults() {
	if a.tableExpanded {
		// Restore normal layout
		a.mainFlex.Clear()
		a.mainFlex.AddItem(a.tables, 0, 1, true)
		a.mainFlex.AddItem(a.rightFlex, 0, 3, false)

		a.rightFlex.Clear()
		a.rightFlex.AddItem(a.queryInput, 0, 1, false)
		a.rightFlex.AddItem(a.results, 0, 2, false)

		a.tableExpanded = false
		a.setFocusWithColor(a.results)
	} else {
		// Expand results to fill everything
		a.mainFlex.Clear()
		a.mainFlex.AddItem(a.results, 0, 1, true)

		a.tableExpanded = true
		a.setFocusWithColor(a.results)
	}
}

// refreshData reloads tables list and current table data
func (a *App) refreshData() {
	if a.db == nil {
		return
	}
	a.LoadTables()
	if a.selectedTable != "" {
		a.LoadResults()
	}
	a.updateStatusBar("[green]↻ Refreshed[-]", 0)
}

// toggleSort updates sort state and applies it
func (a *App) toggleSort(col int) {
	// Toggle sort direction if same column, else reset to ascending
	if a.sortColumn == col {
		a.sortAsc = !a.sortAsc
	} else {
		a.sortColumn = col
		a.sortAsc = true
	}
	a.applySort()
}

// applySort sorts the results table based on current sort state
func (a *App) applySort() {
	col := a.sortColumn
	if col == -1 {
		return
	}

	rowCount := a.results.GetRowCount()
	if rowCount <= 2 { // header + at most 1 row, nothing to sort
		return
	}

	colCount := a.results.GetColumnCount()
	if col < 0 || col >= colCount {
		return
	}

	// Collect data rows (skip header at row 0)
	type rowData struct {
		cells []*tview.TableCell
	}
	rows := make([]rowData, 0, rowCount-1)
	for r := 1; r < rowCount; r++ {
		rd := rowData{cells: make([]*tview.TableCell, colCount)}
		for c := 0; c < colCount; c++ {
			rd.cells[c] = a.results.GetCell(r, c)
		}
		rows = append(rows, rd)
	}

	// Sort by the selected column
	asc := a.sortAsc
	sort.SliceStable(rows, func(i, j int) bool {
		textI := rows[i].cells[col].Text
		textJ := rows[j].cells[col].Text

		// Try numeric sort first
		numI, errI := strconv.ParseFloat(strings.TrimSpace(textI), 64)
		numJ, errJ := strconv.ParseFloat(strings.TrimSpace(textJ), 64)
		if errI == nil && errJ == nil {
			if asc {
				return numI < numJ
			}
			return numI > numJ
		}

		// Fall back to string sort (case-insensitive)
		if asc {
			return strings.ToLower(textI) < strings.ToLower(textJ)
		}
		return strings.ToLower(textI) > strings.ToLower(textJ)
	})

	// Re-apply sorted rows to the table
	for r, rd := range rows {
		for c, cell := range rd.cells {
			a.results.SetCell(r+1, c, cell)
		}
	}

	// Update header to show sort indicator
	for c := 0; c < colCount; c++ {
		headerCell := a.results.GetCell(0, c)
		// Strip any existing sort indicators
		name := strings.TrimRight(headerCell.Text, " ▲▼")
		if c == col {
			if a.sortAsc {
				headerCell.Text = name + " ▲"
			} else {
				headerCell.Text = name + " ▼"
			}
		} else {
			headerCell.Text = name
		}
	}
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

		// F5 — Refresh data (works from main page)
		if event.Key() == tcell.KeyF5 && page == "main" {
			a.refreshData()
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
			case 's', 'S':
				// Show service dashboard from anywhere
				a.showServiceDashboard()
				return nil
			}
		}

		if page != "main" {
			return event
		}

		// Tab — cycle focus between panels
		if event.Key() == tcell.KeyTab {
			a.cycleFocus()
			return nil
		}

		if event.Modifiers()&tcell.ModAlt != 0 {
			switch event.Rune() {
			case 't', 'T':
				a.setFocusWithColor(a.tables)
				return nil
			case 'q', 'Q':
				a.setFocusWithColor(a.queryInput)
				return nil
			case 'r', 'R':
				a.setFocusWithColor(a.results)
				return nil
			case 'f', 'F':
				a.toggleExpandResults()
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
