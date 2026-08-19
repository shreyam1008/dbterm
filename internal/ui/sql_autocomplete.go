package ui

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
)

const (
	defaultSQLCompletionLimit    = 6
	sqlCompletionCatalogWarmup   = 125 * time.Millisecond
	sqlCompletionDatabaseTimeout = 1500 * time.Millisecond
)

type sqlCompletionKind uint8

const (
	sqlCompletionKeyword sqlCompletionKind = iota
	sqlCompletionTable
	sqlCompletionView
	sqlCompletionColumn
	sqlCompletionSchema
	sqlCompletionDatabase
	sqlCompletionFunction
	sqlCompletionProcedure
	sqlCompletionTemplate
)

type sqlCompletionItem struct {
	label       string
	insertText  string
	detail      string
	kind        sqlCompletionKind
	score       int
	appendSpace bool
}

type sqlCompletionState struct {
	visible      bool
	items        []sqlCompletionItem
	selected     int
	replaceStart int
	replaceEnd   int
	prependSpace bool
}

type sqlCompletionRelation struct {
	name    string
	kind    sqlCompletionKind
	columns []string
}

type sqlCompletionCatalog struct {
	relations []sqlCompletionRelation
	schemas   []string
	databases []string
}

type sqlCompletionInput struct {
	text        string
	cursor      int
	manual      bool
	limit       int
	dbType      config.DBType
	catalog     sqlCompletionCatalog
	activeTable string
	routines    []sqlCompletionRoutine
}

type sqlCompletionRoutine struct {
	name string
	kind sqlCompletionKind
}

type sqlCompletionResult struct {
	items        []sqlCompletionItem
	replaceStart int
	replaceEnd   int
	prependSpace bool
}

type sqlCompletionExpectation uint8

const (
	sqlCompletionGeneral sqlCompletionExpectation = iota
	sqlCompletionStatementStart
	sqlCompletionRelationExpected
	sqlCompletionColumnExpected
	sqlCompletionDatabaseExpected
	sqlCompletionClauseExpected
)

type sqlLexeme struct {
	text   string
	symbol bool
}

var sqlStatementKeywords = []string{
	"SELECT", "WITH", "INSERT INTO", "UPDATE", "DELETE FROM", "CREATE TABLE",
	"ALTER TABLE", "DROP TABLE", "TRUNCATE TABLE", "EXPLAIN", "BEGIN", "COMMIT",
	"ROLLBACK",
}

var sqlCommonKeywords = []string{
	"SELECT", "FROM", "WHERE", "WITH", "AS", "DISTINCT", "ALL", "INSERT INTO",
	"VALUES", "UPDATE", "SET", "DELETE FROM", "CREATE TABLE", "CREATE INDEX",
	"ALTER TABLE", "DROP TABLE", "TRUNCATE TABLE", "JOIN", "INNER JOIN", "LEFT JOIN",
	"RIGHT JOIN", "FULL OUTER JOIN", "CROSS JOIN", "ON", "USING", "AND", "OR", "NOT",
	"NULL", "IS NULL", "IS NOT NULL", "IN", "NOT IN", "EXISTS", "BETWEEN", "LIKE",
	"CASE", "WHEN", "THEN", "ELSE", "END", "GROUP BY", "ORDER BY", "HAVING",
	"ASC", "DESC", "LIMIT", "OFFSET", "UNION", "UNION ALL", "INTERSECT", "EXCEPT",
	"RETURNING", "PRIMARY KEY", "FOREIGN KEY", "REFERENCES", "UNIQUE", "DEFAULT",
	"CHECK", "NOT NULL", "EXPLAIN", "BEGIN", "COMMIT", "ROLLBACK",
}

var sqlCommonFunctions = []string{
	"COUNT", "SUM", "AVG", "MIN", "MAX", "COALESCE", "NULLIF", "CAST", "LOWER",
	"UPPER", "LENGTH", "SUBSTRING", "TRIM", "ROUND", "ABS", "CURRENT_DATE",
	"CURRENT_TIME", "CURRENT_TIMESTAMP",
}

var sqlNextClauseKeywords = []string{
	"WHERE", "JOIN", "LEFT JOIN", "GROUP BY", "ORDER BY", "HAVING", "LIMIT", "OFFSET", "RETURNING",
}

var sqlDialectKeywords = map[config.DBType][]string{
	config.PostgreSQL: {
		"ILIKE", "SIMILAR TO", "ON CONFLICT", "DO NOTHING", "DO UPDATE", "RETURNING",
		"CREATE SCHEMA", "SET SEARCH_PATH", "VACUUM", "ANALYZE",
	},
	config.MySQL: {
		"USE", "SHOW", "DESCRIBE", "REPLACE INTO", "INSERT IGNORE", "ON DUPLICATE KEY UPDATE",
		"AUTO_INCREMENT", "CREATE DATABASE", "DROP DATABASE",
	},
	config.SQLite: {
		"PRAGMA", "INSERT OR REPLACE", "INSERT OR IGNORE", "ON CONFLICT", "WITHOUT ROWID",
		"VACUUM", "ATTACH DATABASE", "DETACH DATABASE",
	},
	config.Turso: {
		"PRAGMA", "INSERT OR REPLACE", "INSERT OR IGNORE", "ON CONFLICT", "WITHOUT ROWID",
		"VACUUM", "ATTACH DATABASE", "DETACH DATABASE",
	},
	config.CloudflareD1: {
		"PRAGMA", "INSERT OR REPLACE", "INSERT OR IGNORE", "ON CONFLICT", "WITHOUT ROWID",
		"VACUUM", "ATTACH DATABASE", "DETACH DATABASE",
	},
}

var sqlDialectFunctions = map[config.DBType][]string{
	config.PostgreSQL:   {"NOW", "DATE_TRUNC", "STRING_AGG", "ARRAY_AGG", "JSON_AGG", "JSONB_AGG", "GENERATE_SERIES"},
	config.MySQL:        {"NOW", "IFNULL", "CONCAT", "GROUP_CONCAT", "DATE_FORMAT", "JSON_EXTRACT"},
	config.SQLite:       {"DATE", "TIME", "DATETIME", "JULIANDAY", "STRFTIME", "GROUP_CONCAT", "JSON_EXTRACT"},
	config.Turso:        {"DATE", "TIME", "DATETIME", "JULIANDAY", "STRFTIME", "GROUP_CONCAT", "JSON_EXTRACT"},
	config.CloudflareD1: {"DATE", "TIME", "DATETIME", "JULIANDAY", "STRFTIME", "GROUP_CONCAT", "JSON_EXTRACT"},
}

var sqlReservedWords = func() map[string]struct{} {
	reserved := make(map[string]struct{}, len(sqlCommonKeywords)+len(sqlStatementKeywords))
	for _, keyword := range sqlCommonKeywords {
		for _, word := range strings.Fields(keyword) {
			reserved[word] = struct{}{}
			reserved[strings.ToLower(word)] = struct{}{}
		}
	}
	for _, keyword := range sqlStatementKeywords {
		for _, word := range strings.Fields(keyword) {
			reserved[word] = struct{}{}
			reserved[strings.ToLower(word)] = struct{}{}
		}
	}
	for _, keywords := range sqlDialectKeywords {
		for _, keyword := range keywords {
			for _, word := range strings.Fields(keyword) {
				reserved[word] = struct{}{}
				reserved[strings.ToLower(word)] = struct{}{}
			}
		}
	}
	return reserved
}()

func newSQLCompletionView() *tview.Table {
	view := tview.NewTable().
		SetSelectable(true, false).
		SetSelectedStyle(tcell.StyleDefault.Background(blue).Foreground(crust))
	view.SetBorder(true).
		SetTitle(" SQL suggestions  ↑/↓ choose  Tab/Enter insert  Esc close ").
		SetBorderColor(surface1).
		SetTitleColor(teal).
		SetBackgroundColor(mantle)
	return view
}

func (a *App) refreshSQLCompletions(manual bool) {
	if a == nil || a.queryInput == nil || a.sqlCompletionApplying {
		return
	}
	if !manual && a.focusedPanel != a.queryInput {
		a.hideSQLCompletions()
		return
	}

	selection, start, end := a.queryInput.GetSelection()
	if selection != "" || start != end {
		a.hideSQLCompletions()
		return
	}

	result := completeSQL(sqlCompletionInput{
		text:        a.queryInput.GetText(),
		cursor:      end,
		manual:      manual,
		limit:       a.sqlCompletionLimit(),
		dbType:      a.dbType,
		catalog:     a.sqlCompletionCatalog,
		activeTable: a.selectedTable,
		routines:    a.sqlCompletionRoutines,
	})
	if len(result.items) == 0 {
		a.hideSQLCompletions()
		return
	}

	selectedLabel := ""
	if a.sqlCompletionState.visible && a.sqlCompletionState.selected >= 0 && a.sqlCompletionState.selected < len(a.sqlCompletionState.items) {
		selectedLabel = a.sqlCompletionState.items[a.sqlCompletionState.selected].label
	}
	a.sqlCompletionState = sqlCompletionState{
		visible:      true,
		items:        result.items,
		replaceStart: result.replaceStart,
		replaceEnd:   result.replaceEnd,
		prependSpace: result.prependSpace,
	}
	for index, item := range result.items {
		if item.label == selectedLabel {
			a.sqlCompletionState.selected = index
			break
		}
	}
	a.renderSQLCompletions()
}

func (a *App) sqlCompletionLimit() int {
	if a == nil {
		return defaultSQLCompletionLimit
	}
	switch {
	case a.lastScreenH > 0 && a.lastScreenH < 18:
		return 2
	case a.lastScreenH > 0 && a.lastScreenH < 24:
		return 3
	default:
		return defaultSQLCompletionLimit
	}
}

func sqlCompletionRoutinesFromObjects(objects map[int]databaseObjectListItem) []sqlCompletionRoutine {
	if len(objects) == 0 {
		return nil
	}
	routines := make([]sqlCompletionRoutine, 0)
	for _, object := range objects {
		switch object.objType {
		case database.ObjFunctions:
			routines = append(routines, sqlCompletionRoutine{name: object.name, kind: sqlCompletionFunction})
		case database.ObjStoredProcedures:
			routines = append(routines, sqlCompletionRoutine{name: object.name, kind: sqlCompletionProcedure})
		}
	}
	sort.Slice(routines, func(i, j int) bool { return strings.ToLower(routines[i].name) < strings.ToLower(routines[j].name) })
	return routines
}

func (a *App) renderSQLCompletions() {
	if a == nil || a.sqlCompletionView == nil || !a.sqlCompletionState.visible || len(a.sqlCompletionState.items) == 0 {
		a.hideSQLCompletions()
		return
	}

	a.sqlCompletionView.Clear()
	for row, item := range a.sqlCompletionState.items {
		kindCell := tview.NewTableCell(sqlCompletionKindLabel(item.kind)).
			SetTextColor(sqlCompletionKindColor(item.kind)).
			SetMaxWidth(10)
		labelCell := tview.NewTableCell(tview.Escape(item.label)).
			SetTextColor(text).
			SetExpansion(1)
		detailCell := tview.NewTableCell(tview.Escape(item.detail)).
			SetTextColor(overlay0).
			SetMaxWidth(32)
		a.sqlCompletionView.SetCell(row, 0, kindCell)
		a.sqlCompletionView.SetCell(row, 1, labelCell)
		a.sqlCompletionView.SetCell(row, 2, detailCell)
	}
	a.sqlCompletionView.Select(a.sqlCompletionState.selected, 0)
	if a.lastScreenW > 0 && a.lastScreenW < 80 {
		a.sqlCompletionView.SetTitle(" Suggestions  ↑/↓  Tab/Enter insert ")
	} else {
		a.sqlCompletionView.SetTitle(" SQL suggestions  ↑/↓ choose  Tab/Enter insert  Esc close ")
	}
	if a.rightFlex != nil {
		a.rightFlex.ResizeItem(a.sqlCompletionView, a.sqlCompletionPopupHeight(), 0)
	}
	if a.statusBar != nil {
		a.updateStatusBar("", a.currentResultRowCount())
	}
}

func (a *App) sqlCompletionPopupHeight() int {
	if a == nil || !a.sqlCompletionState.visible || len(a.sqlCompletionState.items) == 0 {
		return 0
	}
	return min(len(a.sqlCompletionState.items), a.sqlCompletionLimit()) + 2
}

func (a *App) hideSQLCompletions() bool {
	if a == nil {
		return false
	}
	wasVisible := a.sqlCompletionState.visible
	a.sqlCompletionState = sqlCompletionState{}
	if a.sqlCompletionView != nil {
		a.sqlCompletionView.Clear()
	}
	if a.rightFlex != nil && a.sqlCompletionView != nil {
		a.rightFlex.ResizeItem(a.sqlCompletionView, 0, 0)
	}
	if wasVisible && a.statusBar != nil {
		a.updateStatusBar("", a.currentResultRowCount())
	}
	return wasVisible
}

func (a *App) moveSQLCompletion(delta int) bool {
	if a == nil || !a.sqlCompletionState.visible || len(a.sqlCompletionState.items) == 0 {
		return false
	}
	count := len(a.sqlCompletionState.items)
	a.sqlCompletionState.selected = (a.sqlCompletionState.selected + delta + count) % count
	a.renderSQLCompletions()
	return true
}

func (a *App) acceptSQLCompletion() bool {
	if a == nil || a.queryInput == nil || !a.sqlCompletionState.visible || len(a.sqlCompletionState.items) == 0 {
		return false
	}
	index := a.sqlCompletionState.selected
	if index < 0 || index >= len(a.sqlCompletionState.items) {
		index = 0
	}
	item := a.sqlCompletionState.items[index]
	replacement := item.insertText
	query := a.queryInput.GetText()
	if a.sqlCompletionState.prependSpace && shouldPrependSQLCompletionSpace(query, a.sqlCompletionState.replaceStart) {
		replacement = " " + replacement
	}
	if item.appendSpace && shouldAppendSQLCompletionSpace(query, a.sqlCompletionState.replaceEnd) {
		replacement += " "
	}

	a.sqlCompletionApplying = true
	a.queryInput.Replace(a.sqlCompletionState.replaceStart, a.sqlCompletionState.replaceEnd, replacement)
	a.sqlCompletionApplying = false
	a.hideSQLCompletions()
	return true
}

func shouldPrependSQLCompletionSpace(text string, start int) bool {
	if start <= 0 || start > len(text) {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(text[:start])
	return !unicode.IsSpace(r) && !strings.ContainsRune("(,.;", r)
}

func shouldAppendSQLCompletionSpace(text string, end int) bool {
	if end < 0 || end > len(text) {
		return false
	}
	if end == len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[end:])
	return !unicode.IsSpace(r) && !strings.ContainsRune(",;.)", r)
}

func (a *App) handleSQLCompletionKey(event *tcell.EventKey) bool {
	if a == nil || event == nil || a.queryInput == nil {
		return false
	}
	if event.Key() == tcell.KeyCtrlSpace {
		a.refreshSQLCompletions(true)
		return true
	}
	if !a.sqlCompletionState.visible {
		return false
	}

	switch event.Key() {
	case tcell.KeyUp:
		return a.moveSQLCompletion(-1)
	case tcell.KeyDown:
		return a.moveSQLCompletion(1)
	case tcell.KeyTab, tcell.KeyEnter:
		return a.acceptSQLCompletion()
	case tcell.KeyEscape:
		return a.hideSQLCompletions()
	case tcell.KeyCtrlP:
		return a.moveSQLCompletion(-1)
	case tcell.KeyCtrlN:
		return a.moveSQLCompletion(1)
	}
	return false
}

func sqlCompletionKindLabel(kind sqlCompletionKind) string {
	switch kind {
	case sqlCompletionTable:
		return " TABLE "
	case sqlCompletionView:
		return " VIEW "
	case sqlCompletionColumn:
		return " COLUMN "
	case sqlCompletionSchema:
		return " SCHEMA "
	case sqlCompletionDatabase:
		return " DB "
	case sqlCompletionFunction:
		return " FUNC "
	case sqlCompletionProcedure:
		return " PROC "
	case sqlCompletionTemplate:
		return " READY "
	default:
		return " SQL "
	}
}

func sqlCompletionKindColor(kind sqlCompletionKind) tcell.Color {
	switch kind {
	case sqlCompletionTable, sqlCompletionView:
		return peach
	case sqlCompletionColumn:
		return blue
	case sqlCompletionSchema, sqlCompletionDatabase:
		return mauve
	case sqlCompletionFunction, sqlCompletionProcedure:
		return green
	case sqlCompletionTemplate:
		return teal
	default:
		return yellow
	}
}

func completeSQL(input sqlCompletionInput) sqlCompletionResult {
	if input.cursor < 0 || input.cursor > len(input.text) {
		return sqlCompletionResult{}
	}
	if input.limit <= 0 {
		input.limit = defaultSQLCompletionLimit
	}

	replaceStart, replaceEnd := sqlIdentifierFragmentRange(input.text, input.cursor)
	prependSpace := false
	prefix := input.text[replaceStart:input.cursor]
	statementStart, statementEnd := sqlStatementBounds(input.text, input.cursor)
	before := input.text[statementStart:replaceStart]
	beforeTokens, safe := lexSQLCompletion(before)
	if !safe {
		return sqlCompletionResult{}
	}

	statementTokens, _ := lexSQLCompletion(input.text[statementStart:statementEnd])
	expectation := sqlCompletionExpectationFor(beforeTokens)
	qualified := strings.Contains(prefix, ".")
	if !input.manual && !qualified && len([]rune(prefix)) < 2 &&
		expectation != sqlCompletionRelationExpected && expectation != sqlCompletionColumnExpected && expectation != sqlCompletionDatabaseExpected {
		return sqlCompletionResult{}
	}

	aliases, referencedRelations := sqlCompletionAliases(statementTokens, input.catalog)
	if sqlCompletionPrefixIsComplete(input, prefix, expectation, aliases) {
		if !input.manual {
			return sqlCompletionResult{replaceStart: replaceStart, replaceEnd: replaceEnd}
		}
		// Ctrl+Space after an already-complete identifier means "what can I do
		// next?". Insert at the cursor instead of replacing that identifier.
		replaceStart, replaceEnd = input.cursor, input.cursor
		prependSpace = true
		prefix = ""
		beforeTokens, safe = lexSQLCompletion(input.text[statementStart:input.cursor])
		if !safe {
			return sqlCompletionResult{}
		}
		expectation = sqlCompletionExpectationAfterComplete(beforeTokens, input.catalog)
	}
	items := buildSQLCompletionItems(input, prefix, expectation, aliases, referencedRelations, beforeTokens)
	if len(items) > input.limit {
		items = items[:input.limit]
	}
	return sqlCompletionResult{items: items, replaceStart: replaceStart, replaceEnd: replaceEnd, prependSpace: prependSpace}
}

func sqlCompletionPrefixIsComplete(input sqlCompletionInput, prefix string, expectation sqlCompletionExpectation, aliases map[string]string) bool {
	if prefix == "" {
		return false
	}
	if expectation == sqlCompletionRelationExpected {
		if _, ok := findSQLCompletionRelation(input.catalog, prefix); ok {
			return true
		}
	}
	if dot := strings.LastIndex(prefix, "."); dot >= 0 {
		qualifier, column := prefix[:dot], prefix[dot+1:]
		relationName := qualifier
		if aliasTarget, ok := aliases[strings.ToLower(qualifier)]; ok {
			relationName = aliasTarget
		}
		if relation, ok := findSQLCompletionRelation(input.catalog, relationName); ok {
			for _, candidate := range relation.columns {
				if strings.EqualFold(candidate, column) {
					return true
				}
			}
		}
		return false
	}
	switch expectation {
	case sqlCompletionStatementStart:
		for _, keyword := range sqlStatementKeywords {
			if strings.EqualFold(keyword, prefix) {
				return true
			}
		}
	case sqlCompletionDatabaseExpected:
		for _, databaseName := range input.catalog.databases {
			if strings.EqualFold(databaseName, prefix) {
				return true
			}
		}
	}
	return false
}

func sqlIdentifierFragmentRange(text string, cursor int) (start, end int) {
	start, end = cursor, cursor
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if !isSQLCompletionIdentifierRune(r) {
			break
		}
		start -= size
	}
	for end < len(text) {
		r, size := utf8.DecodeRuneInString(text[end:])
		if !isSQLCompletionIdentifierRune(r) {
			break
		}
		end += size
	}
	return start, end
}

func isSQLCompletionIdentifierRune(r rune) bool {
	return r == '_' || r == '$' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func sqlStatementBounds(text string, cursor int) (start, end int) {
	start = strings.LastIndex(text[:cursor], ";") + 1
	if next := strings.Index(text[cursor:], ";"); next >= 0 {
		end = cursor + next
	} else {
		end = len(text)
	}
	return start, end
}

func lexSQLCompletion(text string) ([]sqlLexeme, bool) {
	tokens := make([]sqlLexeme, 0, 24)
	for index := 0; index < len(text); {
		r, size := utf8.DecodeRuneInString(text[index:])
		if unicode.IsSpace(r) {
			index += size
			continue
		}
		if r == '-' && index+1 < len(text) && text[index+1] == '-' {
			if newline := strings.IndexByte(text[index+2:], '\n'); newline >= 0 {
				index += 2 + newline + 1
				continue
			}
			return tokens, false
		}
		if r == '#' {
			if newline := strings.IndexByte(text[index+1:], '\n'); newline >= 0 {
				index += 1 + newline + 1
				continue
			}
			return tokens, false
		}
		if r == '/' && index+1 < len(text) && text[index+1] == '*' {
			if closeAt := strings.Index(text[index+2:], "*/"); closeAt >= 0 {
				index += 2 + closeAt + 2
				continue
			}
			return tokens, false
		}
		if r == '$' {
			if delimiter, contentStart, ok := sqlDollarQuoteDelimiter(text, index); ok {
				if closeAt := strings.Index(text[contentStart:], delimiter); closeAt >= 0 {
					index = contentStart + closeAt + len(delimiter)
					continue
				}
				return tokens, false
			}
		}
		if r == '\'' {
			next, closed := skipSQLQuoted(text, index, '\'')
			if !closed {
				return tokens, false
			}
			index = next
			continue
		}
		if r == '"' || r == '`' {
			next, value, closed := readSQLQuotedIdentifier(text, index, byte(r))
			if !closed {
				return tokens, false
			}
			tokens = append(tokens, sqlLexeme{text: value})
			index = next
			continue
		}
		if isSQLCompletionWordRune(r) {
			start := index
			index += size
			for index < len(text) {
				next, nextSize := utf8.DecodeRuneInString(text[index:])
				if !isSQLCompletionWordRune(next) {
					break
				}
				index += nextSize
			}
			tokens = append(tokens, sqlLexeme{text: text[start:index]})
			continue
		}
		tokens = append(tokens, sqlLexeme{text: string(r), symbol: true})
		index += size
	}
	return tokens, true
}

func sqlDollarQuoteDelimiter(text string, start int) (delimiter string, contentStart int, ok bool) {
	if start < 0 || start >= len(text) || text[start] != '$' || start+1 >= len(text) {
		return "", start, false
	}
	if text[start+1] == '$' {
		return "$$", start + 2, true
	}
	first, firstSize := utf8.DecodeRuneInString(text[start+1:])
	if first != '_' && !unicode.IsLetter(first) {
		return "", start, false
	}
	for index := start + 1 + firstSize; index < len(text); {
		if text[index] == '$' {
			return text[start : index+1], index + 1, true
		}
		r, size := utf8.DecodeRuneInString(text[index:])
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", start, false
		}
		index += size
	}
	return "", start, false
}

func isSQLCompletionWordRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func skipSQLQuoted(text string, start int, quote byte) (int, bool) {
	for index := start + 1; index < len(text); index++ {
		if text[index] == '\\' {
			index++
			continue
		}
		if text[index] != quote {
			continue
		}
		if index+1 < len(text) && text[index+1] == quote {
			index++
			continue
		}
		return index + 1, true
	}
	return len(text), false
}

func readSQLQuotedIdentifier(text string, start int, quote byte) (int, string, bool) {
	var out strings.Builder
	for index := start + 1; index < len(text); index++ {
		if text[index] != quote {
			out.WriteByte(text[index])
			continue
		}
		if index+1 < len(text) && text[index+1] == quote {
			out.WriteByte(quote)
			index++
			continue
		}
		return index + 1, out.String(), true
	}
	return len(text), out.String(), false
}

func sqlCompletionExpectationFor(tokens []sqlLexeme) sqlCompletionExpectation {
	if len(tokens) == 0 || tokens[len(tokens)-1].text == ";" {
		return sqlCompletionStatementStart
	}
	words := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !token.symbol {
			words = append(words, strings.ToUpper(token.text))
		}
	}
	if len(words) == 0 {
		return sqlCompletionStatementStart
	}
	if sqlCompletionInsideSelectList(words) || sqlCompletionInsideInsertColumnList(tokens, words) {
		return sqlCompletionColumnExpected
	}
	last := words[len(words)-1]
	switch last {
	case "FROM", "JOIN", "UPDATE", "INTO", "TABLE", "REFERENCES", "TRUNCATE":
		return sqlCompletionRelationExpected
	case "USE", "DATABASE":
		return sqlCompletionDatabaseExpected
	case "SELECT", "WHERE", "ON", "HAVING", "SET", "AND", "OR", "WHEN", "THEN", "ELSE", "BY", "RETURNING":
		return sqlCompletionColumnExpected
	}
	return sqlCompletionGeneral
}

func sqlCompletionInsideSelectList(words []string) bool {
	lastSelect := -1
	for index, word := range words {
		if word == "SELECT" {
			lastSelect = index
		}
	}
	if lastSelect < 0 {
		return false
	}
	for _, word := range words[lastSelect+1:] {
		if word == "FROM" {
			return false
		}
	}
	return true
}

func sqlCompletionInsideInsertColumnList(tokens []sqlLexeme, words []string) bool {
	hasInsert, hasInto := false, false
	for _, word := range words {
		switch word {
		case "INSERT":
			hasInsert = true
		case "INTO":
			hasInto = hasInsert
		case "VALUES", "SELECT":
			if hasInto {
				return false
			}
		}
	}
	if !hasInto {
		return false
	}
	depth := 0
	for _, token := range tokens {
		if !token.symbol {
			continue
		}
		switch token.text {
		case "(":
			depth++
		case ")":
			depth--
		}
	}
	return depth > 0
}

func sqlCompletionExpectationAfterComplete(tokens []sqlLexeme, catalog sqlCompletionCatalog) sqlCompletionExpectation {
	expectation := sqlCompletionExpectationFor(tokens)
	if expectation != sqlCompletionGeneral {
		return expectation
	}
	for index := len(tokens) - 1; index >= 0; index-- {
		if tokens[index].symbol {
			continue
		}
		switch strings.ToUpper(tokens[index].text) {
		case "FROM", "JOIN":
			name, next := readSQLIdentifierPath(tokens, index+1)
			if next == len(tokens) {
				if _, ok := findSQLCompletionRelation(catalog, name); ok {
					return sqlCompletionClauseExpected
				}
			}
			return expectation
		case "UPDATE":
			name, next := readSQLIdentifierPath(tokens, index+1)
			if next == len(tokens) {
				if _, ok := findSQLCompletionRelation(catalog, name); ok {
					return sqlCompletionClauseExpected
				}
			}
			return expectation
		}
	}
	return expectation
}

func sqlCompletionAliases(tokens []sqlLexeme, catalog sqlCompletionCatalog) (map[string]string, []string) {
	aliases := make(map[string]string)
	referenced := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for index := 0; index < len(tokens); index++ {
		upper := strings.ToUpper(tokens[index].text)
		if tokens[index].symbol || (upper != "FROM" && upper != "JOIN" && upper != "UPDATE" && upper != "INTO") {
			continue
		}
		name, next := readSQLIdentifierPath(tokens, index+1)
		if name == "" {
			continue
		}
		relation, ok := findSQLCompletionRelation(catalog, name)
		if !ok {
			continue
		}
		key := strings.ToLower(relation.name)
		if _, exists := seen[key]; !exists {
			referenced = append(referenced, relation.name)
			seen[key] = struct{}{}
		}
		base := sqlIdentifierBase(relation.name)
		aliases[strings.ToLower(base)] = relation.name
		if next < len(tokens) && strings.EqualFold(tokens[next].text, "AS") {
			next++
		}
		if next < len(tokens) && !tokens[next].symbol && !isSQLAliasStopWord(tokens[next].text) {
			aliases[strings.ToLower(tokens[next].text)] = relation.name
		}
	}
	return aliases, referenced
}

func readSQLIdentifierPath(tokens []sqlLexeme, start int) (string, int) {
	if start >= len(tokens) || tokens[start].symbol {
		return "", start
	}
	parts := []string{tokens[start].text}
	index := start + 1
	for index+1 < len(tokens) && tokens[index].symbol && tokens[index].text == "." && !tokens[index+1].symbol {
		parts = append(parts, tokens[index+1].text)
		index += 2
	}
	return strings.Join(parts, "."), index
}

func isSQLAliasStopWord(word string) bool {
	upper := strings.ToUpper(word)
	if _, reserved := sqlReservedWords[upper]; reserved {
		return true
	}
	switch upper {
	case "WHERE", "JOIN", "LEFT", "RIGHT", "FULL", "INNER", "CROSS", "ON", "GROUP", "ORDER", "HAVING", "LIMIT", "OFFSET", "RETURNING":
		return true
	default:
		return false
	}
}

type sqlCompletionCollector struct {
	prefix        string
	prefixFolded  string
	typoPrefix    []rune
	previousWords []string
	dbType        config.DBType
	limit         int
	items         []sqlCompletionItem
	worst         int
	allowTypos    bool
}

func buildSQLCompletionItems(input sqlCompletionInput, prefix string, expectation sqlCompletionExpectation, aliases map[string]string, referenced []string, beforeTokens []sqlLexeme) []sqlCompletionItem {
	collector := sqlCompletionCollector{
		prefix: prefix, prefixFolded: strings.ToLower(prefix), dbType: input.dbType,
		previousWords: sqlCompletionWords(beforeTokens), limit: input.limit,
		items:      make([]sqlCompletionItem, 0, input.limit),
		allowTypos: input.manual || expectation == sqlCompletionRelationExpected,
	}
	if collector.allowTypos {
		collector.typoPrefix = []rune(collector.prefixFolded)
	}

	if qualifier, partial, ok := strings.Cut(prefix, "."); ok {
		// Use the last dot for schema.table and alias.column fragments.
		if dot := strings.LastIndex(prefix, "."); dot >= 0 {
			qualifier, partial = prefix[:dot], prefix[dot+1:]
		}
		if relationName, exists := aliases[strings.ToLower(qualifier)]; exists {
			collector.addRelationColumns(relationName, qualifier, partial, input.catalog, 0)
		} else if relation, found := findSQLCompletionRelation(input.catalog, qualifier); found {
			collector.addRelationColumns(relation.name, qualifier, partial, input.catalog, 0)
		} else {
			collector.addSchemaRelations(qualifier, partial, input.catalog, 0)
		}
		return collector.sorted()
	}

	if input.manual && expectation == sqlCompletionStatementStart {
		collector.addReadyQueryTemplates(input, -30)
	}

	switch expectation {
	case sqlCompletionStatementStart:
		collector.addKeywords(sqlStatementKeywords, 0)
		collector.addKeywords(sqlDialectKeywords[input.dbType], 8)
		collector.addRelations(input.catalog.relations, 25)
	case sqlCompletionRelationExpected:
		if relation, ok := findSQLCompletionRelation(input.catalog, input.activeTable); ok {
			collector.addRelations([]sqlCompletionRelation{relation}, -1)
		}
		collector.addRelations(input.catalog.relations, 0)
		collector.addNames(input.catalog.schemas, sqlCompletionSchema, "schema", 12, true)
		collector.addKeywords(sqlCommonKeywords, 45)
	case sqlCompletionDatabaseExpected:
		collector.addNames(input.catalog.databases, sqlCompletionDatabase, "database", 0, true)
		collector.addNames(input.catalog.schemas, sqlCompletionSchema, "schema", 15, true)
	case sqlCompletionColumnExpected:
		collector.addColumnsForRelations(referenced, input.catalog, 0)
		if len(referenced) == 0 && input.activeTable != "" {
			collector.addColumnsForRelations([]string{input.activeTable}, input.catalog, 5)
		}
		collector.addFunctions(sqlCommonFunctions, 18)
		collector.addFunctions(sqlDialectFunctions[input.dbType], 20)
		collector.addKeywords(sqlCommonKeywords, 30)
		collector.addRelations(input.catalog.relations, 55)
	case sqlCompletionClauseExpected:
		collector.addReadyClauseTemplates(referenced, input.catalog, -15)
		collector.addKeywords(sqlNextClauseKeywords, 0)
		collector.addColumnsForRelations(referenced, input.catalog, 20)
	default:
		collector.addKeywords(sqlCommonKeywords, 0)
		collector.addKeywords(sqlDialectKeywords[input.dbType], 3)
		collector.addRelations(input.catalog.relations, 15)
		collector.addColumnsForRelations(referenced, input.catalog, 20)
		if len(referenced) == 0 && input.activeTable != "" {
			collector.addColumnsForRelations([]string{input.activeTable}, input.catalog, 24)
		}
		collector.addFunctions(sqlCommonFunctions, 28)
		collector.addFunctions(sqlDialectFunctions[input.dbType], 30)
		collector.addNames(input.catalog.schemas, sqlCompletionSchema, "schema", 38, true)
		collector.addNames(input.catalog.databases, sqlCompletionDatabase, "database", 42, true)
	}

	for _, routine := range input.routines {
		insert := sqlCompletionIdentifier(input.dbType, routine.name)
		if routine.kind == sqlCompletionFunction {
			insert += "("
		}
		collector.add(routine.name, insert, routine.kind, "database routine", 26, false, routine.name)
	}
	return collector.sorted()
}

func (c *sqlCompletionCollector) addKeywords(keywords []string, priority int) {
	for _, keyword := range keywords {
		insert, matchText, ok := contextualSQLKeyword(keyword, c.previousWords)
		if !ok {
			continue
		}
		detail := "SQL keyword"
		if insert != keyword {
			detail = "completes " + keyword
		}
		c.add(keyword, insert, sqlCompletionKeyword, detail, priority, true, matchText)
	}
}

func sqlCompletionWords(tokens []sqlLexeme) []string {
	words := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !token.symbol {
			words = append(words, strings.ToUpper(token.text))
		}
	}
	return words
}

func contextualSQLKeyword(keyword string, previousWords []string) (insert, matchText string, ok bool) {
	parts := strings.Fields(keyword)
	if len(parts) <= 1 || len(previousWords) == 0 {
		return keyword, keyword, true
	}
	maxPrefix := min(len(parts)-1, len(previousWords))
	for count := maxPrefix; count > 0; count-- {
		previousStart := len(previousWords) - count
		matches := true
		for index := 0; index < count; index++ {
			if !strings.EqualFold(previousWords[previousStart+index], parts[index]) {
				matches = false
				break
			}
		}
		if matches {
			remaining := strings.Join(parts[count:], " ")
			return remaining, remaining, remaining != ""
		}
	}
	return keyword, keyword, true
}

func (c *sqlCompletionCollector) addFunctions(functions []string, priority int) {
	for _, function := range functions {
		c.add(function+"()", function+"(", sqlCompletionFunction, "SQL function", priority, false, function)
	}
}

func (c *sqlCompletionCollector) addRelations(relations []sqlCompletionRelation, priority int) {
	matched := false
	for _, relation := range relations {
		if c.addRelation(relation, priority) {
			matched = true
		}
	}
	// Fuzzy matching is deliberately a second pass. Nearly every keystroke has
	// a prefix/substring match, so edit-distance work should only run when the
	// cheaper relation search found nothing at all.
	if matched || !c.allowTypos || len(c.typoPrefix) < 3 {
		return
	}
	c.addRelationTypos(relations, priority)
}

func (c *sqlCompletionCollector) addRelation(relation sqlCompletionRelation, priority int) bool {
	matchScore, ok := sqlCompletionMatchScoreFolded(relation.name, c.prefix, c.prefixFolded)
	if !ok {
		return false
	}
	if c.prefix != "" && strings.EqualFold(relation.name, c.prefix) {
		return true
	}
	detail := "table"
	if relation.kind == sqlCompletionView {
		detail = "view"
	}
	c.addScored(relation.name, sqlCompletionIdentifier(c.dbType, relation.name), relation.kind, detail, priority, matchScore, true)
	return true
}

func (c *sqlCompletionCollector) addNames(names []string, kind sqlCompletionKind, detail string, priority int, appendSpace bool) {
	for _, name := range names {
		c.add(name, sqlCompletionIdentifier(c.dbType, name), kind, detail, priority, appendSpace, name)
	}
}

func (c *sqlCompletionCollector) addColumnsForRelations(names []string, catalog sqlCompletionCatalog, priority int) {
	seen := make(map[string]struct{})
	for _, name := range names {
		relation, ok := findSQLCompletionRelation(catalog, name)
		if !ok {
			continue
		}
		for _, column := range relation.columns {
			key := strings.ToLower(column)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			c.add(column, sqlCompletionIdentifier(c.dbType, column), sqlCompletionColumn, relation.name, priority, false, column)
		}
	}
}

func (c *sqlCompletionCollector) addRelationColumns(relationName, qualifier, partial string, catalog sqlCompletionCatalog, priority int) {
	relation, ok := findSQLCompletionRelation(catalog, relationName)
	if !ok {
		return
	}
	originalPrefix, originalFolded := c.prefix, c.prefixFolded
	c.prefix, c.prefixFolded = partial, strings.ToLower(partial)
	c.typoPrefix = []rune(c.prefixFolded)
	for _, column := range relation.columns {
		label := qualifier + "." + column
		insert := qualifier + "." + sqlCompletionIdentifier(c.dbType, column)
		c.add(label, insert, sqlCompletionColumn, relation.name, priority, false, column)
	}
	c.prefix, c.prefixFolded = originalPrefix, originalFolded
	c.typoPrefix = []rune(originalFolded)
}

func (c *sqlCompletionCollector) addSchemaRelations(schema, partial string, catalog sqlCompletionCatalog, priority int) {
	originalPrefix, originalFolded := c.prefix, c.prefixFolded
	c.prefix, c.prefixFolded = partial, strings.ToLower(partial)
	c.typoPrefix = []rune(c.prefixFolded)
	for _, relation := range catalog.relations {
		namespace, name := splitQualifiedIdentifier(relation.name)
		if !strings.EqualFold(namespace, schema) {
			continue
		}
		label := schema + "." + name
		c.add(label, sqlCompletionIdentifier(c.dbType, relation.name), relation.kind, "relation in "+schema, priority, true, name)
	}
	c.prefix, c.prefixFolded = originalPrefix, originalFolded
	c.typoPrefix = []rune(originalFolded)
}

func (c *sqlCompletionCollector) add(label, insert string, kind sqlCompletionKind, detail string, priority int, appendSpace bool, matchText string) bool {
	matchScore, ok := sqlCompletionMatchScoreFolded(matchText, c.prefix, c.prefixFolded)
	if !ok {
		return false
	}
	if c.prefix != "" && strings.EqualFold(matchText, c.prefix) {
		return true
	}
	c.addScored(label, insert, kind, detail, priority, matchScore, appendSpace)
	return true
}

func (c *sqlCompletionCollector) addScored(label, insert string, kind sqlCompletionKind, detail string, priority, matchScore int, appendSpace bool) {
	item := sqlCompletionItem{
		label: label, insertText: insert, kind: kind, detail: detail,
		score: priority*100 + matchScore, appendSpace: appendSpace,
	}
	if c.limit <= 0 {
		c.limit = defaultSQLCompletionLimit
	}
	if len(c.items) >= c.limit && !sqlCompletionItemLess(item, c.items[c.worst]) {
		return
	}
	for index, previous := range c.items {
		if previous.kind == kind && strings.EqualFold(previous.insertText, insert) {
			if sqlCompletionItemLess(item, previous) {
				c.items[index] = item
				c.findWorst()
			}
			return
		}
	}
	if len(c.items) < c.limit {
		c.items = append(c.items, item)
		if len(c.items) == 1 || sqlCompletionItemLess(c.items[c.worst], item) {
			c.worst = len(c.items) - 1
		}
		return
	}
	c.items[c.worst] = item
	c.findWorst()
}

func (c *sqlCompletionCollector) findWorst() {
	c.worst = 0
	for index := 1; index < len(c.items); index++ {
		if sqlCompletionItemLess(c.items[c.worst], c.items[index]) {
			c.worst = index
		}
	}
}

func (c *sqlCompletionCollector) sorted() []sqlCompletionItem {
	sort.SliceStable(c.items, func(i, j int) bool { return sqlCompletionItemLess(c.items[i], c.items[j]) })
	return c.items
}

func sqlCompletionMatchScoreFolded(candidate, prefix, prefixFolded string) (int, bool) {
	if prefix == "" {
		return 0, true
	}
	if strings.HasPrefix(candidate, prefix) {
		return len(candidate) - len(prefix), true
	}
	if isASCIIString(candidate) && isASCIIString(prefixFolded) {
		if len(candidate) >= len(prefixFolded) && asciiEqualFold(candidate[:len(prefixFolded)], prefixFolded) {
			return 20 + len(candidate) - len(prefixFolded), true
		}
		if index := asciiIndexFold(candidate, prefixFolded); index >= 0 {
			return 50 + index, true
		}
		matchAt, gaps := 0, 0
		for index := 0; index < len(candidate) && matchAt < len(prefixFolded); index++ {
			if asciiLower(candidate[index]) == prefixFolded[matchAt] {
				matchAt++
			} else if matchAt > 0 {
				gaps++
			}
		}
		if matchAt == len(prefixFolded) {
			return 90 + gaps, true
		}
		return 0, false
	}
	if len(candidate) >= len(prefix) && strings.EqualFold(candidate[:len(prefix)], prefix) {
		return 20 + len(candidate) - len(prefix), true
	}
	if index := sqlCompletionIndexFold(candidate, prefixFolded); index >= 0 {
		return 50 + index, true
	}

	// A compact subsequence match catches abbreviations such as "ljo" for
	// LEFT JOIN without bringing a fuzzy-search dependency into the input path.
	matchAt := 0
	gaps := 0
	for _, r := range candidate {
		if matchAt >= len(prefixFolded) {
			break
		}
		wanted, size := utf8.DecodeRuneInString(prefixFolded[matchAt:])
		if unicode.ToLower(r) == wanted {
			matchAt += size
		} else if matchAt > 0 {
			gaps++
		}
	}
	if matchAt == len(prefixFolded) {
		return 90 + gaps, true
	}
	return 0, false
}

func sqlCompletionIndexFold(candidate, foldedPrefix string) int {
	if foldedPrefix == "" {
		return 0
	}
	if len(foldedPrefix) > len(candidate) {
		return -1
	}
	for start := 0; start+len(foldedPrefix) <= len(candidate); start++ {
		if strings.EqualFold(candidate[start:start+len(foldedPrefix)], foldedPrefix) {
			return start
		}
	}
	return -1
}

func sqlCompletionItemLess(left, right sqlCompletionItem) bool {
	if left.score != right.score {
		return left.score < right.score
	}
	return left.label < right.label
}

func findSQLCompletionRelation(catalog sqlCompletionCatalog, name string) (sqlCompletionRelation, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return sqlCompletionRelation{}, false
	}
	for _, relation := range catalog.relations {
		if strings.EqualFold(relation.name, name) {
			return relation, true
		}
	}
	var match sqlCompletionRelation
	found := false
	for _, relation := range catalog.relations {
		if !strings.EqualFold(sqlIdentifierBase(relation.name), name) {
			continue
		}
		if found {
			return sqlCompletionRelation{}, false
		}
		match, found = relation, true
	}
	return match, found
}

func sqlIdentifierBase(identifier string) string {
	if dot := strings.LastIndex(identifier, "."); dot >= 0 {
		return identifier[dot+1:]
	}
	return identifier
}

func sqlCompletionIdentifier(dbType config.DBType, identifier string) string {
	needsQuotes := false
	start := 0
	for index := 0; index <= len(identifier); index++ {
		if index < len(identifier) && identifier[index] != '.' {
			continue
		}
		if sqlCompletionIdentifierNeedsQuotes(dbType, identifier[start:index]) {
			needsQuotes = true
			break
		}
		start = index + 1
	}
	if !needsQuotes {
		return identifier
	}

	parts := strings.Split(identifier, ".")
	for index, part := range parts {
		if sqlCompletionIdentifierNeedsQuotes(dbType, part) {
			parts[index] = quoteIdentifier(dbType, part)
		}
	}
	return strings.Join(parts, ".")
}

func sqlCompletionIdentifierNeedsQuotes(dbType config.DBType, identifier string) bool {
	if identifier == "" {
		return false
	}
	hasUpper := false
	for index, r := range identifier {
		if unicode.IsUpper(r) {
			hasUpper = true
		}
		if index == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return true
			}
		} else if r != '_' && r != '$' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
	}
	if _, reserved := sqlReservedWords[identifier]; reserved {
		return true
	}
	if hasUpper {
		if _, reserved := sqlReservedWords[strings.ToUpper(identifier)]; reserved {
			return true
		}
	}
	return dbType == config.PostgreSQL && hasUpper
}

type sqlCompletionCatalogBuilder struct {
	relations map[string]*sqlCompletionRelation
	schemas   map[string]string
	databases map[string]string
}

func newSQLCompletionCatalogBuilder(tables []string, databaseName string) *sqlCompletionCatalogBuilder {
	builder := &sqlCompletionCatalogBuilder{
		relations: make(map[string]*sqlCompletionRelation, len(tables)),
		schemas:   make(map[string]string),
		databases: make(map[string]string),
	}
	for _, table := range tables {
		builder.addRelation(table, sqlCompletionTable, "")
	}
	builder.addDatabase(databaseName)
	return builder
}

func (b *sqlCompletionCatalogBuilder) addRelation(name string, kind sqlCompletionKind, column string) {
	name = strings.TrimSpace(name)
	column = strings.TrimSpace(column)
	if name == "" {
		return
	}
	key := strings.ToLower(name)
	relation, ok := b.relations[key]
	if !ok {
		relation = &sqlCompletionRelation{name: name, kind: kind}
		b.relations[key] = relation
	} else if kind == sqlCompletionView {
		relation.kind = kind
	}
	if namespace, _ := splitQualifiedIdentifier(name); namespace != "" {
		b.schemas[strings.ToLower(namespace)] = namespace
	}
	if column == "" {
		return
	}
	for _, existing := range relation.columns {
		if strings.EqualFold(existing, column) {
			return
		}
	}
	relation.columns = append(relation.columns, column)
}

func (b *sqlCompletionCatalogBuilder) addSchema(name string) {
	name = strings.TrimSpace(name)
	if name != "" {
		b.schemas[strings.ToLower(name)] = name
	}
}

func (b *sqlCompletionCatalogBuilder) addDatabase(name string) {
	name = strings.TrimSpace(name)
	if name != "" {
		b.databases[strings.ToLower(name)] = name
	}
}

func (b *sqlCompletionCatalogBuilder) catalog() sqlCompletionCatalog {
	catalog := sqlCompletionCatalog{
		relations: make([]sqlCompletionRelation, 0, len(b.relations)),
		schemas:   make([]string, 0, len(b.schemas)),
		databases: make([]string, 0, len(b.databases)),
	}
	for _, relation := range b.relations {
		copyRelation := *relation
		copyRelation.columns = append([]string(nil), relation.columns...)
		catalog.relations = append(catalog.relations, copyRelation)
	}
	for _, schema := range b.schemas {
		catalog.schemas = append(catalog.schemas, schema)
	}
	for _, databaseName := range b.databases {
		catalog.databases = append(catalog.databases, databaseName)
	}
	sort.Slice(catalog.relations, func(i, j int) bool {
		return strings.ToLower(catalog.relations[i].name) < strings.ToLower(catalog.relations[j].name)
	})
	sort.Slice(catalog.schemas, func(i, j int) bool { return strings.ToLower(catalog.schemas[i]) < strings.ToLower(catalog.schemas[j]) })
	sort.Slice(catalog.databases, func(i, j int) bool {
		return strings.ToLower(catalog.databases[i]) < strings.ToLower(catalog.databases[j])
	})
	return catalog
}

func (a *App) reloadSQLCompletionCatalog() {
	if a == nil {
		return
	}
	generation := a.sqlCompletionGeneration.Add(1)
	databaseName := a.dbName
	if cfg := a.currentConnectionConfig(); cfg != nil && strings.TrimSpace(cfg.Database) != "" {
		databaseName = cfg.Database
	}
	tables := append([]string(nil), a.tableOrder...)
	builder := newSQLCompletionCatalogBuilder(tables, databaseName)
	a.sqlCompletionCatalog = builder.catalog()
	a.updateSidebarSearchIndex(a.sqlCompletionCatalog)

	db := a.db
	dbType := a.dbType
	if db == nil {
		return
	}
	go func() {
		// Let foreground table/result loading claim the small DB pool first.
		// Suggestions already have the table-list seed during this short warmup.
		timer := time.NewTimer(sqlCompletionCatalogWarmup)
		defer timer.Stop()
		<-timer.C
		if a.sqlCompletionGeneration.Load() != generation {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		catalog := loadSQLCompletionCatalog(ctx, db, dbType, tables, databaseName)
		// Large schemas can contain hundreds of thousands of searchable columns.
		// Build immutable search structures off the UI goroutine, then swap them
		// in together with the catalog.
		searchIndex := buildSidebarSearchIndex(tables, catalog, nil)
		searchLookup := buildSidebarSearchLookup(searchIndex)
		a.queueUpdateDraw(func() {
			if a.db != db || a.dbType != dbType || a.sqlCompletionGeneration.Load() != generation {
				return
			}
			a.sqlCompletionCatalog = catalog
			a.applySidebarSearchState(searchIndex, searchLookup)
			if a.focusedPanel == a.queryInput {
				a.refreshSQLCompletions(false)
			}
		})
	}()
}

func (a *App) resetSQLCompletionCatalog() {
	if a == nil {
		return
	}
	a.sqlCompletionGeneration.Add(1)
	a.sqlCompletionCatalog = sqlCompletionCatalog{}
	a.sqlCompletionRoutines = nil
	a.sidebarSearchIndex = nil
	a.sidebarSearchLookup = sidebarSearchLookup{}
	a.sidebarRenderedSearch = sidebarSelection{}
	a.sidebarColumnMetadata = nil
	a.sidebarMetadataLoads = nil
	a.expandedSidebarTable = ""
	a.hideSQLCompletions()
}

func loadSQLCompletionCatalog(ctx context.Context, db *sql.DB, dbType config.DBType, tables []string, databaseName string) sqlCompletionCatalog {
	builder := newSQLCompletionCatalogBuilder(tables, databaseName)
	if db == nil {
		return builder.catalog()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	switch dbType {
	case config.PostgreSQL:
		rows, err := db.QueryContext(ctx, `SELECT c.table_schema, c.table_name, t.table_type, c.column_name
FROM information_schema.columns c
JOIN information_schema.tables t
  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema NOT IN ('pg_catalog', 'information_schema')
ORDER BY c.table_schema, c.table_name, c.ordinal_position`)
		if err == nil {
			for rows.Next() {
				var schema, table, tableType, column string
				if rows.Scan(&schema, &table, &tableType, &column) != nil {
					break
				}
				kind := sqlCompletionTable
				if strings.EqualFold(tableType, "VIEW") {
					kind = sqlCompletionView
				}
				builder.addSchema(schema)
				builder.addRelation(qualifiedIdentifier(schema, table), kind, column)
			}
			rows.Close()
		}
	case config.MySQL:
		rows, err := db.QueryContext(ctx, `SELECT c.table_name, t.table_type, c.column_name
FROM information_schema.columns c
JOIN information_schema.tables t
  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema = DATABASE()
ORDER BY c.table_name, c.ordinal_position`)
		if err == nil {
			for rows.Next() {
				var table, tableType, column string
				if rows.Scan(&table, &tableType, &column) != nil {
					break
				}
				kind := sqlCompletionTable
				if strings.EqualFold(tableType, "VIEW") {
					kind = sqlCompletionView
				}
				builder.addRelation(table, kind, column)
			}
			rows.Close()
		}
		builder.addSchema(databaseName)
	case config.SQLite, config.Turso, config.CloudflareD1:
		rows, err := db.QueryContext(ctx, `SELECT m.name, m.type, p.name
FROM sqlite_master AS m
JOIN pragma_table_info(m.name) AS p
WHERE m.type IN ('table', 'view')
  AND m.name NOT LIKE 'sqlite_%'
ORDER BY m.name, p.cid`)
		if err == nil {
			for rows.Next() {
				var relationName, relationType, column string
				if rows.Scan(&relationName, &relationType, &column) != nil {
					break
				}
				kind := sqlCompletionTable
				if strings.EqualFold(relationType, "view") {
					kind = sqlCompletionView
				}
				builder.addRelation(relationName, kind, column)
			}
			rows.Close()
		}
	}

	if query := database.ListDatabasesQuery(dbType); query != "" && ctx.Err() == nil {
		databaseCtx, cancel := context.WithTimeout(ctx, sqlCompletionDatabaseTimeout)
		if rows, err := db.QueryContext(databaseCtx, query); err == nil {
			for rows.Next() {
				var name string
				if rows.Scan(&name) != nil {
					break
				}
				builder.addDatabase(name)
			}
			rows.Close()
		}
		cancel()
	}
	return builder.catalog()
}
