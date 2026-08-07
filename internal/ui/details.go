package ui

import (
	"fmt"
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
			if ref, ok := cell.GetReference().(resultCellReference); ok {
				val = ref.value
			}
		}

		// Field name column
		table.SetCell(i+1, 0, tview.NewTableCell(fmt.Sprintf(" %s ", colName)).
			SetTextColor(blue).
			SetAlign(tview.AlignRight).
			SetReference(colName))

		// Value column
		table.SetCell(i+1, 1, tview.NewTableCell(fmt.Sprintf(" %s ", val)).
			SetTextColor(text).
			SetExpansion(1).
			SetReference(val))
	}
	table.Select(1, 1)

	// Instructions footer for the modal
	instruction := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(" [yellow]Esc/Enter[-] Close  │  [yellow]c[-] Copy selected cell  │  [yellow]Arrows[-] Move ")
	instruction.SetBackgroundColor(crust)

	detailsFlex.AddItem(table, 0, 1, true)
	detailsFlex.AddItem(instruction, 1, 0, false)

	// Modal shadow background
	// We use a pages concept to overlay.
	// Since we want this to act like a modal, we can put it in a centered flex.

	// Create a centered frame
	center := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(detailsFlex, 0, 3, true).
		AddItem(nil, 0, 1, false)
	frame := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(center, 0, 3, true).
		AddItem(nil, 0, 1, false)

	// Key bindings
	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyEnter {
			a.pages.RemovePage("row_details")
			a.app.SetFocus(a.results)
			return nil
		}
		if event.Rune() == 'c' || event.Rune() == 'C' {
			selectedRow, selectedCol := table.GetSelection()
			if cell := table.GetCell(selectedRow, selectedCol); cell != nil {
				value := strings.TrimSpace(cell.Text)
				if rawValue, ok := cell.GetReference().(string); ok {
					value = rawValue
				}
				a.copyValueAsync(value, func(err error) {
					if err != nil {
						a.flashStatus("[yellow]Cell copied inside dbterm (system clipboard unavailable)[-]", a.currentResultRowCount(), 2*time.Second)
					}
				})
				a.flashStatus("[green]Cell copied[-]", a.currentResultRowCount(), 2*time.Second)
			}
			return nil
		}
		return event
	})

	a.pages.AddPage("row_details", frame, true, true)
	a.app.SetFocus(table)
}
