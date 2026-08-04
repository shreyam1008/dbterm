//go:build windows

package osservice

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// writeSecureSystemTaskTempFile keeps an elevated task definition out of the
// run-as user's writable log directory. Otherwise that user could replace the
// XML between creation and schtasks registration and gain LocalSystem code
// execution.
func writeSecureSystemTaskTempFile(data []byte) (string, func(), error) {
	windowsDirectory, err := windows.GetWindowsDirectory()
	if err != nil {
		return "", nil, fmt.Errorf("resolve Windows directory for secure task definition: %w", err)
	}
	return writeSecureSystemTaskTempFileIn(filepath.Join(windowsDirectory, "Temp"), data)
}

func writeSecureSystemTaskTempFileIn(parent string, data []byte) (string, func(), error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", nil, fmt.Errorf("resolve elevated user SID for secure task definition: %w", err)
	}
	callerSID := tokenUser.User.Sid.String()
	securityDescriptor, err := windows.SecurityDescriptorFromString(
		"O:" + callerSID + "D:P" +
			"(A;OICI;GA;;;" + callerSID + ")" +
			"(A;OICI;GA;;;SY)" +
			"(A;OICI;GA;;;BA)",
	)
	if err != nil {
		return "", nil, fmt.Errorf("build private ACL for secure task definition: %w", err)
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: securityDescriptor,
	}

	var directory string
	for attempt := 0; attempt < 20; attempt++ {
		suffix := make([]byte, 16)
		if _, err := rand.Read(suffix); err != nil {
			return "", nil, fmt.Errorf("generate secure task-definition directory name: %w", err)
		}
		directory = filepath.Join(parent, ".dbterm-backup-task-"+hex.EncodeToString(suffix))
		pathPointer, err := windows.UTF16PtrFromString(directory)
		if err != nil {
			return "", nil, fmt.Errorf("encode secure task-definition directory: %w", err)
		}
		err = windows.CreateDirectory(pathPointer, &attributes)
		if err == nil {
			break
		}
		if err != windows.ERROR_ALREADY_EXISTS {
			return "", nil, fmt.Errorf("create secure task-definition directory in %s: %w", parent, err)
		}
		directory = ""
	}
	if directory == "" {
		return "", nil, fmt.Errorf("create unique secure task-definition directory in %s after 20 attempts", parent)
	}

	path := filepath.Join(directory, "task.xml")
	cleanup := func() {
		_ = os.Remove(path)
		_ = os.Remove(directory)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create secure temporary task definition %s: %w", path, err)
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			cleanup()
		}
	}()
	if _, err := file.Write(data); err != nil {
		return "", nil, fmt.Errorf("write secure temporary task definition %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return "", nil, fmt.Errorf("sync secure temporary task definition %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return "", nil, fmt.Errorf("close secure temporary task definition %s: %w", path, err)
	}
	ok = true
	return path, cleanup, nil
}
