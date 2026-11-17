package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"workbuddy2api-gui/internal/gateway"
	"workbuddy2api-gui/internal/ops"
)

func TestDesktopLANAccessSessionAndProxy(t *testing.T) {
	status := http.StatusOK
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/lan-access" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Errorf("bad proxy request %s %s", r.Method, r.URL)
		}
		w.WriteHeader(status)
		io.WriteString(w, `{"lan_enabled":true,"entries":[{"time":"2026-11-17T00:00:00Z","peer":"192.168.1.20","method":"POST","path":"/v1/chat/completions","status":200,"result":"ok"}],"totals":{"ok":1}}`)
	}))
	defer upstream.Close()
	s := newPolicyServer(true)
	s.svc = ops.New(s.cfg, nil, gateway.New(upstream.URL, func() string { return "synthetic" }, time.Second), nil)
	token, ok, _ := s.sessions.Create("admin", "secret123", s.cfg, "127.0.0.1")
	if !ok {
		t.Fatal("session")
	}
	get := func(auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8787/api/lan-access", nil)
		if auth {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := get(false); w.Code == http.StatusOK {
		t.Fatal("unauthenticated LAN log exposed")
	}
	w := get(true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"peer":"192.168.1.20"`) {
		t.Fatalf("authenticated: %d %s", w.Code, w.Body)
	}
	status = http.StatusNotFound
	if w := get(true); w.Code == http.StatusOK || !strings.Contains(w.Body.String(), "未提供局域网请求记录") {
		t.Fatalf("old gateway: %d %s", w.Code, w.Body)
	}
}
