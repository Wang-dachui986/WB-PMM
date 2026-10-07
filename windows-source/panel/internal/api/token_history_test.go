package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"workbuddy2api-gui/internal/gateway"
	"workbuddy2api-gui/internal/ops"
)

func TestDesktopTokenHistorySessionAndProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/token-history" || r.URL.Query().Get("from") != "2026-10-01T00:00:00Z" || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Errorf("bad proxy request %s", r.URL)
		}
		io.WriteString(w, `{"since":"2026-10-01T00:00:00Z","rows":[],"warning":"","retention_days":400}`)
	}))
	defer upstream.Close()
	s := newPolicyServer(true)
	s.svc = ops.New(s.cfg, nil, gateway.New(upstream.URL, func() string { return "synthetic" }, time.Second), nil)
	token, ok, _ := s.sessions.Create("admin", "secret123", s.cfg, "127.0.0.1")
	if !ok {
		t.Fatal("session")
	}
	for _, auth := range []bool{false, true} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8787/api/token-history?from=2026-10-01T00:00:00Z&to=2026-10-02T00:00:00Z", nil)
		if auth {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if auth && w.Code != 200 {
			t.Fatalf("authenticated: %d %s", w.Code, w.Body)
		}
		if !auth && w.Code == 200 {
			t.Fatal("unauthenticated history exposed")
		}
	}
}
