package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCLIFileSetDefaultsToRequiredRecursiveCapture(t *testing.T) {
	root := filepath.Join(t.TempDir(), "photos")
	set, err := buildCLIFileSet("profile-photos", root, nil, []string{"**/*.tmp"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if set.Label != "profile-photos" || set.Root != filepath.Clean(root) || !set.Required {
		t.Fatalf("file set = %#v", set)
	}
	if len(set.Include) != 1 || set.Include[0] != "**" {
		t.Fatalf("default includes = %#v", set.Include)
	}
	if len(set.Exclude) != 1 || set.Exclude[0] != "**/*.tmp" {
		t.Fatalf("excludes = %#v", set.Exclude)
	}
}

func TestBuildCLIFileSetRejectsUnsafePatternsAndLabels(t *testing.T) {
	root := filepath.Join(t.TempDir(), "files")
	for _, test := range []struct {
		name     string
		label    string
		includes []string
		want     string
	}{
		{name: "traversal", label: "files", includes: []string{"../secret"}, want: "parent"},
		{name: "nonportable label", label: "profile photos", includes: []string{"**"}, want: "label"},
		{name: "embedded double star", label: "files", includes: []string{"images/**.jpg"}, want: "double-star"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildCLIFileSet(test.label, root, test.includes, nil, true)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildCLIFileSet() error = %v, want %q", err, test.want)
			}
		})
	}
}
