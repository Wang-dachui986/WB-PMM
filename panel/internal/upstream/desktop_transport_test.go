package upstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type desktopProxyRT func(*http.Request) (*http.Response, error)

func (f desktopProxyRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDesktopTransportSwitchKeepsInflightClient(t *testing.T) {
	var direct, proxied atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"via":"direct"}}`)
	}))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"via":"proxy"}}`)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	c := New(time.Second)
	started, release := make(chan struct{}), make(chan struct{})
	c.HTTP.Transport = desktopProxyRT(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"via":"old"}}`))}, nil
	})
	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", origin.URL, nil)
		_, err := c.doJSON(req)
		done <- err
	}()
	<-started
	if err := c.SetDesktopTransport(proxy.URL, time.Second); err != nil {
		t.Fatal(err)
	}
	first := c.httpClient()
	req, _ := http.NewRequest("GET", origin.URL, nil)
	if raw, err := c.doJSON(req); err != nil || !strings.Contains(string(raw), "proxy") {
		t.Fatalf("proxy not used: %s %v", raw, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.SetDesktopTransport("", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	second := c.httpClient()
	if first == second || first.Transport == second.Transport || second.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("live transport reused or environment proxy inherited")
	}
	if raw, err := c.doJSON(req); err != nil || !strings.Contains(string(raw), "direct") {
		t.Fatalf("direct not used: %s %v", raw, err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = c.SetDesktopTransport(proxy.URL, time.Second)
			_ = c.SetDesktopTransport("", time.Second)
		}
	}()
	for i := 0; i < 20; i++ {
		if _, err := c.doJSON(req); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	c.httpClient().CloseIdleConnections()
	if direct.Load() == 0 || proxied.Load() == 0 {
		t.Fatal("both routes were not exercised")
	}
	for _, bad := range []string{"https://localhost:80", "http://u:p@localhost:80", "http://localhost:80/", "http://localhost:80?", "http://localhost:80#", "http://localhost", "http://localhost:0"} {
		if err := c.SetDesktopTransport(bad, time.Second); err == nil {
			t.Fatal("invalid proxy accepted")
		}
	}
}
