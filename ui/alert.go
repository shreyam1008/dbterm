package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type statusSeverity string

const (
	statusInfo    statusSeverity = "info"
	statusSuccess statusSeverity = "success"
	statusWarning statusSeverity = "warning"
	statusError   statusSeverity = "error"
)

// ShowAlert displays a modal alert and returns to returnPage when dismissed.
// Reserve this for blocking/destructive/unrecoverable flows.
func (a *App) ShowAlert(message string, returnPage string) {
	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"  OK  "}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			a.pages.RemovePage("alert")
			if returnPage != "" {
				a.pages.ShowPage(returnPage)
			}
		})

	modal.SetBackgroundColor(bg).
		SetButtonBackgroundColor(surface1).
		SetButtonTextColor(green).
		SetTextColor(text)

	a.pages.AddPage("alert", modal, true, true)
	a.app.SetFocus(modal)
}

func (a *App) showStatusMessage(severity statusSeverity, message string, rowCount int) {
	a.showTimedStatusMessage(severity, message, rowCount, 2200*time.Millisecond)
}

func (a *App) showTimedStatusMessage(severity statusSeverity, message string, rowCount int, duration time.Duration) {
	a.flashStatus(fmt.Sprintf("[%s]%s %s[-]", statusSeverityColor(severity), statusSeverityIcon(severity), message), rowCount, duration)
}

func (a *App) showErrorStatus(summary, details string, rowCount int) {
	a.lastErrorDetails = strings.TrimSpace(details)
	if a.lastErrorDetails != "" {
		summary = strings.TrimSpace(summary) + " [gray](Alt+E for details)[-]"
	}
	a.showStatusMessage(statusError, summary, rowCount)
}

func statusSeverityColor(severity statusSeverity) string {
	switch severity {
	case statusSuccess:
		return "green"
	case statusWarning:
		return "yellow"
	case statusError:
		return "red"
	default:
		return "teal"
	}
}

func statusSeverityIcon(severity statusSeverity) string {
	switch severity {
	case statusSuccess:
		return iconSuccess
	case statusWarning:
		return iconWarn
	case statusError:
		return iconFail
	default:
		return iconInfo
	}
}

func (a *App) showLastErrorDetails(returnPage string) {
	if strings.TrimSpace(a.lastErrorDetails) == "" {
		a.showStatusMessage(statusInfo, "No error details to show", a.currentResultRowCount())
		return
	}

	detail := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetText(a.lastErrorDetails + "\n\n[gray]Esc closes details.[-]")
	detail.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Error Details ", iconFail)).
		SetBorderColor(red).
		SetTitleColor(red)
	detail.SetBackgroundColor(bg)
	detail.SetTextColor(text)

	detail.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyEnter {
			a.pages.RemovePage("error_details")
			if returnPage != "" {
				a.pages.ShowPage(returnPage)
			}
			return nil
		}
		return event
	})

	modalW, modalH := a.modalSize(60, 100, 16, 32)
	grid := tview.NewGrid().
		SetColumns(0, modalW, 0).
		SetRows(0, modalH, 0).
		AddItem(detail, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage("error_details", grid, true, true)
	a.app.SetFocus(detail)
}
