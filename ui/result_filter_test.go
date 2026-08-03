package ui

import (
	"testing"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

func TestResultFilterClauseUsesDatabasePlaceholderAndQuotedColumn(t *testing.T) {
	tests := []struct {
		name   string
		dbType config.DBType
		column string
		want   string
	}{
		{name: "postgres", dbType: config.PostgreSQL, column: "user_id", want: ` WHERE "user_id" = $1`},
		{name: "mysql", dbType: config.MySQL, column: "user_id", want: " WHERE `user_id` = ?"},
		{name: "sqlite", dbType: config.SQLite, column: "user_id", want: ` WHERE "user_id" = ?`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resultFilterClause(tt.dbType, tt.column); got != tt.want {
				t.Fatalf("resultFilterClause() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCurrentResultCellUsesUntruncatedReferenceValue(t *testing.T) {
	results := tview.NewTable().SetSelectable(true, true)
	results.SetCell(0, 0, tview.NewTableCell("USER_ID").SetReference("user_id"))
	results.SetCell(1, 0, tview.NewTableCell("preview...").SetReference(resultCellReference{value: "complete-value"}))
	results.Select(1, 0)

	app := &App{results: results}
	_, _, column, value, ok := app.currentResultCell()
	if !ok {
		t.Fatal("expected selected result cell")
	}
	if column != "user_id" {
		t.Fatalf("column = %q, want user_id", column)
	}
	if value != "complete-value" {
		t.Fatalf("value = %q, want complete-value", value)
	}
}

func TestClipboardValueFallsBackToInternalCopy(t *testing.T) {
	app := &App{
		copiedCellValue:    "42",
		hasCopiedCellValue: true,
		copiedCellSystem:   false,
	}
	value, err := app.clipboardValue()
	if err != nil {
		t.Fatalf("clipboardValue() error = %v", err)
	}
	if value != "42" {
		t.Fatalf("clipboardValue() = %q, want 42", value)
	}
}

func TestTrimClipboardLineEndingPreservesOtherWhitespace(t *testing.T) {
	if got := trimClipboardLineEnding("  value  \r\n"); got != "  value  " {
		t.Fatalf("trimClipboardLineEnding() = %q", got)
	}
}

func TestResultValuePreviewKeepsStatusOnOneLine(t *testing.T) {
	if got := resultValuePreview("first\nsecond", 40); got != `first\nsecond` {
		t.Fatalf("resultValuePreview() = %q", got)
	}
}
