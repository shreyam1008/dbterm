//go:build windows

package ui

func inAppUpdateCommand(executable, version string) (string, []string, bool, error) {
	return executable, []string{"--update", version}, false, nil
}
