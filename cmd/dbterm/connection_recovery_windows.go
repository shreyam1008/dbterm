//go:build windows

package main

import "fmt"

func recoverSudoConnections() error {
	return fmt.Errorf("sudo connection recovery is available on Linux and macOS only")
}

func legacySudoConnectionCount(string) (int, string) {
	return 0, ""
}
