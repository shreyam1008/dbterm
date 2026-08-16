package mcpserver

import (
	"fmt"
	"strings"
	"unicode"
)

var forbiddenSQLWords = map[string]struct{}{
	"ALTER": {}, "ANALYZE": {}, "ATTACH": {}, "BEGIN": {}, "CALL": {},
	"COMMIT": {}, "COPY": {}, "CREATE": {}, "DEALLOCATE": {}, "DELETE": {},
	"DETACH": {}, "DO": {}, "DROP": {}, "EXEC": {}, "EXECUTE": {},
	"GRANT": {}, "INSERT": {}, "INSTALL": {}, "INTO": {}, "LOAD": {},
	"LOCK": {}, "MERGE": {}, "PREPARE": {}, "REINDEX": {}, "RELEASE": {},
	"REPLACE": {}, "RESET": {}, "REVOKE": {}, "ROLLBACK": {}, "SAVEPOINT": {},
	"SET": {}, "TRUNCATE": {}, "UPDATE": {}, "UPSERT": {}, "VACUUM": {},
}

var noArgumentPragmas = map[string]struct{}{
	"COLLATION_LIST": {}, "COMPILE_OPTIONS": {}, "DATABASE_LIST": {},
	"FOREIGN_KEYS": {}, "TABLE_LIST": {},
}

var identifierArgumentPragmas = map[string]struct{}{
	"FOREIGN_KEY_LIST": {}, "INDEX_INFO": {}, "INDEX_LIST": {},
	"INDEX_XINFO": {}, "TABLE_INFO": {}, "TABLE_XINFO": {},
}

// validateReadOnlySQL intentionally accepts a small, auditable SQL surface.
// The database's read-only transaction/credential is still the final safety
// boundary because SQL functions can have side effects that syntax alone
// cannot determine.
func validateReadOnlySQL(query string, maxBytes int) error {
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("SQL is required")
	}
	if len(query) > maxBytes {
		return fmt.Errorf("SQL exceeds the %d-byte limit", maxBytes)
	}
	cleaned, statements, err := lexSQL(query)
	if err != nil {
		return err
	}
	if statements > 1 {
		return fmt.Errorf("only one SQL statement is allowed")
	}
	words := sqlWords(cleaned)
	if len(words) == 0 {
		return fmt.Errorf("SQL statement is empty")
	}
	switch words[0] {
	case "SELECT", "WITH", "VALUES":
	case "EXPLAIN":
		if len(words) < 2 || (words[1] != "SELECT" && words[1] != "WITH" && words[1] != "VALUES") {
			return fmt.Errorf("EXPLAIN is limited to SELECT, WITH SELECT, or VALUES")
		}
	case "PRAGMA":
		if err := validatePragma(cleaned, words); err != nil {
			return err
		}
	default:
		return fmt.Errorf("statement type %s is not allowed; use SELECT, WITH SELECT, VALUES, EXPLAIN SELECT, or an introspection PRAGMA", words[0])
	}
	for _, word := range words[1:] {
		if _, forbidden := forbiddenSQLWords[word]; forbidden {
			return fmt.Errorf("SQL keyword %s is not allowed by the read-only policy", word)
		}
	}
	return nil
}

func validateExplainableSQL(query string, maxBytes int) error {
	if err := validateReadOnlySQL(query, maxBytes); err != nil {
		return err
	}
	cleaned, _, _ := lexSQL(query)
	words := sqlWords(cleaned)
	if len(words) == 0 || (words[0] != "SELECT" && words[0] != "WITH" && words[0] != "VALUES") {
		return fmt.Errorf("only SELECT, WITH SELECT, or VALUES can be explained")
	}
	return nil
}

func validatePragma(cleaned string, words []string) error {
	if strings.Contains(cleaned, "=") || len(words) < 2 {
		return fmt.Errorf("only read-only introspection PRAGMAs are allowed")
	}
	statement := strings.TrimSpace(cleaned)
	rest := strings.TrimSpace(statement[len("PRAGMA"):])
	nameEnd := strings.IndexAny(rest, " \t\r\n(")
	if nameEnd < 0 {
		nameEnd = len(rest)
	}
	name := strings.ToUpper(rest[:nameEnd])
	tail := strings.TrimSpace(rest[nameEnd:])
	if _, ok := noArgumentPragmas[name]; ok {
		if tail != "" {
			return fmt.Errorf("PRAGMA %s does not accept an argument in the read-only policy", name)
		}
		return nil
	}
	if _, ok := identifierArgumentPragmas[name]; !ok {
		return fmt.Errorf("PRAGMA %s is not in the read-only allowlist", name)
	}
	if len(tail) < 3 || tail[0] != '(' || tail[len(tail)-1] != ')' {
		return fmt.Errorf("PRAGMA %s requires one table or index identifier", name)
	}
	identifier := strings.TrimSpace(tail[1 : len(tail)-1])
	if len(identifier) >= 2 && ((identifier[0] == '"' && identifier[len(identifier)-1] == '"') || (identifier[0] == '`' && identifier[len(identifier)-1] == '`')) {
		identifier = identifier[1 : len(identifier)-1]
	}
	if !safeIdentifier.MatchString(identifier) {
		return fmt.Errorf("PRAGMA %s requires one safe table or index identifier", name)
	}
	return nil
}

// lexSQL removes comments and quoted contents while preserving identifiers.
// It also counts semicolon-separated statements outside quoted regions.
func lexSQL(input string) (string, int, error) {
	var out strings.Builder
	statements := 1
	hasAfterSemicolon := false
	semicolonSeen := false
	for i := 0; i < len(input); {
		c := input[i]
		switch {
		case c == '-' && i+1 < len(input) && input[i+1] == '-':
			i += 2
			for i < len(input) && input[i] != '\n' {
				i++
			}
			out.WriteByte(' ')
		case c == '/' && i+1 < len(input) && input[i+1] == '*':
			mysqlExecutable := i+2 < len(input) && input[i+2] == '!'
			mariaDBExecutable := i+3 < len(input) && (input[i+2] == 'M' || input[i+2] == 'm') && input[i+3] == '!'
			if mysqlExecutable || mariaDBExecutable {
				return "", 0, fmt.Errorf("MySQL/MariaDB executable comments are not allowed")
			}
			i += 2
			closed := false
			for i+1 < len(input) {
				if input[i] == '*' && input[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return "", 0, fmt.Errorf("unterminated SQL block comment")
			}
			out.WriteByte(' ')
		case c == '#':
			for i < len(input) && input[i] != '\n' {
				i++
			}
			out.WriteByte(' ')
		case c == '\'' || c == '"' || c == '`':
			quote := c
			if quote != '\'' {
				out.WriteByte(quote)
			}
			i++
			closed := false
			for i < len(input) {
				if input[i] == quote {
					if i+1 < len(input) && input[i+1] == quote {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				if input[i] == '\\' && quote == '\'' && i+1 < len(input) {
					i += 2
					continue
				}
				if quote != '\'' {
					out.WriteByte(input[i])
				}
				i++
			}
			if !closed {
				return "", 0, fmt.Errorf("unterminated quoted SQL value")
			}
			if quote != '\'' {
				out.WriteByte(quote)
			} else {
				out.WriteByte(' ')
			}
		case c == ';':
			semicolonSeen = true
			i++
		case unicode.IsSpace(rune(c)):
			out.WriteByte(' ')
			i++
		default:
			if semicolonSeen && !unicode.IsSpace(rune(c)) {
				hasAfterSemicolon = true
			}
			out.WriteByte(c)
			i++
		}
	}
	if hasAfterSemicolon {
		statements = 2
	}
	return out.String(), statements, nil
}

func sqlWords(cleaned string) []string {
	return strings.FieldsFunc(strings.ToUpper(cleaned), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_')
	})
}
