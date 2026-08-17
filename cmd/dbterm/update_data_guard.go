package main

import (
	"fmt"
	"os"
	"strings"
)

type updateDataGuard struct {
	profile string
	files   map[string]bool
}

func captureUpdateDataGuard(sudoInvoker string) (*updateDataGuard, error) {
	profile, paths, err := updateDataPaths(strings.TrimSpace(sudoInvoker))
	if err != nil {
		return nil, err
	}
	guard := &updateDataGuard{profile: profile, files: make(map[string]bool, len(paths))}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		_, err := os.Lstat(path)
		switch {
		case err == nil:
			guard.files[path] = true
		case os.IsNotExist(err):
			guard.files[path] = false
		default:
			return nil, fmt.Errorf("inspect protected dbterm data %s: %w", path, err)
		}
	}
	return guard, nil
}

func (g *updateDataGuard) verify() error {
	if g == nil {
		return nil
	}
	for path, existed := range g.files {
		if !existed {
			continue
		}
		if _, err := os.Lstat(path); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("protected user data disappeared during update: %s", path)
			}
			return fmt.Errorf("verify protected dbterm data %s: %w", path, err)
		}
	}
	return nil
}

func (g *updateDataGuard) profilePath() string {
	if g == nil {
		return ""
	}
	return g.profile
}
