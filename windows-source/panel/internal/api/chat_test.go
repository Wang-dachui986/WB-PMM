package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api-gui/internal/gateway"
	"workbuddy2api-gui/internal/ops"
)

func chatServerForTest(url string) *Server {
	s := newPolicyServer(false)
	s.svc = ops.New(s.cfg, nil, gateway.New(url, func() string { return "synthetic" }, time.Second), nil)
	return s
}

func TestChatStreamCompletionTimingAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(90 * time.Millisecond)
			if !fail {
				io.WriteString(w, "data: [DONE]\n\n")
			}
		}))
		s := chatServerForTest(upstream.URL)
		rec := httptest.NewRecorder()
		s.handleChatStream(rec, httptest.NewRequest(http.MethodPost, "/api/chat/stream", strings.NewReader(`{"model":"fake","messages":[{"role":"user","content":"hi"}]}`)))
		upstream.Close()
		var terminal map[string]any
		done := 0
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var value map[string]any
			if err := json.Unmarshal([]byte(line[6:]), &value); err != nil {
				t.Fatal(err)
			}
			if value["done"] == true {
				done++
			}
			terminal = value
		}
		if fail {
			if done != 0 || terminal["error"] == nil {
				t.Fatalf("failure became success: %s", rec.Body)
			}
		} else if done != 1 {
			t.Fatalf("done=%d body=%s", done, rec.Body)
		}
		first := terminal["ttfb_ms"].(float64)
		elapsed := terminal["elapsed_ms"].(float64)
		if first < 20 || elapsed-first < 70 {
			t.Fatalf("TTFB must be request-to-first-reasoning: first=%v elapsed=%v", first, elapsed)
		}
	}
}

func TestChatCancelReachesUpstream(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "nonstream"}[stream], func(t *testing.T) {
			arrived := make(chan struct{})
			cancelled := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"part\"}}]}\n\n")
					w.(http.Flusher).Flush()
				}
				close(arrived)
				select {
				case <-r.Context().Done():
					close(cancelled)
				case <-time.After(3 * time.Second):
				}
			}))
			defer upstream.Close()
			s := chatServerForTest(upstream.URL)
			handler := s.handleChat
			if stream {
				handler = s.handleChatStream
			}
			panel := httptest.NewServer(http.HandlerFunc(handler))
			defer panel.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, panel.URL, strings.NewReader(`{"model":"fake","messages":[{"role":"user","content":"hi"}]}`))
			completed := make(chan struct{})
			go func() {
				defer close(completed)
				res, err := http.DefaultClient.Do(req)
				if err == nil {
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
				}
			}()
			select {
			case <-arrived:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream not reached")
			}
			cancel()
			select {
			case <-cancelled:
			case <-time.After(2 * time.Second):
				t.Fatal("cancel did not reach upstream")
			}
			select {
			case <-completed:
			case <-time.After(2 * time.Second):
				t.Fatal("client did not finish")
			}
		})
	}
}
