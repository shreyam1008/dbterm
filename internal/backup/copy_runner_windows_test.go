//go:build windows

package backup

import (
	"strings"
	"testing"
)

func TestCopyRunnerLocalRefusesCaseAliasOfSameDirectory(t *testing.T) {
	directory := t.TempDir()
	alias := strings.ToUpper(directory)
	if alias == directory {
		alias = strings.ToLower(directory)
	}
	if alias == directory {
		t.Fatal("temporary directory did not contain a character with a case alias")
	}
	if err := ensureDistinctLocalCopyDirectories(directory, alias); err == nil || !strings.Contains(err.Error(), "same physical directory") {
		t.Fatalf("case-alias copy error = %v", err)
	}
}
