//go:build unix

package fsutil

import (
	"os"
	"syscall"
)

func openReadNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// DeviceID 返回文件所在文件系统的设备号，用于判断两个路径是否在同一文件系统上。
// 仅 Unix 提供该信息，其余平台返回 ok=false 由调用方按默认处理。
func DeviceID(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

// FileOwner 返回文件的属主 uid/gid。仅 Unix 提供该信息，其余平台返回 ok=false。
func FileOwner(fi os.FileInfo) (int, int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// PermBitsEnforced 报告本平台是否真的按 mode 位限制文件权限。
func PermBitsEnforced() bool { return true }
