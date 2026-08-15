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

func TestShouldUseInvokerProfile(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "interactive TUI", want: true},
		{name: "connection backup", args: []string{"backup", "run", "nightly"}, want: true},
		{name: "user backup service", args: []string{"backup", "service", "status", "--user"}, want: true},
		{name: "system backup service", args: []string{"backup", "service", "status", "--system"}, want: false},
		{name: "system scope value", args: []string{"backup", "service", "install", "--scope", "system"}, want: false},
		{name: "sudo connection recovery", args: []string{"connections", "recover-sudo"}, want: false},
		{name: "update", args: []string{"--update"}, want: false},
		{name: "uninstall", args: []string{"uninstall"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldUseInvokerProfile(test.args); got != test.want {
				t.Fatalf("shouldUseInvokerProfile(%q) = %t, want %t", test.args, got, test.want)
			}
		})
	}
}
