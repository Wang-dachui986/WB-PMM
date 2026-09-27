package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func scheduledCheckinFixture(t *testing.T) (*Scheduler, <-chan time.Time, <-chan struct{}) {
	t.Helper()
	checkins := make(chan time.Time, 2)
	resources := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/daily-checkin"):
			checkins <- time.Now()
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
		case strings.HasSuffix(r.URL.Path, "/get-user-resource"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"Response":{"Data":{"Accounts":[{"CycleCapacitySize":1000,"CycleCapacityRemain":100,"CycleCapacityUsed":0}]}}}}`))
			resources <- struct{}{}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	p := pool.New("")
	for _, uid := range []string{"a", "b"} {
		p.Add(&auth.Auth{UID: uid, AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	}
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	return New(Config{Pool: p, Upstream: up}), checkins, resources
}

func TestScheduledCheckinPacesEligibleAccounts(t *testing.T) {
	s, checkins, _ := scheduledCheckinFixture(t)

	s.dispatch(context.Background(), taskCheckin)
	first, second := checkinAt(t, checkins), checkinAt(t, checkins)
	if gap := second.Sub(first); gap < 900*time.Millisecond {
		t.Fatalf("automatic check-ins only %s apart; expected pacing", gap)
	}
}

func TestScheduledCheckinStopsBeforeNextAccountOnCancel(t *testing.T) {
	s, checkins, resources := scheduledCheckinFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.dispatch(ctx, taskCheckin); close(done) }()
	select {
	case <-resources:
	case <-time.After(2 * time.Second):
		t.Fatal("first account did not complete")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled check-in did not stop after cancellation")
	}
	if len(checkins) != 1 {
		t.Fatalf("check-in calls=%d, want only first account", len(checkins))
	}
}

func checkinAt(t *testing.T, checkins <-chan time.Time) time.Time {
	t.Helper()
	select {
	case at := <-checkins:
		return at
	case <-time.After(3 * time.Second):
		t.Fatal("expected check-in call did not arrive")
		return time.Time{}
	}
}
