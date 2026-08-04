//go:build !linux && !darwin && !windows

package osservice

import (
	"fmt"
	"runtime"
)

func newPlatformManager(Options, commandRunner) (Manager, error) {
	return nil, fmt.Errorf("backup agent service registration is not supported on %s", runtime.GOOS)
}
