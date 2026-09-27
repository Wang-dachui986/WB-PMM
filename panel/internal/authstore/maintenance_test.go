package authstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaintenanceRevokeDrainsAndOldOperationCannotWrite(t *testing.T) {
	store, _ := New(t.TempDir())
	acct := &Account{UID: "account-one", AccessToken: "synthetic-old"}
	if err := store.Save(acct); err != nil {
		t.Fatal(err)
	}
	gate, _ := NewMaintenanceGate("panel-instance")
	if err := os.Chmod(store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.EnableMaintenance(gate); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(acct); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatalf("legacy Save=%v", err)
	}
	if err := store.Delete(acct.UID); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatalf("legacy Delete=%v", err)
	}
	if err := store.Rename(acct.UID, "account-two"); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatalf("legacy Rename=%v", err)
	}
	permit := MaintenancePermit{InstanceID: "panel-instance", Generation: 1, Action: MaintainRefresh, UIDs: []string{acct.UID}, ExpiresAt: time.Now().Add(time.Minute)}
	if err := gate.Grant(permit); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gate.Begin(context.Background(), MaintainRefresh, permit.UIDs); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatalf("unbound begin=%v", err)
	}
	ctx, done, err := gate.Begin(BindMaintenance(context.Background(), permit.InstanceID, 1), MaintainRefresh, permit.UIDs)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	acct.AccessToken = "synthetic-updated"
	if err := store.SaveContext(ctx, acct); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gate.Begin(BindMaintenance(context.Background(), permit.InstanceID, 1), MaintainRefresh, permit.UIDs); err == nil {
		t.Fatal("concurrent maintenance admitted")
	}
	result := make(chan error, 1)
	go func() { result <- gate.Revoke(context.Background(), permit.InstanceID, 1) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("revoke did not cancel transport context")
	}
	if state, active, _ := gate.Status(); state != "draining" || active != 1 {
		t.Fatalf("state=%s active=%d", state, active)
	}
	select {
	case err := <-result:
		t.Fatalf("revoke returned before caller exited: %v", err)
	default:
	}
	if err := store.SaveContext(ctx, acct); !errors.Is(err, ErrMaintenanceRequired) && !errors.Is(err, context.Canceled) {
		t.Fatalf("late write=%v", err)
	}
	permit.Generation = 2
	permit.Action = MaintainDelete
	if err := gate.Grant(permit); err == nil {
		t.Fatal("grant reopened draining gate")
	}
	done()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := gate.Grant(permit); err != nil {
		t.Fatal(err)
	}
	deleteCtx, deleteDone, err := gate.Begin(BindMaintenance(context.Background(), permit.InstanceID, 2), MaintainDelete, permit.UIDs)
	if err != nil {
		t.Fatal(err)
	}
	defer deleteDone()
	if err := store.SaveContext(deleteCtx, acct); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatalf("delete permission allowed save: %v", err)
	}
	if err := store.DeleteContext(deleteCtx, acct.UID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(ctx, acct); err == nil {
		t.Fatal("old refresh resurrected deleted account")
	}
	if _, err := store.Get(acct.UID); !os.IsNotExist(err) {
		t.Fatalf("deleted account exists: %v", err)
	}
	deleteDone()
	if err := gate.Revoke(context.Background(), permit.InstanceID, 2); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceWriteAndRevokeAreOrdered(t *testing.T) {
	gate, _ := NewMaintenanceGate("panel")
	permit := MaintenancePermit{InstanceID: "panel", Generation: 1, Action: MaintainImport, UIDs: []string{"account-one"}, ExpiresAt: time.Now().Add(time.Minute)}
	if err := gate.Grant(permit); err != nil {
		t.Fatal(err)
	}
	ctx, done, err := gate.Begin(BindMaintenance(context.Background(), "panel", 1), MaintainImport, permit.UIDs)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	entered, release := make(chan struct{}), make(chan struct{})
	wrote := make(chan error, 1)
	go func() {
		wrote <- gate.withWrite(ctx, "save", permit.UIDs, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- gate.Revoke(context.Background(), "panel", 1) }()
	select {
	case <-ctx.Done():
		t.Fatal("revoke raced into a disk write")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	done()
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
}

func TestMaintenancePermitValidation(t *testing.T) {
	base := MaintenancePermit{InstanceID: "panel", Generation: 1, Action: MaintainRefresh, UIDs: []string{"account-one"}, ExpiresAt: time.Now().Add(time.Minute)}
	for name, change := range map[string]func(*MaintenancePermit){
		"instance":         func(p *MaintenancePermit) { p.InstanceID = "other" },
		"generation":       func(p *MaintenancePermit) { p.Generation = 0 },
		"expired":          func(p *MaintenancePermit) { p.ExpiresAt = time.Now().Add(-time.Second) },
		"unbounded expiry": func(p *MaintenancePermit) { p.ExpiresAt = time.Now().Add(time.Hour) },
		"action":           func(p *MaintenancePermit) { p.Action = "anything" },
		"no selection":     func(p *MaintenancePermit) { p.UIDs = nil },
		"duplicate":        func(p *MaintenancePermit) { p.UIDs = []string{"account-one", "account-one"} },
		"traversal":        func(p *MaintenancePermit) { p.UIDs = []string{"../account-one"} },
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := NewMaintenanceGate("panel")
			p := base
			change(&p)
			if err := g.Grant(p); err == nil {
				t.Fatal("invalid permit accepted")
			}
		})
	}
}

func TestDeleteIncludesLegacyFilenamesAndSaveConfinesPath(t *testing.T) {
	store, _ := New(t.TempDir())
	acct := &Account{UID: "account-one", AccessToken: "synthetic"}
	if err := store.Save(acct); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(store.Dir(), "workbuddy-legacy.json")
	if err := os.Rename(acct.FilePath, legacy); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(acct.UID); err != nil || got.FilePath != legacy {
		t.Fatalf("legacy Get=%v %v", got, err)
	}
	if err := store.Save(&Account{UID: "account-two", AccessToken: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	// A canonical-looking filename can contain a different UID. It must not
	// make deletion of the selected account erase another account's credential.
	otherRaw, err := os.ReadFile(store.PathFor("account-two"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.PathFor(acct.UID), otherRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(acct.UID); err != nil || got.UID != acct.UID || got.FilePath != legacy {
		t.Fatalf("filename mismatch selected wrong account: %v %v", got, err)
	}
	if err := store.Delete(acct.UID); err != nil {
		t.Fatal(err)
	}
	list, _ := store.List()
	if len(list) != 2 || list[0].UID != "account-two" || list[1].UID != "account-two" {
		t.Fatalf("delete left matching aliases: %+v", list)
	}
	outside := filepath.Join(t.TempDir(), "workbuddy-escape.json")
	if err := store.Save(&Account{UID: acct.UID, AccessToken: "synthetic", FilePath: outside}); err == nil {
		t.Fatal("out-of-directory Save accepted")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside file written: %v", err)
	}
}
