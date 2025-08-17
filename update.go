package main

import (
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
		return replaceWindowsBinary(exePath, downloadPath, oldVersion)
	}
	return replaceUnixBinary(exePath, downloadPath, oldVersion)
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

func replaceUnixBinary(exePath, downloadedPath, oldVersion string) error {
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
	printUpdateSummary(oldVersion)
	return nil
}

func replaceWindowsBinary(exePath, downloadedPath, oldVersion string) error {
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
	printUpdateSummary(oldVersion)
	fmt.Println("  \033[33mAction:\033[0m Close this terminal, then run dbterm again.")
	fmt.Println()
	return nil
}

// printUpdateSummary shows old→new version info and release notes after a successful update.
func printUpdateSummary(oldVersion string) {
	newVersion, newName, newDesc := latestManifestRelease()
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
