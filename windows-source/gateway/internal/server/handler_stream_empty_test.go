package server

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// TestChatStreamEmptyUpstreamLogged502 RED：上游 200 + 空流（0 有效帧）时，
// StreamHint 写 error 帧 + [DONE] 兜底（wire 200），但日志状态必须收敛为 502
// 观测——此前 `_ =` 吞错把失败流记成 200 假成功（排障被误导）。
func TestChatStreamEmptyUpstreamLogged502(t *testing.T) {
	withChatLog(t)
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, "", true // 空 body，非 SSE
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	out := captureStdout(t, func() {
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	})
	if rec.Code != 200 {
		t.Fatalf("wire code=%d want 200 (headers already sent before empty-stream detected)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "empty upstream stream") {
		t.Errorf("client should receive empty-stream error frame: %s", rec.Body)
	}
	if !strings.Contains(out, "| 502 |") {
		t.Errorf("log row status must be 502 (empty stream is upstream failure), got:\n%s", out)
	}
}

func TestChatStreamFailureMetrics(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		failed     int64
	}{
		{"truncated", "data: {\"choices\":[{\"delta\":{\"content\":\"part\"}}]}\n\n", 1},
		{"upstream error", "data: {\"choices\":[{\"delta\":{\"content\":\"part\"}}]}\n\ndata: {\"error\":{\"message\":\"synthetic failure\"}}\n\ndata: [DONE]\n\n", 1},
		{"null after content id", "data: {\"id\":\"real-id\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: null\n\ndata: [DONE]\n\n", 1},
		{"malformed after content", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: not-json\n\ndata: [DONE]\n\n", 1},
		{"complete", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetMetricsForTest(t)
			withChatLog(t)
			up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, tc.wire, true })
			p := testPoolWith(&auth.Auth{UID: "synthetic", AccessToken: "synthetic", ExpiresAt: 9999999999})
			defer p.Close()
			h := NewHandler(Config{Pool: p, Upstream: up})
			rec := httptest.NewRecorder()
			out := captureStdout(t, func() {
				h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"test"}]}`)))
			})
			if tc.failed == 1 && !strings.Contains(out, "| 502 |") {
				t.Fatalf("upstream failure must log 502: %s", out)
			}
			total := MetricsSnapshotOf().Total
			if total.Requests != 1 || total.Failed != tc.failed || total.Success != 1-tc.failed {
				t.Fatalf("wire=%d metrics=%+v", rec.Code, total)
			}
			if rec.Code != 200 || strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
				t.Fatalf("SSE termination changed: %d %s", rec.Code, rec.Body)
			}
			if tc.failed == 1 && !strings.Contains(rec.Body.String(), `"error"`) {
				t.Fatal("error frame missing")
			}
		})
	}
}

// The panel closes its request as soon as it receives an SSE error. That
// disconnect must not relabel an already observed upstream failure as 499.
func TestChatStreamErrorBeforeClientCancel(t *testing.T) {
	for _, upstreamError := range []bool{true, false} {
		t.Run(map[bool]string{true: "upstream error stays 502", false: "user stop stays 499"}[upstreamError], func(t *testing.T) {
			resetMetricsForTest(t)
			withChatLog(t)
			up := &upstream.Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				reader, writer := io.Pipe()
				go func() {
					defer writer.Close()
					frame := `{"choices":[{"delta":{"content":"partial"}}]}`
					if upstreamError {
						frame = `{"error":{"message":"synthetic upstream failure"}}`
					}
					if _, err := io.WriteString(writer, "data: "+frame+"\n\n"); err != nil {
						return
					}
					select {
					case <-r.Context().Done():
						writer.CloseWithError(r.Context().Err())
					case <-time.After(time.Second):
						io.WriteString(writer, "data: [DONE]\n\n")
					}
				}()
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
			})}}
			p := testPoolWith(&auth.Auth{UID: "synthetic", AccessToken: "synthetic", ExpiresAt: 9999999999})
			defer p.Close()
			h := NewHandler(Config{Pool: p, Upstream: up})
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r); close(done) }))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := captureStdout(t, func() {
				req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"test"}]}`))
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				defer response.Body.Close()
				line, err := bufio.NewReader(response.Body).ReadString('\n')
				if err != nil || !strings.HasPrefix(line, "data: ") {
					t.Errorf("missing first frame: %q %v", line, err)
					return
				}
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("handler did not finish")
				}
			})
			status := "| 499 |"
			if upstreamError {
				status = "| 502 |"
			}
			if !strings.Contains(out, status) {
				t.Fatalf("want %s after client closes first frame, log=%s", status, out)
			}
			if total := MetricsSnapshotOf().Total; total.Failed != 1 || total.Success != 0 {
				t.Fatalf("wrong metrics: %+v", total)
			}
		})
	}
}
