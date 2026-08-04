package backup

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var findRestoreTool = exec.LookPath

// requireClientTool resolves database client binaries in PATH first. launchd
// commonly omits package-manager directories, so macOS also checks stable
// Homebrew and MacPorts locations without invoking a shell.
func requireClientTool(name string) (string, error) {
	if path, err := findRestoreTool(name); err == nil {
		return path, nil
	}
	candidates := clientToolCandidates(runtime.GOOS, name)
	if runtime.GOOS == "windows" {
		candidates = append(candidates, discoverWindowsClientTools(name)...)
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !usableClientToolMode(runtime.GOOS, info.Mode()) {
			continue
		}
		return filepath.Clean(candidate), nil
	}
	return "", fmt.Errorf("%s was not found; install the matching database command-line client and ensure it is available in PATH", name)
}

func usableClientToolMode(goos string, mode os.FileMode) bool {
	if !mode.IsRegular() {
		return false
	}
	// Windows does not expose a meaningful Unix execute bit for ordinary .exe
	// files. Their extension and the OS loader determine executability.
	return goos == "windows" || mode.Perm()&0o111 != 0
}

func discoverWindowsClientTools(name string) []string {
	executable := name
	if !strings.HasSuffix(strings.ToLower(executable), ".exe") {
		executable += ".exe"
	}
	roots := []string{
		os.Getenv("ProgramW6432"),
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
	}
	seen := make(map[string]struct{})
	var matches []string
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		for _, pattern := range windowsClientToolPatterns(root, name, executable) {
			found, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			for _, candidate := range found {
				key := strings.ToLower(filepath.Clean(candidate))
				if _, duplicate := seen[key]; duplicate {
					continue
				}
				seen[key] = struct{}{}
				matches = append(matches, candidate)
			}
		}
	}
	var direct []string
	if chocolatey := strings.TrimSpace(os.Getenv("ChocolateyInstall")); chocolatey != "" {
		direct = append(direct, filepath.Join(chocolatey, "bin", executable))
	}
	if profile := strings.TrimSpace(os.Getenv("USERPROFILE")); profile != "" {
		direct = append(direct, filepath.Join(profile, "scoop", "shims", executable))
	}
	for _, candidate := range direct {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			key := strings.ToLower(filepath.Clean(candidate))
			if _, duplicate := seen[key]; !duplicate {
				seen[key] = struct{}{}
				matches = append(matches, candidate)
			}
		}
	}
	// Versioned installation folders sort newest-first with digit runs compared
	// numerically (PostgreSQL 17 must win over 9.6). Every match remains as a
	// fallback if the preferred installation disappears before use.
	sort.SliceStable(matches, func(i, j int) bool {
		return naturalPathCompare(strings.ToLower(matches[i]), strings.ToLower(matches[j])) > 0
	})
	return matches
}

func naturalPathCompare(left, right string) int {
	for leftIndex, rightIndex := 0, 0; leftIndex < len(left) || rightIndex < len(right); {
		if leftIndex >= len(left) {
			return -1
		}
		if rightIndex >= len(right) {
			return 1
		}
		leftDigit := left[leftIndex] >= '0' && left[leftIndex] <= '9'
		rightDigit := right[rightIndex] >= '0' && right[rightIndex] <= '9'
		if leftDigit && rightDigit {
			leftEnd := leftIndex
			for leftEnd < len(left) && left[leftEnd] >= '0' && left[leftEnd] <= '9' {
				leftEnd++
			}
			rightEnd := rightIndex
			for rightEnd < len(right) && right[rightEnd] >= '0' && right[rightEnd] <= '9' {
				rightEnd++
			}
			leftNumber := strings.TrimLeft(left[leftIndex:leftEnd], "0")
			rightNumber := strings.TrimLeft(right[rightIndex:rightEnd], "0")
			if leftNumber == "" {
				leftNumber = "0"
			}
			if rightNumber == "" {
				rightNumber = "0"
			}
			if len(leftNumber) != len(rightNumber) {
				if len(leftNumber) > len(rightNumber) {
					return 1
				}
				return -1
			}
			if leftNumber != rightNumber {
				if leftNumber > rightNumber {
					return 1
				}
				return -1
			}
			leftIndex, rightIndex = leftEnd, rightEnd
			continue
		}
		if left[leftIndex] != right[rightIndex] {
			if left[leftIndex] > right[rightIndex] {
				return 1
			}
			return -1
		}
		leftIndex++
		rightIndex++
	}
	return 0
}

func windowsClientToolPatterns(root, name, executable string) []string {
	switch name {
	case "psql", "pg_restore", "pg_dump":
		return []string{filepath.Join(root, "PostgreSQL", "*", "bin", executable)}
	case "mysql", "mysqldump":
		return []string{
			filepath.Join(root, "MySQL", "MySQL Server *", "bin", executable),
			filepath.Join(root, "MariaDB *", "bin", executable),
		}
	case "sqlite3":
		return []string{
			filepath.Join(root, "SQLite", executable),
			filepath.Join(root, "sqlite-tools*", executable),
		}
	default:
		return nil
	}
}

func clientToolCandidates(goos, name string) []string {
	if goos != "darwin" {
		return nil
	}
	candidates := []string{
		path.Join("/opt/homebrew/bin", name),
		path.Join("/usr/local/bin", name),
		path.Join("/opt/local/bin", name),
	}
	switch name {
	case "psql", "pg_restore", "pg_dump":
		candidates = append(candidates,
			path.Join("/opt/homebrew/opt/libpq/bin", name),
			path.Join("/usr/local/opt/libpq/bin", name),
		)
	case "mysql", "mysqldump":
		candidates = append(candidates,
			path.Join("/opt/homebrew/opt/mysql-client/bin", name),
			path.Join("/usr/local/opt/mysql-client/bin", name),
		)
	case "sqlite3":
		candidates = append(candidates,
			path.Join("/opt/homebrew/opt/sqlite/bin", name),
			path.Join("/usr/local/opt/sqlite/bin", name),
		)
	}
	return candidates
}
