//go:build linux || darwin

package main

import "os"

func interactiveSudoInvoker() string {
	return normalizedSudoInvoker(os.Geteuid(), os.Getenv("SUDO_USER"))
}
