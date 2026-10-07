package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDesktopLANBoundary(t *testing.T) {
	resetModelsCache()
	t.Cleanup(resetModelsCache)
	p := testPoolWith()
	t.Cleanup(p.Close)
	h := NewHandler(Config{Pool: p, DesktopMode: true, DesktopLAN: true, APIKey: "synthetic-key", ManagementKey: "synthetic-management", AdminEnabled: true})
	for _, tt := range []struct {
		name, peer, method, path, key string
		lan                           bool
		want                          int
	}{
		{"private models", "192.168.1.20:40000", "GET", "/v1/models", "synthetic-key", true, 200},
		{"private ten", "10.0.0.2:40000", "GET", "/v1/models", "synthetic-key", true, 200},
		{"private 172", "172.16.1.2:40000", "GET", "/v1/models", "synthetic-key", true, 200},
		{"mapped IPv4", "[::ffff:192.168.1.20]:40000", "GET", "/v1/models", "synthetic-key", true, 200},
		{"missing key", "192.168.1.20:40000", "GET", "/v1/models", "", true, 401},
		{"wrong key", "192.168.1.20:40000", "GET", "/v1/models", "wrong", true, 401},
		{"chat auth", "192.168.1.20:40000", "POST", "/v1/chat/completions", "", true, 401},
		{"responses auth", "192.168.1.20:40000", "POST", "/v1/responses", "", true, 401},
		{"messages auth", "192.168.1.20:40000", "POST", "/v1/messages", "", true, 401},
		{"chat route", "192.168.1.20:40000", "POST", "/v1/chat/completions", "synthetic-key", true, 503},
		{"responses route", "192.168.1.20:40000", "POST", "/v1/responses", "synthetic-key", true, 400},
		{"messages route", "192.168.1.20:40000", "POST", "/v1/messages", "synthetic-key", true, 400},
		{"disabled", "192.168.1.20:40000", "GET", "/v1/models", "synthetic-key", false, 403},
		{"public", "8.8.8.8:40000", "GET", "/v1/models", "synthetic-key", true, 403},
		{"private IPv6", "[fd00::2]:40000", "GET", "/v1/models", "synthetic-key", true, 403},
		{"invalid peer", "", "GET", "/v1/models", "synthetic-key", true, 403},
		{"status local", "192.168.1.20:40000", "GET", "/status", "synthetic-key", true, 403},
		{"health local", "192.168.1.20:40000", "GET", "/healthz", "synthetic-key", true, 403},
		{"stats local", "192.168.1.20:40000", "GET", "/v1/stats", "synthetic-key", true, 403},
		{"reset local", "192.168.1.20:40000", "POST", "/v1/stats/reset", "synthetic-key", true, 403},
		{"admin local", "192.168.1.20:40000", "POST", "/admin/accounts/u1/disable", "synthetic-key", true, 403},
		{"drain local", "192.168.1.20:40000", "POST", "/admin/desktop/drain", "synthetic-key", true, 403},
		{"path traversal", "192.168.1.20:40000", "GET", "/v1/../status", "synthetic-key", true, 403},
		{"unexpected method", "192.168.1.20:40000", "DELETE", "/v1/models", "synthetic-key", true, 403},
		{"loopback status", "127.0.0.1:40000", "GET", "/status", "synthetic-key", false, 200},
		{"loopback IPv6", "[::1]:40000", "GET", "/status", "synthetic-key", true, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h.cfg.DesktopLAN = tt.lan
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader("{"))
			req.RemoteAddr = tt.peer
			req.Header.Set("Authorization", "Bearer "+tt.key)
			req.Header.Set(managementKeyHeader, "synthetic-management")
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}
