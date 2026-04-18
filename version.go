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
	fmt.Printf("  Config        %s (%s)\n\n", cfgFile, cfgSize)
	fmt.Println("  \033[33mRESOURCES\033[0m")
	fmt.Println("  RAM (idle)    ~8–12 MB")
	fmt.Println("  RAM (active)  ~10–15 MB with guarded paging/preview limits")
	fmt.Println("  CPU           Near-zero (event-driven TUI)")
	fmt.Println("  Disk          Binary only + tiny JSON config")
	fmt.Println("  Network       Only when connected to remote DB")
	fmt.Println()
	fmt.Println("  \033[33mDRIVERS\033[0m       All pure Go — no CGO, no C deps")
	fmt.Println("  PostgreSQL    lib/pq")
	fmt.Println("  MySQL         go-sql-driver/mysql")
	fmt.Println("  SQLite        modernc.org/sqlite")
	fmt.Println()
	fmt.Println("  \033[33mCLIENT TOOLS\033[0m  Required for SQL dump import/backup")
	fmt.Printf("  psql          %s\n", cliToolStatus("psql"))
	fmt.Printf("  pg_restore    %s\n", cliToolStatus("pg_restore"))
	fmt.Printf("  mysql         %s\n", cliToolStatus("mysql"))
	fmt.Printf("  pg_dump       %s\n", cliToolStatus("pg_dump"))
	fmt.Printf("  mysqldump     %s\n", cliToolStatus("mysqldump"))
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

func cliToolStatus(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return "missing (install and add to PATH)"
	}
	return fmt.Sprintf("found (%s)", path)
}
