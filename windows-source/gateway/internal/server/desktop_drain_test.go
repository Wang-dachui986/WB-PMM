package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

const drainChatBody = `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"synthetic"}]}`

func newDrainTestHandler(t *testing.T) (*Handler, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	p := testPoolWith(&auth.Auth{UID: "synthetic", AccessToken: "synthetic", ExpiresAt: 9999999999})
	t.Cleanup(p.Close)
	h := NewHandler(Config{
		Pool: p, DesktopMode: true, APIKey: "inference-key", ManagementKey: "management-key",
		Upstream: newFakeUpstream(t, func(string) (int, string, bool) {
			calls.Add(1)
			return http.StatusOK, sseOK, true
		}),
	})
	return h, &calls
}

func drainControl(t *testing.T, h *Handler, operation string, generation uint64, wantStatus int) desktopDrainResponse {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/desktop/drain", strings.NewReader(fmt.Sprintf(`{"operation":%q,"generation":%d}`, operation, generation)))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set(managementKeyHeader, "management-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("%s generation %d: status=%d want=%d body=%s", operation, generation, rec.Code, wantStatus, rec.Body)
	}
	var state desktopDrainResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if !state.OK || state.Generation != generation || state.Drained != (state.InFlight == 0) {
			t.Fatalf("invalid drain response: %+v", state)
		}
	} else {
		assertJSONErrorCode(t, rec.Body.String(), "drain_generation_conflict")
	}
	return state
}

func assertDrainState(t *testing.T, state desktopDrainResponse, inFlight uint64, accepting bool) {
	t.Helper()
	if state.InFlight != inFlight || state.Accepting != accepting || state.Drained != (inFlight == 0) {
		t.Fatalf("state=%+v want in_flight=%d accepting=%v", state, inFlight, accepting)
	}
}

type drainTestBody struct{ read func([]byte) (int, error) }

func (b drainTestBody) Read(p []byte) (int, error) { return b.read(p) }
func (b drainTestBody) Close() error               { return nil }

func drainWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("fake request did not reach the expected lifecycle boundary")
	}
}

func assertDrainRejectsChat(t *testing.T, h *Handler) {
	t.Helper()
	var reads int
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer inference-key")
	req.Body = drainTestBody{read: func([]byte) (int, error) { reads++; return 0, io.EOF }}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || reads != 0 {
		t.Fatalf("draining request: status=%d body reads=%d", rec.Code, reads)
	}
	assertJSONErrorCode(t, rec.Body.String(), "gateway_draining")
}

func TestDesktopDrainCountsBlockedBodyAndReleases(t *testing.T) {
	for _, outcome := range []string{"complete", "cancel", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			h, calls := newDrainTestHandler(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			body := strings.NewReader(drainChatBody)
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Authorization", "Bearer inference-key")
			req.Body = drainTestBody{read: func(p []byte) (int, error) {
				once.Do(func() {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
					}
				})
				if ctx.Err() != nil {
					return 0, ctx.Err()
				}
				if outcome == "panic" {
					panic("synthetic body panic")
				}
				return body.Read(p)
			}}
			rec := httptest.NewRecorder()
			var recovered any
			go func() {
				defer func() { recovered = recover(); close(done) }()
				h.ServeHTTP(rec, req)
			}()
			drainWait(t, started)
			assertDrainState(t, drainControl(t, h, "begin", 1, 200), 1, false)
			assertDrainRejectsChat(t, h)
			if calls.Load() != 0 {
				t.Fatal("upstream called before admitted body was released")
			}
			if outcome == "cancel" {
				cancel()
			} else {
				close(release)
			}
			drainWait(t, done)
			if (recovered != nil) != (outcome == "panic") {
				t.Fatalf("unexpected recovered panic: %v", recovered)
			}
			wantCalls := int32(0)
			if outcome == "complete" {
				wantCalls = 1
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "[DONE]") {
					t.Fatalf("admitted chat failed: %d %s", rec.Code, rec.Body)
				}
			}
			if calls.Load() != wantCalls {
				t.Fatalf("upstream calls=%d want=%d", calls.Load(), wantCalls)
			}
			assertDrainState(t, drainControl(t, h, "status", 1, 200), 0, false)
			assertDrainState(t, drainControl(t, h, "end", 1, 200), 0, true)
		})
	}
}

func TestDesktopDrainGenerationAndEmptyBegin(t *testing.T) {
	h, calls := newDrainTestHandler(t)
	drainControl(t, h, "status", 1, 409)
	drainControl(t, h, "end", 1, 409)
	assertDrainState(t, drainControl(t, h, "begin", 10, 200), 0, false)
	assertDrainState(t, drainControl(t, h, "begin", 10, 200), 0, false)
	for _, generation := range []uint64{9, 11} {
		for _, operation := range []string{"begin", "status", "end"} {
			drainControl(t, h, operation, generation, 409)
			assertDrainState(t, drainControl(t, h, "status", 10, 200), 0, false)
		}
	}
	assertDrainRejectsChat(t, h)
	assertDrainState(t, drainControl(t, h, "end", 10, 200), 0, true)
	assertDrainState(t, drainControl(t, h, "end", 10, 200), 0, true)
	drainControl(t, h, "begin", 10, 409)
	assertDrainState(t, drainControl(t, h, "status", 10, 200), 0, true)
	assertDrainState(t, drainControl(t, h, "begin", 11, 200), 0, false)
	drainControl(t, h, "end", 10, 409)
	drainControl(t, h, "begin", 10, 409)
	assertDrainRejectsChat(t, h)
	if calls.Load() != 0 {
		t.Fatal("closed admission called upstream")
	}
	assertDrainState(t, drainControl(t, h, "end", 11, 200), 0, true)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(drainChatBody))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer inference-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || calls.Load() != 1 {
		t.Fatalf("end did not restore chat admission: %d calls=%d", rec.Code, calls.Load())
	}
	// Preserve the full uint64 contract without wrapping the high watermark.
	assertDrainState(t, drainControl(t, h, "begin", ^uint64(0), 200), 0, false)
	drainControl(t, h, "end", ^uint64(0), 200)
	drainControl(t, h, "begin", 1, 409)
}

func TestDesktopDrainStrictJSON(t *testing.T) {
	for _, body := range []string{
		``, `null`, `[]`, `{}`, `{"operation":"begin"}`, `{"generation":1}`,
		`{"operation":"other","generation":1}`, `{"operation":"begin","generation":0}`,
		`{"operation":"begin","generation":-1}`, `{"operation":"begin","generation":1.5}`,
		`{"operation":"begin","generation":18446744073709551616}`,
		`{"operation":"begin","generation":"1"}`, `{"operation":"begin","generation":null}`,
		`{"operation":null,"generation":1}`, `{"operation":"begin","generation":1,"extra":true}`,
		`{"operation":"begin","generation":1} {}`, `{"operation":"begin","generation":1} garbage`,
		`{"operation":"begin","generation":`, strings.Repeat(" ", 1024) + `{"operation":"begin","generation":1}`,
	} {
		h, _ := newDrainTestHandler(t)
		req := httptest.NewRequest("POST", "/admin/desktop/drain", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set(managementKeyHeader, "management-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid JSON %q accepted: %d %s", body, rec.Code, rec.Body)
		}
		assertJSONErrorCode(t, rec.Body.String(), "invalid_request")
		assertDrainState(t, drainControl(t, h, "begin", 1, 200), 0, false)
	}
}

func TestDesktopDrainManagementKeyAndOrdinaryMode(t *testing.T) {
	for _, key := range []string{"", "inference-key", "wrong", "management-key"} {
		h, _ := newDrainTestHandler(t)
		req := httptest.NewRequest("POST", "/admin/desktop/drain", strings.NewReader(`{"operation":"begin","generation":1}`))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer inference-key")
		req.Header.Set(managementKeyHeader, key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		want := 401
		if key == "management-key" {
			want = 200
		}
		if rec.Code != want {
			t.Fatalf("management key %q: status=%d want=%d", key, rec.Code, want)
		}
	}
	h, _ := newDrainTestHandler(t)
	h.cfg.ManagementKey = ""
	req := httptest.NewRequest("POST", "/admin/desktop/drain", strings.NewReader(`{"operation":"begin","generation":1}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal("missing configured management key must fail closed")
	}
	// AdminEnabled does not opt ordinary web deployments into desktop drain.
	for _, admin := range []bool{false, true} {
		cfg := h.cfg
		cfg.DesktopMode, cfg.AdminEnabled, cfg.APIKey = false, admin, ""
		ordinary := NewHandler(cfg)
		for _, method := range []string{"GET", "POST"} {
			rec := httptest.NewRecorder()
			ordinary.ServeHTTP(rec, httptest.NewRequest(method, "/admin/desktop/drain", nil))
			if rec.Code != 404 {
				t.Fatalf("ordinary drain endpoint exposed: admin=%v method=%s code=%d", admin, method, rec.Code)
			}
		}
		ordinary.drain.update("begin", 1)
		rec := httptest.NewRecorder()
		ordinary.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(drainChatBody)))
		if rec.Code != 200 {
			t.Fatalf("ordinary chat behavior changed: %d %s", rec.Code, rec.Body)
		}
	}
}

func TestDesktopDrainTracksStreamingCompletionAndCancel(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", stop), func(t *testing.T) {
			h, _ := newDrainTestHandler(t)
			finish, upstreamDone, handlerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			h.cfg.Upstream = &upstream.Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				reader, writer := io.Pipe()
				go func() {
					defer close(upstreamDone)
					defer writer.Close()
					if _, err := io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"); err != nil {
						return
					}
					select {
					case <-r.Context().Done():
						writer.CloseWithError(r.Context().Err())
					case <-finish:
						io.WriteString(writer, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
				}()
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
			})}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				h.ServeHTTP(w, r)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/chat/completions", strings.NewReader(drainChatBody))
			req.Header.Set("Authorization", "Bearer inference-key")
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if _, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil {
				t.Fatal(err)
			}
			assertDrainState(t, drainControl(t, h, "begin", 1, 200), 1, false)
			assertDrainState(t, drainControl(t, h, "status", 1, 200), 1, false)
			if stop {
				cancel()
			} else {
				close(finish)
				if _, err := io.Copy(io.Discard, response.Body); err != nil {
					t.Fatal(err)
				}
			}
			drainWait(t, handlerDone)
			drainWait(t, upstreamDone)
			assertDrainState(t, drainControl(t, h, "status", 1, 200), 0, false)
			assertDrainState(t, drainControl(t, h, "end", 1, 200), 0, true)
		})
	}
}
