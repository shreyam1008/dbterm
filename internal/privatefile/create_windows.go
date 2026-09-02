//go:build windows

package privatefile

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func create(path string) (*os.File, error) {
	descriptor, callerSID, err := privateSecurityDescriptor(false)
	if err != nil {
		return nil, err
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode private file path: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		&attributes,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	if err := verifyPrivateHandle(handle, callerSID, true); err != nil {
		_ = windows.CloseHandle(handle)
		_ = os.Remove(path)
		return nil, fmt.Errorf("verify private file ACL: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("create private file handle")
	}
	return file, nil
}

func protect(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("private file must be a regular file, not a symbolic link: %s", path)
	}
	return protectNamedPath(path, false)
}

func createPrivateDirectory(path string) error {
	descriptor, callerSID, err := privateSecurityDescriptor(true)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode private directory path: %w", err)
	}
	if err := windows.CreateDirectory(pathPointer, &attributes); err != nil {
		return err
	}
	if err := verifyPrivateNamedPath(path, callerSID, true); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("verify private directory ACL: %w", err)
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	err := createPrivateDirectory(path)
	if err == nil {
		return nil
	}
	if !os.IsExist(err) {
		return err
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return statErr
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("private directory must be a real directory, not a symbolic link: %s", path)
	}
	return protectNamedPath(path, true)
}

func privateSecurityDescriptor(inheritToChildren bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve current user for private ACL: %w", err)
	}
	callerSID := tokenUser.User.Sid
	aceFlags := ""
	if inheritToChildren {
		aceFlags = "OICI"
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + callerSID.String() + "D:P" +
			"(A;" + aceFlags + ";GA;;;" + callerSID.String() + ")" +
			"(A;" + aceFlags + ";GA;;;SY)" +
			"(A;" + aceFlags + ";GA;;;BA)",
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build private ACL: %w", err)
	}
	return descriptor, callerSID, nil
}

func protectNamedPath(path string, directory bool) error {
	descriptor, callerSID, err := privateSecurityDescriptor(directory)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read private ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("apply private ACL: %w", err)
	}
	if err := verifyPrivateNamedPath(path, callerSID, false); err != nil {
		return fmt.Errorf("verify private ACL: %w", err)
	}
	return nil
}

func verifyPrivateHandle(handle windows.Handle, callerSID *windows.SID, requireOwner bool) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return verifyPrivateDescriptor(descriptor, callerSID, requireOwner)
}

func verifyPrivateNamedPath(path string, callerSID *windows.SID, requireOwner bool) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return verifyPrivateDescriptor(descriptor, callerSID, requireOwner)
}

func verifyPrivateDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, callerSID *windows.SID, requireOwner bool) error {
	if descriptor == nil {
		return fmt.Errorf("filesystem returned no security descriptor")
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("filesystem did not preserve the protected private DACL")
	}
	if requireOwner {
		owner, _, ownerErr := descriptor.Owner()
		if ownerErr != nil || owner == nil || !owner.Equals(callerSID) {
			return fmt.Errorf("filesystem did not preserve current-user ownership")
		}
	}
	return verifyPrivatePrincipals(descriptor, callerSID)
}

func verifyPrivatePrincipals(descriptor *windows.SECURITY_DESCRIPTOR, callerSID *windows.SID) error {
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return fmt.Errorf("filesystem returned an empty private DACL")
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve LocalSystem SID: %w", err)
	}
	administratorsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("resolve Administrators SID: %w", err)
	}
	required := []*windows.SID{callerSID, systemSID, administratorsSID}
	found := make([]bool, len(required))
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return fmt.Errorf("read private DACL entry %d: %w", index, err)
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("private DACL contains an unexpected entry type")
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !aceSID.IsValid() {
			return fmt.Errorf("private DACL contains an invalid principal")
		}
		matched := false
		for requiredIndex, requiredSID := range required {
			if aceSID.Equals(requiredSID) {
				found[requiredIndex] = true
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("private DACL grants access to unexpected principal %s", aceSID.String())
		}
	}
	for index, present := range found {
		if !present {
			return fmt.Errorf("filesystem omitted required private principal %s", required[index].String())
		}
	}
	return nil
}
