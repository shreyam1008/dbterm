//go:build windows

package privatefile

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCreateInstallsProtectedWindowsDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-file")
	file, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	assertWindowsPrivateDACL(t, path, true)
}

func TestPrivateDirectoryDACLInheritanceAndExistingFileProtection(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "owned")
	if err := os.Mkdir(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	setBroadWindowsDACL(t, directory, true)
	if err := EnsurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	assertWindowsPrivateDACL(t, directory, true)

	inherited := filepath.Join(directory, "inherited-secret")
	if err := os.WriteFile(inherited, []byte("secret"), 0o666); err != nil {
		t.Fatal(err)
	}
	assertWindowsPrivateDACL(t, inherited, false)

	existing := filepath.Join(parent, "existing-secret")
	if err := os.WriteFile(existing, []byte("secret"), 0o666); err != nil {
		t.Fatal(err)
	}
	setBroadWindowsDACL(t, existing, false)
	if err := Protect(existing); err != nil {
		t.Fatal(err)
	}
	assertWindowsPrivateDACL(t, existing, true)

	temporary, err := CreateTempDirectory(directory, "stage-")
	if err != nil {
		t.Fatal(err)
	}
	assertWindowsPrivateDACL(t, temporary, true)
}

func TestVerifyPrivateDescriptorAcceptsCanonicalWellKnownCallerSID(t *testing.T) {
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:SYD:P(A;;GA;;;SY)(A;;GA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateDescriptor(descriptor, systemSID, true); err != nil {
		t.Fatalf("LocalSystem caller SID should match its canonical SY ACE: %v", err)
	}
}

func setBroadWindowsDACL(t *testing.T, path string, directory bool) {
	t.Helper()
	flags := ""
	if directory {
		flags = "OICI"
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;" + flags + ";GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func assertWindowsPrivateDACL(t *testing.T, path string, requireProtected bool) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if requireProtected {
		if err := verifyPrivateDescriptor(descriptor, tokenUser.User.Sid, false); err != nil {
			t.Fatalf("private path DACL is invalid: %v (%s)", err, descriptor.String())
		}
	} else {
		if err := verifyPrivatePrincipals(descriptor, tokenUser.User.Sid); err != nil {
			t.Fatalf("inherited private path DACL is invalid: %v (%s)", err, descriptor.String())
		}
	}
}
