//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func interactiveSudoInvoker() string {
	return normalizedSudoInvoker(os.Geteuid(), os.Getenv("SUDO_USER"))
}

func relaunchAsSudoInvoker(username string, argv []string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("sudo invoking user is unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate dbterm executable: %w", err)
	}
	sudoPath, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("locate sudo for privilege handoff: %w", err)
	}
	envPath, err := exec.LookPath("env")
	if err != nil {
		return fmt.Errorf("locate env for profile handoff: %w", err)
	}

	args := []string{"sudo", "-H", "-u", username, "--", envPath}
	for _, name := range []string{
		"DBTERM_CONFIG_DIR", "DBTERM_STATE_DIR", "DBTERM_LOG_DIR",
		"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
	} {
		if value, exists := os.LookupEnv(name); exists && strings.TrimSpace(value) != "" {
			args = append(args, name+"="+value)
		}
	}
	args = append(args, executable)
	if len(argv) > 1 {
		args = append(args, argv[1:]...)
	}
	if err := syscall.Exec(sudoPath, args, os.Environ()); err != nil {
		return fmt.Errorf("relaunch dbterm as %s: %w", username, err)
	}
	return nil
}
