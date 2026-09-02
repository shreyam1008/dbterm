package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileSetDefaultsAndStrictGlobMatching(t *testing.T) {
	root := t.TempDir()
	job := runnerSQLiteJob(t.TempDir(), "files_{run}", "job_fileset_defaults")
	job.FileSets = []FileSet{{Label: "uploads", Root: root, Required: true}}
	if err := job.ApplyDefaults(testNow()); err != nil {
		t.Fatal(err)
	}
	if len(job.FileSets[0].Include) != 1 || job.FileSets[0].Include[0] != "**" || !filepath.IsAbs(job.FileSets[0].Root) {
		t.Fatalf("file-set defaults = %+v", job.FileSets[0])
	}
	tests := []struct {
		pattern string
		name    string
		match   bool
	}{
		{"**", "top.txt", true},
		{"**/*.txt", "top.txt", true},
		{"**/*.txt", "nested/deep/file.txt", true},
		{"assets/**", "assets/image/logo.png", true},
		{"assets/*", "assets/image/logo.png", false},
		{"config?.json", "config1.json", true},
	}
	for _, test := range tests {
		if got := matchFileSetGlob(test.pattern, test.name); got != test.match {
			t.Errorf("matchFileSetGlob(%q, %q) = %v, want %v", test.pattern, test.name, got, test.match)
		}
	}
}

func TestFileSetValidationRejectsTraversalAmbiguityAndPortableCollisions(t *testing.T) {
	root := t.TempDir()
	base := runnerSQLiteJob(t.TempDir(), "files_{run}", "job_fileset_invalid")
	tests := []struct {
		name     string
		fileSets []FileSet
		want     string
	}{
		{"traversal", []FileSet{{Label: "data", Root: root, Include: []string{"../secret"}}}, "parent"},
		{"backslash", []FileSet{{Label: "data", Root: root, Include: []string{`nested\*.txt`}}}, "portable"},
		{"partial doublestar", []FileSet{{Label: "data", Root: root, Include: []string{"foo**bar"}}}, "complete path segment"},
		{"unsafe label", []FileSet{{Label: "../data", Root: root, Include: []string{"**"}}}, "label"},
		{"case collision", []FileSet{{Label: "Data", Root: root, Include: []string{"**"}}, {Label: "data", Root: root, Include: []string{"**"}}}, "duplicated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := base
			job.FileSets = test.fileSets
			err := job.ApplyDefaults(testNow())
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("ApplyDefaults() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestFileSetScannerAppliesIncludeThenExclude(t *testing.T) {
	root := t.TempDir()
	writeFileSetTestFile(t, root, "top.txt", "top")
	writeFileSetTestFile(t, root, "nested/keep.txt", "keep")
	writeFileSetTestFile(t, root, "nested/skip.txt", "skip")
	writeFileSetTestFile(t, root, "nested/other.log", "log")
	set := FileSet{Label: "data", Root: root, Include: []string{"**/*.txt"}, Exclude: []string{"nested/skip.*"}, Required: true}
	candidates, err := scanFileSetCandidates(context.Background(), root, set)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, candidate := range candidates {
		names = append(names, candidate.relativePath)
	}
	if got := strings.Join(names, ","); got != "nested/keep.txt,top.txt" {
		t.Fatalf("selected paths = %q", got)
	}
}

func TestFileSetScannerRefusesSymlinkEvenWhenExcluded(t *testing.T) {
	root := t.TempDir()
	target := writeFileSetTestFile(t, root, "real.txt", "real")
	link := filepath.Join(root, "ignored-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	set := FileSet{Label: "data", Root: root, Include: []string{"**/*.txt"}, Exclude: []string{"ignored-*"}}
	_, err := scanFileSetCandidates(context.Background(), root, set)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
		t.Fatalf("symlink scan error = %v", err)
	}

	linkedRoot := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(root, linkedRoot); err != nil {
		t.Skipf("root symbolic links unavailable: %v", err)
	}
	_, err = resolveExactFileSetRoot(linkedRoot)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symbolic link") {
		t.Fatalf("linked root error = %v", err)
	}
}

func TestFileSetSnapshotDetectsChangeAfterScan(t *testing.T) {
	root := t.TempDir()
	path := writeFileSetTestFile(t, root, "changing.txt", "before")
	set := FileSet{Label: "data", Root: root, Include: []string{"**"}, Required: true}
	candidates, err := scanFileSetCandidates(context.Background(), root, set)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("scan = %+v, %v", candidates, err)
	}
	if err := os.WriteFile(path, []byte("after and a different size"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = stageFileSetCandidate(context.Background(), t.TempDir(), candidates[0])
	if err == nil || !strings.Contains(err.Error(), "changed after scan") {
		t.Fatalf("changed-file error = %v", err)
	}
}

func TestFileSetRequiredFailureAndOptionalWarning(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	stage := t.TempDir()
	required := FileSet{Label: "required", Root: missing, Include: []string{"**"}, Required: true}
	if _, _, _, err := prepareJobFileSets(context.Background(), []FileSet{required}, stage); err == nil || !strings.Contains(err.Error(), "required file set") {
		t.Fatalf("required missing root error = %v", err)
	}
	optional := FileSet{Label: "optional", Root: missing, Include: []string{"**"}}
	prepared, summaries, warnings, err := prepareJobFileSets(context.Background(), []FileSet{optional}, stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 0 || len(summaries) != 1 || summaries[0].Consistency != "omitted" || len(warnings) != 1 {
		t.Fatalf("optional result = prepared=%+v summaries=%+v warnings=%v", prepared, summaries, warnings)
	}
	if strings.Contains(warnings[0], missing) {
		t.Fatalf("portable optional warning leaked the absolute root: %q", warnings[0])
	}
}

func writeFileSetTestFile(t *testing.T, root, relative, contents string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testNow() time.Time {
	return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
}
