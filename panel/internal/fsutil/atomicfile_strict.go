package fsutil

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ReadFileStrict rejects symlinks and special files before reading any content.
func ReadFileStrict(path string, limit int64) ([]byte, error) {
	f, err := openReadNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("拒绝读取非普通文件")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("文件超过大小限制")
	}
	return raw, nil
}

// WriteFileAtomicStrict never truncates an existing file. The caller owns the
// destination lock; beforeCommit can recheck its authorization and target after
// temporary-file IO. Rename failure is an error, including bind mounts.
func WriteFileAtomicStrict(ctx context.Context, path string, raw []byte, perm os.FileMode, beforeCommit func() error) error {
	dir := filepath.Dir(path)
	parent, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !parent.IsDir() {
		return fmt.Errorf("写入目录不能是符号链接或非目录")
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		current, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !current.IsDir() || !os.SameFile(parent, current) {
			return fmt.Errorf("写入目录已变化")
		}
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("写入目标不能是符号链接或非普通文件")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if beforeCommit != nil {
			return beforeCommit()
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".workbuddy-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	// Check again after a caller's potentially blocking final validation.
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return os.Rename(tmp.Name(), path)
}
