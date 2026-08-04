//go:build windows

package folderpicker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

const powershellFolderPicker = `Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = 'Choose backup destination'
$dialog.ShowNewFolderButton = $true
$initial = [Environment]::GetEnvironmentVariable('DBTERM_FOLDER_PICKER_START')
if ($initial -and [System.IO.Directory]::Exists($initial)) { $dialog.SelectedPath = $initial }
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
  [Console]::Out.Write($dialog.SelectedPath)
  exit 0
}
exit 1`

// Choose opens the Windows Forms folder chooser. The initial path travels in a
// process-local environment variable and is never interpolated into PowerShell.
func Choose(ctx context.Context, initialFolder string) (string, error) {
	var candidates []pickerCommand
	for _, name := range []string{"powershell.exe", "pwsh.exe"} {
		executable, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		candidates = append(candidates, pickerCommand{
			name: executable,
			args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command", powershellFolderPicker},
			env:  environmentWithValue(os.Environ(), "DBTERM_FOLDER_PICKER_START", cleanInitialFolder(initialFolder)),
		})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("%w: PowerShell was not found; type the destination path", ErrUnavailable)
	}
	return executePicker(ctx, candidates)
}
