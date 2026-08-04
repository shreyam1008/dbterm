//go:build linux

package folderpicker

import (
	"errors"
	"strings"
	"testing"
)

func TestLinuxPickerCandidatesDetectsHeadlessSession(t *testing.T) {
	_, err := linuxPickerCandidates("", func(string) string { return "" }, func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "type the destination") {
		t.Fatalf("error = %v, want friendly headless error", err)
	}
}

func TestLinuxPickerCandidatesPreferZenityThenKdialog(t *testing.T) {
	candidates, err := linuxPickerCandidates("", func(name string) string {
		if name == "DISPLAY" {
			return ":0"
		}
		return ""
	}, func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	})
	if err != nil {
		t.Fatalf("picker candidates: %v", err)
	}
	if len(candidates) != 2 || !strings.HasSuffix(candidates[0].name, "zenity") || !strings.HasSuffix(candidates[1].name, "kdialog") {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestLinuxPickerCandidatesExplainsMissingUtilities(t *testing.T) {
	_, err := linuxPickerCandidates("", func(name string) string {
		if name == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}, func(string) (string, error) {
		return "", errors.New("missing")
	})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "zenity or kdialog") {
		t.Fatalf("error = %v, want utility guidance", err)
	}
}
