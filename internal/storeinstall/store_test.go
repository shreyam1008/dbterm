package storeinstall

import "testing"

func TestPackagePath(t *testing.T) {
	for _, path := range []string{`C:\Program Files\WindowsApps\shreyam1008.dbterm_0.11.1.0_x64__ax0kgekbzfne6\dbterm.exe`, `D:/WindowsApps/SHREYAM1008.DBTERM_0.12.0.0_x64__ax0kgekbzfne6/dbterm.exe`} {
		if !IsPackagePath(path) {
			t.Errorf("missed Store path %q", path)
		}
	}
	for _, path := range []string{`C:\tools\dbterm.exe`, `C:\Users\user\AppData\Local\Microsoft\WindowsApps\dbterm.exe`, `C:\WindowsApps\other.dbterm_1.0.0.0\dbterm.exe`} {
		if IsPackagePath(path) {
			t.Errorf("misclassified %q", path)
		}
	}
}
