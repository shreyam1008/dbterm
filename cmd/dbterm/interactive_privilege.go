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

// shouldUseInvokerProfile reports whether a sudo-launched command should run
// as the person who invoked sudo. Updates and uninstalls may need the elevated
// process to replace the installed binary. Explicit system-service operations
// likewise keep their requested system scope. Everything that can read or
// write connections uses the invoking user's one canonical profile.
func shouldUseInvokerProfile(args []string) bool {
	if len(args) == 0 {
		return true
	}

	command := strings.ToLower(strings.TrimSpace(args[0]))
	if command == "--update" || command == "-u" || command == "update" ||
		strings.HasPrefix(command, "--unin") || strings.HasPrefix(command, "unin") || command == "remove" {
		return false
	}
	if command == "connections" && len(args) > 1 && strings.EqualFold(strings.TrimSpace(args[1]), "recover-sudo") {
		return false
	}

	if command != "backup" {
		return true
	}
	if len(args) < 2 || !strings.EqualFold(strings.TrimSpace(args[1]), "service") {
		return true
	}
	for index := 2; index < len(args); index++ {
		option := strings.ToLower(strings.TrimSpace(args[index]))
		if option == "--system" || option == "--scope=system" {
			return false
		}
		if option == "--scope" && index+1 < len(args) && strings.EqualFold(strings.TrimSpace(args[index+1]), "system") {
			return false
		}
	}
	return true
}
