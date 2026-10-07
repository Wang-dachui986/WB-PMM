package upstream

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDesktopTransportExplicitProxyAndDirect(t *testing.T) {
	var direct, proxied atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1); fmt.Fprint(w, "direct") }))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1); fmt.Fprint(w, "proxy") }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	old, err := NewDesktop(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer old.HTTP.CloseIdleConnections()
	for _, client := range []*http.Client{old.HTTP, old.ChatHTTP} {
		resp, err := client.Get(origin.URL)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	next, err := NewDesktop("")
	if err != nil {
		t.Fatal(err)
	}
	defer next.HTTP.CloseIdleConnections()
	if next.HTTP.Transport == old.HTTP.Transport || next.HTTP.Transport != next.ChatHTTP.Transport || next.HTTP.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("transport construction changed sharing or direct semantics")
	}
	resp, err := next.HTTP.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if proxied.Load() != 2 || direct.Load() != 1 {
		t.Fatalf("unexpected routes proxy=%d direct=%d", proxied.Load(), direct.Load())
	}
	for _, bad := range []string{"https://localhost:80", "http://u:p@localhost:80", "http://localhost:80/", "http://localhost:80?", "http://localhost:80#", "http://localhost", "http://localhost:0"} {
		if _, err := NewDesktop(bad); err == nil {
			t.Fatal("invalid proxy accepted")
		}
	}
}
