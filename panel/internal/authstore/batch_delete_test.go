package authstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestBatchDeletePermitRemainsBoundToConfirmedUIDs(t *testing.T) {
	gate, _ := NewMaintenanceGate("batch-test")
	uids := make([]string, 100)
	for i := range uids {
		uids[i] = fmt.Sprintf("account-%03d", i)
	}
	if err := gate.Grant(MaintenancePermit{InstanceID: "batch-test", Generation: 1, Action: MaintainDelete, UIDs: append(uids, "extra-account"), ExpiresAt: time.Now().Add(time.Minute)}); err == nil {
		t.Fatal("accepted over 100 accounts")
	}
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(&Account{UID: uids[0], AccessToken: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if err = store.Save(&Account{UID: "unselected-account", AccessToken: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(store.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err = store.EnableMaintenance(gate); err != nil {
		t.Fatal(err)
	}
	if err = gate.Grant(MaintenancePermit{InstanceID: "batch-test", Generation: 1, Action: MaintainDelete, UIDs: uids, ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	ctx := BindMaintenance(context.Background(), "batch-test", 1)
	if _, _, err = gate.Begin(ctx, MaintainDelete, []string{"unselected-account"}); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatal("unselected account admitted")
	}
	op, done, err := gate.Begin(ctx, MaintainDelete, []string{uids[0]})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteContext(op, uids[0]); err != nil {
		t.Fatal(err)
	}
	done()
	if _, err = store.Get(uids[0]); !os.IsNotExist(err) {
		t.Fatal("selected credential not removed")
	}
	if _, err = store.Get("unselected-account"); err != nil {
		t.Fatal("unselected credential changed")
	}
	if _, _, err = gate.Begin(ctx, MaintainRefresh, []string{uids[0]}); !errors.Is(err, ErrMaintenanceRequired) {
		t.Fatal("delete permit authorized refresh")
	}
}
