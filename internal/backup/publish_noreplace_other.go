//go:build !linux && !darwin && !windows

package backup

import "fmt"

func atomicPublicationSupported() error {
	return fmt.Errorf("atomic no-replace backup publication is unsupported on this operating system")
}

func atomicPublishNoReplace(_, _ string) error {
	return atomicPublicationSupported()
}
