package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shreyam1008/dbterm/ui"
)

var (
	version = "1.0.0"
	commit  = "dev"
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
		case strings.HasPrefix(arg, "--unin") || strings.HasPrefix(arg, "unin") || arg == "remove":
			// Catches: --uninstall, --unintall, uninstall, uninst, remove, etc.
			printUninstall()
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
	fmt.Printf("  \033[38;2;203;166;247m║\033[0m  \033[1;38;2;203;166;247mdbterm\033[0m %-25s \033[38;2;203;166;247m║\033[0m\n", "v"+version)
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
    dbterm --uninstall        How to remove dbterm

  ` + "\033[33m" + `DATABASES` + "\033[0m" + `
    ⬢ PostgreSQL    ⬡ MySQL    ◆ SQLite

  ` + "\033[33m" + `QUICK START` + "\033[0m" + `
    1. Run ` + "\033[32m" + `dbterm` + "\033[0m" + `
    2. Press ` + "\033[32m" + `N` + "\033[0m" + ` → add a new database connection
    3. Fill in details → Save & Connect
    4. Press ` + "\033[33m" + `Alt+H` + "\033[0m" + ` for SQL cheatsheets per DB

  ` + "\033[33m" + `KEY BINDINGS` + "\033[0m" + `
    Alt+Q  Query editor    Alt+T  Tables list
    Alt+R  Results view    Alt+H  Help panel
    Alt+D  Dashboard       Alt+Enter  Execute
    Ctrl+C Quit

  ` + "\033[38;2;108;112;134m" + `https://github.com/shreyam1008/dbterm
  Based on pgterm by @nabsk911` + "\033[0m" + `
`)
}

func printVersion() {
	fmt.Printf("dbterm v%s (%s)\n", version, commit)
	fmt.Printf("Go %s, %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func printInfo() {
	cfgDir := configDir()
	cfgFile := filepath.Join(cfgDir, "connections.json")

	cfgSize := "not created yet"
	if info, err := os.Stat(cfgFile); err == nil {
		cfgSize = fmtBytes(info.Size())
	}

	binSize := "unknown"
	binPath := "unknown"
	if ex, err := os.Executable(); err == nil {
		binPath = ex
		if info, err := os.Stat(ex); err == nil {
			binSize = fmtBytes(info.Size())
		}
	}

	fmt.Print(`
  ` + "\033[1;38;2;203;166;247m" + `dbterm` + "\033[0m" + ` — System Info
`)
	fmt.Printf("  \033[33mVersion\033[0m       %s (%s)\n", version, commit)
	fmt.Printf("  \033[33mGo\033[0m            %s\n", runtime.Version())
	fmt.Printf("  \033[33mOS / Arch\033[0m     %s / %s\n\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("  \033[33mPATHS\033[0m")
	fmt.Printf("  Binary        %s (%s)\n", binPath, binSize)
	fmt.Printf("  Config        %s (%s)\n\n", cfgFile, cfgSize)
	fmt.Println("  \033[33mRESOURCES\033[0m")
	fmt.Println("  RAM (idle)    ~8–12 MB")
	fmt.Println("  RAM (active)  ~15–30 MB (scales with result set)")
	fmt.Println("  CPU           Near-zero (event-driven TUI)")
	fmt.Println("  Disk          Binary only + tiny JSON config")
	fmt.Println("  Network       Only when connected to remote DB")
	fmt.Println()
	fmt.Println("  \033[33mDRIVERS\033[0m       All pure Go — no CGO, no C deps")
	fmt.Println("  PostgreSQL    lib/pq")
	fmt.Println("  MySQL         go-sql-driver/mysql")
	fmt.Println("  SQLite        modernc.org/sqlite")
	fmt.Println()
	fmt.Println("  \033[33mINSTALL\033[0m       No Go required")
	fmt.Println("  macOS/Linux   curl -fsSL https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.sh | bash")
	fmt.Println("  Windows       powershell -NoProfile -ExecutionPolicy Bypass -Command \"irm https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.ps1 | iex\"")
	fmt.Println("  \033[33mREMOVE\033[0m        dbterm --uninstall")
	fmt.Println()
}

func printUninstall() {
	cfgDir := configDir()

	binPath := "$(which dbterm)"
	if path, err := os.Executable(); err == nil {
		binPath = path
	}

	fmt.Print(`
  ` + "\033[1;38;2;203;166;247m" + `dbterm` + "\033[0m" + ` — Uninstall
`)
	if runtime.GOOS == "windows" {
		fmt.Println("\n  \033[33mStep 1:\033[0m Remove the binary")
		fmt.Printf("  PS> Remove-Item -Force \"%s\"\n", binPath)
		fmt.Println("\n  \033[33mStep 2:\033[0m Remove saved connections (optional)")
		fmt.Printf("  PS> Remove-Item -Recurse -Force \"%s\"\n", cfgDir)
		fmt.Println("\n  \033[33mStep 3:\033[0m Remove dbterm from user PATH (optional)")
		fmt.Println("  PS> $p=[Environment]::GetEnvironmentVariable(\"Path\",\"User\")")
		fmt.Println("  PS> [Environment]::SetEnvironmentVariable(\"Path\",(($p -split ';' | ? { $_ -ne \"$env:LOCALAPPDATA\\dbterm\\bin\" }) -join ';'),\"User\")")
	} else {
		fmt.Println("\n  \033[33mStep 1:\033[0m Remove the binary")
		fmt.Printf("  $ rm %s\n", binPath)
		fmt.Println("\n  \033[33mStep 2:\033[0m Remove saved connections (optional)")
		fmt.Printf("  $ rm -rf %s\n", cfgDir)
		fmt.Println("\n  \033[33mStep 3:\033[0m Clean Go cache (optional, frees ~50MB)")
		fmt.Println("  $ go clean -modcache")
	}
	fmt.Println("\n  \033[38;2;166;227;161m✓\033[0m That's everything. dbterm stores nothing else.")
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

func fmtBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
