package ui

// Shared UI glyphs/icons used across views.
//
// Keep these printable ASCII and exactly one terminal cell wide. Emoji width is
// not consistent across terminal emulators (and often depends on the selected
// font), which can make borders, titles, and status bars drift out of alignment.
const (
	iconDashboard = "D"
	iconConnect   = "@"
	iconHelp      = "?"
	iconServices  = "*"
	iconDatabase  = "#"
	iconTables    = "T"
	iconQuery     = ">"
	iconResults   = "="
	iconBackup    = "B"
	iconPin       = "+"

	iconBack     = "<"
	iconRefresh  = "~"
	iconDropdown = "v"
	iconInfo     = "i"
	iconWarn     = "!"
	iconSuccess  = "+"
	iconFail     = "x"
)
