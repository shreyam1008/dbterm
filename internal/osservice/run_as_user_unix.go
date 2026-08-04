//go:build linux || darwin

package osservice

import (
	"fmt"
	"os"
	"os/user"
	"strings"
)

func resolveRunAsUser(options Options) (string, error) {
	if options.Scope != ScopeSystem {
		return "", nil
	}
	username := strings.TrimSpace(options.RunAsUser)
	if username == "" {
		var candidate *user.User
		var err error
		if sudoUser := strings.TrimSpace(os.Getenv("SUDO_USER")); sudoUser != "" && !strings.EqualFold(sudoUser, "root") {
			candidate, err = user.Lookup(sudoUser)
		} else if pkexecUID := strings.TrimSpace(os.Getenv("PKEXEC_UID")); pkexecUID != "" {
			candidate, err = user.LookupId(pkexecUID)
		} else {
			candidate, err = user.Current()
		}
		if err != nil {
			return "", fmt.Errorf("resolve the invoking user for system backup service: %w", err)
		}
		username = candidate.Username
	}
	if err := validateRunAsUser(username); err != nil {
		return "", err
	}
	resolved, err := user.Lookup(username)
	if err != nil {
		return "", fmt.Errorf("look up backup service run-as user %q: %w", username, err)
	}
	if resolved.Uid == "0" || strings.EqualFold(resolved.Username, "root") {
		return "", fmt.Errorf("backup agent system service must not run as root; choose the invoking non-root user")
	}
	if err := validateRunAsUser(resolved.Username); err != nil {
		return "", err
	}
	return resolved.Username, nil
}
