package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/osservice"
)

const defaultRepo = "shreyam1008/dbterm"

// ── CLI: --update ──

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

	// Capture the current version before updating
	oldVersion := buildVersion()
	oldReleaseName := buildReleaseName(oldVersion)

	targetOS, targetArch, err := updateTargetForRuntime(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	assetName := fmt.Sprintf("dbterm-%s-%s", targetOS, targetArch)
	if targetOS == "windows" {
		assetName += ".exe"
	}

	fmt.Print("\n  \033[1;38;2;203;166;247mdbterm\033[0m — Update\n")
	if oldReleaseName != "" {
		fmt.Printf("  \033[33mCurrent\033[0m       v%s \"%s\"\n", oldVersion, oldReleaseName)
	} else {
		fmt.Printf("  \033[33mCurrent\033[0m       v%s\n", oldVersion)
	}
	fmt.Printf("  Target        %s/%s\n", targetOS, targetArch)
	fmt.Printf("  Source        %s\n", repo)

	// Resolve the actual target version and check if already up to date
	resolvedVersion := versionSpec
	if strings.EqualFold(versionSpec, "latest") {
		if tag := resolveLatestTag(repo); tag != "" {
			resolvedVersion = tag
		}
	}
	displayVersion := strings.TrimPrefix(resolvedVersion, "v")
	fmt.Printf("  Version       %s\n", displayVersion)

	if normalizeVersion(oldVersion) != "dev" && normalizeVersion(oldVersion) == normalizeVersion(resolvedVersion) {
		fmt.Println()
		fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Already up to date — v%s\n", oldVersion)
		if oldReleaseName != "" {
			fmt.Printf("  \033[38;2;108;112;134mRelease \"%s\"\033[0m\n", oldReleaseName)
		}
		fmt.Println()
		return nil
	}

	sudoInvoker := interactiveSudoInvoker()
	dataGuard, err := captureUpdateDataGuard(sudoInvoker)
	if err != nil {
		return fmt.Errorf("could not establish the user-data safety guard: %w", err)
	}
	if profile := dataGuard.profilePath(); profile != "" {
		fmt.Printf("  Data profile  %s (guarded, never replaced)\n", profile)
	}

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

	manageBackupAgent := shouldManageBackupAgentDuringUpdate(sudoInvoker)
	agent := backupAgentLifecycle{}
	if manageBackupAgent {
		agent, err = prepareBackupAgentForUpdate()
		if err != nil {
			return err
		}
	} else {
		fmt.Printf("  Privilege     sudo only replaces the executable; %s's profile stays in user scope\n", sudoInvoker)
	}

	if runtime.GOOS == "windows" {
		if err := replaceWindowsBinary(exePath, downloadPath, oldVersion, resolvedVersion, agent.stopped); err != nil {
			return restoreBackupAgentAfterUpdateFailure(agent, err)
		}
		if err := dataGuard.verify(); err != nil {
			return fmt.Errorf("binary update was scheduled, but the data guard failed: %w", err)
		}
		if agent.stopped {
			fmt.Println("  \033[33mBackup agent\033[0m Will restart after the delayed Windows replacement.")
		}
		return nil
	}
	if err := replaceUnixBinary(exePath, downloadPath, oldVersion, resolvedVersion); err != nil {
		return restoreBackupAgentAfterUpdateFailure(agent, err)
	}
	if err := dataGuard.verify(); err != nil {
		return fmt.Errorf("binary was updated, but the data guard failed: %w", err)
	}
	if !manageBackupAgent {
		fmt.Println("  \033[38;2;166;227;161m✓\033[0m Saved connections, backup state/artifacts, and Change Profiler anchors were not opened or modified")
		fmt.Println("  \033[38;2;166;227;161m✓\033[0m The per-user backup agent was left running; it will use the new binary on its next start")
		return nil
	}
	if agent.stopped {
		if err := refreshAndStartBackupAgent(agent.manager); err != nil {
			return fmt.Errorf("dbterm was updated, but the backup agent registration could not be refreshed/restarted: %w (run `dbterm backup service install`)", err)
		}
		fmt.Println("  \033[38;2;166;227;161m✓\033[0m Backup agent registration refreshed and restarted")
	}
	return nil
}

// A sudo update needs elevation only to replace the installed executable.
// Inspecting or restarting a user service from that root process resolves the
// wrong HOME/systemd context and risks creating a second root-owned dbterm
// profile. Unix replaces the binary atomically, so the invoking user's running
// agent can safely keep its old executable mapping until its next normal start.
func shouldManageBackupAgentDuringUpdate(sudoInvoker string) bool {
	return strings.TrimSpace(sudoInvoker) == ""
}

func restoreBackupAgentAfterUpdateFailure(agent backupAgentLifecycle, updateErr error) error {
	if !agent.stopped {
		return updateErr
	}
	if err := startBackupAgent(agent.manager); err != nil {
		return fmt.Errorf("%w; additionally could not restart the previously-running backup agent: %v (run `dbterm backup service start`)", updateErr, err)
	}
	return updateErr
}

type backupAgentLifecycle struct {
	manager osservice.Manager
	status  osservice.Status
	stopped bool
}

func prepareBackupAgentForUpdate() (backupAgentLifecycle, error) {
	state, err := inspectBackupAgentLifecycle(15 * time.Second)
	if err != nil {
		// An atomic Unix rename remains safe when systemd is unavailable; the
		// old agent process will keep running until its next normal restart.
		// Windows cannot replace an executable held by an unobserved process,
		// and launchd is always present on supported macOS releases, so fail
		// closed on those platforms.
		if runtime.GOOS == "linux" && !state.status.Installed {
			if processErr := ensureBackupAgentProcessStopped("update dbterm"); processErr != nil {
				return state, processErr
			}
			fmt.Printf("  \033[33mWarning:\033[0m could not inspect the systemd backup agent: %v\n", err)
			return state, nil
		}
		return state, fmt.Errorf("could not inspect the backup agent before update: %w", err)
	}
	if !state.status.Running {
		if err := ensureBackupAgentProcessStopped("update dbterm"); err != nil {
			return state, err
		}
		return state, nil
	}
	if err := stopBackupAgent(state.manager); err != nil {
		return state, fmt.Errorf("could not stop the running backup agent before update: %w", err)
	}
	state.stopped = true
	if err := waitForBackupAgentProcessExit(backupAgentExitTimeout); err != nil {
		if restartErr := startBackupAgent(state.manager); restartErr != nil {
			return state, fmt.Errorf("could not drain the backup agent before update: %w; additionally could not restart its native service: %v", err, restartErr)
		}
		return state, fmt.Errorf("could not drain the backup agent before update: %w; its native service was restarted", err)
	}
	fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Backup agent stopped (%s)\n", state.status.Manager)
	return state, nil
}

func inspectBackupAgentLifecycle(timeout time.Duration) (backupAgentLifecycle, error) {
	state := backupAgentLifecycle{}
	manager, err := newBackupServiceManager()
	if err != nil {
		return state, err
	}
	state.manager = manager
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	status, err := manager.Status(ctx)
	state.status = status
	return state, err
}

func stopBackupAgent(manager osservice.Manager) error {
	if manager == nil {
		return fmt.Errorf("backup agent manager is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return manager.Stop(ctx)
}

func startBackupAgent(manager osservice.Manager) error {
	if manager == nil {
		return fmt.Errorf("backup agent manager is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return manager.Start(ctx)
}

func refreshAndStartBackupAgent(manager osservice.Manager) error {
	if manager == nil {
		return fmt.Errorf("backup agent manager is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return manager.Install(ctx)
}

// ── Platform helpers ──

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

// resolveLatestTag queries GitHub's latest release redirect to get the actual tag version.
// Returns empty string on any failure (the update will proceed without the check).
func resolveLatestTag(repo string) string {
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}
	resp, err := client.Head(fmt.Sprintf("https://github.com/%s/releases/latest", repo))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc == "" {
		return ""
	}
	// Location is like https://github.com/user/repo/releases/tag/v0.3.8
	parts := strings.Split(loc, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
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

// ── Download and checksum ──

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

// ── Binary replacement ──

func replaceUnixBinary(exePath, downloadedPath, oldVersion, resolvedVersion string) error {
	targetDir := filepath.Dir(exePath)
	stagedPath := filepath.Join(targetDir, fmt.Sprintf(".dbterm-update-%d", time.Now().UnixNano()))

	if err := copyFile(downloadedPath, stagedPath, 0o755); err != nil {
		if isPermissionErr(err) {
			return fmt.Errorf("permission denied writing %s. Update dbterm through the installer or package manager that owns %s, or install it in a user-writable directory; keep backup-service commands in your normal user account and do not rerun the whole update with sudo", targetDir, exePath)
		}
		return fmt.Errorf("failed to stage update in %s: %w", targetDir, err)
	}

	if err := os.Rename(stagedPath, exePath); err != nil {
		_ = os.Remove(stagedPath)
		if isPermissionErr(err) {
			return fmt.Errorf("permission denied replacing %s. Update that binary through its installer/package manager, or install dbterm in a user-writable directory; keep backup-service commands in your normal user account and do not rerun the whole update with sudo", exePath)
		}
		return fmt.Errorf("failed to replace binary: %w", err)
	}

	fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Updated: %s\n", exePath)
	printUpdateSummary(exePath, oldVersion, resolvedVersion)
	return nil
}

func replaceWindowsBinary(exePath, downloadedPath, oldVersion, resolvedVersion string, restartBackupAgent bool) error {
	stagedPath := exePath + ".new"
	if err := copyFile(downloadedPath, stagedPath, 0o755); err != nil {
		if isPermissionErr(err) {
			return fmt.Errorf("permission denied writing %s. Update that binary through its installer/package manager or move dbterm to a user-writable directory; keep backup-service commands in your normal user account rather than rerunning the whole update from an elevated terminal", exePath)
		}
		return fmt.Errorf("failed to stage update: %w", err)
	}

	cmdLine := windowsUpdateCommand(exePath, stagedPath, restartBackupAgent)
	if err := startDetachedProcess("cmd.exe", "/D", "/S", "/C", cmdLine); err != nil {
		_ = os.Remove(stagedPath)
		return fmt.Errorf("could not schedule binary replacement: %w", err)
	}

	fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Staged update for %s\n", exePath)
	printPendingWindowsUpdateSummary(oldVersion, resolvedVersion)
	fmt.Println("  \033[33mVerify:\033[0m After this command exits, run `dbterm --version` in a new terminal.")
	fmt.Println()
	return nil
}

func windowsUpdateCommand(exePath, stagedPath string, restartBackupAgent bool) string {
	parts := []string{
		"ping 127.0.0.1 -n 3 > nul",
		fmt.Sprintf(`move /Y "%s" "%s" > nul`, stagedPath, exePath),
	}
	if restartBackupAgent {
		// Conditional chaining is intentional: never restart the old binary when
		// replacement failed and left the staged .new file behind.
		parts = append(parts, fmt.Sprintf(`"%s" backup service install > nul 2>&1`, exePath))
	}
	return strings.Join(parts, " && ")
}

func printPendingWindowsUpdateSummary(oldVersion, resolvedVersion string) {
	newVersion := strings.TrimPrefix(strings.TrimSpace(resolvedVersion), "v")
	newName := ""
	for _, release := range manifestReleases() {
		if normalizeVersion(release.version) == normalizeVersion(newVersion) {
			newName = release.name
			break
		}
	}

	fmt.Println()
	fmt.Println("  ╭─────────────────────────────────────────────╮")
	if newVersion == "" {
		fmt.Println("  │  \033[33mPending\033[0m    Windows replacement scheduled")
	} else if newName != "" {
		fmt.Printf("  │  \033[33mPending\033[0m    v%s \"\033[38;2;249;226;175m%s\033[0m\"\n", newVersion, newName)
	} else {
		fmt.Printf("  │  \033[33mPending\033[0m    v%s\n", newVersion)
	}
	if oldVersion != "" && newVersion != "" && normalizeVersion(oldVersion) != normalizeVersion(newVersion) {
		fmt.Printf("  │  \033[38;2;108;112;134mCurrent    v%s\033[0m\n", oldVersion)
	}
	fmt.Println("  ╰─────────────────────────────────────────────╯")
	fmt.Println()
	fmt.Println("  \033[33mUpdate scheduled.\033[0m Installation is not verified yet.")
}

// printUpdateSummary shows old→new version info and release notes after a successful update.
// It execs the NEW binary to read its embedded version info, avoiding the stale-manifest bug.
func printUpdateSummary(newBinaryPath, oldVersion, resolvedVersion string) {
	var newVersion, newName, newDesc string

	// Try to get version info from the new binary itself
	if newBinaryPath != "" {
		out, err := exec.Command(newBinaryPath, "--version").CombinedOutput()
		if err == nil {
			newVersion, newName = parseVersionOutput(string(out))
		}
	}

	// Fallback: use the resolved tag if exec failed
	if newVersion == "" && resolvedVersion != "" {
		newVersion = strings.TrimPrefix(resolvedVersion, "v")
	}

	// Look up release notes from the resolved version's manifest entry
	if newVersion != "" {
		for _, r := range manifestReleases() {
			if normalizeVersion(r.version) == normalizeVersion(newVersion) {
				if newName == "" {
					newName = r.name
				}
				newDesc = r.description
				break
			}
		}
	}

	if newVersion == "" {
		fmt.Println("  \033[38;2;166;227;161m✓\033[0m Update complete.")
		fmt.Println()
		return
	}

	fmt.Println()
	fmt.Println("  ╭─────────────────────────────────────────────╮")
	if newName != "" {
		fmt.Printf("  │  \033[38;2;166;227;161m✓\033[0m \033[1mInstalled\033[0m  v%s \"\033[38;2;249;226;175m%s\033[0m\"\n", newVersion, newName)
	} else {
		fmt.Printf("  │  \033[38;2;166;227;161m✓\033[0m \033[1mInstalled\033[0m  v%s\n", newVersion)
	}
	if oldVersion != "" && normalizeVersion(oldVersion) != normalizeVersion(newVersion) {
		fmt.Printf("  │  \033[38;2;108;112;134mPrevious   v%s\033[0m\n", oldVersion)
	}
	if newDesc != "" {
		fmt.Println("  │")
		fmt.Printf("  │  \033[33mWhat's new:\033[0m %s\n", newDesc)
	}
	fmt.Println("  ╰─────────────────────────────────────────────╯")
	fmt.Println()
	fmt.Println("  \033[38;2;166;227;161m✓\033[0m Update complete. Thank you for using dbterm!")
	fmt.Println()
}

// parseVersionOutput extracts version and release name from `dbterm --version` output.
// Expected format: "dbterm v0.3.9 \"Fakir\"\n..."
func parseVersionOutput(output string) (version, name string) {
	lines := strings.SplitN(output, "\n", 2)
	if len(lines) == 0 {
		return "", ""
	}
	line := strings.TrimSpace(lines[0])
	// Expected: "dbterm v0.3.9 \"Fakir\""
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return "", ""
	}
	version = strings.TrimPrefix(parts[1], "v")

	// Extract quoted name if present
	if idx := strings.Index(line, "\""); idx >= 0 {
		rest := line[idx+1:]
		if end := strings.Index(rest, "\""); end >= 0 {
			name = rest[:end]
		}
	}
	return version, name
}

// ── File utilities ──

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
