package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/session"
)

const creditErrorSSE = "data: {\"error\":{\"code\":14018,\"message\":\"Credits exhausted\"}}\n\n"

func TestChatCreditStreamRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, prefix  string
		stream, retry bool
	}{
		{"first error", "", true, true},
		{"metadata before error", ": keepalive\n\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"total_tokens\":0}}\n\n", true, true},
		{"content delivered", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", true, false},
		{"reasoning delivered", "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"partial\"}}]}\n\n", true, false},
		{"tool delivered", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{}\"}}]}}]}\n\n", true, false},
		{"aggregate first error", "", false, true},
		{"aggregate partial not delivered", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := map[string]int{}
			up := newFakeUpstream(t, func(key string) (int, string, bool) {
				calls[key]++
				if key == "Bearer at-bad" {
					return 200, tc.prefix + creditErrorSSE + "data: [DONE]\n\n", true
				}
				return 200, sseOK, true
			})
			p := testPoolWith(
				&auth.Auth{UID: "bad", AccessToken: "at-bad", ExpiresAt: 9999999999},
				&auth.Auth{UID: "good", AccessToken: "at-good", ExpiresAt: 9999999999},
			)
			defer p.Close()
			store := newBindStore()
			sess := session.New(session.Config{TTL: time.Minute, Store: store, Available: p.AvailableUIDs})
			sess.Bind("credit-conversation", "bad")
			h := NewHandler(Config{Pool: p, Upstream: up, Session: sess})
			body := fmt.Sprintf(`{"model":"cn:hy3","stream":%t,"messages":[{"role":"user","content":"test"}],"metadata":{"conversation_id":"credit-conversation"}}`, tc.stream)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
			if rec.Code != http.StatusOK || calls["Bearer at-bad"] != 1 {
				t.Fatalf("status=%d calls=%v body=%s", rec.Code, calls, rec.Body)
			}
			st, _ := p.Status("bad")
			if !st.Cooling || st.Reason != "余额不足" || st.InFlight != 0 {
				t.Fatalf("exhausted account must cool and release lease: %+v", st)
			}
			if tc.retry {
				if calls["Bearer at-good"] != 1 || strings.Contains(rec.Body.String(), "Credits exhausted") || strings.Contains(rec.Body.String(), "partial") || strings.Count(rec.Body.String(), "你好") != 1 {
					t.Fatalf("expected one clean replacement answer: calls=%v body=%s", calls, rec.Body)
				}
				if uid, ok := store.lastUID("credit-conversation"); !ok || uid != "good" {
					t.Fatalf("binding did not move to successful account: %q %v", uid, ok)
				}
			} else {
				if calls["Bearer at-good"] != 0 || !strings.Contains(rec.Body.String(), "Credits exhausted") {
					t.Fatalf("must not replay a response already delivered: calls=%v body=%s", calls, rec.Body)
				}
				if _, ok := store.lastUID("credit-conversation"); ok {
					t.Fatal("exhausted account must be unbound")
				}
				// A later independent request can recover without replaying the failed one.
				next := httptest.NewRecorder()
				h.ServeHTTP(next, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
				if next.Code != http.StatusOK || calls["Bearer at-good"] != 1 || calls["Bearer at-bad"] != 1 {
					t.Fatalf("next request should use healthy account: calls=%v body=%s", calls, next.Body)
				}
			}
			if tc.stream && strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
				t.Fatalf("expected exactly one terminal marker: %s", rec.Body)
			}
		})
	}
}

func TestChatCreditPreludeNoSpareAccount(t *testing.T) {
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return 200, creditErrorSSE, true
	})
	p := testPoolWith(&auth.Auth{UID: "only", AccessToken: "synthetic", ExpiresAt: 9999999999})
	defer p.Close()
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"cn:hy3","stream":true,"messages":[]}`)))
	if rec.Code != http.StatusServiceUnavailable || calls != 1 || !strings.Contains(rec.Body.String(), "no_healthy_account") {
		t.Fatalf("must report exhaustion without infinite retries: status=%d calls=%d body=%s", rec.Code, calls, rec.Body)
	}
}
