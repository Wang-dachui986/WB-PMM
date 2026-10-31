package ops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api-gui/internal/authstore"
	"workbuddy2api-gui/internal/gateway"
	"workbuddy2api-gui/internal/upstream"
)

func TestCreditFailurePropagatesWithoutDisablingGatewayAccount(t *testing.T) {
	var recovered atomic.Bool
	a := &authstore.Account{UID: "account-one", AccessToken: "synthetic", Domain: "www.codebuddy.cn", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	s := desktopFixture(t, func(*http.Request) (*http.Response, error) {
		if !recovered.Load() {
			return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
		}
		return fakeJSON(`{"Response":{"Data":{"Accounts":[]}}}`)
	}, a)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/healthz" {
			io.WriteString(w, `{"service":"workbuddy2api"}`)
			return
		}
		io.WriteString(w, `{"total":1,"healthy":1,"accounts":[{"uid":"account-one","credits":999}]}`)
	}))
	defer gw.Close()
	s.gw = gateway.New(gw.URL, nil, time.Second)
	s.setCreditCache(a.UID, &upstream.Credits{Remain: 100, Size: 100})
	ctx := grantFixture(t, s, 1, authstore.MaintainCredits, a.UID)
	task, err := s.BatchCredits(ctx, []string{a.UID})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		v, ok := s.tasks.Get(task.ID)
		if !ok {
			t.Fatal("lost task")
		}
		if !v.Running {
			if v.Failed != 1 {
				t.Fatal(v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	accounts, _, _, err := s.Accounts(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatal(err)
	}
	got := accounts[0]
	if got.CreditsStatus != "auth_rejected" || got.CreditsError == "" || got.LiveCredits != nil || got.CreditsAt == nil {
		t.Fatalf("missing failure observation: %+v", got)
	}
	if got.Status != "healthy" || got.Disabled {
		t.Fatal("billing failure changed chat eligibility")
	}
	ov := s.Overview(context.Background())
	if ov.Credits.OK != 0 || ov.Credits.Remain != 0 || ov.Credits.Failed != 1 {
		t.Fatalf("stale gateway balance counted after query failure: %+v", ov.Credits)
	}
	recovered.Store(true)
	if _, err = s.CreditsFor(ctx, a.UID); err != nil {
		t.Fatal(err)
	}
	accounts, _, _, _ = s.Accounts(context.Background())
	got = accounts[0]
	if got.CreditsStatus != "ok" || got.CreditsError != "" || got.LiveCredits == nil {
		t.Fatalf("success did not clear failure: %+v", got)
	}
	s.setCreditError(a.UID, &upstream.Error{Kind: upstream.ErrSoftRate, Status: 429})
	accounts, _, _, _ = s.Accounts(context.Background())
	if accounts[0].CreditsStatus != "failed" {
		t.Fatal("rate limiting mislabeled as invalid credentials")
	}
}
