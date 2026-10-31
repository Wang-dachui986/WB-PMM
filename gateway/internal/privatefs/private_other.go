//go:build !windows

package privatefs

import "os"

func CheckPrivateACL(string) error  { return nil }
func SecurePrivateACL(string) error { return nil }
func EnsurePrivateDir(path string) error {
	return os.MkdirAll(path, 0o700)
}

func WriteAtomic(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
