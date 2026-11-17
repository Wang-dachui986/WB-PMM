package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// stubLocalSubnets replaces the interface subnet lookup for one test.
func stubLocalSubnets(t *testing.T, cidrs ...string) {
	t.Helper()
	var prefixes []netip.Prefix
	for _, c := range cidrs {
		prefixes = append(prefixes, netip.MustParsePrefix(c))
	}
	old := localSubnetPrefixes
	localSubnetPrefixes = func() []netip.Prefix { return prefixes }
	localSubnetCache.Lock()
	localSubnetCache.at = time.Time{}
	localSubnetCache.Unlock()
	t.Cleanup(func() {
		localSubnetPrefixes = old
		localSubnetCache.Lock()
		localSubnetCache.at = time.Time{}
		localSubnetCache.Unlock()
	})
}

func TestDesktopLANBoundary(t *testing.T) {
	resetModelsCache()
	t.Cleanup(resetModelsCache)
	p := testPoolWith()
	t.Cleanup(p.Close)
	h := NewHandler(Config{Pool: p, DesktopMode: true, DesktopLAN: true, APIKey: "synthetic-key", ManagementKey: "synthetic-management", AdminEnabled: true})
	stubLocalSubnets(t, "100.70.0.0/24", "2001:db8:1:2::/64")
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
		{"private IPv6", "[fd00::2]:40000", "GET", "/v1/models", "synthetic-key", true, 200},
		{"link-local IPv6", "[fe80::2%en0]:40000", "GET", "/v1/models", "synthetic-key", true, 200},
		{"link-local IPv4", "169.254.10.2:40000", "GET", "/v1/models", "synthetic-key", true, 200},
		{"same subnet non-private", "100.70.0.9:40000", "GET", "/v1/models", "synthetic-key", true, 200},
		{"same subnet IPv6 global", "[2001:db8:1:2::9]:40000", "GET", "/v1/models", "synthetic-key", true, 200},
		{"other subnet non-private", "100.71.0.9:40000", "GET", "/v1/models", "synthetic-key", true, 403},
		{"public IPv6", "[2001:db8:9::1]:40000", "GET", "/v1/models", "synthetic-key", true, 403},
		{"head models", "192.168.1.20:40000", "HEAD", "/v1/models", "synthetic-key", true, 200},
		{"lan log local only", "192.168.1.20:40000", "GET", "/v1/lan-access", "synthetic-key", true, 403},
		{"token history local only", "192.168.1.20:40000", "GET", "/v1/token-history", "synthetic-key", true, 403},
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

func TestDesktopLANAccessLog(t *testing.T) {
	resetModelsCache()
	t.Cleanup(resetModelsCache)
	p := testPoolWith()
	t.Cleanup(p.Close)
	stubLocalSubnets(t)
	h := NewHandler(Config{Pool: p, DesktopMode: true, DesktopLAN: true, APIKey: "synthetic-key", ManagementKey: "synthetic-management"})
	send := func(peer, method, path, key string) int {
		req := httptest.NewRequest(method, path, strings.NewReader("{"))
		req.RemoteAddr = peer
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	send("192.168.1.20:40000", "GET", "/v1/models", "synthetic-key")
	send("192.168.1.21:40000", "GET", "/v1/models", "wrong")
	send("192.168.1.22:40000", "GET", "/status", "synthetic-key")
	send("8.8.8.8:40000", "GET", "/v1/models", "synthetic-key")
	send("192.168.1.23:40000", "POST", "/v1/chat/completions", "synthetic-key") // no accounts -> 503
	if code := send("127.0.0.1:40000", "GET", "/v1/models", "synthetic-key"); code != http.StatusOK {
		t.Fatalf("loopback models=%d", code)
	}
	if code := send("127.0.0.1:40000", "GET", "/v1/lan-access", ""); code != http.StatusUnauthorized {
		t.Fatalf("lan log without key=%d", code)
	}
	req := httptest.NewRequest("GET", "/v1/lan-access", nil)
	req.RemoteAddr = "[::1]:40000"
	req.Header.Set("Authorization", "Bearer synthetic-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("lan log=%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		LANEnabled bool             `json:"lan_enabled"`
		Entries    []LANAccessEntry `json:"entries"`
		Totals     map[string]int   `json:"totals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.LANEnabled || len(body.Entries) != 5 {
		t.Fatalf("unexpected log: %+v", body)
	}
	want := []struct {
		peer, path, result string
		status             int
	}{
		{"192.168.1.23", "/v1/chat/completions", "failed", 503},
		{"8.8.8.8", "/v1/models", "blocked_source", 403},
		{"192.168.1.22", "/status", "blocked_path", 403},
		{"192.168.1.21", "/v1/models", "unauthorized", 401},
		{"192.168.1.20", "/v1/models", "ok", 200},
	}
	for i, w := range want {
		e := body.Entries[i]
		if e.Peer != w.peer || e.Path != w.path || e.Result != w.result || e.Status != w.status {
			t.Fatalf("entry %d = %+v, want %+v", i, e, w)
		}
	}
	if body.Totals["ok"] != 1 || body.Totals["unauthorized"] != 1 || body.Totals["failed"] != 1 || body.Totals["blocked_path"] != 1 || body.Totals["blocked_source"] != 1 {
		t.Fatalf("totals=%v", body.Totals)
	}
	for i := 0; i < lanAccessLogSize+5; i++ {
		send("192.168.1.30:40000", "GET", "/v1/models", "synthetic-key")
	}
	entries, totals := h.lanLog.snapshot()
	if len(entries) != lanAccessLogSize || totals["ok"] != lanAccessLogSize+6 {
		t.Fatalf("ring size=%d ok=%d", len(entries), totals["ok"])
	}
}

func TestLANStatusWriterKeepsFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &lanStatusWriter{ResponseWriter: rec}
	var rw http.ResponseWriter = w
	f, ok := rw.(http.Flusher)
	if !ok {
		t.Fatal("wrapper must implement http.Flusher for SSE")
	}
	f.Flush()
	if !rec.Flushed || w.status != http.StatusOK {
		t.Fatalf("flushed=%v status=%d", rec.Flushed, w.status)
	}
	if http.NewResponseController(rw).Flush() != nil {
		t.Fatal("ResponseController flush failed")
	}
}

func TestInterfaceSubnetPrefixesSkipBroadMasks(t *testing.T) {
	for _, p := range interfaceSubnetPrefixes() {
		if p.Addr().Is4() && p.Bits() < 16 || p.Addr().Is6() && p.Bits() < 48 {
			t.Fatalf("prefix too broad: %v", p)
		}
		if p.Addr().IsLoopback() {
			t.Fatalf("loopback prefix included: %v", p)
		}
	}
}
