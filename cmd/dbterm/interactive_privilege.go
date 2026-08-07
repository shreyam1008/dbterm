package main

import "strings"

func normalizedSudoInvoker(euid int, sudoUser string) string {
	if euid != 0 {
		return ""
	}
	user := strings.TrimSpace(sudoUser)
	if user == "" || strings.EqualFold(user, "root") {
		return ""
	}
	return user
}
