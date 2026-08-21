package ui

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/rivo/tview"
	projectdocs "github.com/shreyam1008/dbterm/docs"
)

type guideSection struct {
	title   string
	summary string
	body    string
}

var markdownLinkPattern = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
var markdownOrderedListPattern = regexp.MustCompile(`^(\s*)([0-9]+)\.\s+(.+)$`)

func manualGuideSections(a *App) []guideSection {
	sections := []guideSection{{
		title:   "Keyboard & workflows",
		summary: "Effective shortcuts for this installation",
		body:    keyboardHelpTextFor(a),
	}}
	for _, section := range parseGuideMarkdown(projectdocs.UserGuideMarkdown) {
		sections = append(sections, guideSection{
			title:   section.title,
			summary: guideSectionSummary(section.title),
			body:    renderGuideMarkdownForApp(a, section.body),
		})
	}
	sections = append(sections,
		guideSection{
			title:   "Backup Center · full handbook",
			summary: "Every field, agent mode, restore guard, path, and check",
			body:    renderGuideMarkdownForApp(a, markdownDocumentBody(projectdocs.BackupMarkdown)),
		},
		guideSection{
			title:   "Agent/MCP · full handbook",
			summary: "Setup, tools, scope, profile writes, and SQL safety",
			body:    renderGuideMarkdownForApp(a, markdownDocumentBody(projectdocs.MCPMarkdown)),
		},
		guideSection{
			title:   "Change Profiler · technical reference",
			summary: "Portable exact mode and why database logs are opt-in",
			body:    renderGuideMarkdownForApp(a, markdownDocumentBody(projectdocs.ChangeProfilerMarkdown)),
		},
	)
	return sections
}

func renderGuideMarkdownForApp(a *App, markdown string) string {
	return renderGuideMarkdown(substituteGuideShortcuts(a, markdown))
}

func substituteGuideShortcuts(a *App, markdown string) string {
	const completionPrevious = "{{dbterm-guide-completion-previous}}"
	markdown = strings.ReplaceAll(markdown, "`Ctrl+P`/`Ctrl+N`", completionPrevious)

	replacements := []struct {
		defaultShortcut string
		action          keymapAction
	}{
		{"Alt+T", actionFocusTables},
		{"Alt+Q", actionFocusQuery},
		{"Alt+R", actionFocusResults},
		{"Alt+D", actionDashboard},
		{"Alt+H", actionHelp},
		{"Alt+S", actionServices},
		{"Alt+F", actionFullscreen},
		{"Alt+B", actionBackup},
		{"Alt+K", actionBackupCenter},
		{"Alt+W", actionChangeProfiler},
		{"Alt+E", actionExportCSV},
		{"Alt+Y", actionHistory},
		{"Alt+,", actionSettings},
		{"Alt+G", actionSettings},
		{"Alt+I", actionImportDump},
		{"Alt+M", actionInspectSchema},
		{"Alt+A", actionSelectAll},
		{"Alt+C", actionClearSelection},
		{"Ctrl+P", actionCommandPalette},
	}
	settingsToken := "{{dbterm-guide-shortcut-settings}}"
	markdown = strings.ReplaceAll(markdown, "`Alt+,`, or `Alt+G`", settingsToken)
	markdown = strings.ReplaceAll(markdown, "`Alt+,` / `Alt+G`", settingsToken)
	for index, replacement := range replacements {
		token := "{{dbterm-guide-shortcut-" + strconv.Itoa(index) + "}}"
		markdown = strings.ReplaceAll(markdown, "`"+replacement.defaultShortcut+"`", token)
	}
	for index, replacement := range replacements {
		token := "{{dbterm-guide-shortcut-" + strconv.Itoa(index) + "}}"
		markdown = strings.ReplaceAll(markdown, token, "`"+a.effectiveActionShortcut(replacement.action)+"`")
	}
	markdown = strings.ReplaceAll(markdown, settingsToken, "`"+a.effectiveActionShortcut(actionSettings)+"`")

	markdown = strings.ReplaceAll(markdown, completionPrevious, "`Ctrl+P`/`Ctrl+N`")
	markdown = strings.ReplaceAll(markdown, "Default configurable actions:", "Effective configurable actions:")
	markdown = strings.ReplaceAll(markdown, "| Default | Action |", "| Effective | Action |")
	return markdown
}

func markdownDocumentBody(markdown string) string {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func parseGuideMarkdown(markdown string) []guideSection {
	var sections []guideSection
	var current *guideSection

	flush := func() {
		if current == nil {
			return
		}
		current.body = strings.TrimSpace(current.body)
		if current.title != "" && current.body != "" {
			sections = append(sections, *current)
		}
	}

	for _, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "## ") {
			flush()
			current = &guideSection{title: strings.TrimSpace(strings.TrimPrefix(line, "## "))}
			continue
		}
		if current == nil {
			continue
		}
		current.body += line + "\n"
	}
	flush()
	return sections
}

func renderGuideMarkdown(markdown string) string {
	var rendered strings.Builder
	inCodeBlock := false
	lines := strings.Split(strings.TrimSpace(markdown), "\n")

	for lineIndex := 0; lineIndex < len(lines); lineIndex++ {
		rawLine := lines[lineIndex]
		line := strings.TrimRight(rawLine, " \t\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			if !inCodeBlock {
				rendered.WriteByte('\n')
			}
			continue
		}

		if inCodeBlock {
			rendered.WriteString("[#a6e3a1]  ")
			rendered.WriteString(tview.Escape(line))
			rendered.WriteString("[-]\n")
			continue
		}

		if lineIndex+1 < len(lines) && isMarkdownTableSeparator(lines[lineIndex+1]) {
			headers := markdownTableCells(line)
			var rows [][]string
			nextLine := lineIndex + 2
			for nextLine < len(lines) && isMarkdownTableRow(lines[nextLine]) {
				rows = append(rows, markdownTableCells(lines[nextLine]))
				nextLine++
			}
			if len(headers) > 0 && len(rows) > 0 {
				renderGuideTable(&rendered, headers, rows)
				lineIndex = nextLine - 1
				continue
			}
		}

		switch {
		case strings.HasPrefix(line, "## "):
			rendered.WriteString("\n[::b][#cba6f7]")
			rendered.WriteString(renderGuideInline(strings.TrimPrefix(line, "## ")))
			rendered.WriteString("[-][-]\n")
		case strings.HasPrefix(line, "### "):
			rendered.WriteString("\n[::b][#89b4fa]")
			rendered.WriteString(renderGuideInline(strings.TrimPrefix(line, "### ")))
			rendered.WriteString("[-][-]\n")
		case strings.HasPrefix(line, "#### "):
			rendered.WriteString("\n[::b][#a6e3a1]")
			rendered.WriteString(renderGuideInline(strings.TrimPrefix(line, "#### ")))
			rendered.WriteString("[-][-]\n")
		case strings.HasPrefix(line, "- "):
			rendered.WriteString("  [#cba6f7]•[-] ")
			rendered.WriteString(renderGuideInline(strings.TrimPrefix(line, "- ")))
			rendered.WriteByte('\n')
		case strings.HasPrefix(line, "> "):
			rendered.WriteString("[#f9e2af]  ")
			rendered.WriteString(renderGuideInline(strings.TrimPrefix(line, "> ")))
			rendered.WriteString("[-]\n")
		case markdownOrderedListPattern.MatchString(line):
			parts := markdownOrderedListPattern.FindStringSubmatch(line)
			rendered.WriteString(parts[1])
			rendered.WriteString("[#cba6f7]")
			rendered.WriteString(parts[2])
			rendered.WriteString(".[-] ")
			rendered.WriteString(renderGuideInline(parts[3]))
			rendered.WriteByte('\n')
		default:
			rendered.WriteString(renderGuideInline(line))
			rendered.WriteByte('\n')
		}
	}

	return strings.TrimSpace(rendered.String())
}

func isMarkdownTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return len(trimmed) >= 2 && strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
}

func markdownTableCells(line string) []string {
	if !isMarkdownTableRow(line) {
		return nil
	}
	trimmed := strings.TrimSpace(line)
	rawCells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(trimmed, "|"), "|"), "|")
	cells := make([]string, len(rawCells))
	for index, cell := range rawCells {
		cells[index] = strings.TrimSpace(cell)
	}
	return cells
}

func isMarkdownTableSeparator(line string) bool {
	cells := markdownTableCells(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.Trim(strings.TrimSpace(cell), ":")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func renderGuideTable(rendered *strings.Builder, headers []string, rows [][]string) {
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		rendered.WriteString("  [::b][#89b4fa]")
		rendered.WriteString(renderGuideInline(headers[0]))
		rendered.WriteString(":[-][-] ")
		rendered.WriteString(renderGuideInline(row[0]))
		rendered.WriteByte('\n')
		for column := 1; column < len(headers) && column < len(row); column++ {
			rendered.WriteString("    [#a6adc8]")
			rendered.WriteString(renderGuideInline(headers[column]))
			rendered.WriteString(":[-] ")
			rendered.WriteString(renderGuideInline(row[column]))
			rendered.WriteByte('\n')
		}
		rendered.WriteByte('\n')
	}
}

func renderGuideInline(text string) string {
	text = markdownLinkPattern.ReplaceAllString(text, "$1 ($2)")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	text = strings.ReplaceAll(text, "`", "")
	return tview.Escape(text)
}

func guideSectionSummary(title string) string {
	summaries := map[string]string{
		"Start here":                                     "Five-minute setup and capability matrix",
		"Install, verify, update, and uninstall":         "Every supported installation lifecycle",
		"Create and manage connections":                  "PostgreSQL, MySQL, SQLite, Turso, and D1",
		"Dashboard and database discovery":               "Profiles, health checks, and server databases",
		"Navigate the workspace and command palette":     "Tables, Query, Results, and global search",
		"Browse tables, columns, and database objects":   "Schema tree, pins, metadata, and definitions",
		"Write SQL, use autocomplete, and query history": "Execution, local suggestions, and cancellation",
		"Work with result rows and columns":              "Filters, relationships, sorting, paging, and size",
		"Import SQL and export CSV":                      "Supported formats, streaming, and cancellation",
		"Compare changes with Change Profiler":           "Anchors, scans, reports, and attribution limits",
		"Operate local PostgreSQL and MySQL services":    "Status, start/stop, and connect workflows",
		"Back up and restore databases":                  "Instant backups, plans, agents, and restore",
		"Configure Settings and shortcuts":               "Keymap, health checks, and agent permissions",
		"Connect trusted AI agents through MCP":          "Local stdio tools, limits, scope, and safety",
		"CLI reference":                                  "Top-level, backup, restore, service, and MCP CLI",
		"Files, profiles, and recovery":                  "OS paths, mirrors, ownership, and sudo recovery",
		"Troubleshooting":                                "Symptom-based operational recovery",
		"Security, privacy, and deliberate limits":       "Credential boundaries and explicit non-goals",
		"Develop, contribute, and get help":              "Source, tests, issues, and architecture",
	}
	if summary := summaries[title]; summary != "" {
		return summary
	}
	return "Complete dbterm documentation"
}
