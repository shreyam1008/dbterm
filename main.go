package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shreyam1008/dbterm/ui"
)

// Build-time variables (set via -ldflags)
var (
	version = "1.0.0"
	commit  = "dev"
)

func main() {
	if len(os.Args) > 1 {
		switch strings.ToLower(os.Args[1]) {
		case "--help", "-h", "help":
			printHelp()
			return
		case "--version", "-v", "version":
			printVersion()
			return
		case "--info", "-i", "info":
			printInfo()
			return
		case "--uninstall", "uninstall":
			printUninstall()
			return
		default:
			fmt.Printf("Unknown command: %s\n\n", os.Args[1])
			printHelp()
			os.Exit(1)
		}
	}

	// ── Startup Banner ──
	fmt.Println()
	fmt.Println("  \033[38;2;203;166;247m╔══════════════════════════════════╗\033[0m")
	fmt.Println("  \033[38;2;203;166;247m║\033[0m  \033[1;38;2;203;166;247mdbterm\033[0m v" + version + "                  \033[38;2;203;166;247m║\033[0m")
	fmt.Println("  \033[38;2;203;166;247m║\033[0m  \033[38;2;166;173;200mMulti-database terminal client\033[0m  \033[38;2;203;166;247m║\033[0m")
	fmt.Println("  \033[38;2;203;166;247m╚══════════════════════════════════╝\033[0m")
	fmt.Println()
	fmt.Printf("  \033[38;2;137;180;250m⬢\033[0m PostgreSQL   \033[38;2;249;226;175m⬡\033[0m MySQL   \033[38;2;166;227;161m◆\033[0m SQLite\n")
	fmt.Printf("  \033[38;2;108;112;134mConfig: %s\033[0m\n", configPath())
	fmt.Println()
	fmt.Println("  \033[38;2;166;227;161mStarting...\033[0m Press \033[33mAlt+H\033[0m for help inside the app.")
	fmt.Println()

	app := ui.NewApp()
	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "\033[31mFatal: %s\033[0m\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println(`
  ` + "\033[1;38;2;203;166;247m" + `dbterm` + "\033[0m" + ` — Multi-database terminal client

  ` + "\033[33m" + `USAGE` + "\033[0m" + `
    dbterm                   Launch the TUI
    dbterm --help, -h        Show this help
    dbterm --version, -v     Show version
    dbterm --info, -i        Show config, storage, and system info
    dbterm --uninstall       Show uninstall instructions

  ` + "\033[33m" + `SUPPORTED DATABASES` + "\033[0m" + `
    ⬢ PostgreSQL    ⬡ MySQL    ◆ SQLite

  ` + "\033[33m" + `QUICK START` + "\033[0m" + `
    1. Run ` + "\033[32m" + `dbterm` + "\033[0m" + `
    2. Press ` + "\033[32m" + `N` + "\033[0m" + ` to add a new database connection
    3. Choose your DB type, fill in details, and connect
    4. Press ` + "\033[33m" + `Alt+H` + "\033[0m" + ` inside the app for keyboard shortcuts & SQL cheatsheets

  ` + "\033[33m" + `KEY BINDINGS (inside TUI)` + "\033[0m" + `
    Alt+Q ............ Focus Query editor
    Alt+R ............ Focus Results view
    Alt+T ............ Focus Tables list
    Alt+Enter ........ Execute query
    Alt+H ............ Help & cheatsheets
    Alt+D ............ Back to dashboard
    Ctrl+C ........... Quit

  ` + "\033[38;2;108;112;134m" + `Repository: https://github.com/shreyam1008/dbterm
  Based on pgterm by @nabsk911` + "\033[0m" + `
`)
}

func printVersion() {
	fmt.Printf("dbterm v%s (%s)\n", version, commit)
	fmt.Printf("Go %s on %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func printInfo() {
	cfgDir := configDir()
	cfgFile := filepath.Join(cfgDir, "connections.json")

	// Check config size
	cfgSize := "not created yet"
	if info, err := os.Stat(cfgFile); err == nil {
		cfgSize = formatBytes(info.Size())
	}

	// Binary size
	binSize := "unknown"
	if ex, err := os.Executable(); err == nil {
		if info, err := os.Stat(ex); err == nil {
			binSize = formatBytes(info.Size())
		}
	}

	fmt.Println(`
  ` + "\033[1;38;2;203;166;247m" + `dbterm` + "\033[0m" + ` — System Information
`)
	fmt.Printf("  \033[33mVersion\033[0m          %s (%s)\n", version, commit)
	fmt.Printf("  \033[33mGo\033[0m               %s\n", runtime.Version())
	fmt.Printf("  \033[33mOS / Arch\033[0m        %s / %s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println()
	fmt.Println("  \033[33mSTORAGE\033[0m")
	fmt.Printf("  Binary size      %s\n", binSize)
	fmt.Printf("  Config dir       %s\n", cfgDir)
	fmt.Printf("  Config file      %s\n", cfgFile)
	fmt.Printf("  Config size      %s\n", cfgSize)
	fmt.Println()
	fmt.Println("  \033[33mRESOURCE USAGE\033[0m")
	fmt.Println("  RAM (idle)       ~8–12 MB")
	fmt.Println("  RAM (active)     ~15–30 MB (depends on query results)")
	fmt.Println("  CPU              Minimal (event-driven TUI, near-zero idle)")
	fmt.Println("  Network          Only when connected to remote DB")
	fmt.Println()
	fmt.Println("  \033[33mSUPPORTED DRIVERS\033[0m")
	fmt.Println("  PostgreSQL       lib/pq (pure Go)")
	fmt.Println("  MySQL            go-sql-driver/mysql (pure Go)")
	fmt.Println("  SQLite           modernc.org/sqlite (pure Go, no CGO)")
	fmt.Println()
	fmt.Printf("  \033[38;2;108;112;134mRun \033[0mdbterm --uninstall\033[38;2;108;112;134m for removal instructions.\033[0m\n\n")
}

func printUninstall() {
	cfgDir := configDir()

	ex := "$(which dbterm)"
	if path, err := os.Executable(); err == nil {
		ex = path
	}

	fmt.Println(`
  ` + "\033[1;38;2;203;166;247m" + `dbterm` + "\033[0m" + ` — Uninstall Instructions
`)
	fmt.Println("  \033[33m1. Remove the binary\033[0m")
	fmt.Printf("     rm %s\n\n", ex)
	fmt.Println("  \033[33m2. Remove saved connections (optional)\033[0m")
	fmt.Printf("     rm -rf %s\n\n", cfgDir)
	fmt.Println("  \033[33m3. Remove Go module cache (optional)\033[0m")
	fmt.Println("     go clean -modcache")
	fmt.Println()
	fmt.Println("  \033[38;2;108;112;134mThat's it! dbterm stores nothing else on your system.\033[0m")
	fmt.Println()
}

func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "~/.config/dbterm"
	}
	return filepath.Join(home, ".config", "dbterm")
}

func configPath() string {
	return filepath.Join(configDir(), "connections.json")
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
