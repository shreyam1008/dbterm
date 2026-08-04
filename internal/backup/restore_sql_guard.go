package backup

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

const (
	restoreSQLBufferSize      = 64 << 10
	maxExecutableCommentBytes = 1 << 20
)

// validatePostgresSQLForRestore blocks psql client commands that can escape
// the selected database or access the local machine. pg_dump's \restrict,
// \unrestrict and COPY-data terminator commands remain supported.
func validatePostgresSQLForRestore(path, targetDatabase string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open PostgreSQL SQL backup for safety scan: %w", err)
	}
	defer file.Close()

	scanner := postgresSQLSafetyScanner{
		reader:             bufio.NewReaderSize(file, restoreSQLBufferSize),
		targetDatabase:     targetDatabase,
		line:               1,
		lineOnlyWhitespace: true,
	}
	return scanner.scan()
}

type postgresSQLLexState uint8

const (
	postgresLexNormal postgresSQLLexState = iota
	postgresLexSingleQuote
	postgresLexDoubleQuote
	postgresLexLineComment
	postgresLexBlockComment
	postgresLexDollarQuote
)

type postgresSQLSafetyScanner struct {
	reader              *bufio.Reader
	targetDatabase      string
	line                int
	state               postgresSQLLexState
	blockDepth          int
	dollarDelimiter     string
	dollarMatch         int
	escapeString        bool
	lineOnlyWhitespace  bool
	inCopyData          bool
	copyPending         bool
	token               strings.Builder
	statementTokenCount int
	copyStatement       bool
	copySawFrom         bool
	copyFromStdin       bool
}

func (scanner *postgresSQLSafetyScanner) scan() error {
	for {
		if scanner.inCopyData {
			terminator, eof, err := consumePostgresCopyDataLine(scanner.reader, scanner.line)
			if err != nil {
				return err
			}
			if terminator {
				scanner.inCopyData = false
			}
			if eof {
				if !terminator {
					return fmt.Errorf("PostgreSQL SQL backup ended inside COPY FROM stdin data")
				}
				return nil
			}
			scanner.line++
			scanner.lineOnlyWhitespace = true
			continue
		}

		character, err := scanner.reader.ReadByte()
		if errors.Is(err, io.EOF) {
			if err := scanner.flushToken(); err != nil {
				return err
			}
			if scanner.copyPending {
				return fmt.Errorf("PostgreSQL SQL backup ended inside COPY FROM stdin data")
			}
			if scanner.state == postgresLexBlockComment || scanner.state == postgresLexDollarQuote ||
				scanner.state == postgresLexSingleQuote || scanner.state == postgresLexDoubleQuote {
				return fmt.Errorf("PostgreSQL SQL backup ended inside a quoted value or block comment at line %d", scanner.line)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("scan PostgreSQL SQL backup at line %d: %w", scanner.line, err)
		}
		if character == 0 {
			return fmt.Errorf("PostgreSQL SQL backup contains a NUL byte at line %d", scanner.line)
		}

		switch scanner.state {
		case postgresLexSingleQuote:
			if scanner.escapeString && character == '\\' {
				escaped, err := scanner.reader.ReadByte()
				if err != nil {
					return fmt.Errorf("PostgreSQL SQL backup ended inside an escape string at line %d", scanner.line)
				}
				if escaped == 0 {
					return fmt.Errorf("PostgreSQL SQL backup contains a NUL byte at line %d", scanner.line)
				}
				if escaped == '\n' {
					scanner.nextLine(false)
				}
				continue
			}
			if character == '\'' {
				if next, _ := scanner.reader.Peek(1); len(next) == 1 && next[0] == '\'' {
					_, _ = scanner.reader.ReadByte()
					continue
				}
				scanner.state = postgresLexNormal
			} else if character == '\n' {
				scanner.nextLine(false)
			}
		case postgresLexDoubleQuote:
			if character == '"' {
				if next, _ := scanner.reader.Peek(1); len(next) == 1 && next[0] == '"' {
					_, _ = scanner.reader.ReadByte()
					continue
				}
				scanner.state = postgresLexNormal
			} else if character == '\n' {
				scanner.nextLine(false)
			}
		case postgresLexLineComment:
			if character == '\n' {
				scanner.state = postgresLexNormal
				scanner.nextLine(true)
			}
		case postgresLexBlockComment:
			if character == '/' {
				if next, _ := scanner.reader.Peek(1); len(next) == 1 && next[0] == '*' {
					_, _ = scanner.reader.ReadByte()
					scanner.blockDepth++
				}
			} else if character == '*' {
				if next, _ := scanner.reader.Peek(1); len(next) == 1 && next[0] == '/' {
					_, _ = scanner.reader.ReadByte()
					scanner.blockDepth--
					if scanner.blockDepth == 0 {
						scanner.state = postgresLexNormal
					}
				}
			} else if character == '\n' {
				scanner.nextLine(false)
			}
		case postgresLexDollarQuote:
			if character == scanner.dollarDelimiter[scanner.dollarMatch] {
				scanner.dollarMatch++
				if scanner.dollarMatch == len(scanner.dollarDelimiter) {
					scanner.state = postgresLexNormal
					scanner.dollarMatch = 0
				}
			} else if character == '$' {
				scanner.dollarMatch = 1
			} else {
				scanner.dollarMatch = 0
			}
			if character == '\n' {
				scanner.nextLine(false)
			}
		case postgresLexNormal:
			if err := scanner.scanNormal(character); err != nil {
				return err
			}
		}
	}
}

func (scanner *postgresSQLSafetyScanner) scanNormal(character byte) error {
	switch character {
	case ' ', '\t', '\f', '\r':
		return scanner.flushToken()
	case '\n':
		if err := scanner.flushToken(); err != nil {
			return err
		}
		scanner.nextLine(true)
		return nil
	case '\\':
		if err := scanner.flushToken(); err != nil {
			return err
		}
		command, err := readPostgresMetaCommand(scanner.reader)
		if err != nil {
			return fmt.Errorf("scan PostgreSQL psql command at line %d: %w", scanner.line, err)
		}
		command = "\\" + strings.ToLower(command)
		if command == `\c` || command == `\connect` {
			return fmt.Errorf("PostgreSQL SQL restore blocked database-switching psql command %q at line %d; the selected target remains %q", command, scanner.line, scanner.targetDatabase)
		}
		if command != `\restrict` && command != `\unrestrict` {
			return fmt.Errorf("PostgreSQL SQL restore blocked unsafe psql command %q at line %d", command, scanner.line)
		}
		if !scanner.lineOnlyWhitespace {
			return fmt.Errorf("PostgreSQL SQL restore blocked inline psql command %q at line %d", command, scanner.line)
		}
		eof, err := validatePostgresGuardArguments(scanner.reader, scanner.line)
		if err != nil {
			return err
		}
		if eof {
			return nil
		}
		scanner.nextLine(true)
		return nil
	case '\'':
		escape := strings.EqualFold(scanner.token.String(), "E")
		if err := scanner.flushToken(); err != nil {
			return err
		}
		scanner.copyPending = false
		scanner.lineOnlyWhitespace = false
		scanner.escapeString = escape
		scanner.state = postgresLexSingleQuote
		return nil
	case '"':
		if err := scanner.flushToken(); err != nil {
			return err
		}
		scanner.copyPending = false
		scanner.lineOnlyWhitespace = false
		scanner.state = postgresLexDoubleQuote
		return nil
	case '-':
		if next, _ := scanner.reader.Peek(1); len(next) == 1 && next[0] == '-' {
			if err := scanner.flushToken(); err != nil {
				return err
			}
			_, _ = scanner.reader.ReadByte()
			scanner.lineOnlyWhitespace = false
			scanner.state = postgresLexLineComment
			return nil
		}
	case '/':
		if next, _ := scanner.reader.Peek(1); len(next) == 1 && next[0] == '*' {
			if err := scanner.flushToken(); err != nil {
				return err
			}
			_, _ = scanner.reader.ReadByte()
			scanner.lineOnlyWhitespace = false
			scanner.state = postgresLexBlockComment
			scanner.blockDepth = 1
			return nil
		}
	case '$':
		if err := scanner.flushToken(); err != nil {
			return err
		}
		delimiter, tag, ok, err := readPostgresDollarDelimiter(scanner.reader)
		if err != nil {
			return fmt.Errorf("scan PostgreSQL dollar quote at line %d: %w", scanner.line, err)
		}
		if tag != "" {
			scanner.addToken(tag)
		}
		if ok {
			scanner.copyPending = false
			scanner.lineOnlyWhitespace = false
			scanner.dollarDelimiter = delimiter
			scanner.dollarMatch = 0
			scanner.state = postgresLexDollarQuote
		}
		return nil
	case ';':
		if err := scanner.flushToken(); err != nil {
			return err
		}
		scanner.copyPending = scanner.copyStatement && scanner.copyFromStdin
		scanner.resetStatement()
		scanner.lineOnlyWhitespace = false
		return nil
	}

	if isPostgresTokenByte(character) {
		if scanner.token.Len() >= 4096 {
			return fmt.Errorf("PostgreSQL SQL token exceeds 4096 bytes at line %d", scanner.line)
		}
		scanner.token.WriteByte(character)
	} else {
		if err := scanner.flushToken(); err != nil {
			return err
		}
		scanner.copyPending = false
	}
	scanner.lineOnlyWhitespace = false
	return nil
}

func (scanner *postgresSQLSafetyScanner) flushToken() error {
	if scanner.token.Len() == 0 {
		return nil
	}
	token := scanner.token.String()
	scanner.token.Reset()
	scanner.addToken(token)
	return nil
}

func (scanner *postgresSQLSafetyScanner) addToken(value string) {
	upper := strings.ToUpper(value)
	if scanner.copyPending {
		scanner.copyPending = false
	}
	if scanner.statementTokenCount == 0 {
		scanner.copyStatement = upper == "COPY"
	}
	if scanner.copyStatement {
		if scanner.copySawFrom && upper == "STDIN" {
			scanner.copyFromStdin = true
		}
		scanner.copySawFrom = upper == "FROM"
	}
	scanner.statementTokenCount++
}

func (scanner *postgresSQLSafetyScanner) resetStatement() {
	scanner.statementTokenCount = 0
	scanner.copyStatement = false
	scanner.copySawFrom = false
	scanner.copyFromStdin = false
}

func (scanner *postgresSQLSafetyScanner) nextLine(allowCopy bool) {
	scanner.line++
	scanner.lineOnlyWhitespace = scanner.state == postgresLexNormal
	if allowCopy && scanner.copyPending {
		scanner.inCopyData = true
		scanner.copyPending = false
	}
}

func readPostgresMetaCommand(reader *bufio.Reader) (string, error) {
	var command strings.Builder
	for {
		peek, err := reader.Peek(1)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		character := peek[0]
		if character == 0 {
			return "", fmt.Errorf("NUL byte in psql command")
		}
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' || character == '\f' {
			break
		}
		_, _ = reader.ReadByte()
		if command.Len() >= 128 {
			return "", fmt.Errorf("psql command name exceeds 128 bytes")
		}
		command.WriteByte(character)
	}
	if command.Len() == 0 {
		return "", fmt.Errorf("empty psql command")
	}
	return command.String(), nil
}

func validatePostgresGuardArguments(reader *bufio.Reader, line int) (bool, error) {
	var arguments strings.Builder
	for {
		character, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			fields := strings.Fields(arguments.String())
			if len(fields) != 1 {
				return true, fmt.Errorf("PostgreSQL pg_dump guard at line %d must contain exactly one key", line)
			}
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if character == 0 || character == '\\' {
			return false, fmt.Errorf("PostgreSQL pg_dump guard contains an unsafe character at line %d", line)
		}
		if character == '\n' {
			fields := strings.Fields(arguments.String())
			if len(fields) != 1 {
				return false, fmt.Errorf("PostgreSQL pg_dump guard at line %d must contain exactly one key", line)
			}
			return false, nil
		}
		if arguments.Len() >= 4096 {
			return false, fmt.Errorf("PostgreSQL pg_dump guard key exceeds 4096 bytes at line %d", line)
		}
		arguments.WriteByte(character)
	}
}

func readPostgresDollarDelimiter(reader *bufio.Reader) (delimiter, tag string, ok bool, err error) {
	var value strings.Builder
	for {
		character, readErr := reader.ReadByte()
		if errors.Is(readErr, io.EOF) {
			return "", value.String(), false, nil
		}
		if readErr != nil {
			return "", "", false, readErr
		}
		if character == '$' {
			return "$" + value.String() + "$", "", true, nil
		}
		valid := character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			value.Len() > 0 && character >= '0' && character <= '9' || character >= 0x80
		if !valid {
			if err := reader.UnreadByte(); err != nil {
				return "", "", false, err
			}
			return "", value.String(), false, nil
		}
		if value.Len() >= 4096 {
			return "", "", false, fmt.Errorf("dollar quote tag exceeds 4096 bytes")
		}
		value.WriteByte(character)
	}
}

func consumePostgresCopyDataLine(reader *bufio.Reader, line int) (terminator, eof bool, err error) {
	length := 0
	candidate := true
	for {
		character, readErr := reader.ReadByte()
		if errors.Is(readErr, io.EOF) {
			return candidate && length == 2, true, nil
		}
		if readErr != nil {
			return false, false, fmt.Errorf("scan PostgreSQL COPY data at line %d: %w", line, readErr)
		}
		if character == 0 {
			return false, false, fmt.Errorf("PostgreSQL SQL backup contains a NUL byte at line %d", line)
		}
		if character == '\n' {
			return candidate && (length == 2 || length == 3), false, nil
		}
		if length == 0 && character != '\\' || length == 1 && character != '.' || length == 2 && character != '\r' || length > 2 {
			candidate = false
		}
		length++
	}
}

func isPostgresTokenByte(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' || character == '_' || character >= 0x80
}

func validateMySQLSQLForRestore(path, targetDatabase string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open MySQL SQL backup for safety scan: %w", err)
	}
	defer file.Close()
	if err := scanMySQLDirectives(bufio.NewReaderSize(file, restoreSQLBufferSize), targetDatabase); err != nil {
		return fmt.Errorf("MySQL SQL safety scan failed: %w", err)
	}
	return nil
}

func scanMySQLDirectives(reader *bufio.Reader, targetDatabase string) error {
	var tokens []string
	var token strings.Builder
	line := 1
	flushToken := func() {
		if token.Len() == 0 {
			return
		}
		if len(tokens) < 16 {
			tokens = append(tokens, token.String())
		}
		token.Reset()
	}
	finishStatement := func() error {
		flushToken()
		_, err := validateMySQLDirectiveTokens(tokens, targetDatabase, line)
		tokens = tokens[:0]
		return err
	}

	for {
		value, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			return finishStatement()
		}
		if err != nil {
			return err
		}
		if value == 0 {
			return fmt.Errorf("NUL byte at line %d", line)
		}
		switch value {
		case '\n':
			flushToken()
			if recognized, err := validateMySQLDirectiveTokens(tokens, targetDatabase, line); err != nil {
				return err
			} else if recognized || firstTokenEqual(tokens, "delimiter") {
				tokens = tokens[:0]
			}
			line++
		case ';':
			if err := finishStatement(); err != nil {
				return err
			}
		case '\'', '"', '`':
			flushToken()
			quoted, err := readMySQLQuoted(reader, value, &line)
			if err != nil {
				return err
			}
			if len(tokens) < 16 {
				tokens = append(tokens, quoted)
			}
		case '#':
			flushToken()
			if err := skipMySQLLineComment(reader, &line); err != nil {
				return err
			}
			if recognized, err := validateMySQLDirectiveTokens(tokens, targetDatabase, line-1); err != nil {
				return err
			} else if recognized || firstTokenEqual(tokens, "delimiter") {
				tokens = tokens[:0]
			}
		case '-':
			peek, _ := reader.Peek(1)
			if len(peek) == 1 && peek[0] == '-' {
				_, _ = reader.ReadByte()
				flushToken()
				if err := skipMySQLLineComment(reader, &line); err != nil {
					return err
				}
				if recognized, err := validateMySQLDirectiveTokens(tokens, targetDatabase, line-1); err != nil {
					return err
				} else if recognized || firstTokenEqual(tokens, "delimiter") {
					tokens = tokens[:0]
				}
			} else {
				flushToken()
			}
		case '/':
			peek, _ := reader.Peek(1)
			if len(peek) == 1 && peek[0] == '*' {
				_, _ = reader.ReadByte()
				flushToken()
				executable := false
				if marker, _ := reader.Peek(1); len(marker) == 1 && marker[0] == '!' {
					_, _ = reader.ReadByte()
					executable = true
				} else if marker, _ := reader.Peek(2); len(marker) == 2 && (marker[0] == 'M' || marker[0] == 'm') && marker[1] == '!' {
					_, _ = reader.Discard(2)
					executable = true
				}
				comment, err := readMySQLBlockComment(reader, executable, &line)
				if err != nil {
					return err
				}
				if executable {
					comment = bytes.TrimLeftFunc(comment, func(character rune) bool {
						return unicode.IsSpace(character) || unicode.IsDigit(character)
					})
					if strings.EqualFold(strings.TrimSpace(string(comment)), `\- enable the sandbox mode`) {
						break
					}
					if err := scanMySQLDirectives(bufio.NewReader(bytes.NewReader(comment)), targetDatabase); err != nil {
						return fmt.Errorf("unsafe executable MySQL comment: %w", err)
					}
				}
			} else {
				flushToken()
			}
		case '\\':
			flushToken()
			return fmt.Errorf("local mysql client command is not allowed at line %d", line)
		default:
			if isMySQLTokenByte(value) {
				if token.Len() < 4096 {
					token.WriteByte(value)
				}
			} else {
				flushToken()
			}
		}
	}
}

func validateMySQLDirectiveTokens(tokens []string, targetDatabase string, line int) (bool, error) {
	if len(tokens) == 0 {
		return false, nil
	}
	if mysqlLocalCommand(tokens[0]) {
		return true, fmt.Errorf("local %s client command is not allowed at line %d", strings.ToUpper(tokens[0]), line)
	}
	if strings.EqualFold(tokens[0], "use") {
		if len(tokens) < 2 || strings.TrimSpace(tokens[1]) == "" {
			return true, fmt.Errorf("malformed USE directive at line %d", line)
		}
		if !strings.EqualFold(tokens[1], targetDatabase) {
			return true, fmt.Errorf("USE directive targets database %q at line %d, not selected target %q", tokens[1], line, targetDatabase)
		}
		return true, nil
	}
	if len(tokens) < 2 || (!strings.EqualFold(tokens[1], "database") && !strings.EqualFold(tokens[1], "schema")) {
		return false, nil
	}
	verb := strings.ToLower(tokens[0])
	if verb != "create" && verb != "drop" && verb != "alter" {
		return false, nil
	}
	index := 2
	if verb == "create" && index+2 < len(tokens) && strings.EqualFold(tokens[index], "if") && strings.EqualFold(tokens[index+1], "not") && strings.EqualFold(tokens[index+2], "exists") {
		index += 3
	} else if verb == "drop" && index+1 < len(tokens) && strings.EqualFold(tokens[index], "if") && strings.EqualFold(tokens[index+1], "exists") {
		index += 2
	}
	if index >= len(tokens) || strings.TrimSpace(tokens[index]) == "" {
		return true, fmt.Errorf("malformed %s DATABASE directive at line %d", strings.ToUpper(verb), line)
	}
	if verb == "drop" {
		return true, fmt.Errorf("DROP DATABASE directive for %q is not allowed at line %d; dbterm never drops the target database", tokens[index], line)
	}
	if !strings.EqualFold(tokens[index], targetDatabase) {
		return true, fmt.Errorf("%s DATABASE directive targets %q at line %d, not selected target %q", strings.ToUpper(verb), tokens[index], line, targetDatabase)
	}
	return true, nil
}

func mysqlLocalCommand(token string) bool {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "source", "system", "connect", "edit", "pager", "tee":
		return true
	default:
		return false
	}
}

func firstTokenEqual(tokens []string, expected string) bool {
	return len(tokens) > 0 && strings.EqualFold(tokens[0], expected)
}

func isMySQLTokenByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' ||
		value == '_' || value == '$' || value == '.' || value == '-'
}

func readMySQLQuoted(reader *bufio.Reader, quote byte, line *int) (string, error) {
	var value strings.Builder
	for {
		character, err := reader.ReadByte()
		if err != nil {
			return "", fmt.Errorf("unterminated quoted value at line %d", *line)
		}
		if character == 0 {
			return "", fmt.Errorf("NUL byte at line %d", *line)
		}
		if character == '\n' {
			*line++
		}
		if character == '\\' {
			escaped, err := reader.ReadByte()
			if err != nil {
				return "", fmt.Errorf("unterminated escape at line %d", *line)
			}
			if escaped == '\n' {
				*line++
			}
			if value.Len() < 4096 {
				value.WriteByte(escaped)
			}
			continue
		}
		if character == quote {
			peek, _ := reader.Peek(1)
			if len(peek) == 1 && peek[0] == quote {
				_, _ = reader.ReadByte()
				if value.Len() < 4096 {
					value.WriteByte(quote)
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

func skipMySQLLineComment(reader *bufio.Reader, line *int) error {
	for {
		character, err := reader.ReadByte()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if character == '\n' {
			*line++
			return nil
		}
	}
}

func readMySQLBlockComment(reader *bufio.Reader, capture bool, line *int) ([]byte, error) {
	var content []byte
	previous := byte(0)
	for {
		character, err := reader.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("unterminated block comment at line %d", *line)
		}
		if character == '\n' {
			*line++
		}
		if previous == '*' && character == '/' {
			if capture && len(content) > 0 {
				content = content[:len(content)-1]
			}
			return content, nil
		}
		if capture {
			if len(content) >= maxExecutableCommentBytes {
				return nil, fmt.Errorf("executable MySQL comment exceeds %d bytes", maxExecutableCommentBytes)
			}
			content = append(content, character)
		}
		previous = character
	}
}
