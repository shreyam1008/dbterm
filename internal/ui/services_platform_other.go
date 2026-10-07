//go:build !windows

package ui

import (
	"context"
	"os/exec"
	"strings"
)

func getPlatformServiceInfo(string) *serviceInfo { return nil }

func runPlatformServiceCmd(ctx context.Context, action, unit, password string) ([]byte, error) {
	var cmd *exec.Cmd
	if password != "" {
		cmd = exec.CommandContext(ctx, "sudo", "-S", "-k", "-p", "", "systemctl", action, unit)
		cmd.Stdin = strings.NewReader(password + "\n")
	} else {
		cmd = exec.CommandContext(ctx, "sudo", "-n", "systemctl", action, unit)
	}
	return cmd.CombinedOutput()
}
