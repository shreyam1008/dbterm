package backup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var dangerousSQLiteFunctions = map[string]struct{}{
	"CSV":            {},
	"EDIT":           {},
	"EVAL":           {},
	"FSDIR":          {},
	"LOAD_EXTENSION": {},
	"READFILE":       {},
	"SHELL":          {},
	"SYSTEM":         {},
	"WRITEFILE":      {},
	"ZIPFILE":        {},
}

var dangerousSQLiteModules = map[string]struct{}{
	"CSV":     {},
	"FILEIO":  {},
	"FSDIR":   {},
	"ZIPFILE": {},
}

var dangerousSQLitePragmas = map[string]struct{}{
	"DATA_STORE_DIRECTORY": {},
	"TEMP_STORE_DIRECTORY": {},
}

// validateSQLiteSQLForRestore performs a streaming lexical scan. The sqlite3
// client still performs the authoritative parse, but constructs that could
// escape the private staging database are rejected before the client starts.
func validateSQLiteSQLForRestore(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open SQLite SQL backup for safety scan: %w", err)
	}
	defer file.Close()
	if err := scanSQLiteSQLSafety(bufio.NewReaderSize(file, restoreSQLBufferSize)); err != nil {
		return fmt.Errorf("SQLite SQL safety scan failed: %w", err)
	}
	return nil
}

func scanSQLiteSQLSafety(reader *bufio.Reader) error {
	if prefix, _ := reader.Peek(3); len(prefix) == 3 && prefix[0] == 0xef && prefix[1] == 0xbb && prefix[2] == 0xbf {
		_, _ = reader.Discard(3)
	}
	line := 1
	atLineStart := true
	var token strings.Builder
	statementTokens := make([]string, 0, 32)
	pendingFunction := ""
	transactionDepth := 0
	inTriggerBody := false

	processToken := func(value string) error {
		upper := strings.ToUpper(value)
		if upper == "" {
			return nil
		}
		if pendingFunction != "" {
			pendingFunction = ""
		}
		if len(statementTokens) < 64 {
			statementTokens = append(statementTokens, upper)
		}
		if len(statementTokens) == 1 && (upper == "ATTACH" || upper == "DETACH") {
			return fmt.Errorf("%s is not allowed at line %d because restore input must stay inside the staged database", upper, line)
		}
		if len(statementTokens) > 1 && statementTokens[0] == "VACUUM" && upper == "INTO" {
			return fmt.Errorf("VACUUM INTO is not allowed at line %d because it writes an arbitrary file", line)
		}
		if len(statementTokens) > 1 && statementTokens[0] == "PRAGMA" {
			if _, blocked := dangerousSQLitePragmas[upper]; blocked {
				return fmt.Errorf("PRAGMA %s is not allowed at line %d because it changes a filesystem location", upper, line)
			}
			if _, blocked := dangerousSQLiteFunctions[upper]; blocked {
				return fmt.Errorf("PRAGMA %s is not allowed at line %d", upper, line)
			}
		}
		if len(statementTokens) >= 2 && statementTokens[len(statementTokens)-2] == "USING" && sqliteVirtualTablePrefix(statementTokens) {
			if _, blocked := dangerousSQLiteModules[upper]; blocked {
				return fmt.Errorf("SQLite virtual-table module %s is not allowed at line %d because it can access external files", upper, line)
			}
		}
		if _, dangerous := dangerousSQLiteFunctions[upper]; dangerous {
			pendingFunction = upper
		}
		return nil
	}
	flushToken := func() error {
		if token.Len() == 0 {
			return nil
		}
		value := token.String()
		token.Reset()
		return processToken(value)
	}
	finishStatement := func() {
		if len(statementTokens) > 0 {
			if inTriggerBody {
				if len(statementTokens) == 1 && statementTokens[0] == "END" {
					inTriggerBody = false
				}
			} else if statementTokens[0] == "CREATE" && containsSQLiteToken(statementTokens, "TRIGGER") && containsSQLiteToken(statementTokens, "BEGIN") {
				inTriggerBody = true
			} else {
				switch statementTokens[0] {
				case "BEGIN", "SAVEPOINT":
					transactionDepth++
				case "COMMIT", "END":
					transactionDepth = 0
				case "RELEASE":
					if transactionDepth > 0 {
						transactionDepth--
					}
				case "ROLLBACK":
					if len(statementTokens) < 2 || statementTokens[1] != "TO" {
						transactionDepth = 0
					}
				}
			}
		}
		statementTokens = statementTokens[:0]
		pendingFunction = ""
	}

	for {
		character, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			if err := flushToken(); err != nil {
				return err
			}
			finishStatement()
			if inTriggerBody {
				return fmt.Errorf("SQLite SQL ended inside a CREATE TRIGGER body")
			}
			if transactionDepth != 0 {
				return fmt.Errorf("SQLite SQL ends with an open transaction or savepoint; the staged restore would be rolled back")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if character == 0 {
			return fmt.Errorf("NUL byte at line %d", line)
		}

		if atLineStart && character == '.' {
			peek, _ := reader.Peek(1)
			if len(peek) == 1 && isASCIIIdentifierStart(peek[0]) {
				return fmt.Errorf("sqlite3 dot command is not allowed at line %d", line)
			}
		}

		switch character {
		case '\n':
			if err := flushToken(); err != nil {
				return err
			}
			line++
			atLineStart = true
		case '\r':
			if err := flushToken(); err != nil {
				return err
			}
			if peek, _ := reader.Peek(1); len(peek) == 1 && peek[0] == '\n' {
				_, _ = reader.ReadByte()
			}
			line++
			atLineStart = true
		case ' ', '\t', '\f':
			if err := flushToken(); err != nil {
				return err
			}
		case ';':
			if err := flushToken(); err != nil {
				return err
			}
			finishStatement()
			atLineStart = false
		case '(':
			if err := flushToken(); err != nil {
				return err
			}
			if pendingFunction != "" {
				return fmt.Errorf("SQLite function %s is not allowed at line %d because it can access files, extensions, or the host process", pendingFunction, line)
			}
			atLineStart = false
		case '\'', '"', '`', '[':
			if err := flushToken(); err != nil {
				return err
			}
			pendingFunction = ""
			quoted, err := readSQLiteQuoted(reader, character, &line)
			if err != nil {
				return err
			}
			if character != '\'' {
				if err := processToken(quoted); err != nil {
					return err
				}
			}
			atLineStart = false
		case '-':
			peek, _ := reader.Peek(1)
			if len(peek) == 1 && peek[0] == '-' {
				_, _ = reader.ReadByte()
				if err := flushToken(); err != nil {
					return err
				}
				if err := skipSQLiteLineComment(reader, &line); err != nil {
					return err
				}
				atLineStart = true
			} else {
				if err := flushToken(); err != nil {
					return err
				}
				pendingFunction = ""
				atLineStart = false
			}
		case '/':
			peek, _ := reader.Peek(1)
			if len(peek) == 1 && peek[0] == '*' {
				_, _ = reader.ReadByte()
				if err := flushToken(); err != nil {
					return err
				}
				endedAtLineStart, err := skipSQLiteBlockComment(reader, &line)
				if err != nil {
					return err
				}
				atLineStart = endedAtLineStart
			} else {
				if err := flushToken(); err != nil {
					return err
				}
				pendingFunction = ""
				atLineStart = false
			}
		default:
			if isSQLiteTokenByte(character) {
				if token.Len() >= 4096 {
					return fmt.Errorf("SQLite SQL token exceeds 4096 bytes at line %d", line)
				}
				token.WriteByte(character)
			} else {
				if err := flushToken(); err != nil {
					return err
				}
				pendingFunction = ""
			}
			atLineStart = false
		}
	}
}

func containsSQLiteToken(tokens []string, expected string) bool {
	for _, token := range tokens {
		if token == expected {
			return true
		}
	}
	return false
}

func sqliteVirtualTablePrefix(tokens []string) bool {
	return len(tokens) >= 5 && tokens[0] == "CREATE" && tokens[1] == "VIRTUAL" && tokens[2] == "TABLE"
}

func isASCIIIdentifierStart(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character == '_'
}

func isSQLiteTokenByte(character byte) bool {
	return isASCIIIdentifierStart(character) || character >= '0' && character <= '9' || character == '$' || character >= 0x80
}

func readSQLiteQuoted(reader *bufio.Reader, opener byte, line *int) (string, error) {
	closer := opener
	if opener == '[' {
		closer = ']'
	}
	var value strings.Builder
	for {
		character, err := reader.ReadByte()
		if err != nil {
			return "", fmt.Errorf("unterminated SQLite quoted value at line %d", *line)
		}
		if character == 0 {
			return "", fmt.Errorf("NUL byte at line %d", *line)
		}
		if character == '\n' {
			(*line)++
		}
		if character == closer {
			peek, _ := reader.Peek(1)
			if len(peek) == 1 && peek[0] == closer {
				_, _ = reader.ReadByte()
				if value.Len() < 4096 {
					value.WriteByte(closer)
				}
				continue
			}
			return value.String(), nil
		}
		if value.Len() < 4096 {
			value.WriteByte(character)
		}
	}
}

func skipSQLiteLineComment(reader *bufio.Reader, line *int) error {
	for {
		character, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if character == 0 {
			return fmt.Errorf("NUL byte at line %d", *line)
		}
		if character == '\n' {
			(*line)++
			return nil
		}
	}
}

func skipSQLiteBlockComment(reader *bufio.Reader, line *int) (bool, error) {
	previous := byte(0)
	atLineStart := false
	for {
		character, err := reader.ReadByte()
		if err != nil {
			return false, fmt.Errorf("unterminated SQLite block comment at line %d", *line)
		}
		if character == 0 {
			return false, fmt.Errorf("NUL byte at line %d", *line)
		}
		if character == '\n' {
			(*line)++
			atLineStart = true
		} else if character != ' ' && character != '\t' && character != '\r' {
			atLineStart = false
		}
		if previous == '*' && character == '/' {
			return atLineStart, nil
		}
		previous = character
	}
}
