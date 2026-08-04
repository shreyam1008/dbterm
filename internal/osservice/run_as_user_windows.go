//go:build windows

package osservice

import "fmt"

func resolveRunAsUser(options Options) (string, error) {
	if options.RunAsUser != "" {
		return "", fmt.Errorf("backup agent run-as user is not supported on Windows; system tasks run as LocalSystem")
	}
	return "", nil
}
