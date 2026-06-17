package ui

import (
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/internal/history"
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
	settings   *config.Settings
	keymap     *actionKeymap
	historyMgr *history.Manager
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
	resultLimit   int // >0 preview rows, -1 means adaptive safe max

	// Pagination state
	pageOffset    int // current OFFSET for fallback paginated table browsing
	pageSize      int // actual rows shown per page after safety limits
	totalRowCount int // cached COUNT(*) for the selected table (-1 = unknown)
	pageKey       string
	pageAnchors   []any
	lastSeenKey   any

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
	focusedPanel  tview.Primitive // cached focus target (avoids lock-unsafe GetFocus calls)

	// Import runtime state
	importMu              sync.Mutex
	importRunning         bool
	importCancelRequested bool
	importCancel          func()
	importCancelNotify    func()

	// Column width / zoom state
	tableZoom         int         // global zoom offset in steps (range: -5 to +10)
	colWidthOverrides map[int]int // per-column max-width overrides (col index → width)
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

	historyMgr, historyErr := history.NewManager(history.DefaultMaxEntriesPerConnection)
	if historyErr != nil {
		fmt.Printf("⚠ Warning: query history disabled: %v\n", historyErr)
	}

	settings, settingsErr := config.LoadSettings()
	if settingsErr != nil {
		fmt.Printf("⚠ Warning: settings load failed, using defaults: %v\n", settingsErr)
	}
	if settings == nil {
		settings = config.DefaultSettings()
	}

	keymap, keymapErr := newActionKeymap(settings)
	if keymapErr != nil {
		fmt.Printf("⚠ Warning: keymap config invalid, using defaults: %v\n", keymapErr)
		keymap, keymapErr = newActionKeymap(config.DefaultSettings())
		if keymapErr != nil {
			fmt.Printf("⚠ Warning: default keymap unavailable: %v\n", keymapErr)
		}
	}

	return &App{
		app:           tview.NewApplication(),
		store:         store,
		settings:      settings,
		keymap:        keymap,
		historyMgr:    historyMgr,
		resultLimit:   defaultTablePreviewLimit,
		totalRowCount: -1,
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
		SetPlaceholder("  Write SQL here — Enter to run, Shift+Enter for newline").
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

	// ── Results table input: sort on 's', key navigation, column width ──
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
			a.toggleCurrentResultRowSelection()
			return nil
		case ']':
			a.nextPage()
			return nil
		case '[':
			a.prevPage()
			return nil
		}

		// Pagination: PgDn/PgUp for next/prev page, Home/End for first/last page
		switch event.Key() {
		case tcell.KeyPgDn:
			a.nextPage()
			return nil
		case tcell.KeyPgUp:
			a.prevPage()
			return nil
		case tcell.KeyHome:
			a.firstPage()
			return nil
		case tcell.KeyEnd:
			a.lastPage()
			return nil
		}

		// + / - in Results adjusts the selected column width.
		if isIncreaseKey(event) {
			_, col := a.results.GetSelection()
			a.adjustColumnWidth(col, colWidthStep)
			return nil
		}
		if isDecreaseKey(event) {
			_, col := a.results.GetSelection()
			a.adjustColumnWidth(col, -colWidthStep)
			return nil
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

	// Execute query on Enter; Shift+Enter or Alt+Enter inserts newline
	a.queryInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			// Alt+Enter or Shift+Enter = insert newline (let tview handle it)
			if event.Modifiers()&tcell.ModAlt != 0 || event.Modifiers()&tcell.ModShift != 0 {
				return event
			}
			// Plain Enter = execute query
			query := a.queryInput.GetText()
			if query == "" {
				a.ShowAlert(fmt.Sprintf("%s No query to execute.\n\nType a SQL query and press Enter.", iconInfo), "main")
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
	selectedCount := a.selectedResultRowCount()

	if a.db == nil {
		if width < 58 {
			a.statusBar.SetText("  [gray]○[-]  [yellow]Alt+H[-]  [yellow]Q[-]")
			return
		}
		if width < 80 {
			a.statusBar.SetText("  [gray]○ offline[-]  │  [yellow]Alt+H[-] Help  │  [yellow]Q[-] Quit")
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
	if selectedCount > 0 && width >= 70 {
		if width < 98 {
			parts = append(parts, fmt.Sprintf("[yellow]sel:%d[-]", selectedCount))
		} else {
			parts = append(parts, fmt.Sprintf("[yellow]%d selected[-]", selectedCount))
		}
	}
	if width >= 84 {
		parts = append(parts, a.resultLimitStatus(width))
	}
	if width >= 70 {
		parts = append(parts, a.paginationStatus(width))
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
	a.focusedPanel = target
	// Refresh status bar so context-sensitive footer hints update
	a.updateStatusBar("", a.currentResultRowCount())
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

func (a *App) currentPageLimit() int {
	if a.pageSize > 0 {
		return a.pageSize
	}
	limit := a.effectiveResultLimit()
	if limit > 0 {
		return limit
	}
	return 0
}

func (a *App) setResultLimit(limit int) {
	if limit == 0 {
		limit = defaultTablePreviewLimit
	}
	if limit != adaptiveTablePreviewLimit && limit < tablePreviewSteps[0] {
		limit = tablePreviewSteps[0]
	}
	if a.resultLimit == limit {
		return
	}

	prevLimit := a.resultLimit
	a.resultLimit = limit
	a.pageOffset = 0 // reset to first page when page size changes
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
	if current == adaptiveTablePreviewLimit {
		return
	}
	next := adaptiveTablePreviewLimit
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
	if current == adaptiveTablePreviewLimit {
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

func (a *App) toggleAdaptiveResultLimit() {
	if a.effectiveResultLimit() == adaptiveTablePreviewLimit {
		a.setResultLimit(defaultTablePreviewLimit)
		return
	}
	a.setResultLimit(adaptiveTablePreviewLimit)
}

func (a *App) resultLimitReadable() string {
	if a.effectiveResultLimit() == adaptiveTablePreviewLimit {
		return "safe max"
	}
	return fmt.Sprintf("%d rows", a.effectiveResultLimit())
}

func (a *App) resultLimitStatus(width int) string {
	limit := a.effectiveResultLimit()
	if width < 120 {
		if limit == adaptiveTablePreviewLimit {
			return "[#a6adc8]lim[-]:[yellow]auto[-]"
		}
		return fmt.Sprintf("[#a6adc8]lim[-]:[yellow]%d[-]", limit)
	}

	if limit == adaptiveTablePreviewLimit {
		return "[#a6adc8]preview[-] [yellow]auto[-]"
	}
	return fmt.Sprintf("[#a6adc8]preview[-] [yellow]%d[-]", limit)
}

func (a *App) paginationStatus(width int) string {
	limit := a.currentPageLimit()
	if limit <= 0 {
		return ""
	}
	page := a.currentPageNumber(limit)
	if a.totalRowCount >= 0 && a.pageKey == "" {
		totalPages := (a.totalRowCount + limit - 1) / limit
		if totalPages < 1 {
			totalPages = 1
		}
		if width < 120 {
			return fmt.Sprintf("[#a6adc8]pg[-]:[yellow]%d/%d[-]", page, totalPages)
		}
		return fmt.Sprintf("[#a6adc8]page[-] [yellow]%d/%d[-]", page, totalPages)
	}
	if page > 1 {
		if width < 120 {
			return fmt.Sprintf("[#a6adc8]pg[-]:[yellow]%d[-]", page)
		}
		return fmt.Sprintf("[#a6adc8]page[-] [yellow]%d[-]", page)
	}
	if a.pageKey != "" {
		if width < 120 {
			return "[#a6adc8]pg[-]:[yellow]keyset[-]"
		}
		return fmt.Sprintf("[#a6adc8]page[-] [yellow]%d keyset:%s[-]", page, a.pageKey)
	}
	if width < 120 {
		return "[#a6adc8]pg[-]:[yellow]offset[-]"
	}
	return "[#a6adc8]page[-] [yellow]offset[-]"
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
		action, hasAction := a.resolveAction(event)

		// Ctrl+C cancels import when running; otherwise it quits (except row details modal).
		if event.Key() == tcell.KeyCtrlC {
			if a.isImportRunning() {
				a.requestImportCancel()
				return nil
			}

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

		if hasAction {
			switch action {
			case actionHelp:
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
			case actionDashboard:
				if page == "main" || page == "help" {
					a.pages.HidePage(page)
					a.showDashboard()
				}
				return nil
			case actionServices:
				// Show service dashboard from anywhere.
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

		// Ctrl+=/- for table zoom, Ctrl+0 for reset (any focus in main)
		if event.Modifiers()&tcell.ModCtrl != 0 || event.Key() == tcell.KeyCtrlUnderscore {
			switch {
			case isIncreaseKey(event):
				a.zoomTable(1)
				return nil
			case isDecreaseKey(event):
				a.zoomTable(-1)
				return nil
			case isZeroKey(event):
				a.resetTableZoom()
				return nil
			}
		}

		if hasAction {
			switch action {
			case actionFocusTables:
				a.setFocusWithColor(a.tables)
				return nil
			case actionFocusQuery:
				// If query editor is already focused, let the event pass through.
				// This helps international keyboard layouts that rely on AltGr combos.
				if a.app.GetFocus() == a.queryInput {
					return event
				}
				a.setFocusWithColor(a.queryInput)
				return nil
			case actionFocusResults:
				a.setFocusWithColor(a.results)
				return nil
			case actionFullscreen:
				if a.app.GetFocus() == a.queryInput {
					return event
				}
				a.toggleExpandResults()
				return nil
			case actionBackup:
				if a.app.GetFocus() == a.queryInput {
					return event
				}
				a.showBackupModal()
				return nil
			case actionImportDump:
				a.showImportModal()
				return nil
			case actionInspectSchema:
				if a.app.GetFocus() == a.queryInput {
					return event
				}
				a.showSelectedTableMetadata()
				return nil
			case actionSelectAll:
				if a.app.GetFocus() == a.queryInput {
					return event
				}
				a.selectAllResultRows()
				return nil
			case actionClearSelection:
				if a.app.GetFocus() == a.queryInput {
					return event
				}
				a.clearResultRowSelection()
				return nil
			case actionExportCSV:
				if a.app.GetFocus() == a.queryInput {
					return event
				}
				a.exportCurrentResultsToCSV()
				return nil
			case actionHistory:
				a.showHistoryModal()
				return nil
			case actionSettings:
				a.showSettings()
				return nil
			}
		}

		if event.Modifiers()&tcell.ModAlt != 0 {
			switch event.Rune() {
			case '=', '+':
				a.increaseResultLimit()
				return nil
			case '-', '_':
				a.decreaseResultLimit()
				return nil
			case '0':
				a.toggleAdaptiveResultLimit()
				return nil
			}
		}
		return event
	})
}

func (a *App) resolveAction(event *tcell.EventKey) (keymapAction, bool) {
	if a == nil || a.keymap == nil {
		return "", false
	}
	return a.keymap.Resolve(event)
}

func isIncreaseKey(event *tcell.EventKey) bool {
	r := event.Rune()
	return r == '+' || r == '='
}

func isDecreaseKey(event *tcell.EventKey) bool {
	if event.Key() == tcell.KeyCtrlUnderscore {
		return true
	}
	r := event.Rune()
	return r == '-' || r == '_'
}

func isZeroKey(event *tcell.EventKey) bool {
	return event.Rune() == '0'
}

// ── Column width / zoom helpers ──

const (
	colWidthStep   = 4
	minColWidth    = 8
	minZoom        = -5
	maxZoom        = 10
	defaultColBase = 30 // initial base width when first adjusting a column
)

// applyColumnWidths sets MaxWidth on every cell based on zoom + per-column overrides.
func (a *App) applyColumnWidths() {
	rowCount := a.results.GetRowCount()
	colCount := a.results.GetColumnCount()
	if rowCount == 0 || colCount == 0 {
		return
	}

	screenW, _ := a.getScreenSize()
	maxW := max(minColWidth, screenW)

	for c := 0; c < colCount; c++ {
		w := 0 // 0 = auto / unlimited
		if override, ok := a.colWidthOverrides[c]; ok {
			w = override
		}
		if a.tableZoom != 0 && w == 0 {
			// Apply zoom from a reasonable base
			w = defaultColBase + a.tableZoom*colWidthStep
		} else if a.tableZoom != 0 {
			w += a.tableZoom * colWidthStep
		}
		if w > 0 {
			w = clamp(w, minColWidth, maxW)
		}
		for r := 0; r < rowCount; r++ {
			if cell := a.results.GetCell(r, c); cell != nil {
				cell.SetMaxWidth(w)
			}
		}
	}
}

// adjustColumnWidth changes the width of a single column by delta characters.
func (a *App) adjustColumnWidth(col, delta int) {
	if a.colWidthOverrides == nil {
		a.colWidthOverrides = make(map[int]int)
	}
	screenW, _ := a.getScreenSize()
	maxW := max(minColWidth, screenW)

	current, ok := a.colWidthOverrides[col]
	if !ok {
		current = defaultColBase
	}
	newW := clamp(current+delta, minColWidth, maxW)
	a.colWidthOverrides[col] = newW
	a.applyColumnWidths()

	a.flashStatus(fmt.Sprintf("[teal]col %d width → %d[-]", col+1, newW), a.currentResultRowCount(), 1200*time.Millisecond)
}

// zoomTable adjusts the global zoom level for all columns.
func (a *App) zoomTable(delta int) {
	newZoom := clamp(a.tableZoom+delta, minZoom, maxZoom)
	if newZoom == a.tableZoom {
		return
	}
	a.tableZoom = newZoom
	a.applyColumnWidths()

	label := "default"
	if a.tableZoom > 0 {
		label = fmt.Sprintf("+%d", a.tableZoom)
	} else if a.tableZoom < 0 {
		label = fmt.Sprintf("%d", a.tableZoom)
	}
	a.flashStatus(fmt.Sprintf("[teal]zoom %s[-]", label), a.currentResultRowCount(), 1200*time.Millisecond)
}

// resetTableZoom resets zoom and per-column overrides to defaults.
func (a *App) resetTableZoom() {
	a.tableZoom = 0
	a.colWidthOverrides = nil
	a.applyColumnWidths()
	a.flashStatus("[green]zoom reset[-]", a.currentResultRowCount(), 1200*time.Millisecond)
}

// clearColumnOverrides resets per-column width overrides (called on table/query change).
func (a *App) clearColumnOverrides() {
	a.colWidthOverrides = nil
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
		EnablePaste(true).
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
	inQuery := a.focusedPanel == a.queryInput
	switch {
	case width < 72:
		if inQuery {
			return "[yellow]Enter[-] Run ▶  │  [yellow]Esc[-] Back"
		}
		return "[yellow]Space[-] Select  │  [yellow]Alt+E[-] CSV  │  [yellow]Esc[-] Back"
	case width < 90:
		if inQuery {
			return fmt.Sprintf("[yellow]Enter[-] Run ▶  │  [yellow]Shift+Enter[-] Newline  │  [yellow]Esc[-] Back  │  [yellow]Alt+H[-] Help %s", iconHelp)
		}
		return fmt.Sprintf("[yellow]Space[-] Select  │  [yellow]Alt+A/C[-] All/Clear  │  [yellow]Alt+E[-] CSV  │  [yellow]Alt+H[-] %s", iconHelp)
	case width < 120:
		if inQuery {
			return fmt.Sprintf("[yellow]Enter[-] Run ▶  │  [yellow]Shift+Enter[-] Newline  │  [yellow]F5[-] %s  │  [yellow]Alt+D/Esc[-] Dash %s",
				iconRefresh, iconDashboard)
		}
		return fmt.Sprintf("[yellow]F5[-] %s  │  [yellow]Space[-] Toggle Sel  │  [yellow]Alt+A/C/E[-] All/Clear/CSV  │  [yellow]Enter[-] Detail  │  [yellow]Alt+D/Esc[-] Dash %s",
			iconRefresh, iconDashboard)
	default:
		if inQuery {
			return fmt.Sprintf("[yellow]Enter[-] Run ▶  │  [yellow]Shift+Enter[-] Newline  │  [yellow]F5[-] %s  │  [yellow]Alt+H[-] Help %s  │  [yellow]Esc/Bksp[-] Dashboard %s",
				iconRefresh, iconHelp, iconDashboard)
		}
		return fmt.Sprintf("[yellow]F5[-] %s  │  [yellow]Space[-] Toggle Sel  │  [yellow]Alt+A[-] All  │  [yellow]Alt+C[-] Clear  │  [yellow]Alt+E[-] CSV  │  [yellow]Enter[-] Detail  │  [yellow]Alt+H[-] Help %s  │  [yellow]Esc/Bksp[-] Dashboard %s",
			iconRefresh, iconHelp, iconDashboard)
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
