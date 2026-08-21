// Package docs exposes dbterm's user documentation to the offline TUI.
package docs

import _ "embed"

// UserGuideMarkdown is the canonical full user guide used by both the website
// and the in-app Guide & SQL Reference.
//
//go:embed user-guide.md
var UserGuideMarkdown string

// BackupMarkdown is the complete field-by-field Backup Center handbook.
//
//go:embed backup.md
var BackupMarkdown string

// MCPMarkdown is the complete local agent/MCP safety and tool reference.
//
//go:embed mcp.md
var MCPMarkdown string

// ChangeProfilerMarkdown describes the portable Change Profiler model and its
// deliberate attribution limits.
//
//go:embed change-profiler.md
var ChangeProfilerMarkdown string
