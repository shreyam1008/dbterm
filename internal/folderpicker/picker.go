// Package folderpicker opens the host desktop's native folder chooser when one
// is available. Callers must always keep a typed-path fallback: servers and
// minimal desktop installs commonly have no graphical session or picker tool.
package folderpicker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var (
	// ErrUnavailable means no usable graphical folder chooser is available.
	ErrUnavailable = errors.New("graphical folder chooser is unavailable")
	// ErrCancelled means the chooser was closed without selecting a folder.
	ErrCancelled = errors.New("folder selection was canceled")
)

type pickerCommand struct {
	name string
	args []string
	env  []string
}

func executePicker(ctx context.Context, candidates []pickerCommand) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(candidates) == 0 {
		return "", ErrUnavailable
	}

	var failures []error
	for _, candidate := range candidates {
		command := exec.CommandContext(ctx, candidate.name, candidate.args...)
		if candidate.env != nil {
			command.Env = candidate.env
		}
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if err == nil {
			selection, parseErr := parseSelection(output)
			if parseErr != nil {
				return "", parseErr
			}
			return selection, nil
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", ErrCancelled
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		failures = append(failures, fmt.Errorf("%s: %s", candidate.name, detail))
	}

	return "", fmt.Errorf("%w: %v", ErrUnavailable, errors.Join(failures...))
}

func parseSelection(output []byte) (string, error) {
	selection := strings.TrimRight(string(output), "\r\n")
	if selection == "" {
		return "", ErrCancelled
	}
	if strings.ContainsRune(selection, '\x00') {
		return "", fmt.Errorf("folder chooser returned an invalid path")
	}
	return selection, nil
}

func cleanInitialFolder(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return path
	}
	return ""
}

func environmentWithValue(environment []string, key, value string) []string {
	prefix := strings.ToUpper(key) + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if strings.HasPrefix(strings.ToUpper(item), prefix) {
			continue
		}
		result = append(result, item)
	}
	return append(result, key+"="+value)
}
