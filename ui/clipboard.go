package ui

import (
	"fmt"
	"os/exec"
	"strings"
)

type clipboardCommand struct {
	name string
	args []string
}

var clipboardWriteCommands = []clipboardCommand{
	{name: "wl-copy"},
	{name: "xclip", args: []string{"-selection", "clipboard"}},
	{name: "xsel", args: []string{"--clipboard", "--input"}},
	{name: "pbcopy"},
	{name: "clip.exe"},
}

var clipboardReadCommands = []clipboardCommand{
	{name: "wl-paste", args: []string{"--no-newline"}},
	{name: "xclip", args: []string{"-selection", "clipboard", "-o"}},
	{name: "xsel", args: []string{"--clipboard", "--output"}},
	{name: "pbpaste"},
	{name: "powershell.exe", args: []string{"-NoProfile", "-Command", "Get-Clipboard -Raw"}},
	{name: "pwsh", args: []string{"-NoProfile", "-Command", "Get-Clipboard -Raw"}},
	{name: "powershell", args: []string{"-NoProfile", "-Command", "Get-Clipboard -Raw"}},
}

func copyToClipboard(value string) error {
	var lastErr error
	for _, candidate := range clipboardWriteCommands {
		if _, err := exec.LookPath(candidate.name); err != nil {
			continue
		}
		cmd := exec.Command(candidate.name, candidate.args...)
		cmd.Stdin = strings.NewReader(value)
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("clipboard command failed: %w", lastErr)
	}
	return fmt.Errorf("no clipboard utility found (install wl-clipboard, xclip, or xsel)")
}

func readFromClipboard() (string, error) {
	var lastErr error
	for _, candidate := range clipboardReadCommands {
		if _, err := exec.LookPath(candidate.name); err != nil {
			continue
		}
		output, err := exec.Command(candidate.name, candidate.args...).Output()
		if err == nil {
			return trimClipboardLineEnding(string(output)), nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", fmt.Errorf("clipboard command failed: %w", lastErr)
	}
	return "", fmt.Errorf("no clipboard reader found (install wl-clipboard, xclip, or xsel)")
}

func trimClipboardLineEnding(value string) string {
	return strings.TrimRight(value, "\r\n")
}

func (a *App) copyValue(value string) error {
	err := copyToClipboard(value)
	if a != nil {
		a.copiedCellValue = value
		a.hasCopiedCellValue = true
		a.copiedCellSystem = err == nil
	}
	return err
}

func (a *App) clipboardValue() (string, error) {
	if a != nil && a.hasCopiedCellValue && !a.copiedCellSystem {
		return a.copiedCellValue, nil
	}
	value, err := readFromClipboard()
	if err == nil {
		return value, nil
	}
	if a != nil && a.hasCopiedCellValue {
		return a.copiedCellValue, nil
	}
	return "", err
}
