package ui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
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

func TestResultFilterSQLComposesOrderedParameterizedPredicates(t *testing.T) {
	filter := newResultValueFilter("orders", []resultFilterPredicate{
		{column: "status", operator: resultFilterEqual, value: "paid"},
		{column: "total", operator: resultFilterGreaterEqual, value: int64(100)},
		{column: "deleted_at", operator: resultFilterIsNull, value: "ignored"},
		{column: "region", operator: resultFilterNotEqual, value: "test"},
	})

	clause, args := resultFilterSQL(config.PostgreSQL, filter)
	wantClause := ` WHERE "status" = $1 AND "total" >= $2 AND "deleted_at" IS NULL AND "region" <> $3`
	if clause != wantClause {
		t.Fatalf("resultFilterSQL() clause = %q, want %q", clause, wantClause)
	}
	wantArgs := []any{"paid", int64(100), "test"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("resultFilterSQL() args = %#v, want %#v", args, wantArgs)
	}
}

func TestResultFilterSQLUsesQuestionPlaceholdersOutsidePostgres(t *testing.T) {
	filter := newResultValueFilter("users", []resultFilterPredicate{
		{column: "age", operator: resultFilterGreater, value: "18"},
		{column: "age", operator: resultFilterLessEqual, value: "65"},
		{column: "email", operator: resultFilterIsNotNull},
	})

	clause, args := resultFilterSQL(config.MySQL, filter)
	if want := " WHERE `age` > ? AND `age` <= ? AND `email` IS NOT NULL"; clause != want {
		t.Fatalf("resultFilterSQL() clause = %q, want %q", clause, want)
	}
	if !reflect.DeepEqual(args, []any{"18", "65"}) {
		t.Fatalf("resultFilterSQL() args = %#v", args)
	}
}

func TestResultFilterSQLContainsAndStartsWithEscapeWildcards(t *testing.T) {
	filter := newResultValueFilter("items", []resultFilterPredicate{
		{column: "name", operator: resultFilterContains, value: "50%_off=sale"},
		{column: "code", operator: resultFilterStartsWith, value: "A_B"},
	})

	clause, args := resultFilterSQL(config.SQLite, filter)
	if want := ` WHERE CAST("name" AS TEXT) LIKE ? ESCAPE '=' AND CAST("code" AS TEXT) LIKE ? ESCAPE '='`; clause != want {
		t.Fatalf("resultFilterSQL() clause = %q, want %q", clause, want)
	}
	wantArgs := []any{"%50=%=_off==sale%", "A=_B%"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("resultFilterSQL() args = %#v, want %#v", args, wantArgs)
	}
}

func TestResultFilterSQLTextOperatorsWorkOnNonTextColumns(t *testing.T) {
	filter := newResultValueFilter("items", []resultFilterPredicate{
		{column: "numeric_id", operator: resultFilterContains, value: "42"},
	})

	postgresClause, _ := resultFilterSQL(config.PostgreSQL, filter)
	if postgresClause != ` WHERE CAST("numeric_id" AS TEXT) LIKE $1 ESCAPE '='` {
		t.Fatalf("PostgreSQL text clause = %q", postgresClause)
	}
	mysqlClause, _ := resultFilterSQL(config.MySQL, filter)
	if mysqlClause != " WHERE CAST(`numeric_id` AS CHAR) LIKE ? ESCAPE '='" {
		t.Fatalf("MySQL text clause = %q", mysqlClause)
	}
}

func TestLatestResultFilterPredicateForColumnKeepsOperator(t *testing.T) {
	filter := newResultValueFilter("items", []resultFilterPredicate{
		{column: "created_at", operator: resultFilterGreaterEqual, value: "2026-01-01"},
		{column: "status", operator: resultFilterEqual, value: "open"},
	})
	predicate, ok := latestResultFilterPredicateForColumn(filter, "created_at")
	if !ok || predicate.operator != resultFilterGreaterEqual || predicate.value != "2026-01-01" {
		t.Fatalf("latest predicate = (%#v, %v)", predicate, ok)
	}
}

func TestLegacyResultValueFilterDefaultsToEquality(t *testing.T) {
	filter := &resultValueFilter{table: "items", column: "code", value: "beta"}
	clause, args := resultFilterSQL(config.PostgreSQL, filter)
	if clause != ` WHERE "code" = $1` || !reflect.DeepEqual(args, []any{"beta"}) {
		t.Fatalf("legacy filter rendered as (%q, %#v)", clause, args)
	}
}

func TestResultFilterApplyReplacementPrefersSameColumnAndOperator(t *testing.T) {
	predicates := []resultFilterPredicate{
		{column: "tenant_id", operator: resultFilterEqual, value: "one"},
		{column: "created_at", operator: resultFilterGreaterEqual, value: "2026-01-01"},
		{column: "tenant_id", operator: resultFilterNotEqual, value: "blocked"},
	}

	if got := resultFilterPredicateReplacementIndex(predicates, resultFilterPredicate{
		column: "tenant_id", operator: resultFilterEqual, value: "two",
	}); got != 0 {
		t.Fatalf("same-operator replacement index = %d, want 0", got)
	}
	if got := resultFilterPredicateReplacementIndex(predicates, resultFilterPredicate{
		column: "created_at", operator: resultFilterLess, value: "2027-01-01",
	}); got != 1 {
		t.Fatalf("same-column replacement index = %d, want 1", got)
	}
	if got := resultFilterPredicateReplacementIndex(predicates, resultFilterPredicate{
		column: "status", operator: resultFilterEqual, value: "open",
	}); got != -1 {
		t.Fatalf("new-column replacement index = %d, want -1 append", got)
	}
}

func TestChangedResultFilterPredicatesSeparatesApplyFromAddAND(t *testing.T) {
	existing := []resultFilterPredicate{{column: "tenant_id", operator: resultFilterEqual, value: "one"}}
	replacement := resultFilterPredicate{column: "tenant_id", operator: resultFilterEqual, value: "two"}

	applied, changedIndex := changedResultFilterPredicates(existing, replacement, false)
	if changedIndex != 0 || len(applied) != 1 || applied[0].value != "two" {
		t.Fatalf("Apply result = %#v at %d, want one updated predicate", applied, changedIndex)
	}
	if existing[0].value != "one" {
		t.Fatalf("Apply mutated existing predicates: %#v", existing)
	}

	composed, changedIndex := changedResultFilterPredicates(existing, replacement, true)
	if changedIndex != -1 || len(composed) != 2 || composed[0].value != "one" || composed[1].value != "two" {
		t.Fatalf("Add AND result = %#v at %d, want both predicates in order", composed, changedIndex)
	}

	otherColumn, changedIndex := changedResultFilterPredicates(existing, resultFilterPredicate{
		column: "status", operator: resultFilterEqual, value: "active",
	}, false)
	if changedIndex != -1 || len(otherColumn) != 2 || otherColumn[1].column != "status" {
		t.Fatalf("Apply on another column = %#v at %d, want AND append", otherColumn, changedIndex)
	}
}

func TestNewResultCellReferencePreservesRawDisplayAndNullIdentity(t *testing.T) {
	nullReference := newResultCellReference(nil, "NULL")
	if !nullReference.isNull || nullReference.rawValue != nil || nullReference.value != "NULL" {
		t.Fatalf("NULL reference = %#v", nullReference)
	}

	literalReference := newResultCellReference("NULL", "NULL")
	if literalReference.isNull || literalReference.rawValue != "NULL" || literalReference.value != "NULL" {
		t.Fatalf("literal NULL reference = %#v", literalReference)
	}

	longValue := strings.Repeat("x", maxCellPreviewRunes+1)
	display, _ := formatCellValue(longValue)
	longReference := newResultCellReference(longValue, display)
	if longReference.value != longValue || longReference.displayValue != display || !longReference.truncated {
		t.Fatalf("long reference did not preserve raw/display distinction: %#v", longReference)
	}
}

func TestNewResultCellReferenceClonesBinaryScanBuffer(t *testing.T) {
	input := []byte("original")
	reference := newResultCellReference(input, "original")
	input[0] = 'X'
	raw, ok := reference.rawValue.([]byte)
	if !ok || string(raw) != "original" || reference.value != "original" {
		t.Fatalf("binary reference changed with scan buffer: raw=%q value=%q", raw, reference.value)
	}
}

func TestCopiedSQLNullBecomesIsNullWhileTextNullDoesNot(t *testing.T) {
	results := tview.NewTable().SetSelectable(true, true)
	results.SetCell(0, 0, tview.NewTableCell("DELETED_AT").SetReference("deleted_at"))
	results.SetCell(1, 0, tview.NewTableCell("NULL").SetReference(newResultCellReference(nil, "NULL")))
	results.SetCell(2, 0, tview.NewTableCell("NULL").SetReference(newResultCellReference("NULL", "NULL")))

	app := &App{results: results, copiedCellValue: "NULL", hasCopiedCellValue: true}
	app.cacheCopiedResultCellIdentity(1, 0)
	predicate, ok := app.cachedCopiedSQLNullPredicate("deleted_at")
	if !ok || predicate.operator != resultFilterIsNull || predicate.value != nil {
		t.Fatalf("copied SQL NULL predicate = (%#v, %v)", predicate, ok)
	}
	clause, args := resultFilterSQL(config.SQLite, newResultValueFilter("users", []resultFilterPredicate{predicate}))
	if clause != ` WHERE "deleted_at" IS NULL` || len(args) != 0 {
		t.Fatalf("copied SQL NULL rendered as (%q, %#v)", clause, args)
	}

	app.cacheCopiedResultCellIdentity(2, 0)
	if _, ok := app.cachedCopiedSQLNullPredicate("deleted_at"); ok {
		t.Fatal("literal text NULL must remain an equality clipboard value")
	}
}

func TestClipboardPredicatePreservesInternalSQLNullIdentity(t *testing.T) {
	app := &App{
		copiedCellValue:    "NULL",
		hasCopiedCellValue: true,
		copiedCellSystem:   false,
		copiedCellIsNull:   true,
	}
	var got resultFilterPredicate
	app.withClipboardResultPredicate("deleted_at", resultFilterEqual, func(predicate resultFilterPredicate) {
		got = predicate
	})
	if got.column != "deleted_at" || got.operator != resultFilterIsNull || got.value != nil {
		t.Fatalf("clipboard NULL predicate = %#v", got)
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

func TestCopiedCellClipboardValueUsesInternalWhenSystemClipboardFailed(t *testing.T) {
	app := &App{
		copiedCellValue:    "cross-table-id",
		hasCopiedCellValue: true,
		copiedCellSystem:   false,
	}
	value, ok := app.copiedCellClipboardValue()
	if !ok || value != "cross-table-id" {
		t.Fatalf("copiedCellClipboardValue() = (%q,%v), want cached cell value", value, ok)
	}
}

func TestCopiedCellClipboardValueReadsSystemAfterSuccessfulCopy(t *testing.T) {
	app := &App{
		copiedCellValue:    "old-id",
		hasCopiedCellValue: true,
		copiedCellSystem:   true,
	}
	if value, ok := app.copiedCellClipboardValue(); ok {
		t.Fatalf("copiedCellClipboardValue() = (%q,true), want system clipboard read", value)
	}
	if value, ok := app.cachedCopiedCellValue(); !ok || value != "old-id" {
		t.Fatalf("cachedCopiedCellValue() = (%q,%v), want fallback", value, ok)
	}
}

func TestReadFromClipboardHonorsCanceledContextBeforeRunningCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readFromClipboardContext(ctx); err == nil {
		t.Fatal("readFromClipboardContext() expected cancellation error")
	}
}

func TestResultFilterViewStateRestore(t *testing.T) {
	app := &App{
		selectedTable: "orders",
		resultFilter:  &resultValueFilter{table: "orders", column: "user_id", value: "42"},
		pageOffset:    200,
		pageSize:      100,
		totalRowCount: 325,
	}
	state := app.captureResultFilterViewState()

	app.resultFilter.value = "changed"
	app.pageOffset = 0
	app.pageSize = 50
	app.totalRowCount = -1
	app.restoreResultFilterViewState(state)

	if app.resultFilter == nil || app.resultFilter.value != "42" {
		t.Fatalf("restored filter = %#v, want original value", app.resultFilter)
	}
	if app.pageOffset != 200 || app.pageSize != 100 || app.totalRowCount != 325 {
		t.Fatalf("restored pagination = (%d,%d,%d), want (200,100,325)", app.pageOffset, app.pageSize, app.totalRowCount)
	}
	app.selectTableWithRememberedFilter("users")
	app.selectTableWithRememberedFilter("orders")
	if app.resultFilter == nil || app.resultFilter.value != "42" {
		t.Fatalf("remembered rollback filter = %#v, want original value", app.resultFilter)
	}
}

func TestResultFiltersAreRememberedPerTable(t *testing.T) {
	app := &App{
		selectedTable: "users",
		resultFilter: newResultValueFilter("users", []resultFilterPredicate{
			{column: "status", operator: resultFilterEqual, value: "active"},
		}),
	}

	app.selectTableWithRememberedFilter("orders")
	if app.resultFilter != nil {
		t.Fatalf("orders inherited users filter: %#v", app.resultFilter)
	}
	app.setCurrentResultFilter(newResultValueFilter("orders", []resultFilterPredicate{
		{column: "total", operator: resultFilterGreaterEqual, value: "100"},
	}))

	app.selectTableWithRememberedFilter("users")
	users := app.activeResultFilter("users")
	if users == nil || latestResultFilterValueForColumn(users, "status") != "active" {
		t.Fatalf("restored users filter = %#v, want status=active", users)
	}

	app.selectTableWithRememberedFilter("orders")
	orders := app.activeResultFilter("orders")
	orderPredicate, hasOrderPredicate := latestResultFilterPredicateForColumn(orders, "total")
	if !hasOrderPredicate || orderPredicate.operator != resultFilterGreaterEqual || orderPredicate.value != "100" {
		t.Fatalf("restored orders filter = %#v, want total>=100", orders)
	}

	app.setCurrentResultFilter(nil)
	app.selectTableWithRememberedFilter("users")
	app.selectTableWithRememberedFilter("orders")
	if app.activeResultFilter("orders") != nil {
		t.Fatalf("cleared orders filter was restored: %#v", app.resultFilter)
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

func TestResultFilterBadgeExplicitlyShowsFilteredState(t *testing.T) {
	app := &App{
		selectedTable: "users",
		resultFilter: newResultValueFilter("users", []resultFilterPredicate{
			{column: "status", operator: resultFilterEqual, value: "active"},
		}),
	}
	badge := app.resultFilterBadge()
	if !strings.Contains(badge, "FILTERED 1") || !strings.Contains(badge, "Esc clears") {
		t.Fatalf("filter badge is not explicit enough: %q", badge)
	}
}
