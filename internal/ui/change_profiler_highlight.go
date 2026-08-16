package ui

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	profiler "github.com/shreyam1008/dbterm/internal/changeprofiler"
)

func (a *App) applyProfilerHighlightsToResults(table string) {
	if a == nil || a.results == nil || a.profilerStore == nil || a.profilerAnchorID == "" {
		return
	}
	summary, ok := a.profilerTableChanges[table]
	if !ok || len(summary.KeyColumns) == 0 || summary.KeyKind == profiler.KeyFullRow || summary.KeyKind == profiler.KeyRowID {
		return
	}
	diffs, err := a.profilerStore.ListDiffRows(context.Background(), a.profilerAnchorID, table, 5000)
	if err != nil {
		return
	}
	columns := make(map[string]int, a.results.GetColumnCount())
	for col := 0; col < a.results.GetColumnCount(); col++ {
		columns[a.resultColumnName(col)] = col
	}
	byKey := make(map[string][]profiler.DiffRow, len(diffs))
	for _, diff := range diffs {
		if diff.Kind == profiler.DiffDeleted {
			continue
		}
		key := profilerExpectedKey(summary.KeyColumns, diff)
		byKey[key] = append(byKey[key], diff)
	}
	for row := 1; row < a.results.GetRowCount(); row++ {
		for _, diff := range byKey[profilerResultKey(a.results, row, columns, summary.KeyColumns)] {
			if !resultRowMatchesDiff(a.results, row, columns, summary.KeyColumns, diff) {
				continue
			}
			changed := make(map[string]bool, len(diff.ChangedColumns))
			for _, name := range diff.ChangedColumns {
				changed[name] = true
			}
			for col := 0; col < a.results.GetColumnCount(); col++ {
				cell := a.results.GetCell(row, col)
				if cell == nil {
					continue
				}
				ref, ok := cell.GetReference().(resultCellReference)
				if !ok {
					continue
				}
				ref.profilerKind = string(diff.Kind)
				ref.profilerCell = changed[a.resultColumnName(col)]
				cell.SetReference(ref)
				a.restoreProfilerCellStyle(cell)
			}
			break
		}
	}
}

func profilerExpectedKey(keys []string, diff profiler.DiffRow) string {
	values := diff.After
	if diff.Kind == profiler.DiffDeleted {
		values = diff.Before
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		parts = append(parts, profilerKeyPart(key, value.Text, value.Null))
	}
	return strings.Join(parts, "|")
}

func profilerResultKey(results interface {
	GetCell(row, column int) *tview.TableCell
}, row int, columns map[string]int, keys []string) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		col, ok := columns[key]
		if !ok {
			return ""
		}
		cell := results.GetCell(row, col)
		if cell == nil {
			return ""
		}
		ref, ok := cell.GetReference().(resultCellReference)
		if !ok {
			return ""
		}
		value := ref.value
		if bytes, ok := ref.rawValue.([]byte); ok {
			value = "0x" + hex.EncodeToString(bytes)
		}
		parts = append(parts, profilerKeyPart(key, value, ref.isNull))
	}
	return strings.Join(parts, "|")
}

func profilerKeyPart(name, value string, null bool) string {
	if null {
		value = ""
	}
	return strconv.Itoa(len(name)) + ":" + name + ":" + strconv.FormatBool(null) + ":" + strconv.Itoa(len(value)) + ":" + value
}

func resultRowMatchesDiff(results interface {
	GetCell(row, column int) *tview.TableCell
}, row int, columns map[string]int, keys []string, diff profiler.DiffRow) bool {
	values := diff.After
	if diff.Kind == profiler.DiffDeleted {
		values = diff.Before
	}
	for _, key := range keys {
		col, ok := columns[key]
		if !ok {
			return false
		}
		cell := results.GetCell(row, col)
		ref, ok := cell.GetReference().(resultCellReference)
		if !ok || !profilerValueMatches(ref, values[key]) {
			return false
		}
	}
	return true
}

func profilerValueMatches(ref resultCellReference, expected profiler.Value) bool {
	if expected.Null {
		return ref.isNull
	}
	if ref.isNull {
		return false
	}
	if expected.Kind == "bytes" {
		if bytes, ok := ref.rawValue.([]byte); ok {
			return expected.Text == "0x"+hex.EncodeToString(bytes)
		}
	}
	return expected.Text == ref.value
}

func (a *App) restoreProfilerCellStyle(cell *tview.TableCell) {
	if cell == nil {
		return
	}
	ref, ok := cell.GetReference().(resultCellReference)
	if !ok || ref.profilerKind == "" {
		cell.SetBackgroundColor(tcell.ColorDefault)
		cell.SetTransparency(true)
		return
	}
	cell.SetTransparency(false)
	switch profiler.DiffKind(ref.profilerKind) {
	case profiler.DiffInserted:
		cell.SetBackgroundColor(insertRowBG)
	case profiler.DiffUpdated:
		if ref.profilerCell {
			cell.SetBackgroundColor(updateCellBG)
		} else {
			cell.SetBackgroundColor(updateRowBG)
		}
	default:
		cell.SetBackgroundColor(tcell.ColorDefault)
		cell.SetTransparency(true)
	}
}
