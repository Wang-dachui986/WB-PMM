//go:build !unix

package fsutil

import (
	"fmt"
	"os"
)

func openReadNoFollow(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("拒绝读取非普通文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		f.Close()
		return nil, fmt.Errorf("读取目标已变化")
	}
	return f, nil
}

// DeviceID 在非 Unix 平台没有设备号概念，返回 ok=false 由调用方按默认处理。
func DeviceID(os.FileInfo) (uint64, bool) { return 0, false }

// FileOwner 在非 Unix 平台没有 uid/gid 概念，返回 ok=false 由调用方降级。
func FileOwner(os.FileInfo) (int, int, bool) { return 0, 0, false }

// PermBitsEnforced 在非 Unix 平台恒为 false：Windows 用 ACL 而非 mode 位，
// 传给 os.OpenFile 的 0600 不会被内核强制，因此权限断言在该平台无意义。
func PermBitsEnforced() bool { return false }
