package ui

import (
	"math/bits"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/shreyam1008/dbterm/internal/config"
)

var sqlMissingRelationPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brelation\s+["']([^"']+)["']\s+does not exist`),
	regexp.MustCompile(`(?i)\bno such table:\s*([^\s;]+)`),
	regexp.MustCompile(`(?i)\btable\s+'([^']+)'\s+doesn't exist`),
	regexp.MustCompile(`(?i)\bunknown table\s+["']?([^\s;"']+)`),
}

var sqlMissingColumnPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bcolumn\s+["']([^"']+)["']\s+does not exist`),
	regexp.MustCompile(`(?i)\bno such column:\s*([^\s;]+)`),
	regexp.MustCompile(`(?i)\bunknown column\s+["']([^"']+)["']`),
}

func (c *sqlCompletionCollector) addReadyQueryTemplates(input sqlCompletionInput, priority int) {
	relation, ok := findSQLCompletionRelation(input.catalog, input.activeTable)
	if !ok {
		return
	}
	table := sqlCompletionIdentifier(input.dbType, relation.name)
	detail := "ready query · " + relation.name
	c.add("Preview rows · "+relation.name,
		"SELECT * FROM "+table+" LIMIT 100;",
		sqlCompletionTemplate, detail, priority, false, "select preview rows view all")
	c.add("Count rows · "+relation.name,
		"SELECT COUNT(*) AS row_count FROM "+table+";",
		sqlCompletionTemplate, detail, priority+1, false, "count rows total")

	if columns := sqlTemplateColumnList(input.dbType, relation.columns, len(relation.columns)); columns != "" {
		c.add("Select named columns · "+relation.name,
			"SELECT "+columns+" FROM "+table+" LIMIT 100;",
			sqlCompletionTemplate, detail, priority+2, false, "select columns fields")
	}
	if column := sqlTemplateRecentColumn(relation.columns); column != "" {
		quotedColumn := sqlCompletionIdentifier(input.dbType, column)
		c.add("Newest rows by "+column,
			"SELECT * FROM "+table+" ORDER BY "+quotedColumn+" DESC LIMIT 100;",
			sqlCompletionTemplate, detail, priority+3, false, "newest recent latest order")
	}
	if column := sqlTemplateGroupColumn(relation.columns); column != "" {
		quotedColumn := sqlCompletionIdentifier(input.dbType, column)
		c.add("Summarize by "+column,
			"SELECT "+quotedColumn+", COUNT(*) AS row_count FROM "+table+
				" GROUP BY "+quotedColumn+" ORDER BY row_count DESC LIMIT 100;",
			sqlCompletionTemplate, detail, priority+4, false, "summarize group distinct values")
	}
}

func (c *sqlCompletionCollector) addReadyClauseTemplates(referenced []string, catalog sqlCompletionCatalog, priority int) {
	if len(referenced) == 0 {
		return
	}
	relation, ok := findSQLCompletionRelation(catalog, referenced[0])
	if !ok {
		return
	}
	if !sqlCompletionHasWord(c.previousWords, "LIMIT") {
		c.add("Limit to 100 rows", "LIMIT 100;", sqlCompletionTemplate,
			"ready clause", priority, false, "limit preview rows")
	}
	if column := sqlTemplateRecentColumn(relation.columns); column != "" &&
		!sqlCompletionHasWord(c.previousWords, "ORDER") {
		quotedColumn := sqlCompletionIdentifier(c.dbType, column)
		c.add("Newest first by "+column,
			"ORDER BY "+quotedColumn+" DESC LIMIT 100;",
			sqlCompletionTemplate, "ready clause", priority+1, false, "newest recent latest order")
	}
	if column := sqlTemplateFilterColumn(relation.columns); column != "" &&
		!sqlCompletionHasWord(c.previousWords, "WHERE") {
		quotedColumn := sqlCompletionIdentifier(c.dbType, column)
		c.add("Filter non-NULL "+column,
			"WHERE "+quotedColumn+" IS NOT NULL",
			sqlCompletionTemplate, "ready clause", priority+2, true, "where filter non null")
	}
}

func sqlTemplateColumnList(dbType config.DBType, columns []string, limit int) string {
	if limit <= 0 || len(columns) == 0 {
		return ""
	}
	count := min(len(columns), limit)
	quoted := make([]string, 0, count)
	for _, column := range columns[:count] {
		if strings.TrimSpace(column) != "" {
			quoted = append(quoted, sqlCompletionIdentifier(dbType, column))
		}
	}
	return strings.Join(quoted, ", ")
}

func sqlTemplateRecentColumn(columns []string) string {
	return sqlTemplatePreferredColumn(columns, []string{
		"updated_at", "modified_at", "created_at", "timestamp", "event_time", "date", "time",
	})
}

func sqlTemplateGroupColumn(columns []string) string {
	return sqlTemplatePreferredColumn(columns, []string{
		"status", "state", "type", "category", "role", "country", "kind",
	})
}

func sqlTemplateFilterColumn(columns []string) string {
	if preferred := sqlTemplatePreferredColumn(columns, []string{
		"status", "state", "name", "email", "type", "category", "created_at", "updated_at",
	}); preferred != "" {
		return preferred
	}
	for _, column := range columns {
		if !strings.EqualFold(column, "id") && !strings.HasSuffix(strings.ToLower(column), "_id") {
			return column
		}
	}
	if len(columns) > 0 {
		return columns[0]
	}
	return ""
}

func sqlTemplatePreferredColumn(columns, preferred []string) string {
	for _, wanted := range preferred {
		for _, column := range columns {
			if strings.EqualFold(column, wanted) {
				return column
			}
		}
	}
	return ""
}

func sqlCompletionHasWord(words []string, wanted string) bool {
	for _, word := range words {
		if strings.EqualFold(word, wanted) {
			return true
		}
	}
	return false
}

func sqlCompletionTypoDistance(candidate, foldedPrefix string) (int, bool) {
	foldedPrefix = strings.ToLower(strings.TrimSpace(foldedPrefix))
	if isASCIIString(candidate) && isASCIIString(foldedPrefix) {
		return sqlCompletionTypoDistanceASCII(candidate, foldedPrefix)
	}
	return sqlCompletionTypoDistanceRunes(candidate, []rune(foldedPrefix))
}

type sqlRelationTypoCandidate struct {
	relation sqlCompletionRelation
	score    int
}

func (c *sqlCompletionCollector) addRelationTypos(relations []sqlCompletionRelation, priority int) {
	if c == nil || len(relations) == 0 {
		return
	}
	// Exact edit distance is comparatively expensive. Rank with a fixed-size,
	// allocation-light character/position sketch first, then run Damerau-
	// Levenshtein only on the strongest candidates. Non-ASCII identifiers keep
	// the complete correctness-first path.
	if !isASCIIString(c.prefixFolded) {
		for _, relation := range relations {
			c.addRelationTypo(relation, priority)
		}
		return
	}
	candidateLimit := max(48, c.limit*8)
	candidates := make([]sqlRelationTypoCandidate, 0, candidateLimit)
	maxDistance := sqlTypoMaxDistance(len(c.prefixFolded))
	querySketch := buildSQLTypoSketch(c.prefixFolded)
	worst := -1
	for _, relation := range relations {
		base := strings.ToLower(sqlIdentifierBase(relation.name))
		if !isASCIIString(base) || absInt(len(base)-len(c.prefixFolded)) > maxDistance {
			continue
		}
		candidate := sqlRelationTypoCandidate{relation: relation, score: sqlTypoSketchScore(base, c.prefixFolded, querySketch)}
		if len(candidates) < candidateLimit {
			candidates = append(candidates, candidate)
			if len(candidates) == candidateLimit {
				worst = worstSQLRelationTypoCandidate(candidates)
			}
			continue
		}
		if candidate.score < candidates[worst].score {
			candidates[worst] = candidate
			worst = worstSQLRelationTypoCandidate(candidates)
		}
	}
	for _, candidate := range candidates {
		c.addRelationTypo(candidate.relation, priority)
	}
}

func (c *sqlCompletionCollector) addRelationTypo(relation sqlCompletionRelation, priority int) bool {
	distance, ok := sqlCompletionTypoDistance(relation.name, c.prefixFolded)
	if !ok {
		return false
	}
	detail := "table"
	if relation.kind == sqlCompletionView {
		detail = "view"
	}
	c.addScored(relation.name, sqlCompletionIdentifier(c.dbType, relation.name), relation.kind, detail, priority, 130+distance*12, true)
	return true
}

func worstSQLRelationTypoCandidate(candidates []sqlRelationTypoCandidate) int {
	worst := 0
	for index := 1; index < len(candidates); index++ {
		if candidates[index].score > candidates[worst].score {
			worst = index
		}
	}
	return worst
}

type sqlTypoSketch struct {
	sum     int
	squares int
	mask    uint64
}

func buildSQLTypoSketch(value string) sqlTypoSketch {
	var sketch sqlTypoSketch
	for index := 0; index < len(value); index++ {
		bucket := sqlTypoSketchBucket(value[index])
		sketch.sum += bucket
		sketch.squares += bucket * bucket
		sketch.mask |= uint64(1) << bucket
	}
	return sketch
}

func sqlTypoSketchScore(candidate, query string, querySketch sqlTypoSketch) int {
	positionalDifferences := absInt(len(candidate) - len(query))
	candidateSketch := sqlTypoSketch{}
	for index := 0; index < len(candidate); index++ {
		bucket := sqlTypoSketchBucket(candidate[index])
		candidateSketch.sum += bucket
		candidateSketch.squares += bucket * bucket
		candidateSketch.mask |= uint64(1) << bucket
		if index < len(query) && candidate[index] != query[index] {
			positionalDifferences++
		}
	}
	return absInt(len(candidate)-len(query))*16 +
		absInt(candidateSketch.sum-querySketch.sum)*2 +
		absInt(candidateSketch.squares-querySketch.squares)/8 +
		bits.OnesCount64(candidateSketch.mask^querySketch.mask)*6 +
		positionalDifferences
}

func sqlTypoSketchBucket(value byte) int {
	switch {
	case value >= 'a' && value <= 'z':
		return int(value - 'a')
	case value >= '0' && value <= '9':
		return 26 + int(value-'0')
	case value == '_':
		return 36
	default:
		return 37
	}
}

func sqlCompletionTypoDistanceASCII(candidate, foldedPrefix string) (int, bool) {
	if len(foldedPrefix) < 3 {
		return 0, false
	}
	maxDistance := sqlTypoMaxDistance(len(foldedPrefix))
	candidate = strings.ToLower(strings.TrimSpace(candidate))
	variants := [2]string{candidate, strings.ToLower(sqlIdentifierBase(candidate))}
	best := maxDistance + 1
	for index, variant := range variants {
		if index == 1 && variant == variants[0] {
			continue
		}
		lengths := [2]int{len(variant), min(len(foldedPrefix), len(variant))}
		for lengthIndex, length := range lengths {
			if lengthIndex == 1 && length == lengths[0] {
				continue
			}
			if length < 1 || absInt(length-len(foldedPrefix)) > maxDistance {
				continue
			}
			distance := sqlDamerauLevenshteinASCIIBounded(foldedPrefix, variant[:length], maxDistance)
			if distance < best {
				best = distance
			}
		}
	}
	return best, best <= maxDistance
}

func sqlDamerauLevenshteinASCII(left, right string) int {
	return sqlDamerauLevenshteinASCIIBounded(left, right, max(len(left), len(right)))
}

func sqlDamerauLevenshteinASCIIBounded(left, right string, maxDistance int) int {
	if absInt(len(left)-len(right)) > maxDistance {
		return maxDistance + 1
	}
	cols := len(right) + 1
	var fixed [3 * 64]int
	storage := fixed[:]
	if cols > 64 {
		storage = make([]int, 3*cols)
	}
	previousPrevious := storage[:cols]
	previous := storage[cols : 2*cols]
	current := storage[2*cols : 3*cols]
	for col := range previous {
		previous[col] = col
	}
	for row := 1; row <= len(left); row++ {
		start := max(1, row-maxDistance)
		end := min(len(right), row+maxDistance)
		current[0] = row
		if start > 1 {
			current[start-1] = maxDistance + 1
		}
		if end+1 < cols {
			current[end+1] = maxDistance + 1
		}
		for col := start; col <= end; col++ {
			cost := 0
			if left[row-1] != right[col-1] {
				cost = 1
			}
			current[col] = min(min(previous[col]+1, current[col-1]+1), previous[col-1]+cost)
			if row > 1 && col > 1 && left[row-1] == right[col-2] && left[row-2] == right[col-1] {
				current[col] = min(current[col], previousPrevious[col-2]+1)
			}
		}
		previousPrevious, previous, current = previous, current, previousPrevious
	}
	if previous[len(right)] > maxDistance {
		return maxDistance + 1
	}
	return previous[len(right)]
}

func sqlCompletionTypoDistanceRunes(candidate string, prefixRunes []rune) (int, bool) {
	if len(prefixRunes) < 3 {
		return 0, false
	}
	maxDistance := sqlTypoMaxDistance(len(prefixRunes))
	candidate = strings.ToLower(strings.TrimSpace(candidate))
	variants := []string{candidate}
	if base := strings.ToLower(sqlIdentifierBase(candidate)); base != candidate {
		variants = append(variants, base)
	}
	best := maxDistance + 1
	for _, variant := range variants {
		variantRunes := []rune(variant)
		// Compare the complete identifier and, for a partially typed longer
		// candidate, an equal-length prefix. This catches swaps such as usre/user
		// without evaluating four mostly redundant matrices per catalog entry.
		lengths := [2]int{len(variantRunes), min(len(prefixRunes), len(variantRunes))}
		for index, length := range lengths {
			if index == 1 && length == lengths[0] {
				continue
			}
			if length < 1 || length > len(variantRunes) || absInt(length-len(prefixRunes)) > maxDistance {
				continue
			}
			distance := sqlDamerauLevenshtein(prefixRunes, variantRunes[:length])
			if distance < best {
				best = distance
			}
		}
	}
	return best, best <= maxDistance
}

func sqlTypoMaxDistance(length int) int {
	switch {
	case length >= 10:
		return 3
	case length >= 6:
		return 2
	default:
		return 1
	}
}

func sqlDamerauLevenshtein(left, right []rune) int {
	cols := len(right) + 1
	// Optimal-string-alignment distance needs only the current and previous
	// two rows. A fixed backing array keeps common SQL identifiers allocation
	// free; unusually long identifiers use one compact fallback allocation.
	var fixed [3 * 64]int
	storage := fixed[:]
	if cols > 64 {
		storage = make([]int, 3*cols)
	}
	previousPrevious := storage[:cols]
	previous := storage[cols : 2*cols]
	current := storage[2*cols : 3*cols]
	for col := range previous {
		previous[col] = col
	}
	for row := 1; row <= len(left); row++ {
		current[0] = row
		for col := 1; col < cols; col++ {
			cost := 0
			if left[row-1] != right[col-1] {
				cost = 1
			}
			current[col] = min(
				min(previous[col]+1, current[col-1]+1),
				previous[col-1]+cost,
			)
			if row > 1 && col > 1 && left[row-1] == right[col-2] && left[row-2] == right[col-1] {
				current[col] = min(current[col], previousPrevious[col-2]+1)
			}
		}
		previousPrevious, previous, current = previous, current, previousPrevious
	}
	return previous[len(right)]
}

func sqlMissingRelationSuggestion(message string, catalog sqlCompletionCatalog) (string, bool) {
	unknown := sqlCompletionErrorIdentifier(message, sqlMissingRelationPatterns)
	if unknown == "" {
		return "", false
	}
	unknown = sqlIdentifierBase(unknown)
	if utf8.RuneCountInString(unknown) < 3 {
		return "", false
	}

	unknownRunes := []rune(strings.ToLower(unknown))
	maxDistance := sqlTypoMaxDistance(len(unknownRunes))
	bestDistance := maxDistance + 1
	bestName := ""
	ambiguous := false
	for _, relation := range catalog.relations {
		candidateRunes := []rune(strings.ToLower(sqlIdentifierBase(relation.name)))
		if absInt(len(candidateRunes)-len(unknownRunes)) > maxDistance {
			continue
		}
		distance := sqlDamerauLevenshtein(unknownRunes, candidateRunes)
		switch {
		case distance < bestDistance:
			bestDistance = distance
			bestName = relation.name
			ambiguous = false
		case distance == bestDistance && !strings.EqualFold(bestName, relation.name):
			ambiguous = true
		}
	}
	if bestName == "" || bestDistance > maxDistance || ambiguous {
		return "", false
	}
	return bestName, true
}

func sqlMissingColumnSuggestion(message, query string, catalog sqlCompletionCatalog, activeTable string) (string, bool) {
	unknown := sqlIdentifierBase(sqlCompletionErrorIdentifier(message, sqlMissingColumnPatterns))
	if utf8.RuneCountInString(unknown) < 3 {
		return "", false
	}
	tokens, _ := lexSQLCompletion(query)
	_, referenced := sqlCompletionAliases(tokens, catalog)
	if len(referenced) == 0 && strings.TrimSpace(activeTable) != "" {
		referenced = []string{activeTable}
	}
	unknownRunes := []rune(strings.ToLower(unknown))
	maxDistance := sqlTypoMaxDistance(len(unknownRunes))
	bestDistance := maxDistance + 1
	bestName := ""
	ambiguous := false
	for _, relationName := range referenced {
		relation, ok := findSQLCompletionRelation(catalog, relationName)
		if !ok {
			continue
		}
		for _, column := range relation.columns {
			candidateRunes := []rune(strings.ToLower(column))
			if absInt(len(candidateRunes)-len(unknownRunes)) > maxDistance {
				continue
			}
			distance := sqlDamerauLevenshtein(unknownRunes, candidateRunes)
			switch {
			case distance < bestDistance:
				bestDistance = distance
				bestName = column
				ambiguous = false
			case distance == bestDistance && !strings.EqualFold(bestName, column):
				ambiguous = true
			}
		}
	}
	if bestName == "" || bestDistance > maxDistance || ambiguous {
		return "", false
	}
	return bestName, true
}

func sqlCompletionErrorIdentifier(message string, patterns []*regexp.Regexp) string {
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(message)
		if len(match) > 1 {
			return strings.Trim(match[1], "\"'`.,")
		}
	}
	return ""
}

func sqlErrorMatchesAnyPattern(message string, patterns []*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(message) {
			return true
		}
	}
	return false
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
