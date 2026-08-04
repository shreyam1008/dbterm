package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/appdirs"
)

var (
	version = "dev"
	commit  = "dev"
)

//go:embed releases/versions.txt
var embeddedVersionsManifest string

// ── CLI: --version ──

func printVersion() {
	versionText := buildVersion()
	releaseName := buildReleaseName(versionText)
	commitText := buildCommit()

	if releaseName != "" {
		fmt.Printf("dbterm v%s \"%s\"\n", versionText, releaseName)
	} else {
		fmt.Printf("dbterm v%s\n", versionText)
	}
	fmt.Printf("Build %s\n", commitText)
	fmt.Printf("Go %s, %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// ── CLI: --info ──

func printInfo() {
	cfgDir := configDir()
	cfgFile := filepath.Join(cfgDir, "connections.json")
	stateDir, stateDirErr := appdirs.StateDir()
	logDir, logDirErr := appdirs.LogDir()
	catalogPath := "unavailable"
	if stateDirErr == nil {
		catalogPath = filepath.Join(stateDir, "backup", "backups.db")
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
	versionText := buildVersion()
	releaseName := buildReleaseName(versionText)
	commitText := buildCommit()
	if releaseName != "" {
		fmt.Printf("  \033[33mVersion\033[0m       %s (%s)\n", versionText, releaseName)
	} else {
		fmt.Printf("  \033[33mVersion\033[0m       %s\n", versionText)
	}
	fmt.Printf("  \033[33mBuild\033[0m         %s\n", commitText)
	fmt.Printf("  \033[33mGo\033[0m            %s\n", runtime.Version())
	fmt.Printf("  \033[33mOS / Arch\033[0m     %s / %s\n\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("  \033[33mPATHS\033[0m")
	fmt.Printf("  Binary        %s (%s)\n", binPath, binSize)
	fmt.Printf("  Config dir    %s (%s)\n", cfgDir, pathStatus(cfgDir, false))
	fmt.Printf("  Connections   %s (%s)\n", cfgFile, pathStatus(cfgFile, true))
	if stateDirErr != nil {
		fmt.Printf("  State dir     unavailable (%v)\n", stateDirErr)
	} else {
		fmt.Printf("  State dir     %s (%s)\n", stateDir, pathStatus(stateDir, false))
	}
	if logDirErr != nil {
		fmt.Printf("  Logs          unavailable (%v)\n", logDirErr)
	} else {
		fmt.Printf("  Logs          %s (%s)\n", logDir, pathStatus(logDir, false))
	}
	if catalogPath == "unavailable" {
		fmt.Println("  Backup catalog unavailable")
	} else {
		fmt.Printf("  Backup catalog %s (%s)\n", catalogPath, pathStatus(catalogPath, true))
	}
	fmt.Println()
	fmt.Println("  \033[33mBACKUP AGENT\033[0m")
	agent, agentErr := inspectBackupAgentLifecycle(5 * time.Second)
	if agentErr != nil {
		fmt.Printf("  Status        unavailable (%v)\n", agentErr)
	} else {
		fmt.Printf("  Manager       %s\n", agent.status.Manager)
		fmt.Printf("  Installed     %t\n", agent.status.Installed)
		fmt.Printf("  Running       %t\n", agent.status.Running)
		if strings.TrimSpace(agent.status.Detail) != "" {
			fmt.Printf("  Detail        %s\n", agent.status.Detail)
		}
	}
	fmt.Println()
	fmt.Println("  \033[33mRESOURCES\033[0m")
	fmt.Println("  TUI           Event-driven with guarded paging/preview limits")
	fmt.Println("  Agent         Sleeps between polls; runs one backup job at a time")
	fmt.Println("  Disk          Binary + JSON config + SQLite catalog + logs + one private raw-backup stage")
	fmt.Println("  Artifacts     Completed artifacts are stored only in destinations chosen per backup job")
	fmt.Println("  Network       Database connections, scheduled backups, and updates")
	fmt.Println()
	fmt.Println("  \033[33mDRIVERS\033[0m       All pure Go — no CGO, no C deps")
	fmt.Println("  PostgreSQL    lib/pq")
	fmt.Println("  MySQL         go-sql-driver/mysql")
	fmt.Println("  SQLite        modernc.org/sqlite")
	fmt.Println("  Turso         libsql-client-go")
	fmt.Println("  Cloudflare D1 dbterm ordered-raw adapter (cfd1 API client)")
	fmt.Println()
	fmt.Println("  \033[33mCLIENT TOOLS\033[0m  PostgreSQL/MySQL backup/restore; SQLite SQL restore")
	fmt.Printf("  psql          %s\n", cliToolStatus("psql"))
	fmt.Printf("  pg_restore    %s\n", cliToolStatus("pg_restore"))
	fmt.Printf("  mysql         %s\n", cliToolStatus("mysql"))
	fmt.Printf("  pg_dump       %s\n", cliToolStatus("pg_dump"))
	fmt.Printf("  mysqldump     %s\n", cliToolStatus("mysqldump"))
	fmt.Printf("  sqlite3       %s\n", cliToolStatus("sqlite3"))
	fmt.Println()
	fmt.Println("  \033[33mINSTALL\033[0m       No Go required")
	fmt.Println("  macOS/Linux   curl -fsSL https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.sh | bash")
	fmt.Println("  Windows       powershell -NoProfile -ExecutionPolicy Bypass -Command \"irm https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.ps1 | iex\"")
	fmt.Println("  \033[33mUPDATE\033[0m        dbterm --update [version]")
	fmt.Println("  \033[33mREMOVE\033[0m        dbterm --uninstall [--purge] [--yes]")
	fmt.Println()
}

// ── Build metadata helpers ──

func buildVersion() string {
	if version != "" && version != "dev" {
		return strings.TrimPrefix(version, "v")
	}

	if v, _, _ := latestManifestRelease(); v != "" {
		return strings.TrimPrefix(v, "v")
	}

	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return strings.TrimPrefix(bi.Main.Version, "v")
		}
	}
	return "dev"
}

func buildCommit() string {
	if commit != "" && commit != "dev" {
		return commit
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				if len(s.Value) > 7 {
					return s.Value[:7]
				}
				return s.Value
			}
		}
	}
	return "dev"
}

func buildReleaseName(versionText string) string {
	releases := manifestReleases()
	if len(releases) == 0 {
		return ""
	}

	target := normalizeVersion(versionText)
	if target != "" {
		for _, release := range releases {
			if normalizeVersion(release.version) == target {
				return release.name
			}
		}
	}

	return releases[0].name
}

// ── Release manifest parsing ──

type manifestRelease struct {
	version     string
	name        string
	description string
}

func latestManifestRelease() (version, name, description string) {
	releases := manifestReleases()
	if len(releases) == 0 {
		return "", "", ""
	}

	release := releases[0]
	return release.version, release.name, release.description
}

func manifestReleases() []manifestRelease {
	lines := strings.Split(embeddedVersionsManifest, "\n")
	releases := make([]manifestRelease, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		parts := strings.SplitN(trimmed, "|", 3)
		if len(parts) < 3 {
			continue
		}

		v := strings.TrimSpace(parts[0])
		n := strings.TrimSpace(parts[1])
		d := strings.TrimSpace(parts[2])
		if v == "" || n == "" || d == "" {
			continue
		}
		releases = append(releases, manifestRelease{
			version:     v,
			name:        n,
			description: d,
		})
	}

	return releases
}

func normalizeVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

// ── Config + formatting utilities ──

func configDir() string {
	if dir, err := appdirs.ConfigDir(); err == nil {
		return dir
	}

	// Keep diagnostic/uninstall output usable if platform path discovery fails.
	// Destructive purge validation rejects the unresolved tilde fallback.
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

func pathStatus(path string, includeSize bool) string {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "not created yet"
		}
		return "unavailable: " + err.Error()
	}
	if includeSize && !info.IsDir() {
		return fmtBytes(info.Size())
	}
	if info.IsDir() {
		return "ready"
	}
	return "exists"
}

func cliToolStatus(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return "missing (install and add to PATH)"
	}
	return fmt.Sprintf("found (%s)", path)
}
