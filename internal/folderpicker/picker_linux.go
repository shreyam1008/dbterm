//go:build linux

package folderpicker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Choose opens zenity or kdialog without passing the path through a shell.
func Choose(ctx context.Context, initialFolder string) (string, error) {
	candidates, err := linuxPickerCandidates(initialFolder, os.Getenv, exec.LookPath)
	if err != nil {
		return "", err
	}
	return executePicker(ctx, candidates)
}

type getenvFunc func(string) string
type lookPathFunc func(string) (string, error)

func linuxPickerCandidates(initialFolder string, getenv getenvFunc, lookPath lookPathFunc) ([]pickerCommand, error) {
	if strings.TrimSpace(getenv("DISPLAY")) == "" && strings.TrimSpace(getenv("WAYLAND_DISPLAY")) == "" {
		return nil, fmt.Errorf("%w: no graphical display was detected; type the destination path instead", ErrUnavailable)
	}
	initialFolder = cleanInitialFolder(initialFolder)
	var candidates []pickerCommand
	if executable, err := lookPath("zenity"); err == nil {
		args := []string{"--file-selection", "--directory", "--title=Choose backup destination"}
		if initialFolder != "" {
			args = append(args, "--filename="+initialFolder+string(filepath.Separator))
		}
		candidates = append(candidates, pickerCommand{name: executable, args: args})
	}
	if executable, err := lookPath("kdialog"); err == nil {
		args := []string{"--getexistingdirectory", initialFolder, "--title", "Choose backup destination"}
		candidates = append(candidates, pickerCommand{name: executable, args: args})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: install zenity or kdialog, or type the destination path", ErrUnavailable)
	}
	return candidates, nil
}
