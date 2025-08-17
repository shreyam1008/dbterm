package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ── CLI: --uninstall ──

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

// ── Validation helpers ──

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

// ── Confirmation prompt ──

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

// ── Windows-specific uninstall ──

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
