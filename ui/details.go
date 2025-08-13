package ui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// showRowDetail displays a modal with all columns/values for a specific row
func (a *App) showRowDetail(row int) {
	if a.results == nil || row <= 0 { // row 0 is header
		return
	}

	colCount := a.results.GetColumnCount()
	if colCount == 0 {
		return
	}

	// Create a new flex container for the detail view
	detailsFlex := tview.NewFlex().SetDirection(tview.FlexRow)
	detailsFlex.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s Row Details (Row %d) ", iconResults, row)).
		SetBorderColor(yellow).
		SetTitleColor(mauve).
		SetBackgroundColor(mantle)

	// Create a table to show Field | Value
	table := tview.NewTable().
		SetBorders(true).
		SetSelectable(true, true)
	table.SetBackgroundColor(mantle)

	// Headers
	table.SetCell(0, 0, tview.NewTableCell(" Column ").
		SetTextColor(peach).
		SetAttributes(tcell.AttrBold).
		SetSelectable(false))
	table.SetCell(0, 1, tview.NewTableCell(" Value ").
		SetTextColor(peach).
		SetAttributes(tcell.AttrBold).
		SetSelectable(false))

	// Populate data
	for i := 0; i < colCount; i++ {
		colName := ""
		headerCell := a.results.GetCell(0, i)
		if headerCell != nil {
			// Strip sort indicators
			colName = strings.TrimSuffix(strings.TrimSuffix(headerCell.Text, " ▲"), " ▼")
		}

		val := ""
		cell := a.results.GetCell(row, i)
		if cell != nil {
			val = cell.Text
		}

		// Field name column
		table.SetCell(i+1, 0, tview.NewTableCell(fmt.Sprintf(" %s ", colName)).
			SetTextColor(blue).
			SetAlign(tview.AlignRight))

		// Value column
		table.SetCell(i+1, 1, tview.NewTableCell(fmt.Sprintf(" %s ", val)).
			SetTextColor(text).
			SetExpansion(1))
	}

	// Instructions footer for the modal
	instruction := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(" [yellow]Esc/Enter[-] Close  │  [yellow]c[-] Copy CSV  │  [yellow]↑/↓[-] Scroll ")
	instruction.SetBackgroundColor(crust)

	detailsFlex.AddItem(table, 0, 1, true)
	detailsFlex.AddItem(instruction, 1, 0, false)

	// Modal shadow background
	// We use a pages concept to overlay. 
	// Since we want this to act like a modal, we can put it in a centered flex.
	
	// Create a centered frame
	frame := tview.NewFlex().
		AddItem(nil, 0, 1, false). // Left spacer
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false). // Top spacer
			AddItem(detailsFlex, 0, 3, true). // Content (60% height)
			AddItem(nil, 0, 1, false), // Bottom spacer
			0, 3, true). // Width (60% width)
		AddItem(nil, 0, 1, false) // Right spacer

	// Key bindings
	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyEnter {
			a.pages.RemovePage("row_details")
			a.app.SetFocus(a.results)
			return nil
		}
		if event.Rune() == 'c' || event.Rune() == 'C' {
			// Build CSV content
			var sb strings.Builder
			sb.WriteString("Column,Value\n")
			for i := 0; i < colCount; i++ {
				colName := ""
				if h := a.results.GetCell(0, i); h != nil {
					colName = strings.TrimSuffix(strings.TrimSuffix(h.Text, " ▲"), " ▼")
				}
				val := ""
				if c := a.results.GetCell(row, i); c != nil {
					val = c.Text
				}
				// Simple CSV escaping: if contains comma or quote, wrap in quotes and escape quotes
				if strings.ContainsAny(val, "\",\n") {
					val = "\"" + strings.ReplaceAll(val, "\"", "\"\"") + "\""
				}
				sb.WriteString(fmt.Sprintf("%s,%s\n", colName, val))
			}
			
			if err := copyToClipboard(sb.String()); err != nil {
				a.ShowAlert(fmt.Sprintf("Failed to copy: %v", err), "row_details")
			} else {
				a.flashStatus(" [green]Row copied to clipboard![-]", a.currentResultRowCount(), 2*time.Second)
			}
			return nil
		}
		return event
	})

	a.pages.AddPage("row_details", frame, true, true)
	a.app.SetFocus(table)
}

// copyToClipboard tries to copy text using standard Linux/Unix utilities
func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	
	// Try xclip
	if _, err := exec.LookPath("xclip"); err == nil {
		cmd = exec.Command("xclip", "-selection", "clipboard")
	} else if _, err := exec.LookPath("wl-copy"); err == nil {
		// Wayland
		cmd = exec.Command("wl-copy")
	} else if _, err := exec.LookPath("pbcopy"); err == nil {
		// macOS
		cmd = exec.Command("pbcopy")
	} else {
		return fmt.Errorf("no clipboard utility found (install xclip or wl-copy)")
	}
	
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
