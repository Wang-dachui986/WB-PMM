//go:build windows

package fsutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivateACLAndStrictWrite(t *testing.T) {
	if got := dirOf(`C:\config.json`); got != `C:\` {
		t.Fatalf("Windows drive root parent = %q", got)
	}
	root := t.TempDir()
	if err := SecurePrivateACL(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := WriteFileAtomicStrict(context.Background(), path, []byte("synthetic"), 0o600, nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateACL(path); err != nil {
		t.Fatal(err)
	}
	wide := filepath.Join(root, "wide.json")
	if err := os.WriteFile(wide, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.String() + ")(A;;GR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(wide, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateACL(wide); err == nil {
		t.Fatal("accepted an Everyone read ACE")
	}
	if err := WriteFileAtomicStrict(context.Background(), wide, []byte("new"), 0o600, nil); err == nil {
		t.Fatal("overwrote a file with a broad ACL")
	}
}
