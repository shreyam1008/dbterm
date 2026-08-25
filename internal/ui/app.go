package ui

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	profiler "github.com/shreyam1008/dbterm/internal/changeprofiler"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/history"
)

// ── Catppuccin Mocha ──────────────────────────────────────────────────
var (
	bg           = tcell.NewRGBColor(30, 30, 46)    // #1e1e2e  base
	mantle       = tcell.NewRGBColor(24, 24, 37)    // #181825  mantle
	crust        = tcell.NewRGBColor(17, 17, 27)    // #11111b  crust
	green        = tcell.NewRGBColor(166, 227, 161) // #a6e3a1  green
	surface0     = tcell.NewRGBColor(49, 50, 68)    // #313244  surface0
	surface1     = tcell.NewRGBColor(69, 71, 90)    // #45475a  surface1
	red          = tcell.NewRGBColor(243, 139, 168) // #f38ba8  red
	peach        = tcell.NewRGBColor(255, 180, 150) // #ffb496  peach
	blue         = tcell.NewRGBColor(137, 180, 250) // #89b4fa  blue
	mauve        = tcell.NewRGBColor(203, 166, 247) // #cba6f7  mauve
	yellow       = tcell.NewRGBColor(249, 226, 175) // #f9e2af  yellow
	teal         = tcell.NewRGBColor(148, 226, 213) // #94e2d5  teal
	text         = tcell.NewRGBColor(205, 214, 244) // #cdd6f4  text
	subtext0     = tcell.NewRGBColor(166, 173, 200) // #a6adc8  subtext0
	overlay0     = tcell.NewRGBColor(108, 112, 134) // #6c7086  overlay0
	insertRowBG  = tcell.NewRGBColor(32, 58, 43)
	updateRowBG  = tcell.NewRGBColor(61, 55, 31)
	updateCellBG = tcell.NewRGBColor(91, 75, 31)
	deleteRowBG  = tcell.NewRGBColor(63, 34, 43)
)

// App holds all TUI state for the dbterm application
type App struct {
	app                    *tview.Application
	db                     *sql.DB
	pages                  *tview.Pages
	store                  *config.Store
	settings               *config.Settings
	keymap                 *actionKeymap
	historyMgr             *history.Manager
	backupStore            *backupcore.Store
	profilerStore          *profiler.Store
	buildInfo              BuildInfo
	startupNotice          string
	startupNoticeRecovered bool

	// Backup Center keeps its original caller across internal refreshes and
	// nested forms so Esc returns to the exact panel that opened it.
	backupCenterReturnPage  string
	backupCenterReturnFocus tview.Primitive
	backupCenterSelectedJob string
	profilerReturnPage      string
	profilerReturnFocus     tview.Primitive
	profilerSelectedAnchor  string
	profilerAnchorID        string
	profilerTableChanges    map[string]profiler.TableSummary
	helpReturnPage          string
	helpReturnFocus         tview.Primitive
	dbType                  config.DBType
	dbName                  string // name of current connection (from config)
	activeConn              *config.ConnectionConfig

	// Main UI components
	tables                *tview.List
	databaseObjects       map[int]databaseObjectListItem
	tableIdentifiers      map[int]string
	tableColumnItems      map[int]sidebarColumnRef
	tableOrder            []string
	tableSidebarItems     int
	tableSearch           string
	expandedSidebarTable  string
	sidebarColumnMetadata map[string][]sidebarColumnMeta
	sidebarMetadataLoads  map[string]bool
	sidebarSearchIndex    []sidebarSearchEntry
	sidebarSearchLookup   sidebarSearchLookup
	sidebarRenderedSearch sidebarSelection
	databaseObjectCount   int
	selectedTable         string
	activeTable           string          // table whose rows are currently visible
	visitedTables         map[string]bool // tables opened during the active connection
	tableResultsActive    bool
	resultPositions       map[string]resultSelectionState // remembered cursor/scroll position per table
	resultColumnSearch    string                          // type-ahead search while the result header row is active
	resultFilter          *resultValueFilter
	resultFilters         map[string]*resultValueFilter // remembered per table for the active connection
	copiedCellValue       string
	hasCopiedCellValue    bool
	copiedCellSystem      bool
	copiedCellIsNull      bool
	clipboardGeneration   atomic.Uint64
	objectGeneration      atomic.Uint64
	loadingGeneration     atomic.Uint64
	loadingMu             sync.Mutex
	loadingReturns        map[uint64]loadingReturnState
	results               *tview.Table
	queryInput            *tview.TextArea
	sqlCompletionView     *tview.Table
	sqlCompletionState    sqlCompletionState
	sqlCompletionCatalog  sqlCompletionCatalog
	sqlCompletionRoutines []sqlCompletionRoutine
	sqlCompletionApplying bool
	statusBar             *tview.TextView
	tableCount            int
	queryStart            time.Time
	queryMu               sync.Mutex
	queryRunning          bool
	queryStartedAt        time.Time
	queryCancel           context.CancelFunc
	resultLimit           int // >0 preview rows, -1 means adaptive safe max
	resultExportMu        sync.Mutex
	resultExportRunning   bool
	resultExportCancel    context.CancelFunc

	// Pagination state
	pageOffset              int           // current OFFSET for paginated table browsing
	pageSize                int           // actual rows shown per page after safety limits
	totalRowCount           int           // cached COUNT(*) for the selected table (-1 = unknown)
	resultGeneration        atomic.Uint64 // invalidates async result metadata updates
	sqlCompletionGeneration atomic.Uint64 // invalidates async autocomplete metadata
	sidebarSearchGeneration atomic.Uint64 // debounces lazy metadata while type-ahead changes
	resultNavStack          []resultNavigationState

	// Layout components for scaling
	rightFlex *tview.Flex
	mainFlex  *tview.Flex

	// Sidebar resizing is session-local. Dragging the Tables panel's right
	// border updates the fixed width without disturbing the responsive stack.
	sidebarWidth    int
	sidebarDragging bool

	// Sorting state
	sortColumn int    // current sort column index (-1 = none)
	sortAsc    bool   // true = ascending
	sortMode   string // "page" for local visible-row sort, "server" for table ORDER BY

	// UI state
	tableExpanded      bool // results fullscreen mode
	lastScreenW        int
	lastScreenH        int
	focusedPanel       tview.Primitive // cached focus target (avoids lock-unsafe GetFocus calls)
	paletteReturnFocus tview.Primitive
	paletteReturnPage  string
	guideResize        func(width int)

	// Import runtime state
	importMu              sync.Mutex
	importRunning         bool
	importCancelRequested bool
	importCancel          func()
	importCancelNotify    func()

	// Column width state
	colWidthOverrides map[string]int // per-column widths (column name → width)
}

// BuildInfo is immutable metadata shown by the in-app Version & Update page.
type BuildInfo struct {
	Version     string
	ReleaseName string
	Commit      string
	Repository  string
}

// NewApp creates a new dbterm application instance
func NewApp() *App {
	return NewAppWithBuildInfo(BuildInfo{
		Version:    "dev",
		Commit:     "dev",
		Repository: "shreyam1008/dbterm",
	})
}

// NewAppWithBuildInfo creates an application with release metadata supplied by
// the main package, which owns the embedded release manifest and linker values.
func NewAppWithBuildInfo(buildInfo BuildInfo) *App {
	store, err := config.LoadStore()
	if store == nil {
		store = &config.Store{}
	}
	startupNotice := store.RecoveryNotice()
	startupNoticeRecovered := startupNotice != ""
	if err != nil {
		fmt.Printf("⚠ Warning: could not safely load saved connections: %v\n", err)
		startupNotice = fmt.Sprintf("Saved connections need attention: %v", err)
		startupNoticeRecovered = false
	}

	historyMgr, historyErr := history.NewManager(history.DefaultMaxEntriesPerConnection)
	if historyErr != nil {
		fmt.Printf("⚠ Warning: query history disabled: %v\n", historyErr)
	}

	settings, settingsErr := config.LoadSettings()
	if settingsErr != nil {
		fmt.Printf("⚠ Warning: settings required attention: %v\n", settingsErr)
	}
	if settings == nil {
		settings = config.DefaultSettings()
	}

	keymap, keymapErr := newActionKeymap(settings)
	if keymapErr != nil {
		fmt.Printf("⚠ Warning: keymap config invalid, using defaults: %v\n", keymapErr)
		settings = config.DefaultSettings()
		keymap, keymapErr = newActionKeymap(settings)
		if keymapErr != nil {
			fmt.Printf("⚠ Warning: default keymap unavailable: %v\n", keymapErr)
		}
	}

	return &App{
		app:                    tview.NewApplication(),
		store:                  store,
		settings:               settings,
		keymap:                 keymap,
		historyMgr:             historyMgr,
		resultLimit:            defaultTablePreviewLimit,
		totalRowCount:          -1,
		buildInfo:              normalizeBuildInfo(buildInfo),
		startupNotice:          startupNotice,
		startupNoticeRecovered: startupNoticeRecovered,
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
		SetTitle(a.workspacePanelTitle(iconResults, "Results", actionFocusResults, "")).
		SetBorderColor(surface1).
		SetTitleColor(peach)

	// ── Tables List ──
	a.tables = tview.NewList().ShowSecondaryText(false)
	a.tables.SetBorder(true).
		SetTitle(a.workspacePanelTitle(iconTables, "Tables", actionFocusTables, "")).
		SetBorderColor(surface1).
		SetTitleColor(peach)
	a.tables.SetInputCapture(a.handleTableListInput)
	a.tables.SetMouseCapture(a.handleTableListMouse)
	a.app.SetMouseCapture(a.handleWorkspaceMouse)

	// ── Query Input ──
	a.queryInput = tview.NewTextArea().
		SetPlaceholder("  Write SQL here — Enter to run, Shift+Enter for newline").
		SetPlaceholderStyle(tcell.StyleDefault.Foreground(overlay0))
	a.queryInput.SetBorder(true).
		SetTitle(a.workspacePanelTitle(iconQuery, "Query", actionFocusQuery, "")).
		SetBorderColor(surface1).
		SetTitleColor(peach)
	a.sqlCompletionView = newSQLCompletionView()

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
		AddItem(a.sqlCompletionView, 0, 0, false).
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
		if a.handleResultColumnInput(event) {
			return nil
		}

		if shortcut, ok := plainShortcutRune(event); ok {
			switch shortcut {
			case 'c':
				a.copyCurrentResultCell()
				return nil
			case '/':
				a.showResultFilterModal()
				return nil
			case 'v':
				a.filterSelectedResultColumnByClipboard()
				return nil
			case 'f':
				a.exploreSelectedRelationships()
				return nil
			case 's':
				// Sort by current column.
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

		// Plain +/- adjusts one column. >/< and 0 remain terminal-safe
		// fallbacks for resizing/resetting all columns.
		switch resultSizeShortcutFor(event) {
		case resultSizeSelectedIncrease:
			_, col := a.results.GetSelection()
			a.adjustColumnWidth(col, colWidthStep)
			return nil
		case resultSizeSelectedDecrease:
			_, col := a.results.GetSelection()
			a.adjustColumnWidth(col, -colWidthStep)
			return nil
		case resultSizeAllIncrease:
			a.resizeAllColumns(1)
			return nil
		case resultSizeAllDecrease:
			a.resizeAllColumns(-1)
			return nil
		case resultSizeAllReset:
			a.resetColumnWidths()
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
	a.results.SetSelectionChangedFunc(func(_, _ int) {
		if a.statusBar != nil {
			a.updateStatusBar("", a.currentResultRowCount())
		}
	})

	// Execute query on Enter; Shift+Enter or Alt+Enter inserts newline
	a.queryInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			// Alt+Enter or Shift+Enter = insert newline (let tview handle it)
			if event.Modifiers()&tcell.ModAlt != 0 || event.Modifiers()&tcell.ModShift != 0 {
				return event
			}
			// Plain Enter = execute query
			a.hideSQLCompletions()
			query := a.queryInput.GetText()
			if query == "" {
				a.ShowAlert(fmt.Sprintf("%s No query to execute.\n\nType a SQL query and press Enter.", iconInfo), "main")
				return nil
			}
			a.ExecuteQuery(query)
			return nil
		}
		return event
	})
	a.queryInput.SetChangedFunc(func() { a.refreshSQLCompletions(false) })
	a.queryInput.SetMovedFunc(func() { a.refreshSQLCompletions(false) })
}

// updateStatusBar refreshes the bottom status bar with current state
func (a *App) updateStatusBar(extra string, rowCount int) {
	width, _ := a.getScreenSize()
	actionText := a.statusActionText(width)
	selectedCount := a.selectedResultRowCount()

	if running := a.queryRunningStatus(width, time.Now()); running != "" {
		a.statusBar.SetText("  " + running)
		return
	}

	if a.db == nil {
		helpKey := a.taggedActionShortcut(actionHelp)
		if width < 58 {
			a.statusBar.SetText(fmt.Sprintf("  [gray]○[-]  %s  [yellow]Q[-]", helpKey))
			return
		}
		if width < 80 {
			a.statusBar.SetText(fmt.Sprintf("  [gray]○ offline[-]  │  %s Guide  │  [yellow]Q[-] Quit", helpKey))
			return
		}
		a.statusBar.SetText(fmt.Sprintf("  [gray]○ offline[-]  │  %s no DB  │  [yellow]%s[-] Palette  │  %s Guide  │  [yellow]Q[-] Quit", iconConnect, tview.Escape(a.commandPaletteShortcutHint()), helpKey))
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

	// The focused panel's controls are the most immediately useful content.
	// Keep them first so narrow terminals clip connection metadata, not actions.
	parts = append([]string{actionText}, parts...)
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
	if target == a.queryInput {
		a.refreshSQLCompletions(false)
	} else {
		a.hideSQLCompletions()
	}
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

// cycleFocusReverse mirrors the standard Shift+Tab convention while keeping
// each panel's cursor and type-ahead search exactly where the user left it.
func (a *App) cycleFocusReverse() {
	current := a.app.GetFocus()
	switch current {
	case a.tables:
		a.setFocusWithColor(a.results)
	case a.results:
		a.setFocusWithColor(a.queryInput)
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

// toggleSort updates sort state and applies it
func (a *App) toggleSort(col int) {
	if a.results == nil || col < 0 || col >= a.results.GetColumnCount() {
		a.resetSort()
		a.updateStatusBar("", a.currentResultRowCount())
		return
	}

	previous := a.captureResultNavigationState()

	// Toggle sort direction if same column, else reset to ascending.
	if a.sortColumn == col {
		a.sortAsc = !a.sortAsc
	} else {
		a.sortColumn = col
		a.sortAsc = true
	}

	// Table browsing can be safely re-issued with ORDER BY so pagination is sorted
	// across the full table, not just the rows currently loaded in the UI.
	if a.isTableResultActive() {
		a.sortMode = "server"
		a.pageOffset = 0
		direction := "ascending"
		if !a.sortAsc {
			direction = "descending"
		}
		a.loadCurrentTableAsync(tableLoadOptions{
			loadingText:  fmt.Sprintf("Sorting %s...", direction),
			cancelText:   "Press Esc to cancel sorting.",
			canceledText: "Sort canceled",
			errorText:    "Could not sort table results",
			rollback: func() {
				a.restoreResultNavigationState(previous)
			},
		})
		return
	}

	a.applySort()
	a.updateStatusBar("", a.currentResultRowCount())
}

func (a *App) resetSort() {
	a.sortColumn = -1
	a.sortAsc = true
	a.sortMode = ""
}

func (a *App) isTableResultActive() bool {
	return a.selectedTable != "" && a.tableResultsActive
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
	prevOffset := a.pageOffset
	prevPageSize := a.pageSize
	prevTotal := a.totalRowCount
	restorePrevious := func() {
		a.resultLimit = prevLimit
		a.pageOffset = prevOffset
		a.pageSize = prevPageSize
		a.totalRowCount = prevTotal
	}
	a.resultLimit = limit
	a.pageOffset = 0 // reset to first page when page size changes
	a.pageSize = 0
	a.totalRowCount = -1
	if a.db == nil || !a.isTableResultActive() {
		a.advanceResultGeneration()
		a.updateStatusBar("", a.currentResultRowCount())
		return
	}

	request, err := a.prepareTableResultRequest()
	if err != nil || request == nil {
		restorePrevious()
		a.advanceResultGeneration()
		if err == nil {
			err = fmt.Errorf("table result request is unavailable")
		}
		a.ShowAlert(fmt.Sprintf("%s Could not change preview rows:\n\n%v", iconWarn, err), "main")
		return
	}

	returnFocus := a.app.GetFocus()
	restoreFocus := func() {
		switch returnFocus {
		case a.tables, a.queryInput, a.results:
			a.setFocusWithColor(returnFocus)
		case nil:
			return
		default:
			a.app.SetFocus(returnFocus)
		}
	}
	a.runTableResultRequestAsync(
		request,
		fmt.Sprintf("Loading %s...", a.resultLimitReadable()),
		"Press Esc to cancel changing preview rows.",
		tableResultAsyncCallbacks{
			rollback: restorePrevious,
			onCancel: func() {
				restoreFocus()
				a.flashStatus("[yellow]Preview row change canceled[-]", a.currentResultRowCount(), 1400*time.Millisecond)
			},
			onError: func(loadErr error) {
				restoreFocus()
				a.ShowAlert(fmt.Sprintf("%s Could not change preview rows:\n\n%v", iconWarn, loadErr), "main")
			},
			onSuccess: func() {
				restoreFocus()
				a.flashStatus(fmt.Sprintf("[green]%s Preview %s[-]", iconRefresh, a.resultLimitReadable()), a.currentResultRowCount(), 1400*time.Millisecond)
			},
		},
	)
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
	page := (a.pageOffset / limit) + 1
	if a.totalRowCount >= 0 {
		totalPages := (a.totalRowCount + limit - 1) / limit
		if totalPages < 1 {
			totalPages = 1
		}
		if width < 120 {
			return fmt.Sprintf("[#a6adc8]pg[-]:[yellow]%d/%d[-]", page, totalPages)
		}
		return fmt.Sprintf("[#a6adc8]page[-] [yellow]%d/%d[-]", page, totalPages)
	}
	if a.pageOffset > 0 {
		if width < 120 {
			return fmt.Sprintf("[#a6adc8]pg[-]:[yellow]%d[-]", page)
		}
		return fmt.Sprintf("[#a6adc8]page[-] [yellow]%d[-]", page)
	}
	return ""
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
		mode := "p"
		if a.sortMode == "server" {
			mode = "s"
		}
		return fmt.Sprintf("[#a6adc8]s[-]:[yellow]%s%s[-][#6c7086]%s[-]", truncateForDisplay(col, 7), dir, mode)
	}

	dir := "asc"
	if !a.sortAsc {
		dir = "desc"
	}
	mode := "page"
	if a.sortMode == "server" {
		mode = "server"
	}
	return fmt.Sprintf("[#a6adc8]sort[-] [yellow]%s %s[-] [#6c7086](%s)[-]", truncateForDisplay(col, 14), dir, mode)
}

// applySort sorts the results table based on current sort state
func (a *App) applySort() {
	col := a.sortColumn
	if col == -1 {
		return
	}

	rowCount := a.results.GetRowCount()
	if rowCount <= 2 { // header + at most 1 row, nothing to sort
		a.sortMode = "page"
		a.setSortHeaderIndicator()
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
		textI, nullI := resultCellSortText(rows[i].cells[col])
		textJ, nullJ := resultCellSortText(rows[j].cells[col])
		if nullI || nullJ {
			if nullI == nullJ {
				return false
			}
			if asc {
				return nullI
			}
			return nullJ
		}

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

	a.sortMode = "page"
	a.setSortHeaderIndicator()
}

func resultCellSortText(cell *tview.TableCell) (string, bool) {
	if cell == nil {
		return "", true
	}
	if reference, ok := cell.GetReference().(resultCellReference); ok {
		if reference.isNull {
			return "", true
		}
		return reference.value, false
	}
	return tview.Unescape(cell.Text), false
}

func (a *App) setSortHeaderIndicator() {
	a.refreshResultColumnHeaders()
}

func stripSortIndicator(text string) string {
	name := strings.TrimSpace(text)
	name = strings.TrimSuffix(strings.TrimSuffix(name, " ▲"), " ▼")
	return name
}

func (a *App) setupKeyBindings() {
	a.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		page, _ := a.pages.GetFrontPage()
		action, hasAction := a.resolveAction(event)

		// Ctrl+C cancels active work before quitting (except row details modal).
		if event.Key() == tcell.KeyCtrlC {
			if a.isQueryRunning() {
				a.cancelActiveQuery()
				return nil
			}

			if a.isImportRunning() {
				a.requestImportCancel()
				return nil
			}

			if a.cancelActiveResultExport() {
				return nil
			}

			// Cancellable loading overlays handle Ctrl+C exactly like Esc and
			// remain visible until their worker reports the final safe outcome.
			if page == "loading" {
				return event
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
			// The visible workspace owns query cancellation. Front overlays such as
			// the palette, filter builder, and loading modal must receive the Esc
			// promised by their own footer/cancel text.
			if page == "main" && a.isQueryRunning() {
				a.cancelActiveQuery()
				return nil
			}

			current := a.app.GetFocus()
			if page == "main" && current == a.queryInput && a.handleSQLCompletionKey(event) {
				return nil
			}
			if page == "main" && current == a.results && a.hasActiveResultColumnSearch() {
				a.clearResultColumnSearch()
				return nil
			}
			// Let the Tables list clear an active type-ahead search first.
			if current == a.tables && a.hasActiveTableSearch() {
				return event
			}
			// In filtered table results, the first Esc removes the filter. A
			// second Esc follows the normal path back to the Dashboard.
			if page == "main" && current == a.results && a.isTableResultActive() && a.activeResultFilter(a.selectedTable) != nil {
				a.clearResultFilterAndReload()
				return nil
			}
			// If in query input, unfocus to tables
			if current == a.queryInput {
				a.setFocusWithColor(a.tables)
				return nil
			}
			// If anywhere else in main view, go back to dashboard
			if page == "main" {
				if current == a.results {
					a.resetCurrentResultPosition()
				}
				a.pages.HidePage("main")
				a.showDashboard()
				return nil
			}
			return event
		}

		// Backspace Handling
		if event.Key() == tcell.KeyBackspace || event.Key() == tcell.KeyBackspace2 {
			current := a.app.GetFocus()
			// Let the Tables list edit an active type-ahead search.
			if current == a.tables && a.hasActiveTableSearch() {
				return event
			}
			// If in query input, let it delete text
			if current == a.queryInput {
				return event
			}
			if page == "main" && current == a.results && a.hasActiveResultColumnSearch() {
				return event
			}
			// Related-row hops keep a navigation stack. In Results,
			// Backspace returns through it before using the normal Dashboard path.
			if page == "main" && current == a.results && a.navigateBackFromRelationship() {
				return nil
			}
			// If anywhere else (tables/results), go back to dashboard
			if page == "main" {
				a.pages.HidePage("main")
				a.showDashboard()
				return nil
			}
		}

		// Loading overlays own the keyboard until they finish or Esc cancels.
		// This prevents a background completion from stealing focus from a
		// page opened on top of the operation.
		if page == "loading" || page == instantBackupPage || page == pageBackupForm || page == pageBackupConnectionPicker || page == pageImportProgressModal || page == pageResultExport || page == pageResultExportProgress {
			return event
		}

		if page == "main" && a.app.GetFocus() == a.queryInput && a.handleSQLCompletionKey(event) {
			return nil
		}

		// F5 — Refresh currently selected table results (preserve selection/sort)
		// Ctrl+F5 — Full refresh (reload table list + results)
		if event.Key() == tcell.KeyF5 {
			if event.Modifiers()&tcell.ModCtrl != 0 {
				a.refreshDataAsync()
				return nil
			}
			if page == "main" && a.selectedTable != "" {
				a.refreshCurrentTableAsync()
				return nil
			}
			return nil
		}

		if hasAction {
			switch action {
			case actionCommandPalette:
				a.showCommandPalette()
				return nil
			case actionHelp:
				if page == "help" {
					a.closeHelp()
				} else {
					a.showHelp()
				}
				return nil
			case actionDashboard:
				if page != "dashboard" {
					if page == "main" {
						a.pages.HidePage(page)
					} else {
						if page == "help" {
							a.guideResize = nil
						}
						a.pages.RemovePage(page)
					}
					a.showDashboard()
				}
				return nil
			case actionSettings:
				if page != pageSettings {
					a.showSettings()
				}
				return nil
			case actionServices:
				// Show service dashboard from anywhere.
				a.showServiceDashboard()
				return nil
			case actionBackupCenter:
				a.showBackupCenter()
				return nil
			case actionChangeProfiler:
				a.showChangeProfiler()
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
		if event.Key() == tcell.KeyBacktab {
			a.cycleFocusReverse()
			return nil
		}

		// Modifier-aware result sizing. Ctrl changes every column; Alt changes
		// the number of rows fetched per page. Plain keys continue to the
		// Results primitive, where they resize only the selected column.
		switch resultSizeShortcutFor(event) {
		case resultSizeAllIncrease:
			if resultSizeHasControlModifier(event) {
				a.resizeAllColumns(1)
				return nil
			}
		case resultSizeAllDecrease:
			if resultSizeHasControlModifier(event) {
				a.resizeAllColumns(-1)
				return nil
			}
		case resultSizeAllReset:
			if resultSizeHasControlModifier(event) {
				a.resetColumnWidths()
				return nil
			}
		case resultSizeRowsIncrease:
			a.increaseResultLimit()
			return nil
		case resultSizeRowsDecrease:
			a.decreaseResultLimit()
			return nil
		case resultSizeRowsToggle:
			a.toggleAdaptiveResultLimit()
			return nil
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

type resultSizeShortcut uint8

const (
	resultSizeNone resultSizeShortcut = iota
	resultSizeSelectedIncrease
	resultSizeSelectedDecrease
	resultSizeAllIncrease
	resultSizeAllDecrease
	resultSizeAllReset
	resultSizeRowsIncrease
	resultSizeRowsDecrease
	resultSizeRowsToggle
)

func resultSizeHasControlModifier(event *tcell.EventKey) bool {
	return event != nil && (event.Modifiers()&tcell.ModCtrl != 0 || event.Key() == tcell.KeyCtrlUnderscore)
}

func resultSizeShortcutFor(event *tcell.EventKey) resultSizeShortcut {
	if event == nil {
		return resultSizeNone
	}

	modifiers := event.Modifiers()
	hasCtrl := resultSizeHasControlModifier(event)
	hasAlt := modifiers&tcell.ModAlt != 0
	hasMeta := modifiers&tcell.ModMeta != 0

	if hasCtrl && !hasAlt && !hasMeta {
		switch {
		case isIncreaseKey(event):
			return resultSizeAllIncrease
		case isDecreaseKey(event):
			return resultSizeAllDecrease
		case isZeroKey(event):
			return resultSizeAllReset
		}
		return resultSizeNone
	}

	if hasAlt && !hasCtrl && !hasMeta {
		switch {
		case isIncreaseKey(event):
			return resultSizeRowsIncrease
		case isDecreaseKey(event):
			return resultSizeRowsDecrease
		case isZeroKey(event):
			return resultSizeRowsToggle
		}
		return resultSizeNone
	}

	if hasCtrl || hasAlt || hasMeta {
		return resultSizeNone
	}

	switch event.Rune() {
	case '+', '=':
		return resultSizeSelectedIncrease
	case '-', '_':
		return resultSizeSelectedDecrease
	case '>':
		return resultSizeAllIncrease
	case '<':
		return resultSizeAllDecrease
	case '0':
		return resultSizeAllReset
	default:
		return resultSizeNone
	}
}

// ── Column width helpers ──

const (
	colWidthStep   = 4
	minColWidth    = 8
	defaultColBase = 30
)

// applyColumnWidths gives every result column the same bounded default width.
func (a *App) applyColumnWidths() {
	if a == nil || a.results == nil {
		return
	}
	rowCount := a.results.GetRowCount()
	colCount := a.results.GetColumnCount()
	if rowCount == 0 || colCount == 0 {
		return
	}

	screenW, _ := a.getScreenSize()
	maxW := max(minColWidth, screenW)

	for c := 0; c < colCount; c++ {
		name := a.resultColumnName(c)
		w := defaultColBase
		if override, ok := a.colWidthOverrides[name]; ok {
			w = override
		}
		w = clamp(w, minColWidth, maxW)
		for r := 0; r < rowCount; r++ {
			if cell := a.results.GetCell(r, c); cell != nil {
				cell.SetMaxWidth(w).SetExpansion(0)
			}
		}
	}
	a.refreshResultColumnHeaders()
}

// adjustColumnWidth changes the width of a single column by delta characters.
func (a *App) adjustColumnWidth(col, delta int) {
	name := a.resultColumnName(col)
	if name == "" {
		return
	}
	if a.colWidthOverrides == nil {
		a.colWidthOverrides = make(map[string]int)
	}
	screenW, _ := a.getScreenSize()
	maxW := max(minColWidth, screenW)

	current, ok := a.colWidthOverrides[name]
	if !ok {
		current = defaultColBase
	}
	newW := clamp(current+delta, minColWidth, maxW)
	a.colWidthOverrides[name] = newW
	a.applyColumnWidths()

	message := fmt.Sprintf("[teal]%s width → %d[-]", tview.Escape(name), newW)
	if err := a.persistColumnWidths(); err != nil {
		message = "[yellow]Width changed for this session, but could not be saved[-]"
	}
	a.flashStatus(message, a.currentResultRowCount(), 1200*time.Millisecond)
}

// resizeAllColumns adjusts every visible column by one step.
func (a *App) resizeAllColumns(delta int) {
	if a == nil || a.results == nil || delta == 0 {
		return
	}
	if a.colWidthOverrides == nil {
		a.colWidthOverrides = make(map[string]int)
	}
	screenW, _ := a.getScreenSize()
	maxW := max(minColWidth, screenW)
	changed := false
	for col := 0; col < a.results.GetColumnCount(); col++ {
		name := a.resultColumnName(col)
		if name == "" {
			continue
		}
		current := defaultColBase
		if override, ok := a.colWidthOverrides[name]; ok {
			current = override
		}
		width := clamp(current+delta*colWidthStep, minColWidth, maxW)
		if width != current {
			changed = true
		}
		a.colWidthOverrides[name] = width
	}
	if !changed {
		return
	}
	a.applyColumnWidths()

	message := "[teal]All columns narrowed[-]"
	if delta > 0 {
		message = "[teal]All columns widened[-]"
	}
	if err := a.persistColumnWidths(); err != nil {
		message = "[yellow]Widths changed for this session, but could not be saved[-]"
	}
	a.flashStatus(message, a.currentResultRowCount(), 1200*time.Millisecond)
}

// resetColumnWidths resets all columns to the uniform default.
func (a *App) resetColumnWidths() {
	a.colWidthOverrides = nil
	a.applyColumnWidths()
	message := "[green]Column widths reset[-]"
	if err := a.persistColumnWidths(); err != nil {
		message = "[yellow]Widths reset for this session, but could not be saved[-]"
	}
	a.flashStatus(message, a.currentResultRowCount(), 1200*time.Millisecond)
}

// clearColumnOverrides resets widths for non-table query results.
func (a *App) clearColumnOverrides() {
	a.colWidthOverrides = nil
}

func (a *App) restoreColumnWidths(table string) {
	a.colWidthOverrides = nil
	if a == nil || a.settings == nil {
		return
	}
	connection, ok := a.activeConnectionKey()
	if !ok {
		return
	}
	columns := a.settings.TableColumnWidths[connection][table]
	if len(columns) == 0 {
		return
	}
	a.colWidthOverrides = make(map[string]int, len(columns))
	for column, width := range columns {
		a.colWidthOverrides[column] = width
	}
}

func (a *App) persistColumnWidths() error {
	if a == nil || a.settings == nil || !a.isTableResultActive() {
		return nil
	}
	connection, ok := a.activeConnectionKey()
	if !ok {
		return nil
	}
	if a.settings.TableColumnWidths == nil {
		a.settings.TableColumnWidths = make(map[string]map[string]map[string]int)
	}
	if len(a.colWidthOverrides) == 0 {
		if tables := a.settings.TableColumnWidths[connection]; tables != nil {
			delete(tables, a.selectedTable)
			if len(tables) == 0 {
				delete(a.settings.TableColumnWidths, connection)
			}
		}
	} else {
		if a.settings.TableColumnWidths[connection] == nil {
			a.settings.TableColumnWidths[connection] = make(map[string]map[string]int)
		}
		columns := make(map[string]int, len(a.colWidthOverrides))
		for column, width := range a.colWidthOverrides {
			columns[column] = width
		}
		a.settings.TableColumnWidths[connection][a.selectedTable] = columns
	}
	return config.SaveSettings(a.settings)
}

// cleanup gracefully closes the database connection
func (a *App) cleanup() {
	a.advanceResultGeneration()
	a.objectGeneration.Add(1)
	a.resetSQLCompletionCatalog()
	a.requestActiveQueryCancel()
	a.cancelActiveResultExport()
	a.clearTableSessionState()
	a.resultNavStack = nil
	if a.db != nil {
		a.db.Close()
		a.db = nil
	}
	a.activeConn = nil
}

// clearTableSessionState drops visual browsing history and remembered filters.
// These hints describe only the active database connection and must never leak
// into the next connection.
func (a *App) clearTableSessionState() {
	if a == nil {
		return
	}
	a.activeTable = ""
	a.visitedTables = nil
	a.selectedTable = ""
	a.tableSearch = ""
	a.sidebarSearchGeneration.Add(1)
	a.expandedSidebarTable = ""
	a.sidebarSearchIndex = nil
	a.sidebarSearchLookup = sidebarSearchLookup{}
	a.sidebarRenderedSearch = sidebarSelection{}
	a.sidebarColumnMetadata = nil
	a.sidebarMetadataLoads = nil
	a.tableResultsActive = false
	a.resultPositions = nil
	a.resultColumnSearch = ""
	a.resultFilter = nil
	a.resultFilters = nil
	a.refreshTableSidebarState()
}

func (a *App) advanceResultGeneration() uint64 {
	if a == nil {
		return 0
	}
	return a.resultGeneration.Add(1)
}

func (a *App) currentResultGeneration() uint64 {
	if a == nil {
		return 0
	}
	return a.resultGeneration.Load()
}

// Run starts the application
func (a *App) Run() error {
	// The backup catalog is intentionally opened on first use. Keep this as a
	// closure so a store opened after Run starts is still closed on exit.
	defer func() {
		if a.backupStore != nil {
			_ = a.backupStore.Close()
		}
		if a.profilerStore != nil {
			_ = a.profilerStore.Close()
		}
	}()
	a.setupUI()
	a.setupKeyBindings()
	a.showDashboard()
	if strings.TrimSpace(a.startupNotice) != "" {
		icon := iconWarn
		suffix := "\n\nThe unreadable file was not silently discarded. Open an issue if you need help recovering it."
		if a.startupNoticeRecovered {
			icon = iconSuccess
			suffix = "\n\nA new private recovery mirror is already in place."
		}
		a.ShowAlert(fmt.Sprintf("%s %s%s", icon, tview.Escape(a.startupNotice), suffix), "dashboard")
		a.startupNotice = ""
	}

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
	if a.guideResize != nil {
		if a.pages != nil && a.pages.HasPage("help") {
			a.guideResize(width)
		} else {
			a.guideResize = nil
		}
	}

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
	completionHeight := 0
	if a.sqlCompletionState.visible && a.sqlCompletionView != nil {
		completionHeight = a.sqlCompletionPopupHeight()
	}
	a.rightFlex.AddItem(a.sqlCompletionView, completionHeight, 0, false)
	a.rightFlex.AddItem(a.results, 0, 1, false)

	if width < 110 {
		tablesHeight := clamp(height/4, 4, 10)
		minResultsHeight := 8
		usedHeight := tablesHeight + queryHeight + completionHeight
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

	tablesWidth := a.sidebarWidth
	if tablesWidth <= 0 {
		tablesWidth = clamp(width/4, 24, 38)
	}
	tablesWidth = clamp(tablesWidth, 18, max(18, width-48))
	a.mainFlex.SetDirection(tview.FlexColumn)
	a.mainFlex.AddItem(a.tables, tablesWidth, 0, true)
	a.mainFlex.AddItem(a.rightFlex, 0, 1, false)
	a.updateStatusBar("", a.currentResultRowCount())
}

func (a *App) statusActionText(width int) string {
	inQuery := a.focusedPanel == a.queryInput
	inResults := a.focusedPanel == a.results
	inTables := a.focusedPanel == a.tables
	filterActive := a.isTableResultActive() && a.activeResultFilter(a.selectedTable) != nil
	tableActive := a.isTableResultActive()
	paletteKey := tview.Escape(a.commandPaletteShortcutHint())
	schemaKey := a.taggedActionShortcut(actionInspectSchema)
	exportKey := a.taggedActionShortcut(actionExportCSV)
	helpKey := a.taggedActionShortcut(actionHelp)
	dashboardKey := a.taggedActionShortcut(actionDashboard)
	selectAllKey := a.taggedActionShortcut(actionSelectAll)
	clearSelectionKey := a.taggedActionShortcut(actionClearSelection)
	if inQuery && a.sqlCompletionState.visible {
		return "[yellow]↑/↓[-] Choose  │  [yellow]Tab/Enter[-] Insert  │  [yellow]Esc[-] Close"
	}
	if inResults && a.resultHeaderSelected() {
		find := ""
		if a.resultColumnSearch != "" {
			find = fmt.Sprintf(" [#a6adc8]find:[-][yellow]%s[-]  │ ", tview.Escape(a.resultColumnSearch))
		}
		full := find + "[yellow]Type[-] Find column  │  [yellow]←/→[-] Headers  │  [yellow]↓/Enter[-] Data  │  [yellow]Shift+C[-] Copy name  │  [yellow]Tab[-] Tables"
		medium := find + "[yellow]Type[-] Find  │  [yellow]←/→[-] Move  │  [yellow]↓[-] Data  │  [yellow]Shift+C[-] Copy  │  [yellow]Tab[-] Tables"
		short := find + "[yellow]Type[-] Find column  │  [yellow]↓[-] Data  │  [yellow]Tab[-] Tables"
		minimal := "[yellow]↓[-] Data  │  [yellow]Tab[-] Tables"
		return footerTextThatFits(max(1, width-2), full, medium, short, minimal)
	}
	if inTables {
		full := fmt.Sprintf("[yellow]Type + ↑/↓[-] Find objects  │  [yellow]Drag edge[-] Resize  │  [yellow]Space[-] Pin  │  [yellow]Enter[-] Open  │  [yellow]Shift+C/Right-click[-] Copy  │  %s Schema  │  [yellow]%s[-] Palette", schemaKey, paletteKey)
		medium := fmt.Sprintf("[yellow]→/←[-] Schema  │  [yellow]Type + ↑/↓[-] Find objects  │  [yellow]Space[-] Pin  │  [yellow]Enter[-] Open  │  [yellow]Shift+C[-] Copy  │  [yellow]%s[-] Palette", paletteKey)
		short := "[yellow]→/←[-] Schema  │  [yellow]Space[-] Pin  │  [yellow]Enter[-] Open  │  [yellow]Shift+C[-] Copy  │  [yellow]Esc[-] Back"
		compact := "[yellow]Enter[-] Open  │  [yellow]Shift+C[-] Copy  │  [yellow]Esc[-] Back"
		minimal := "[yellow]Shift+C[-] Copy  │  [yellow]Esc[-] Back"
		return footerTextThatFits(max(1, width-2), full, medium, short, compact, minimal)
	}
	switch {
	case width < 72:
		if inQuery {
			return "[yellow]Enter[-] Run ▶  │  [yellow]Esc[-] Back"
		}
		if inResults {
			if filterActive {
				return "[yellow]Esc[-] Clear filter  │  [yellow]C[-] Copy  │  [yellow]/[-] Find"
			}
			return fmt.Sprintf("[yellow]C[-] Copy  │  [yellow]/[-] Filter  │  %s CSV", exportKey)
		}
		return fmt.Sprintf("[yellow]Space[-] Select  │  %s CSV  │  [yellow]Esc[-] Back", exportKey)
	case width < 90:
		if inQuery {
			return fmt.Sprintf("[yellow]Enter[-] Run ▶  │  [yellow]Shift+Enter[-] Newline  │  [yellow]Esc[-] Back  │  %s Guide %s", helpKey, iconHelp)
		}
		if inResults {
			if filterActive {
				return fmt.Sprintf("[yellow]Esc[-] Clear filter  │  [yellow]/[-] Change  │  %s CSV", exportKey)
			}
			return fmt.Sprintf("[yellow]C[-] Copy  │  [yellow]/[-] Filter  │  [yellow]V[-] Clipboard  │  %s CSV", exportKey)
		}
		return fmt.Sprintf("[yellow]Space[-] Select  │  %s/%s All/Clear  │  %s CSV  │  %s %s", selectAllKey, clearSelectionKey, exportKey, helpKey, iconHelp)
	case width < 120:
		if inQuery {
			return fmt.Sprintf("[yellow]Enter[-] Run ▶  │  [yellow]Shift+Enter[-] Newline  │  [yellow]F5[-] %s  │  %s/[yellow]Esc[-] Dash %s",
				iconRefresh, dashboardKey, iconDashboard)
		}
		if inResults {
			if filterActive {
				return fmt.Sprintf("[yellow]Esc[-] Clear filter  │  [yellow]/[-] Change  │  [yellow]C[-] Copy  │  %s CSV  │  %s %s", exportKey, helpKey, iconHelp)
			}
			if tableActive {
				return fmt.Sprintf("[yellow]C[-] Copy  │  [yellow]/[-] Filter  │  [yellow]V[-] Clipboard  │  [yellow]F[-] Follow FK  │  %s CSV", exportKey)
			}
			return fmt.Sprintf("[yellow]C[-] Copy  │  [yellow]Enter[-] Detail  │  %s CSV  │  [yellow]%s[-] Palette", exportKey, paletteKey)
		}
		return fmt.Sprintf("[yellow]F5[-] %s  │  [yellow]Space[-] Toggle Sel  │  %s/%s/%s All/Clear/CSV  │  [yellow]Enter[-] Detail  │  %s/[yellow]Esc[-] Dash %s",
			iconRefresh, selectAllKey, clearSelectionKey, exportKey, dashboardKey, iconDashboard)
	default:
		if inQuery {
			return fmt.Sprintf("[yellow]Enter[-] Run ▶  │  [yellow]Shift+Enter[-] Newline  │  [yellow]F5[-] %s  │  %s Guide %s  │  [yellow]Esc/Bksp[-] Dashboard %s",
				iconRefresh, helpKey, iconHelp, iconDashboard)
		}
		if inResults {
			if filterActive {
				return fmt.Sprintf("[yellow]Esc[-] Clear filters  │  [yellow]/[-] Change  │  [yellow]C[-] Copy  │  [yellow]V[-] Clipboard  │  [yellow]F[-] Follow FK  │  %s CSV  │  [yellow]%s[-] Palette", exportKey, paletteKey)
			}
			if tableActive {
				return fmt.Sprintf("[yellow]C[-] Copy  │  [yellow]/[-] Filter  │  [yellow]V[-] Clipboard  │  [yellow]F[-] Follow FK  │  %s CSV  │  [yellow]Enter[-] Detail  │  [yellow]F5[-] %s  │  [yellow]%s[-] Palette", exportKey, iconRefresh, paletteKey)
			}
			return fmt.Sprintf("[yellow]C[-] Copy  │  [yellow]Space[-] Select  │  [yellow]Enter[-] Detail  │  %s CSV  │  [yellow]%s[-] Palette  │  %s %s", exportKey, paletteKey, helpKey, iconHelp)
		}
		return fmt.Sprintf("[yellow]F5[-] %s  │  [yellow]Space[-] Toggle Sel  │  %s All  │  %s Clear  │  %s CSV  │  [yellow]Enter[-] Detail  │  %s Guide %s  │  [yellow]Esc/Bksp[-] Dashboard %s",
			iconRefresh, selectAllKey, clearSelectionKey, exportKey, helpKey, iconHelp, iconDashboard)
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
	if a == nil || a.statusBar == nil {
		return
	}
	a.updateStatusBar(extra, rowCount)
	if a.app == nil {
		return
	}
	go func() {
		time.Sleep(duration)
		a.app.QueueUpdateDraw(func() {
			a.updateStatusBar("", rowCount)
		})
	}()
}

func (a *App) tableExistsInList(name string) bool {
	for _, identifier := range a.tableIdentifiers {
		if identifier == name {
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

func (a *App) queueUpdateDraw(fn func()) {
	if fn == nil {
		return
	}
	if a.app == nil {
		fn()
		return
	}
	go a.app.QueueUpdateDraw(fn)
}
