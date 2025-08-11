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
	app        *tview.Application
	db         *sql.DB
	pages      *tview.Pages
	store      *config.Store
	dbType     config.DBType
	dbName     string // name of current connection (from config)
	activeConn *config.ConnectionConfig

	// Main UI components
	tables        *tview.List
	selectedTable string
	results       *tview.Table
	queryInput    *tview.TextArea
	statusBar     *tview.TextView
	tableCount    int
	queryStart    time.Time
	resultLimit   int // >0 preview rows, -1 means no limit (all rows)

	// Layout components for scaling
	rightFlex *tview.Flex
	mainFlex  *tview.Flex

	// Sorting state
	sortColumn int  // current sort column index (-1 = none)
	sortAsc    bool // true = ascending

	// UI state
	tableExpanded bool // results fullscreen mode
	lastScreenW   int
	lastScreenH   int
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
		app:         tview.NewApplication(),
		store:       store,
		resultLimit: defaultTablePreviewLimit,
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
		SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] ", iconResults)).
		SetBorderColor(surface1).
		SetTitleColor(peach)

	// ── Tables List ──
	a.tables = tview.NewList().ShowSecondaryText(false)
	a.tables.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Tables [yellow](Alt+T)[-] ", iconTables)).
		SetBorderColor(surface1).
		SetTitleColor(peach)

	// ── Query Input ──
	a.queryInput = tview.NewTextArea().
		SetPlaceholder("  Write SQL here — Alt+Enter to execute").
		SetPlaceholderStyle(tcell.StyleDefault.Foreground(overlay0))
	a.queryInput.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Query [yellow](Alt+Q)[-] ", iconQuery)).
		SetBorderColor(surface1).
		SetTitleColor(peach)

	// ── Status Bar ──
	a.statusBar = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft).
		SetWrap(false).
		SetWordWrap(false)
	a.statusBar.SetBackgroundColor(crust)
	a.updateStatusBar("", 0)

	// ── Layout ──
	a.rightFlex = tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.queryInput, 0, 1, false).
		AddItem(a.results, 0, 4, false) // Results get 80% vertical space

	a.mainFlex = tview.NewFlex().
		SetDirection(tview.FlexColumn)

	mainLayout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(a.mainFlex, 0, 1, true).
		AddItem(a.statusBar, 1, 0, false)

	a.pages = tview.NewPages()
	a.pages.AddPage("main", mainLayout, true, false)
	a.applyResponsiveLayout(120, 40)

	// ── Results table input: sort on 's', key navigation ──
	a.results.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case 's', 'S':
			// Sort by current column
			row, col := a.results.GetSelection()
			a.toggleSort(col)
			if row <= 0 {
				row = 1
			}
			a.results.Select(row, col)
			return nil
		case ' ':
			// Also support space for row details alongside Enter
			row, _ := a.results.GetSelection()
			if row > 0 {
				a.showRowDetail(row)
				return nil
			}
		}
		if event.Key() == tcell.KeyEnter {
			row, _ := a.results.GetSelection()
			if row > 0 {
				a.showRowDetail(row)
				return nil
			}
		}
		return event
	})

	// Execute query on Alt+Enter
	a.queryInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter && event.Modifiers()&tcell.ModAlt != 0 {
			query := a.queryInput.GetText()
			if query == "" {
				a.ShowAlert(fmt.Sprintf("%s No query to execute.\n\nType a SQL query and press Alt+Enter.", iconInfo), "main")
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
	width, _ := a.getScreenSize()
	actionText := a.statusActionText(width)

	if a.db == nil {
		if width < 58 {
			a.statusBar.SetText("  [gray]○[-]  [yellow]H[-]  [yellow]Q[-]")
			return
		}
		if width < 80 {
			a.statusBar.SetText("  [gray]○ offline[-]  │  [yellow]H[-] Help  │  [yellow]Q[-] Quit")
			return
		}
		a.statusBar.SetText(fmt.Sprintf("  [gray]○ offline[-]  │  %s no DB  │  [yellow]Alt+H[-] Help  │  [yellow]Q[-] Quit", iconConnect))
		return
	}

	var dbIcon, dbShort string
	switch a.dbType {
	case config.PostgreSQL:
		dbIcon = "[#89b4fa]⬢ PostgreSQL[-]"
		dbShort = "[#89b4fa]PG[-]"
	case config.MySQL:
		dbIcon = "[#f9e2af]⬡ MySQL[-]"
		dbShort = "[#f9e2af]MY[-]"
	case config.SQLite:
		dbIcon = "[#a6e3a1]◆ SQLite[-]"
		dbShort = "[#a6e3a1]SL[-]"
	default:
		dbShort = "[#6c7086]DB[-]"
	}

	nameMax := 22
	if width < 90 {
		nameMax = 14
	}
	if width < 70 {
		nameMax = 10
	}

	parts := []string{
		fmt.Sprintf("%s [green]●[-] %s [white]%s[-]", dbIcon, iconConnect, truncateForDisplay(a.dbName, nameMax)),
	}

	if width < 90 {
		parts[0] = fmt.Sprintf("%s [green]●[-] %s [white]%s[-]", dbShort, iconConnect, truncateForDisplay(a.dbName, nameMax))
	}

	if width >= 90 {
		parts = append(parts, fmt.Sprintf("[gray]%d tables[-]", a.tableCount))
	}
	if rowCount > 0 && width >= 64 {
		parts = append(parts, fmt.Sprintf("[teal]%d rows[-]", rowCount))
	}
	if width >= 84 {
		parts = append(parts, a.resultLimitStatus(width))
	}
	if width >= 104 {
		parts = append(parts, a.sortStatus(width))
	}
	if extra != "" && width >= 72 {
		parts = append(parts, extra)
	}

	parts = append(parts, actionText)
	a.statusBar.SetText("  " + strings.Join(parts, "  │  "))
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
		w, h := a.getScreenSize()
		a.tableExpanded = false
		a.lastScreenW = 0
		a.lastScreenH = 0
		a.applyResponsiveLayout(w, h)
		a.setFocusWithColor(a.results)
	} else {
		// Expand results to fill everything
		a.mainFlex.Clear()
		a.mainFlex.SetDirection(tview.FlexColumn)
		a.mainFlex.AddItem(a.results, 0, 1, true)

		a.tableExpanded = true
		a.setFocusWithColor(a.results)
	}
}

// refreshData reloads tables list and current table data
func (a *App) refreshData() error {
	if a.db == nil {
		return nil
	}

	currentTable := a.selectedTable
	currentTableIndex := a.tables.GetCurrentItem()

	if err := a.LoadTables(); err != nil {
		return err
	}

	if a.tableCount == 0 {
		a.selectedTable = ""
		a.results.Clear()
		a.results.SetTitle(fmt.Sprintf(" %s Results [yellow](Alt+R)[-] ", iconResults))
		a.updateStatusBar(fmt.Sprintf("[green]%s DB Refreshed[-]", iconRefresh), 0)
		return nil
	}

	if currentTable == "" {
		if name, _ := a.tables.GetItemText(a.tables.GetCurrentItem()); !strings.HasPrefix(name, "[") {
			a.selectedTable = name
		}
	} else if !a.tableExistsInList(currentTable) && currentTableIndex >= 0 && currentTableIndex < a.tableCount {
		if name, _ := a.tables.GetItemText(currentTableIndex); !strings.HasPrefix(name, "[") {
			a.selectedTable = name
			a.tables.SetCurrentItem(currentTableIndex)
		}
	}

	rowCount := 0
	if a.selectedTable != "" {
		if err := a.LoadResults(); err != nil {
			return err
		}
		rowCount = a.currentResultRowCount()
	}

	a.updateStatusBar(fmt.Sprintf("[green]%s DB Refreshed[-]", iconRefresh), rowCount)
	return nil
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
	a.updateStatusBar("", a.currentResultRowCount())
}

func (a *App) effectiveResultLimit() int {
	if a.resultLimit == 0 {
		return defaultTablePreviewLimit
	}
	return a.resultLimit
}

func (a *App) setResultLimit(limit int) {
	if limit == 0 {
		limit = defaultTablePreviewLimit
	}
	if limit != unlimitedTablePreviewLimit && limit < tablePreviewSteps[0] {
		limit = tablePreviewSteps[0]
	}
	if a.resultLimit == limit {
		return
	}

	prevLimit := a.resultLimit
	a.resultLimit = limit
	if a.db == nil || a.selectedTable == "" {
		a.updateStatusBar("", a.currentResultRowCount())
		return
	}

	if err := a.LoadResults(); err != nil {
		a.resultLimit = prevLimit
		a.ShowAlert(fmt.Sprintf("%s Could not refresh results after changing preview limit:\n\n%v", iconWarn, err), "main")
		return
	}
	a.flashStatus(fmt.Sprintf("[green]%s Preview %s[-]", iconRefresh, a.resultLimitReadable()), a.currentResultRowCount(), 1400*time.Millisecond)
}

func (a *App) increaseResultLimit() {
	current := a.effectiveResultLimit()
	if current == unlimitedTablePreviewLimit {
		return
	}
	next := unlimitedTablePreviewLimit
	for _, step := range tablePreviewSteps {
		if step > current {
			next = step
			break
		}
	}
	a.setResultLimit(next)
}

func (a *App) decreaseResultLimit() {
	current := a.effectiveResultLimit()
	if current == unlimitedTablePreviewLimit {
		a.setResultLimit(tablePreviewSteps[len(tablePreviewSteps)-1])
		return
	}

	prev := tablePreviewSteps[0]
	for _, step := range tablePreviewSteps {
		if step >= current {
			break
		}
		prev = step
	}
	a.setResultLimit(prev)
}

func (a *App) toggleUnlimitedResultLimit() {
	if a.effectiveResultLimit() == unlimitedTablePreviewLimit {
		a.setResultLimit(defaultTablePreviewLimit)
		return
	}
	a.setResultLimit(unlimitedTablePreviewLimit)
}

func (a *App) resultLimitReadable() string {
	if a.effectiveResultLimit() == unlimitedTablePreviewLimit {
		return "all rows"
	}
	return fmt.Sprintf("%d rows", a.effectiveResultLimit())
}

func (a *App) resultLimitStatus(width int) string {
	limit := a.effectiveResultLimit()
	if width < 120 {
		if limit == unlimitedTablePreviewLimit {
			return "[#a6adc8]lim[-]:[yellow]all[-]"
		}
		return fmt.Sprintf("[#a6adc8]lim[-]:[yellow]%d[-]", limit)
	}

	if limit == unlimitedTablePreviewLimit {
		return "[#a6adc8]preview[-] [yellow]all[-]"
	}
	return fmt.Sprintf("[#a6adc8]preview[-] [yellow]%d[-]", limit)
}

func (a *App) sortStatus(width int) string {
	if a.sortColumn < 0 {
		if width < 120 {
			return "[#6c7086]s:--[-]"
		}
		return "[#6c7086]sort: none[-]"
	}

	col := fmt.Sprintf("col%d", a.sortColumn+1)
	if a.results != nil && a.sortColumn >= 0 && a.sortColumn < a.results.GetColumnCount() {
		if cell := a.results.GetCell(0, a.sortColumn); cell != nil {
			name := strings.TrimSpace(cell.Text)
			name = strings.TrimSuffix(strings.TrimSuffix(name, " ▲"), " ▼")
			if name != "" {
				col = strings.ToLower(name)
			}
		}
	}

	if width < 120 {
		dir := "↑"
		if !a.sortAsc {
			dir = "↓"
		}
		return fmt.Sprintf("[#a6adc8]s[-]:[yellow]%s%s[-]", truncateForDisplay(col, 8), dir)
	}

	dir := "asc"
	if !a.sortAsc {
		dir = "desc"
	}
	return fmt.Sprintf("[#a6adc8]sort[-] [yellow]%s %s[-]", truncateForDisplay(col, 14), dir)
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
		name := strings.TrimSuffix(strings.TrimSuffix(headerCell.Text, " ▲"), " ▼")
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

		// Ctrl+C always quits, unless we are in a modal that uses it (like row details)
		if event.Key() == tcell.KeyCtrlC {
			// Check if row_details is the front page.
			// However, pages.GetFrontPage() returns the name of the *visible* page.
			// Since we add row_details as a layer on top, we need to see if it's there.
			// But GetFrontPage might return the last added visible page?
			// Let's assume if "row_details" is visible, we let it handle Ctrl+C.
			if a.pages.HasPage("row_details") {
				// We also need to know if it's actually visible/front.
				// TView doesn't have a simple "IsPageVisible" but we can check name.
				if p, _ := a.pages.GetFrontPage(); p == "row_details" {
					return event
				}
			}

			a.cleanup()
			a.app.Stop()
			return nil
		}

		// Escape Handling
		if event.Key() == tcell.KeyEscape {
			current := a.app.GetFocus()
			// If in query input, unfocus to tables
			if current == a.queryInput {
				a.setFocusWithColor(a.tables)
				return nil
			}
			// If anywhere else in main view, go back to dashboard
			if page == "main" {
				a.pages.HidePage("main")
				a.showDashboard()
				return nil
			}
			return event
		}

		// Backspace Handling
		if event.Key() == tcell.KeyBackspace || event.Key() == tcell.KeyBackspace2 {
			current := a.app.GetFocus()
			// If in query input, let it delete text
			if current == a.queryInput {
				return event
			}
			// If anywhere else (tables/results), go back to dashboard
			if page == "main" {
				a.pages.HidePage("main")
				a.showDashboard()
				return nil
			}
		}

		// F5 — Refresh currently selected table results (preserve selection/sort)
		// Ctrl+F5 — Full refresh (reload table list + results)
		if event.Key() == tcell.KeyF5 {
			if event.Modifiers()&tcell.ModCtrl != 0 {
				if err := a.refreshData(); err != nil {
					a.ShowAlert(fmt.Sprintf("Error refreshing database: %v", err), "main")
				} else {
					a.flashStatus(fmt.Sprintf("[green]%s DB Refreshed[-]", iconRefresh), a.currentResultRowCount(), 1200*time.Millisecond)
				}
				return nil
			}
			if page == "main" && a.selectedTable != "" {
				if err := a.LoadResults(); err != nil {
					a.ShowAlert(fmt.Sprintf("Error refreshing table: %v", err), "main")
				} else {
					a.flashStatus(fmt.Sprintf("[green]%s Table Refreshed[-]", iconRefresh), a.currentResultRowCount(), 1200*time.Millisecond)
				}
				return nil
			}
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
			case 'b', 'B':
				a.showBackupModal()
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
			case '=', '+':
				a.increaseResultLimit()
				return nil
			case '-', '_':
				a.decreaseResultLimit()
				return nil
			case '0':
				a.toggleUnlimitedResultLimit()
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
	a.activeConn = nil
}

// Run starts the application
func (a *App) Run() error {
	a.setupUI()
	a.setupKeyBindings()
	a.showDashboard()

	a.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		w, h := screen.Size()
		a.applyResponsiveLayout(w, h)
		return false
	})

	return a.app.SetRoot(a.pages, true).
		EnableMouse(true).
		Run()
}

func (a *App) applyResponsiveLayout(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	if a.lastScreenW == width && a.lastScreenH == height {
		return
	}
	a.lastScreenW = width
	a.lastScreenH = height

	if a.tableExpanded {
		return
	}

	a.mainFlex.Clear()
	a.rightFlex.Clear()

	queryHeight := clamp(height/5, 3, 9)
	if height < 24 {
		queryHeight = clamp(height/6, 3, 6)
	}

	a.rightFlex.SetDirection(tview.FlexRow)
	a.rightFlex.AddItem(a.queryInput, queryHeight, 0, false)
	a.rightFlex.AddItem(a.results, 0, 1, false)

	if width < 110 {
		tablesHeight := clamp(height/4, 4, 10)
		minResultsHeight := 8
		usedHeight := tablesHeight + queryHeight
		if remaining := (height - 1) - usedHeight; remaining < minResultsHeight {
			reduceBy := min(minResultsHeight-remaining, tablesHeight-4)
			if reduceBy > 0 {
				tablesHeight -= reduceBy
			}
		}

		a.mainFlex.SetDirection(tview.FlexRow)
		a.mainFlex.AddItem(a.tables, tablesHeight, 0, true)
		a.mainFlex.AddItem(a.rightFlex, 0, 1, false)
		a.updateStatusBar("", a.currentResultRowCount())
		return
	}

	tablesWidth := clamp(width/4, 24, 38)
	a.mainFlex.SetDirection(tview.FlexColumn)
	a.mainFlex.AddItem(a.tables, tablesWidth, 0, true)
	a.mainFlex.AddItem(a.rightFlex, 0, 1, false)
	a.updateStatusBar("", a.currentResultRowCount())
}

func (a *App) statusActionText(width int) string {
	switch {
	case width < 72:
		return "[yellow]Esc[-] Back  │  [yellow]F5[-] Refresh  │  [yellow]H[-]"
	case width < 90:
		return fmt.Sprintf("[yellow]F5[-] %s Refresh  │  [yellow]Esc[-] Back  │  [yellow]H[-] Help %s", iconRefresh, iconHelp)
	case width < 120:
		return fmt.Sprintf("[yellow]F5[-] %s  │  [yellow]Alt+F/B[-] Full/%s  │  [yellow]Enter[-] Detail  │  [yellow]Alt+D/Esc[-] Dash %s",
			iconRefresh, iconBackup, iconDashboard)
	default:
		return fmt.Sprintf("[yellow]F5[-] %s  │  [yellow]Alt+F[-] Full  │  [yellow]Alt+B[-] %s  │  [yellow]Enter[-] Detail  │  [yellow]Alt+H[-] Help %s  │  [yellow]Esc/Bksp[-] Dashboard %s",
			iconRefresh, iconBackup, iconHelp, iconDashboard)
	}
}

func (a *App) currentResultRowCount() int {
	if a.results == nil {
		return 0
	}

	if a.results.GetRowCount() == 2 {
		if cell := a.results.GetCell(1, 0); cell != nil && strings.Contains(cell.Text, "No rows returned") {
			return 0
		}
	}

	rows := a.results.GetRowCount() - 1
	if rows < 0 {
		return 0
	}
	return rows
}

func (a *App) flashStatus(extra string, rowCount int, duration time.Duration) {
	a.updateStatusBar(extra, rowCount)
	go func() {
		time.Sleep(duration)
		a.app.QueueUpdateDraw(func() {
			a.updateStatusBar("", rowCount)
		})
	}()
}

func (a *App) tableExistsInList(name string) bool {
	count := a.tables.GetItemCount()
	for i := 0; i < count; i++ {
		main, _ := a.tables.GetItemText(i)
		if main == name {
			return true
		}
	}
	return false
}

func (a *App) getScreenSize() (int, int) {
	if a.lastScreenW > 0 && a.lastScreenH > 0 {
		return a.lastScreenW, a.lastScreenH
	}
	return 120, 40
}

func (a *App) modalSize(minW, maxW, minH, maxH int) (int, int) {
	w, h := a.getScreenSize()
	availableW := max(30, w-4)
	availableH := max(10, h-2)

	if minW > availableW {
		minW = availableW
	}
	if minH > availableH {
		minH = availableH
	}

	return clamp(availableW, minW, maxW), clamp(availableH, minH, maxH)
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
