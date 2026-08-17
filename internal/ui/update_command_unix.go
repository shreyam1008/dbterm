//go:build linux || darwin

package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func inAppUpdateCommand(executable, version string) (string, []string, bool, error) {
	if directoryWritableByCurrentUser(filepath.Dir(executable)) {
		return executable, []string{"--update", version}, false, nil
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return "", nil, false, fmt.Errorf("%s is not user-writable and sudo is unavailable; run `sudo dbterm --update %s`", executable, version)
	}
	return sudo, []string{"--", executable, "--update", version}, true, nil
}

func directoryWritableByCurrentUser(directory string) bool {
	if os.Geteuid() == 0 {
		return true
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	mode := info.Mode().Perm()
	if uint32(os.Geteuid()) == stat.Uid {
		return mode&0o200 != 0
	}
	groups, _ := os.Getgroups()
	for _, group := range groups {
		if uint32(group) == stat.Gid {
			return mode&0o020 != 0
		}
	}
	return mode&0o002 != 0
}
