package ui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/appdirs"
)

func (a *App) showBackupLogViewer() {
	const page = "backupAgentLogs"
	returnPage, _ := a.pages.GetFrontPage()
	returnFocus := a.app.GetFocus()
	directory, err := appdirs.LogDir()
	if err != nil {
		a.ShowAlert(tview.Escape(err.Error()), returnPage)
		return
	}
	const limit = int64(64 * 1024)
	content, paths, err := loadBackupAgentLogTail(directory, limit)
	if err != nil {
		a.ShowAlert(tview.Escape(err.Error()), returnPage)
		return
	}
	view := tview.NewTextView().SetWrap(false).SetScrollable(true)
	view.SetBorder(true).SetBorderColor(surface1).SetTitleColor(mauve).SetBackgroundColor(bg)
	view.SetTextColor(text)
	query := tview.NewInputField().SetLabel(" Filter text: ").SetFieldWidth(0)
	query.SetFieldBackgroundColor(surface0).SetFieldTextColor(text).SetLabelColor(blue)
	query.SetBackgroundColor(bg)
	var following atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	shown := ""
	render := func() {
		mode := "paused"
		if following.Load() {
			mode = "following every 2s"
		}
		view.SetTitle(" Agent Logs · bounded tail · " + mode + " ")
		shown = filterBackupLogText(content, query.GetText())
		view.SetText(shown)
		if following.Load() {
			view.ScrollToEnd()
		}
	}
	refresh := func() {
		fresh, freshPaths, readErr := loadBackupAgentLogTail(directory, limit)
		if readErr != nil {
			view.SetTitle(" Agent Logs · refresh failed ")
			view.SetText(readErr.Error())
			return
		}
		content, paths = fresh, freshPaths
		render()
	}
	query.SetChangedFunc(func(string) {
		render()
		if !following.Load() {
			view.ScrollToBeginning()
		}
	})
	query.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter || key == tcell.KeyEscape || key == tcell.KeyTab {
			a.app.SetFocus(view)
		}
	})
	view.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyBackspace || event.Key() == tcell.KeyBackspace2 {
			cancel()
			a.pages.RemovePage(page)
			if a.pages.HasPage(returnPage) {
				a.pages.ShowPage(returnPage)
				a.app.SetFocus(returnFocus)
			} else {
				a.showBackupCenter()
			}
			return nil
		}
		if event.Key() == tcell.KeyTab {
			a.app.SetFocus(query)
			return nil
		}
		if event.Key() == tcell.KeyUp || event.Key() == tcell.KeyPgUp || event.Key() == tcell.KeyHome {
			following.Store(false)
			render()
		}
		if key, ok := plainShortcutRune(event); ok {
			switch key {
			case '/':
				a.app.SetFocus(query)
				return nil
			case 'f':
				following.Store(!following.Load())
				refresh()
				return nil
			case 'r':
				refresh()
				return nil
			case 'c', 'p':
				value := shown
				if key == 'p' {
					value = strings.Join(paths, "\n")
				}
				a.copyValueAsync(value, func(copyErr error) {
					if ctx.Err() != nil {
						return
					}
					if copyErr != nil {
						a.ShowAlert(tview.Escape(fmt.Sprintf("Copied internally; system clipboard unavailable: %v", copyErr)), page)
					} else {
						a.ShowAlert("Copied.", page)
					}
				})
				return nil
			}
		}
		return event
	})
	footer := tview.NewTextView().SetDynamicColors(true).SetText(" [yellow]/[-] Filter · [yellow]F[-] Follow/pause · [yellow]R[-] Refresh · [yellow]C[-] Copy text · [yellow]P[-] Paths · [yellow]Esc[-] Back")
	footer.SetBackgroundColor(crust)
	body := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(query, 1, 0, false).AddItem(view, 0, 1, true).AddItem(footer, 1, 0, false)
	layout := &backupAuxLayout{Flex: body, footer: footer, hints: []string{footer.GetText(false), " [yellow]/[-] Filter · [yellow]F[-] Follow · [yellow]R[-] Refresh · [yellow]C[-] Copy · [yellow]P[-] Paths · [yellow]Esc[-] Back", " [yellow]/[-] Filter · [yellow]F[-] Follow · [yellow]Esc[-] Back"}}
	a.pages.AddAndSwitchToPage(page, layout, true)
	a.app.SetFocus(view)
	render()
	// One worker per open viewer; closing cancels it, and hidden overlays are
	// never overwritten. All widget access stays on the terminal event loop.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !following.Load() {
					continue
				}
				fresh, freshPaths, readErr := loadBackupAgentLogTail(directory, limit)
				a.app.QueueUpdateDraw(func() {
					if ctx.Err() != nil || !following.Load() {
						return
					}
					if !a.pages.HasPage(page) || a.pages.GetPage(page) != layout {
						cancel()
						return
					}
					if front, _ := a.pages.GetFrontPage(); front != page {
						return
					}
					if readErr != nil {
						view.SetTitle(" Agent Logs · refresh failed ")
						view.SetText(readErr.Error())
						return
					}
					content, paths = fresh, freshPaths
					render()
				})
			}
		}
	}()
}
