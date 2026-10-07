package authstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMaintenanceValidateDoesNotAdmitOrAlias(t *testing.T) {
	gate, _ := NewMaintenanceGate("instance")
	ctx := BindMaintenance(context.Background(), "instance", 1)
	if _, err := gate.Validate(ctx, MaintainRefresh); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatal("closed valid")
	}
	expiry := time.Now().Add(time.Minute)
	if err := gate.Grant(MaintenancePermit{InstanceID: "instance", Generation: 1, Action: MaintainRefresh, UIDs: []string{"account-one"}, ExpiresAt: expiry}); err != nil {
		t.Fatal(err)
	}
	_, done, err := gate.Begin(ctx, MaintainRefresh, []string{"account-one"})
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	p, err := gate.Validate(ctx, MaintainRefresh)
	if err != nil {
		t.Fatal("query rejected active work", err)
	}
	p.UIDs[0] = "account-other"
	current, _ := gate.Validate(ctx, MaintainRefresh)
	if current.UIDs[0] != "account-one" {
		t.Fatal("aliased permit")
	}
	if _, active, _ := gate.Status(); active != 1 {
		t.Fatal("query registered work")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, invalid := range []context.Context{context.Background(), BindMaintenance(context.Background(), "instance", 2), BindMaintenance(context.Background(), "other", 1), cancelled} {
		if _, err := gate.Validate(invalid, MaintainRefresh); !errors.Is(err, ErrMaintenanceRequired) {
			t.Fatal("invalid binding accepted")
		}
	}
	if _, err := gate.Validate(ctx, MaintainCredits); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatal("wrong action accepted")
	}
	drainCtx, stop := context.WithCancel(context.Background())
	stop()
	_ = gate.Revoke(drainCtx, "instance", 1)
	if _, err := gate.Validate(ctx, MaintainRefresh); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatal("draining accepted")
	}
	done()
	if err := gate.Revoke(context.Background(), "instance", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Validate(ctx, MaintainRefresh); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatal("closed accepted")
	}
}

func TestMaintenanceValidateRejectsExpiry(t *testing.T) {
	gate, _ := NewMaintenanceGate("instance")
	expires := time.Now().Add(20 * time.Millisecond)
	if err := gate.Grant(MaintenancePermit{InstanceID: "instance", Generation: 1, Action: MaintainLogin, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	ctx := BindMaintenance(context.Background(), "instance", 1)
	time.Sleep(time.Until(expires) + time.Millisecond)
	if _, err := gate.Validate(ctx, MaintainLogin); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatal("expired accepted")
	}
	_ = gate.Revoke(context.Background(), "instance", 1)
}
