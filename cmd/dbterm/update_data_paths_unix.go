//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shreyam1008/dbterm/internal/appdirs"
)

func updateDataPaths(sudoInvoker string) (string, []string, error) {
	var configDir, stateDir string
	if strings.TrimSpace(sudoInvoker) == "" {
		var err error
		configDir, err = appdirs.ConfigDir()
		if err != nil {
			return "", nil, fmt.Errorf("resolve config profile: %w", err)
		}
		stateDir, err = appdirs.StateDir()
		if err != nil {
			return "", nil, fmt.Errorf("resolve state profile: %w", err)
		}
	} else {
		account, err := user.Lookup(sudoInvoker)
		if err != nil {
			return "", nil, fmt.Errorf("look up sudo invoking user %q: %w", sudoInvoker, err)
		}
		connectionPath, err := invokingUserConnectionsPath(account.HomeDir)
		if err != nil {
			return "", nil, err
		}
		configDir = filepath.Dir(connectionPath)
		stateDir, err = invokingUserStateDir(account.HomeDir)
		if err != nil {
			return "", nil, err
		}
	}

	connections := filepath.Join(configDir, "connections.json")
	recoveryVault := filepath.Join(stateDir, "profile-recovery", "connections.json")
	settingsVault := filepath.Join(stateDir, "profile-recovery", "settings.json")
	return configDir, []string{
		connections,
		connections + ".bak",
		connections + ".bak.previous",
		recoveryVault,
		recoveryVault + ".previous",
		settingsVault,
		filepath.Join(configDir, "settings.json"),
		filepath.Join(stateDir, "backup", "backups.db"),
		filepath.Join(stateDir, "change-profiler", "change-profiler.db"),
	}, nil
}

func invokingUserStateDir(home string) (string, error) {
	if override := strings.TrimSpace(os.Getenv("DBTERM_STATE_DIR")); override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("DBTERM_STATE_DIR must be absolute during sudo update")
		}
		return filepath.Clean(override), nil
	}
	home = filepath.Clean(strings.TrimSpace(home))
	if home == "" || !filepath.IsAbs(home) {
		return "", fmt.Errorf("invoking user's home directory is unavailable")
	}
	if runtime.GOOS == "linux" {
		if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
			if !filepath.IsAbs(xdg) {
				return "", fmt.Errorf("XDG_STATE_HOME must be absolute during sudo update")
			}
			return filepath.Join(filepath.Clean(xdg), "dbterm"), nil
		}
		return filepath.Join(home, ".local", "state", "dbterm"), nil
	}
	return filepath.Join(home, "Library", "Application Support", "dbterm"), nil
}
