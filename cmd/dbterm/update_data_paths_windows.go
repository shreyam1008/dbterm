//go:build windows

package main

import (
	"fmt"
	"path/filepath"

	"github.com/shreyam1008/dbterm/internal/appdirs"
)

func updateDataPaths(string) (string, []string, error) {
	configDir, err := appdirs.ConfigDir()
	if err != nil {
		return "", nil, fmt.Errorf("resolve config profile: %w", err)
	}
	stateDir, err := appdirs.StateDir()
	if err != nil {
		return "", nil, fmt.Errorf("resolve state profile: %w", err)
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
