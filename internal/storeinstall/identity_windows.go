//go:build windows

package storeinstall

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Managed() bool {
	var length uint32
	result, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetCurrentPackageFullName").Call(uintptr(unsafe.Pointer(&length)), 0)
	if result == 122 && length > 0 { // ERROR_INSUFFICIENT_BUFFER means identity exists.
		return true
	}
	executable, _ := os.Executable()
	return IsPackagePath(executable)
}
