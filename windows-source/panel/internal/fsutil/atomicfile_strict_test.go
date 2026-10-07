package fsutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicStrictRechecksContextBeforeCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential.json")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checks := 0
	err := WriteFileAtomicStrict(ctx, path, []byte("replacement"), 0o600, func() error {
		checks++
		if checks == 2 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || checks != 2 {
		t.Fatalf("final validation err=%v checks=%d", err, checks)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "original" {
		t.Fatalf("cancelled commit changed original: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left behind: entries=%d err=%v", len(entries), err)
	}
}

func TestReadFileStrictRejectsSymlinkAndOversize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	link := filepath.Join(dir, "symlink.json")
	if err := os.WriteFile(path, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFileStrict(link, 100); err == nil {
		t.Fatal("followed symlink")
	}
	if _, err := ReadFileStrict(dir, 100); err == nil {
		t.Fatal("read directory")
	}
	if _, err := ReadFileStrict(path, 3); err == nil {
		t.Fatal("accepted oversized input")
	}
	if raw, err := ReadFileStrict(path, 9); err != nil || string(raw) != "synthetic" {
		t.Fatalf("bounded normal read=%q %v", raw, err)
	}
}

func TestWriteFileAtomicStrictRejectsTargetSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.json")
	out := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(out, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, path); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomicStrict(context.Background(), path, []byte("replacement"), 0o600, nil); err == nil {
		t.Fatal("accepted symlink target")
	}
	raw, err := os.ReadFile(out)
	if err != nil || string(raw) != "original" {
		t.Fatalf("outside target changed: %v", err)
	}
}
