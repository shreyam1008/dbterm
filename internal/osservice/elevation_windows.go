//go:build windows

package osservice

import "golang.org/x/sys/windows"

func platformIsElevated() (bool, error) {
	return windows.GetCurrentProcessToken().IsElevated(), nil
}
