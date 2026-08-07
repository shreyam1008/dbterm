package main

import "testing"

func TestNormalizedSudoInvokerDetectsOnlyInteractiveSudoRoot(t *testing.T) {
	tests := []struct {
		name     string
		euid     int
		sudoUser string
		want     string
	}{
		{"sudo user", 0, " alice ", "alice"},
		{"ordinary user", 1000, "alice", ""},
		{"direct root", 0, "", ""},
		{"root through root", 0, "root", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizedSudoInvoker(test.euid, test.sudoUser); got != test.want {
				t.Fatalf("normalizedSudoInvoker(%d, %q) = %q, want %q", test.euid, test.sudoUser, got, test.want)
			}
		})
	}
}
