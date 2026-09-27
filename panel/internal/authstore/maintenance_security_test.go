package authstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workbuddy2api-gui/internal/fsutil"
)

func beginStoreMaintenance(t *testing.T, store *Store, action MaintenanceAction, uids []string, expires time.Time) (*MaintenanceGate, context.Context, func()) {
	t.Helper()
	// testing.TempDir may create its child with 0755; desktop auths is 0700.
	if err := os.Chmod(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	gate, err := NewMaintenanceGate("test-panel")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnableMaintenance(gate); err != nil {
		t.Fatal(err)
	}
	if err := gate.Grant(MaintenancePermit{InstanceID: "test-panel", Generation: 1, Action: action, UIDs: uids, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	ctx, done, err := gate.Begin(BindMaintenance(context.Background(), "test-panel", 1), action, uids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		done()
		_, _, generation := gate.Status()
		timeout, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := gate.Revoke(timeout, "test-panel", generation); err != nil {
			t.Error(err)
		}
	})
	return gate, ctx, done
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertFixtureUnchanged(t *testing.T, path string, want []byte) {
	t.Helper()
	if got := readFixture(t, path); !bytes.Equal(got, want) {
		t.Errorf("fixture unexpectedly changed: %s", filepath.Base(path))
	}
}

func TestDesktopStoreRejectsSymlinks(t *testing.T) {
	store, _ := New(t.TempDir())
	a := &Account{UID: "account-one", AccessToken: "synthetic-old"}
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "outside.json")
	original := readFixture(t, a.FilePath)
	if err := os.WriteFile(out, original, 0o600); err != nil {
		t.Fatal(err)
	}
	// The old fixed .tmp implementation truncated this symlink's target.
	if err := os.Symlink(out, a.FilePath+".tmp"); err != nil {
		t.Fatal(err)
	}
	_, ctx, _ := beginStoreMaintenance(t, store, MaintainRefresh, []string{a.UID}, time.Now().Add(time.Minute))
	a.AccessToken = "synthetic-new"
	if err := store.SaveContext(ctx, a); err != nil {
		t.Fatal(err)
	}
	assertFixtureUnchanged(t, out, original)
	got, err := store.Get(a.UID)
	if err != nil || got.AccessToken != a.AccessToken {
		t.Fatalf("strict write failed: %v", err)
	}
	if err := os.Remove(a.FilePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, a.FilePath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(a.UID); err == nil {
		t.Fatal("Get followed credential symlink")
	}
	accounts, warnings := store.List()
	if len(accounts) != 0 || len(warnings) != 1 {
		t.Fatalf("List followed symlink: accounts=%d warnings=%v", len(accounts), warnings)
	}
	if err := store.SaveContext(ctx, a); err == nil {
		t.Fatal("Save accepted credential symlink")
	}
	assertFixtureUnchanged(t, out, original)
}

func TestDesktopStoreRequiresStablePrivateDirectory(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auths")
		if err := os.Symlink(t.TempDir(), path); err != nil {
			t.Fatal(err)
		}
		store, _ := New(path)
		gate, _ := NewMaintenanceGate("panel")
		if err := store.EnableMaintenance(gate); err == nil {
			t.Fatal("symlink directory accepted")
		}
	})
	t.Run("shared permissions", func(t *testing.T) {
		if !fsutil.PermBitsEnforced() {
			t.Skip("permission bits not enforced")
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		store, _ := New(dir)
		gate, _ := NewMaintenanceGate("panel")
		if err := store.EnableMaintenance(gate); err == nil {
			t.Fatal("non-private directory accepted")
		}
	})
	t.Run("replacement", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "auths")
		store, _ := New(dir)
		a := &Account{UID: "account-one", AccessToken: "synthetic-old"}
		if err := store.Save(a); err != nil {
			t.Fatal(err)
		}
		original := readFixture(t, a.FilePath)
		_, ctx, _ := beginStoreMaintenance(t, store, MaintainRefresh, []string{a.UID}, time.Now().Add(time.Minute))
		if err := os.Rename(dir, dir+"-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		a.AccessToken = "synthetic-new"
		if err := store.SaveContext(ctx, a); err == nil {
			t.Fatal("replaced directory accepted")
		}
		assertFixtureUnchanged(t, filepath.Join(dir+"-old", filepath.Base(a.FilePath)), original)
		if _, err := os.Stat(a.FilePath); !os.IsNotExist(err) {
			t.Fatalf("new directory was written: %v", err)
		}
	})
}

func TestDesktopSaveBindsPermissionToActualTargetUID(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "canonical filename with another UID"
		if explicit {
			name = "explicit FilePath to another UID"
		}
		t.Run(name, func(t *testing.T) {
			store, _ := New(t.TempDir())
			victim := &Account{UID: "account-two", AccessToken: "synthetic-victim"}
			if err := store.Save(victim); err != nil {
				t.Fatal(err)
			}
			target := victim.FilePath
			if !explicit {
				target = store.PathFor("account-one")
				if err := os.Rename(victim.FilePath, target); err != nil {
					t.Fatal(err)
				}
			}
			original := readFixture(t, target)
			_, ctx, _ := beginStoreMaintenance(t, store, MaintainImport, []string{"account-one"}, time.Now().Add(time.Minute))
			candidate := &Account{UID: "account-one", AccessToken: "synthetic-new"}
			if explicit {
				candidate.FilePath = target
			}
			if err := store.SaveContext(ctx, candidate); !errors.Is(err, ErrCredentialConflict) {
				t.Fatalf("wrong-UID overwrite=%v", err)
			}
			assertFixtureUnchanged(t, target, original)
		})
	}
}

func TestDesktopMutationRechecksExpiryAfterStoreLock(t *testing.T) {
	for _, action := range []MaintenanceAction{MaintainRefresh, MaintainDelete, MaintainRename} {
		t.Run(string(action), func(t *testing.T) {
			store, _ := New(t.TempDir())
			a := &Account{UID: "account-one", AccessToken: "synthetic-old"}
			if err := store.Save(a); err != nil {
				t.Fatal(err)
			}
			original := readFixture(t, a.FilePath)
			uids := []string{a.UID}
			if action == MaintainRename {
				uids = append(uids, "account-two")
			}
			_, ctx, _ := beginStoreMaintenance(t, store, action, uids, time.Now().Add(100*time.Millisecond))
			store.mu.Lock()
			started, result := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				switch action {
				case MaintainRefresh:
					copy := *a
					copy.AccessToken = "synthetic-new"
					result <- store.SaveContext(ctx, &copy)
				case MaintainDelete:
					result <- store.DeleteContext(ctx, a.UID)
				case MaintainRename:
					result <- store.RenameContext(ctx, a.UID, "account-two")
				}
			}()
			<-started
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				store.mu.Unlock()
				t.Fatal("permit did not expire")
			}
			store.mu.Unlock()
			if err := <-result; err == nil {
				t.Fatal("expired operation committed after waiting for store lock")
			}
			assertFixtureUnchanged(t, a.FilePath, original)
			if _, err := os.Stat(store.PathFor("account-two")); !os.IsNotExist(err) {
				t.Fatalf("expired rename created target: %v", err)
			}
		})
	}
}

func TestDesktopDuplicateUIDRejectsRefreshAndImportButDeletesAll(t *testing.T) {
	store, _ := New(t.TempDir())
	a := &Account{UID: "account-one", AccessToken: "synthetic-canonical"}
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	canonical := readFixture(t, a.FilePath)
	legacyPath := filepath.Join(store.Dir(), "workbuddy-z-legacy.json")
	legacy := bytes.ReplaceAll(canonical, []byte("synthetic-canonical"), []byte("synthetic-legacy"))
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	gate, ctx, done := beginStoreMaintenance(t, store, MaintainRefresh, []string{a.UID}, time.Now().Add(time.Minute))
	if _, err := store.Get(a.UID); !errors.Is(err, ErrCredentialConflict) {
		t.Fatalf("ambiguous Get=%v", err)
	}
	for _, path := range []string{a.FilePath, legacyPath} {
		candidate := &Account{UID: a.UID, AccessToken: "synthetic-new", FilePath: path}
		if err := store.SaveContext(ctx, candidate); !errors.Is(err, ErrCredentialConflict) {
			t.Fatalf("ambiguous refresh=%v", err)
		}
	}
	done()
	if err := gate.Revoke(context.Background(), "test-panel", 1); err != nil {
		t.Fatal(err)
	}
	for generation, action := range []MaintenanceAction{MaintainImport, MaintainDelete} {
		gen := uint64(generation + 2)
		if err := gate.Grant(MaintenancePermit{InstanceID: "test-panel", Generation: gen, Action: action, UIDs: []string{a.UID}, ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		ctx, done, err := gate.Begin(BindMaintenance(context.Background(), "test-panel", gen), action, []string{a.UID})
		if err != nil {
			t.Fatal(err)
		}
		if action == MaintainImport {
			if err := store.SaveContext(ctx, &Account{UID: a.UID, AccessToken: "synthetic-new"}); !errors.Is(err, ErrCredentialConflict) {
				t.Errorf("ambiguous import=%v", err)
			}
			assertFixtureUnchanged(t, a.FilePath, canonical)
			assertFixtureUnchanged(t, legacyPath, legacy)
		} else {
			if err := store.DeleteContext(ctx, a.UID); err != nil {
				t.Error(err)
			}
			for _, path := range []string{a.FilePath, legacyPath} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("deleted UID left alias: %v", err)
				}
			}
		}
		done()
		if err := gate.Revoke(context.Background(), "test-panel", gen); err != nil {
			t.Fatal(err)
		}
	}
}
