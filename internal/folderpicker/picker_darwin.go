//go:build darwin

package folderpicker

import (
	"context"
	"fmt"
	"os/exec"
)

const appleScriptFolderPicker = `on run argv
  try
    if (count of argv) > 0 and item 1 of argv is not "" then
      set chosenFolder to choose folder with prompt "Choose backup destination" default location (POSIX file (item 1 of argv))
    else
      set chosenFolder to choose folder with prompt "Choose backup destination"
    end if
    return POSIX path of chosenFolder
  on error number -128
    error number -128
  end try
end run`

// Choose opens the macOS folder chooser through a static AppleScript. The
// initial path is a distinct argv value and is never interpolated into script.
func Choose(ctx context.Context, initialFolder string) (string, error) {
	executable, err := exec.LookPath("osascript")
	if err != nil {
		return "", fmt.Errorf("%w: osascript was not found; type the destination path", ErrUnavailable)
	}
	return executePicker(ctx, []pickerCommand{{
		name: executable,
		args: []string{"-e", appleScriptFolderPicker, "--", cleanInitialFolder(initialFolder)},
	}})
}
