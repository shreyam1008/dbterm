//go:build windows

package main

import "fmt"

func interactiveSudoInvoker() string {
	return ""
}

func relaunchAsSudoInvoker(string, []string) error {
	return fmt.Errorf("sudo profile handoff is unavailable on Windows")
}
