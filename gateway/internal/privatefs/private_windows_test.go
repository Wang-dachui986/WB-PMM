//go:build windows

package privatefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsPrivateAccountWrite(t *testing.T) {
	root := t.TempDir()
	if err := SecurePrivateACL(root); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "auths")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "workbuddy-synthetic.json")
	if err := WriteAtomic(path, []byte(`{"synthetic":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateACL(path); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != `{"synthetic":true}` {
		t.Fatalf("read private account: %q, %v", raw, err)
	}
}
