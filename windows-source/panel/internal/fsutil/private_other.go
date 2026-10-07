//go:build !windows

package fsutil

import "os"

func CheckPrivateACL(string) error  { return nil }
func SecurePrivateACL(string) error { return nil }
func EnsurePrivateDir(path string) error {
	return os.MkdirAll(path, 0o700)
}
