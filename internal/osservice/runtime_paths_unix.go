//go:build linux || darwin

package osservice

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

type unixServiceIdentity struct {
	uid    uint32
	groups map[uint32]struct{}
}

func validateSystemRuntimePaths(options Options, runAsUser string) error {
	if options.Scope != ScopeSystem {
		return nil
	}
	identity, err := resolveUnixServiceIdentity(runAsUser)
	if err != nil {
		return err
	}
	if err := validateUnixExecutable(options.Executable, identity); err != nil {
		return err
	}
	for _, directory := range []struct {
		label       string
		path        string
		permissions uint32
	}{
		{label: "config directory", path: options.ConfigDir, permissions: 0o5},
		{label: "state directory", path: options.StateDir, permissions: 0o7},
		{label: "log directory", path: options.LogDir, permissions: 0o7},
	} {
		if err := validateUnixDirectory(directory.path, directory.label, directory.permissions, identity); err != nil {
			return fmt.Errorf("system backup service cannot run as %q: %w", runAsUser, err)
		}
	}
	return nil
}

func resolveUnixServiceIdentity(username string) (unixServiceIdentity, error) {
	account, err := user.Lookup(username)
	if err != nil {
		return unixServiceIdentity{}, fmt.Errorf("look up system backup service user %q: %w", username, err)
	}
	uid, err := parseUnixID(account.Uid, "user", username)
	if err != nil {
		return unixServiceIdentity{}, err
	}
	groupIDs, err := account.GroupIds()
	if err != nil {
		return unixServiceIdentity{}, fmt.Errorf("resolve groups for system backup service user %q: %w", username, err)
	}
	groups := make(map[uint32]struct{}, len(groupIDs)+1)
	primaryGroup, err := parseUnixID(account.Gid, "primary group", username)
	if err != nil {
		return unixServiceIdentity{}, err
	}
	groups[primaryGroup] = struct{}{}
	for _, rawGroup := range groupIDs {
		group, err := parseUnixID(rawGroup, "supplementary group", username)
		if err != nil {
			return unixServiceIdentity{}, err
		}
		groups[group] = struct{}{}
	}
	return unixServiceIdentity{uid: uid, groups: groups}, nil
}

func parseUnixID(value, label, username string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse %s ID %q for system backup service user %q: %w", label, value, username, err)
	}
	return uint32(parsed), nil
}

func validateUnixExecutable(path string, identity unixServiceIdentity) error {
	if err := validateUnixTraversal(filepath.Dir(path), identity); err != nil {
		return fmt.Errorf("backup executable %s is not reachable: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect backup executable %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("backup executable is not a regular file: %s", path)
	}
	if !unixModeAllows(info, identity, 0o5) {
		return fmt.Errorf("backup executable %s is not readable and executable by the selected run-as user", path)
	}
	return nil
}

func validateUnixDirectory(path, label string, permissions uint32, identity unixServiceIdentity) error {
	if err := validateUnixTraversal(filepath.Dir(path), identity); err != nil {
		return fmt.Errorf("%s %s is not reachable: %w", label, path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s %s does not exist; create it as the run-as user before elevated installation", label, path)
		}
		return fmt.Errorf("inspect %s %s: %w", label, path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory: %s", label, path)
	}
	if !unixModeAllows(info, identity, permissions) {
		return fmt.Errorf("%s %s does not grant the selected run-as user the required mode permissions %03o", label, path, permissions)
	}
	return nil
}

func validateUnixTraversal(directory string, identity unixServiceIdentity) error {
	for current := filepath.Clean(directory); ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err != nil {
			return fmt.Errorf("inspect parent directory %s: %w", current, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("path component is not a directory: %s", current)
		}
		if !unixModeAllows(info, identity, 0o1) {
			return fmt.Errorf("parent directory %s is not traversable by the selected run-as user", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func unixModeAllows(info os.FileInfo, identity unixServiceIdentity, required uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	permissions := uint32(info.Mode().Perm())
	available := permissions & 0o7
	if stat.Uid == identity.uid {
		available = permissions >> 6 & 0o7
	} else if _, inGroup := identity.groups[stat.Gid]; inGroup {
		available = permissions >> 3 & 0o7
	}
	return available&required == required
}
