//go:build windows

package processinfo

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

var getProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

type windowsProcessMemoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

func readPlatform(pid int) (Metrics, error) {
	metrics := Metrics{PID: pid}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return metrics, nil
		}
		return metrics, fmt.Errorf("open process with query permission: %w", err)
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return metrics, fmt.Errorf("read process exit state: %w", err)
	}
	if exitCode != windowsStillActive {
		return metrics, nil
	}

	nameBuffer := make([]uint16, 32768)
	nameLength := uint32(len(nameBuffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &nameBuffer[0], &nameLength); err != nil {
		return metrics, fmt.Errorf("read process image name: %w", err)
	}
	metrics.Name = filepath.Base(windows.UTF16ToString(nameBuffer[:nameLength]))

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return metrics, fmt.Errorf("read process start time: %w", err)
	}
	metrics.StartTime = time.Unix(0, creation.Nanoseconds())

	counters := windowsProcessMemoryCounters{Size: uint32(unsafe.Sizeof(windowsProcessMemoryCounters{}))}
	result, _, callErr := getProcessMemoryInfo.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.Size),
	)
	if result == 0 {
		if callErr == syscall.Errno(0) {
			callErr = windows.ERROR_INVALID_FUNCTION
		}
		return metrics, fmt.Errorf("read process resident memory: %w", callErr)
	}
	metrics.RSSBytes = uint64(counters.WorkingSetSize)
	metrics.Alive = true
	return metrics, nil
}
