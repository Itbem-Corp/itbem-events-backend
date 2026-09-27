//go:build windows

package automationagent

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// UserCacheDir maps to LocalAppData on Windows. Roaming AppData is avoided so
// a synced profile cannot accidentally give two physical machines one ID.
func defaultMachineIdentityBaseDirectory() (string, error) { return os.UserCacheDir() }

func secureMachineIdentityDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("machine identity path is not a local directory")
	}
	return setMachineIdentityACL(path, true)
}

func secureMachineIdentityFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("machine identity path is not a regular file")
	}
	return setMachineIdentityACL(path, false)
}

func setMachineIdentityACL(path string, inherit bool) error {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || tokenUser == nil || tokenUser.User.Sid == nil {
		return fmt.Errorf("could not resolve current user security identity")
	}
	inheritance := ""
	if inherit {
		inheritance = "OICI"
	}
	securityDescriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;%s;FA;;;%s)", inheritance, tokenUser.User.Sid.String()))
	if err != nil {
		return fmt.Errorf("could not construct private machine identity ACL")
	}
	acl, _, err := securityDescriptor.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("could not construct private machine identity ACL")
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}

// File.Sync flushes the ID before its atomic hard-link is installed. Go does
// not expose a portable directory-handle FlushFileBuffers operation; NTFS
// still guarantees same-volume hard-link creation is atomic.
func syncMachineIdentityDirectory(string) error { return nil }
