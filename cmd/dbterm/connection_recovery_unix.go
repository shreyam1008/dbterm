//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/persist"
)

func recoverSudoConnections() error {
	invoker := interactiveSudoInvoker()
	if invoker == "" {
		return fmt.Errorf("this recovery must be run through sudo by a non-root user (%s)", connectionsRecoveryUsage)
	}
	account, err := user.Lookup(invoker)
	if err != nil {
		return fmt.Errorf("look up invoking user %q: %w", invoker, err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return fmt.Errorf("parse invoking user ID %q: %w", account.Uid, err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return fmt.Errorf("parse invoking group ID %q: %w", account.Gid, err)
	}

	target, err := invokingUserConnectionsPath(account.HomeDir)
	if err != nil {
		return err
	}
	source, recovered, err := loadLegacyRootConnections(target)
	if err != nil {
		return err
	}

	var current []config.ConnectionConfig
	if err := persist.LoadJSON(target, &current); err != nil {
		return fmt.Errorf("load invoking user's existing connections: %w", err)
	}
	merged, added, err := mergeRecoveredConnections(current, recovered)
	if err != nil {
		return err
	}

	backupPath := ""
	if len(current) > 0 {
		backupPath = filepath.Join(filepath.Dir(target), fmt.Sprintf("connections.before-sudo-recovery-%s.json", time.Now().Format("20060102-150405")))
		if err := persist.SaveJSON(backupPath, current); err != nil {
			return fmt.Errorf("back up existing user connections: %w", err)
		}
	}
	if err := persist.SaveJSON(target, merged); err != nil {
		return fmt.Errorf("save merged user connections: %w", err)
	}
	for _, path := range []string{filepath.Dir(target), target, backupPath} {
		if path == "" {
			continue
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("restore %s ownership to %s: %w", path, invoker, err)
		}
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return fmt.Errorf("secure recovered connections file: %w", err)
	}
	if backupPath != "" {
		if err := os.Chmod(backupPath, 0o600); err != nil {
			return fmt.Errorf("secure recovery backup: %w", err)
		}
	}

	fmt.Printf("\n  Recovered %d connection(s) from %s\n", added, source)
	fmt.Printf("  Canonical profile: %s (%d total)\n", target, len(merged))
	if backupPath != "" {
		fmt.Printf("  Previous user profile backup: %s\n", backupPath)
	}
	fmt.Println("  The legacy root file was left unchanged.")
	fmt.Println()
	return nil
}

func legacySudoConnectionCount(invoker string) (int, string) {
	account, err := user.Lookup(strings.TrimSpace(invoker))
	if err != nil {
		return 0, ""
	}
	target, err := invokingUserConnectionsPath(account.HomeDir)
	if err != nil {
		return 0, ""
	}
	source, connections, err := loadLegacyRootConnections(target)
	if err != nil {
		return 0, ""
	}
	return len(connections), source
}

func invokingUserConnectionsPath(home string) (string, error) {
	if override := strings.TrimSpace(os.Getenv("DBTERM_CONFIG_DIR")); override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("DBTERM_CONFIG_DIR must be absolute for sudo recovery")
		}
		return filepath.Join(filepath.Clean(override), "connections.json"), nil
	}
	home = filepath.Clean(strings.TrimSpace(home))
	if home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("invoking user's home directory is unavailable")
	}
	if runtime.GOOS == "linux" {
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			if !filepath.IsAbs(xdg) {
				return "", fmt.Errorf("XDG_CONFIG_HOME must be absolute for sudo recovery")
			}
			return filepath.Join(filepath.Clean(xdg), "dbterm", "connections.json"), nil
		}
		return filepath.Join(home, ".config", "dbterm", "connections.json"), nil
	}
	return filepath.Join(home, "Library", "Application Support", "dbterm", "connections.json"), nil
}

func loadLegacyRootConnections(target string) (string, []config.ConnectionConfig, error) {
	rootAccount, err := user.Lookup("root")
	if err != nil {
		return "", nil, fmt.Errorf("look up root profile: %w", err)
	}
	var source string
	if runtime.GOOS == "linux" {
		source = filepath.Join(rootAccount.HomeDir, ".config", "dbterm", "connections.json")
	} else {
		source = filepath.Join(rootAccount.HomeDir, "Library", "Application Support", "dbterm", "connections.json")
	}
	if filepath.Clean(source) == filepath.Clean(target) {
		return "", nil, fmt.Errorf("legacy root profile and user profile resolve to the same file; no merge is needed")
	}
	var recovered []config.ConnectionConfig
	if err := persist.LoadJSON(source, &recovered); err != nil {
		return "", nil, fmt.Errorf("load legacy root connections from %s: %w", source, err)
	}
	if len(recovered) == 0 {
		return "", nil, fmt.Errorf("no legacy root connections were found at %s", source)
	}
	return source, recovered, nil
}
