//go:build windows

package privatefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CheckPrivateACL requires a real file or directory owned by this user,
// SYSTEM, or Administrators, with no access granted to any other principal.
func CheckPrivateACL(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("private path is not a regular file or directory")
	}
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return fmt.Errorf("read private ACL: %v", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !trustedSID(owner, user) {
		return fmt.Errorf("private path has an unexpected owner")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 {
		return fmt.Errorf("private path has no restrictive DACL")
	}
	userAllowed := false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil || ace == nil ||
			ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("private ACL contains an unsupported entry")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || !trustedSID(sid, user) {
			return fmt.Errorf("private ACL grants access to another principal")
		}
		if sid.Equals(user) && ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 &&
			(ace.Mask&windows.GENERIC_ALL != 0 ||
				ace.Mask&windows.FILE_GENERIC_READ == windows.FILE_GENERIC_READ &&
					ace.Mask&windows.FILE_GENERIC_WRITE == windows.FILE_GENERIC_WRITE) {
			userAllowed = true
		}
	}
	if !userAllowed {
		return fmt.Errorf("private ACL does not grant this user read and write access")
	}
	return nil
}

func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

func trustedSID(sid, user *windows.SID) bool {
	return sid.Equals(user) || sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

// SecurePrivateACL is used only for newly created empty paths, before secrets
// are written. Existing paths are validated rather than silently repaired.
func SecurePrivateACL(path string) error {
	info, err := os.Lstat(path)
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return fmt.Errorf("new private path is invalid")
	}
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	ownerSD, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION)
	if err != nil || ownerSD == nil {
		return fmt.Errorf("read new path owner: %v", err)
	}
	owner, _, err := ownerSD.Owner()
	if err != nil || owner == nil || !trustedSID(owner, user) {
		return fmt.Errorf("new private path has an unexpected owner")
	}
	flags := ""
	if info.IsDir() {
		flags = "OICI"
	}
	sddl := fmt.Sprintf("D:P(A;%s;GA;;;%s)(A;%s;GA;;;SY)(A;%s;GA;;;BA)",
		flags, user.String(), flags, flags)
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return err
	}
	return CheckPrivateACL(path)
}

// EnsurePrivateDir only creates a direct child of an already private parent.
func EnsurePrivateDir(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return CheckPrivateACL(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := CheckPrivateACL(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err == nil {
		if err := SecurePrivateACL(path); err != nil {
			return err
		}
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	return CheckPrivateACL(path)
}

// WriteAtomic keeps sensitive bytes out of files until their ACL is private.
func WriteAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := CheckPrivateACL(dir); err != nil {
		return err
	}
	if err := CheckPrivateACL(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".workbuddy-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := SecurePrivateACL(f.Name()); err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := CheckPrivateACL(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(f.Name(), path)
}
