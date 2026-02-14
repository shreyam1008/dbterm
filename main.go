package main

import (
	"bufio"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/ui"
)

var (
	version = "dev"
	commit  = "dev"
)

//go:embed releases/versions.txt
var embeddedVersionsManifest string

const defaultRepo = "shreyam1008/dbterm"

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
    Alt+Enter  Execute query       F5  Refresh table
    Ctrl+F5    Full refresh        F/B Toggle Fullscreen/Backup
    Alt+H      Help panel          Alt+D  Dashboard
    Ctrl+C     Quit

  ` + "\033[38;2;108;112;134m" + `https://github.com/shreyam1008/dbterm
  Based on pgterm by @nabsk911` + "\033[0m" + `
`)
}

func printVersion() {
	fmt.Printf("dbterm v%s (%s)\n", buildVersion(), buildCommit())
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
	fmt.Printf("  \033[33mVersion\033[0m       %s (%s)\n", buildVersion(), buildCommit())
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
	fmt.Println("  \033[33mUPDATE\033[0m        dbterm --update [version]")
	fmt.Println("  \033[33mREMOVE\033[0m        dbterm --uninstall [--purge] [--yes]")
	fmt.Println()
}

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

func runUpdate(requestedVersion string) error {
	repo := strings.TrimSpace(os.Getenv("DBTERM_REPO"))
	if repo == "" {
		repo = defaultRepo
	}

	versionSpec := strings.TrimSpace(requestedVersion)
	if versionSpec == "" {
		versionSpec = strings.TrimSpace(os.Getenv("DBTERM_VERSION"))
	}
	if versionSpec == "" {
		versionSpec = "latest"
	}

	targetOS, targetArch, err := updateTargetForRuntime(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	assetName := fmt.Sprintf("dbterm-%s-%s", targetOS, targetArch)
	if targetOS == "windows" {
		assetName += ".exe"
	}

	fmt.Print("\n  \033[1;38;2;203;166;247mdbterm\033[0m — Update\n")
	fmt.Printf("  Target        %s/%s\n", targetOS, targetArch)
	fmt.Printf("  Source        %s\n", repo)
	fmt.Printf("  Version       %s\n", strings.TrimPrefix(versionSpec, "v"))

	baseURL := releaseBaseURL(repo, versionSpec)

	tmpDir, err := os.MkdirTemp("", "dbterm-update-*")
	if err != nil {
		return fmt.Errorf("could not create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	downloadPath := filepath.Join(tmpDir, assetName)
	checksumPath := filepath.Join(tmpDir, "checksums.txt")

	fmt.Printf("  Downloading   %s\n", assetName)
	if err := downloadToFile(baseURL+"/"+assetName, downloadPath); err != nil {
		return fmt.Errorf("download failed (%s): %w", assetName, err)
	}

	if err := downloadToFile(baseURL+"/checksums.txt", checksumPath); err == nil {
		expected, err := checksumForAsset(checksumPath, assetName)
		if err != nil {
			return err
		}
		actual, err := sha256File(downloadPath)
		if err != nil {
			return fmt.Errorf("could not compute checksum: %w", err)
		}
		if !strings.EqualFold(expected, actual) {
			return fmt.Errorf("checksum mismatch for %s", assetName)
		}
		fmt.Println("  \033[38;2;166;227;161m✓\033[0m Checksum verified")
	} else {
		fmt.Println("  \033[33mWarning:\033[0m checksums.txt unavailable, skipping checksum verification.")
	}

	if targetOS != "windows" {
		if err := os.Chmod(downloadPath, 0o755); err != nil {
			return fmt.Errorf("could not set executable bit: %w", err)
		}
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate current binary: %w", err)
	}

	if runtime.GOOS == "windows" {
		return replaceWindowsBinary(exePath, downloadPath)
	}
	return replaceUnixBinary(exePath, downloadPath)
}

func updateTargetForRuntime(goos, goarch string) (string, string, error) {
	var osName, archName string
	switch goos {
	case "linux":
		osName = "linux"
	case "darwin":
		osName = "darwin"
	case "windows":
		osName = "windows"
	default:
		return "", "", fmt.Errorf("self-update is not supported on %s", goos)
	}

	switch goarch {
	case "amd64":
		archName = "amd64"
	case "arm64":
		archName = "arm64"
	default:
		return "", "", fmt.Errorf("self-update is not supported on %s/%s", goos, goarch)
	}

	return osName, archName, nil
}

func releaseBaseURL(repo, versionSpec string) string {
	if versionSpec == "" || strings.EqualFold(versionSpec, "latest") {
		return fmt.Sprintf("https://github.com/%s/releases/latest/download", repo)
	}
	if !strings.HasPrefix(versionSpec, "v") {
		versionSpec = "v" + versionSpec
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, versionSpec)
}

func downloadToFile(url, dest string) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}
	return out.Close()
}

func checksumForAsset(checksumPath, assetName string) (string, error) {
	content, err := os.ReadFile(checksumPath)
	if err != nil {
		return "", fmt.Errorf("could not read checksums.txt: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(name) == assetName {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt does not contain %s", assetName)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func replaceUnixBinary(exePath, downloadedPath string) error {
	targetDir := filepath.Dir(exePath)
	stagedPath := filepath.Join(targetDir, fmt.Sprintf(".dbterm-update-%d", time.Now().UnixNano()))

	if err := copyFile(downloadedPath, stagedPath, 0o755); err != nil {
		if isPermissionErr(err) {
			return fmt.Errorf("permission denied writing %s. Re-run with sudo: sudo dbterm --update", targetDir)
		}
		return fmt.Errorf("failed to stage update in %s: %w", targetDir, err)
	}

	if err := os.Rename(stagedPath, exePath); err != nil {
		_ = os.Remove(stagedPath)
		if isPermissionErr(err) {
			return fmt.Errorf("permission denied replacing %s. Re-run with sudo: sudo dbterm --update", exePath)
		}
		return fmt.Errorf("failed to replace binary: %w", err)
	}

	fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Updated: %s\n", exePath)
	fmt.Println("  \033[38;2;166;227;161m✓\033[0m Update complete.")
	fmt.Println()
	return nil
}

func replaceWindowsBinary(exePath, downloadedPath string) error {
	stagedPath := exePath + ".new"
	if err := copyFile(downloadedPath, stagedPath, 0o755); err != nil {
		if isPermissionErr(err) {
			return fmt.Errorf("permission denied writing %s. Re-run terminal as Administrator and try dbterm --update again", exePath)
		}
		return fmt.Errorf("failed to stage update: %w", err)
	}

	cmdLine := fmt.Sprintf(`ping 127.0.0.1 -n 3 > nul & move /Y "%s" "%s" > nul`, stagedPath, exePath)
	if err := exec.Command("cmd", "/C", cmdLine).Start(); err != nil {
		return fmt.Errorf("could not schedule binary replacement: %w", err)
	}

	fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Downloaded update for %s\n", exePath)
	fmt.Println("  \033[33mAction:\033[0m Close this terminal, then run dbterm again.")
	fmt.Println()
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func isPermissionErr(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "permission denied")
}

func runUninstall(purge bool, assumeYes bool) error {
	cfgDir := configDir()
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate executable path: %w", err)
	}
	exePath = filepath.Clean(exePath)

	if err := validateUninstallTarget(exePath); err != nil {
		return err
	}
	if purge {
		if err := validatePurgeTarget(cfgDir); err != nil {
			return err
		}
	}
	if err := confirmUninstall(exePath, cfgDir, purge, assumeYes); err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		return runWindowsUninstall(exePath, cfgDir, purge)
	}

	fmt.Print("\n  \033[1;38;2;203;166;247mdbterm\033[0m — Uninstall\n")

	if err := os.Remove(exePath); err != nil {
		if os.IsPermission(err) {
			suffix := ""
			if purge {
				suffix = " --purge"
			}
			return fmt.Errorf("permission denied removing %s. Re-run with sudo: sudo dbterm --uninstall%s", exePath, suffix)
		}
		return fmt.Errorf("could not remove binary %s: %w", exePath, err)
	}
	fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Removed binary: %s\n", exePath)

	if purge {
		if err := os.RemoveAll(cfgDir); err != nil {
			return fmt.Errorf("removed binary but failed to remove config %s: %w", cfgDir, err)
		}
		fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Removed config: %s\n", cfgDir)
	} else {
		fmt.Printf("  \033[33mInfo:\033[0m Kept config: %s\n", cfgDir)
		fmt.Println("  Use dbterm --uninstall --purge to remove saved connections too.")
	}

	fmt.Println("  \033[38;2;166;227;161m✓\033[0m Uninstall complete.")
	fmt.Println()
	return nil
}

func validateUninstallTarget(exePath string) error {
	base := strings.ToLower(filepath.Base(exePath))
	if base != "dbterm" && base != "dbterm.exe" {
		return fmt.Errorf("refusing to remove unexpected executable path: %s", exePath)
	}

	info, err := os.Stat(exePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("binary not found: %s", exePath)
		}
		return fmt.Errorf("could not access binary %s: %w", exePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("refusing to remove directory: %s", exePath)
	}
	return nil
}

func validatePurgeTarget(cfgDir string) error {
	if strings.TrimSpace(cfgDir) == "" {
		return fmt.Errorf("config path is empty; refusing purge")
	}
	if strings.HasPrefix(cfgDir, "~") {
		return fmt.Errorf("could not resolve config path (%s); refusing purge", cfgDir)
	}
	clean := filepath.Clean(cfgDir)
	if clean == "/" || clean == "." {
		return fmt.Errorf("unsafe config path for purge: %s", clean)
	}
	return nil
}

func confirmUninstall(exePath, cfgDir string, purge, assumeYes bool) error {
	if assumeYes {
		return nil
	}

	stdinInfo, err := os.Stdin.Stat()
	if err != nil {
		return fmt.Errorf("could not access stdin for confirmation: %w", err)
	}
	if stdinInfo.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("non-interactive input; re-run with --yes to confirm uninstall")
	}

	fmt.Print("\n  \033[1;38;2;203;166;247mdbterm\033[0m — Confirm Uninstall\n")
	fmt.Printf("  Binary        %s\n", exePath)
	if purge {
		fmt.Printf("  Config        %s (will be deleted)\n", cfgDir)
	} else {
		fmt.Printf("  Config        %s (will be kept)\n", cfgDir)
	}
	fmt.Print("  Continue? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("could not read confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("uninstall cancelled")
	}
	return nil
}

func runWindowsUninstall(exePath, cfgDir string, purge bool) error {
	fmt.Print("\n  \033[1;38;2;203;166;247mdbterm\033[0m — Uninstall\n")

	parts := []string{
		"ping 127.0.0.1 -n 3 > nul",
		fmt.Sprintf(`del /f /q "%s"`, exePath),
	}
	if purge {
		parts = append(parts, fmt.Sprintf(`rmdir /s /q "%s"`, cfgDir))
	}

	cmdLine := strings.Join(parts, " & ")
	if err := exec.Command("cmd", "/C", cmdLine).Start(); err != nil {
		return fmt.Errorf("could not schedule uninstall: %w", err)
	}

	fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Scheduled binary removal: %s\n", exePath)
	if purge {
		fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Scheduled config removal: %s\n", cfgDir)
	} else {
		fmt.Printf("  \033[33mInfo:\033[0m Kept config: %s\n", cfgDir)
	}
	fmt.Println("  Close this terminal and open a new one.")
	fmt.Println()
	return nil
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

func latestManifestRelease() (version, name, description string) {
	lines := strings.Split(embeddedVersionsManifest, "\n")
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
		return v, n, d
	}
	return "", "", ""
}
