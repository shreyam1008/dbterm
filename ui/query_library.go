package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

const (
	pageQueryHistoryModal = "queryHistoryModal"
)

type queryLibraryItem struct {
	main      string
	secondary string
	sql       string
}

func (a *App) recordQueryHistory(query string) {
	if a.historyMgr == nil {
		return
	}

	connectionKey, ok := a.activeConnectionKey()
	if !ok {
		return
	}

	if err := a.historyMgr.Append(connectionKey, query); err != nil {
		fmt.Printf("⚠ Warning: failed to persist query history: %v\n", err)
	}
}

func (a *App) showHistoryModal() {
	if a.historyMgr == nil {
		a.ShowAlert(fmt.Sprintf("%s Query history is unavailable.\n\nCheck permissions for ~/.config/dbterm and restart dbterm.", iconWarn), "main")
		return
	}

	connectionKey, ok := a.activeConnectionKey()
	if !ok {
		a.ShowAlert(fmt.Sprintf("%s No active connection.\n\nConnect to a database first, then press Alt+Y.", iconInfo), "main")
		return
	}

	entries := a.historyMgr.Entries(connectionKey)
	if len(entries) == 0 {
		a.ShowAlert(fmt.Sprintf("%s No query history yet for this connection.\n\nRun a query and press Alt+Y again.", iconInfo), "main")
		return
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	items := make([]queryLibraryItem, 0, len(entries))
	for _, entry := range entries {
		timestamp := "unknown time"
		if !entry.Timestamp.IsZero() {
			timestamp = entry.Timestamp.Local().Format("2006-01-02 15:04:05")
		}
		items = append(items, queryLibraryItem{
			main:      " " + truncateForDisplay(compactSQL(entry.SQL), 100),
			secondary: " " + timestamp,
			sql:       entry.SQL,
		})
	}

	a.showQueryLibraryListModal(
		pageQueryHistoryModal,
		fmt.Sprintf(" %s Query History (Newest First) ", iconQuery),
		" [yellow]Enter[-] Load query  │  [yellow]Esc[-] Close ",
		items,
	)
}

func (a *App) showQueryLibraryListModal(pageName, title, footerText string, items []queryLibraryItem) {
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).
		SetTitle(title).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text)
	list.SetSecondaryTextColor(subtext0)
	list.SetSelectedBackgroundColor(surface0)
	list.SetSelectedTextColor(green)

	for _, item := range items {
		list.AddItem(item.main, item.secondary, 0, nil)
	}

	restoreFocus := func() {
		a.pages.RemovePage(pageName)
		if a.focusedPanel != nil {
			a.app.SetFocus(a.focusedPanel)
			return
		}
		a.app.SetFocus(a.tables)
	}

	list.SetSelectedFunc(func(index int, _ string, _ string, _ rune) {
		if index < 0 || index >= len(items) {
			return
		}
		a.pages.RemovePage(pageName)
		a.queryInput.SetText(items[index].sql, true)
		a.setFocusWithColor(a.queryInput)
		a.flashStatus(
			fmt.Sprintf("[green]%s Query loaded[-]", iconSuccess),
			a.currentResultRowCount(),
			1400*time.Millisecond,
		)
	})

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyBackspace || event.Key() == tcell.KeyBackspace2 {
			restoreFocus()
			return nil
		}
		return event
	})

	footer := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(footerText)
	footer.SetBackgroundColor(crust)

	container := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(list, 0, 1, true).
		AddItem(footer, 1, 0, false)

	modalW, modalH := a.modalSize(68, 104, 12, 24)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(container, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(list)
}

func (a *App) activeConnectionKey() (string, bool) {
	cfg := a.currentConnectionConfig()
	if cfg == nil {
		return "", false
	}

	signature := stableConnectionSignature(cfg)
	if signature == "" {
		return "", false
	}

	hash := sha256.Sum256([]byte(signature))
	return hex.EncodeToString(hash[:]), true
}

func stableConnectionSignature(cfg *config.ConnectionConfig) string {
	if cfg == nil {
		return ""
	}

	parts := []string{"v1", string(cfg.Type)}
	switch cfg.Type {
	case config.SQLite:
		path := strings.TrimSpace(cfg.FilePath)
		if path != "" {
			path = filepath.Clean(path)
		}
		parts = append(parts, "file="+path)
	case config.Turso:
		parts = append(parts,
			"host="+strings.ToLower(strings.TrimSpace(cfg.Host)),
			"database="+strings.ToLower(strings.TrimSpace(cfg.Database)),
			"user="+strings.ToLower(strings.TrimSpace(cfg.User)),
		)
	case config.CloudflareD1:
		parts = append(parts,
			"account_id="+strings.ToLower(strings.TrimSpace(cfg.AccountID)),
			"database_id="+strings.ToLower(strings.TrimSpace(cfg.DatabaseID)),
		)
	default:
		parts = append(parts,
			"host="+strings.ToLower(strings.TrimSpace(cfg.Host)),
			"port="+normalizedConnectionPort(cfg.Type, cfg.Port),
			"database="+strings.ToLower(strings.TrimSpace(cfg.Database)),
			"user="+strings.ToLower(strings.TrimSpace(cfg.User)),
		)
	}
	return strings.Join(parts, "|")
}

func normalizedConnectionPort(dbType config.DBType, port string) string {
	trimmedPort := strings.TrimSpace(port)
	if trimmedPort != "" {
		return trimmedPort
	}
	switch dbType {
	case config.MySQL:
		return "3306"
	case config.PostgreSQL:
		return "5432"
	default:
		return ""
	}
}

func compactSQL(query string) string {
	parts := strings.Fields(strings.TrimSpace(query))
	if len(parts) == 0 {
		return "(empty query)"
	}
	return strings.Join(parts, " ")
}
