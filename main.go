package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/shreyam1008/dbterm/ui"
)

func main() {
	if len(os.Args) > 1 {
		arg := strings.ToLower(strings.TrimSpace(os.Args[1]))

		// Normalize common typos and variations
		switch {
		case arg == "--help" || arg == "-h" || arg == "help":
			printHelp()
		case arg == "--version" || arg == "-v" || arg == "version":
			printVersion()
		case arg == "--info" || arg == "-i" || arg == "info":
			printInfo()
		case arg == "--update" || arg == "-u" || arg == "update":
			requestedVersion := ""
			if len(os.Args) > 2 {
				requestedVersion = strings.TrimSpace(os.Args[2])
			}
			if err := runUpdate(requestedVersion); err != nil {
				fmt.Fprintf(os.Stderr, "\n  \033[31mUpdate failed:\033[0m %s\n\n", err)
				os.Exit(1)
			}
		case strings.HasPrefix(arg, "--unin") || strings.HasPrefix(arg, "unin") || arg == "remove":
			// Catches: --uninstall, --unintall, uninstall, uninst, remove, etc.
			purge := hasFlag(os.Args[2:], "--purge", "-p", "purge")
			assumeYes := hasFlag(os.Args[2:], "--yes", "-y", "--force", "force")
			if err := runUninstall(purge, assumeYes); err != nil {
				fmt.Fprintf(os.Stderr, "\n  \033[31mUninstall failed:\033[0m %s\n\n", err)
				os.Exit(1)
			}
		default:
			fmt.Printf("\033[31mUnknown command:\033[0m %s\n\n", os.Args[1])
			printHelp()
			os.Exit(1)
		}
		return
	}

	// ── Startup Banner ──
	fmt.Println()
	fmt.Println("  \033[38;2;203;166;247m╔══════════════════════════════════╗\033[0m")
	fmt.Printf("  \033[38;2;203;166;247m║\033[0m  \033[1;38;2;203;166;247mdbterm\033[0m %-25s \033[38;2;203;166;247m║\033[0m\n", "v"+buildVersion())
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
		fmt.Fprintf(os.Stderr, "\n  \033[31mFatal error:\033[0m %s\n\n", err)
		fmt.Fprintln(os.Stderr, "  If this keeps happening, try:")
		fmt.Fprintln(os.Stderr, "    1. dbterm --info   (check system info)")
		fmt.Fprintln(os.Stderr, "    2. Report at https://github.com/shreyam1008/dbterm/issues")
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`
  ` + "\033[1;38;2;203;166;247m" + `dbterm` + "\033[0m" + ` — Multi-database terminal client

  ` + "\033[33m" + `USAGE` + "\033[0m" + `
    dbterm                   Launch the TUI
    dbterm --help             Show this help
    dbterm --version          Show version info
    dbterm --info             Config, storage & system info
    dbterm --update           Update to latest release
    dbterm --update 0.3.4     Update to a specific version
    dbterm --uninstall        Uninstall dbterm binary
    dbterm --uninstall --yes  Uninstall without confirmation prompt
    dbterm --uninstall --purge Uninstall binary + saved connections

  ` + "\033[33m" + `DATABASES` + "\033[0m" + `
    ⬢ PostgreSQL    ⬡ MySQL    ◆ SQLite

  ` + "\033[33m" + `QUICK START` + "\033[0m" + `
    1. Run ` + "\033[32m" + `dbterm` + "\033[0m" + `
    2. Press ` + "\033[32m" + `N` + "\033[0m" + ` → add a new database connection
    3. Fill in details → Save & Connect
    4. Press ` + "\033[33m" + `Alt+H` + "\033[0m" + ` for SQL cheatsheets per DB

  ` + "\033[33m" + `KEY BINDINGS` + "\033[0m" + `
    Alt+Q/T/R  Focus Query/Tables/Results
    Enter      Execute query          F5  Refresh table
    Ctrl+F5    Full refresh           F/B Toggle Fullscreen/Backup
    Ctrl+/-/0  Zoom table columns     +/- Column width
    Alt+H      Help panel             Alt+D  Dashboard
    Ctrl+C     Quit

  ` + "\033[38;2;108;112;134m" + `Docs: https://shreyam1008.github.io/dbterm/
  Open source: https://shreyam1008.github.io/dbterm/open-source/
  Package docs: https://pkg.go.dev/github.com/shreyam1008/dbterm
  Source: https://github.com/shreyam1008/dbterm
  Inspired by pgterm by @nabsk911` + "\033[0m" + `
`)
}

// hasFlag checks if any of the given flags appear in the argument list.
func hasFlag(args []string, flags ...string) bool {
	for _, a := range args {
		normalized := strings.ToLower(strings.TrimSpace(a))
		for _, f := range flags {
			if normalized == f {
				return true
			}
		}
	}
	return false
}
