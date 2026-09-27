package gateway

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

func TestDesktopConnectionSnapshotIncludesStream(t *testing.T) {
	var wrong atomic.Int32
	makeServer := func(key string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+key {
				wrong.Add(1)
			}
			if r.URL.Path == "/v1/chat/completions" {
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				return
			}
			fmt.Fprint(w, `{}`)
		}))
	}
	a, b := makeServer("KEY_A"), makeServer("KEY_B")
	defer a.Close()
	defer b.Close()
	c := New(a.URL, func() string { return "KEY_A" }, time.Second)
	defer c.http.CloseIdleConnections()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c.SetConnection(a.URL, "KEY_A")
			c.SetConnection(b.URL, "KEY_B")
		}
	}()
	for i := 0; i < 40; i++ {
		resp, err := c.do(context.Background(), "GET", "/probe", nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err := c.ChatStream(context.Background(), []byte(`{}`), func(Delta) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if wrong.Load() != 0 {
		t.Fatalf("URL and key crossed snapshots %d times", wrong.Load())
	}
	if c.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("internal gateway requests must remain direct")
	}
}

func TestWebConnectionKeepsLiveKeyCallback(t *testing.T) {
	seen := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	key := "first"
	c := New(srv.URL, func() string { return key }, time.Second)
	defer c.http.CloseIdleConnections()
	for _, value := range []string{"first", "second"} {
		key = value
		c.SetBaseURL(srv.URL + "/")
		resp, err := c.do(context.Background(), "GET", "/probe", nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if strings.Join([]string{<-seen, <-seen}, ",") != "Bearer first,Bearer second" {
		t.Fatal("legacy callback changed")
	}
}
