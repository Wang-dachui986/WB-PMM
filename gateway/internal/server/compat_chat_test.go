package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatChatSinkFragmentsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var chunks []map[string]any
	sink := &compatChatSink{header: make(http.Header), cancel: cancel, onChunk: func(chunk map[string]any) error {
		chunks = append(chunks, chunk)
		return nil
	}}
	// Exercise arbitrary Write boundaries, CRLF, multiline data and the terminal marker.
	for _, part := range []string{"data: {\"choices\":", "\r\ndata: []}\r\n\r", "\ndata: [DONE]\n\n"} {
		if _, err := sink.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if len(chunks) != 1 || !sink.done || ctx.Err() != nil {
		t.Fatalf("chunks=%d done=%v context=%v", len(chunks), sink.done, ctx.Err())
	}
	stopped := errors.New("downstream closed")
	sink = &compatChatSink{header: make(http.Header), cancel: cancel, onChunk: func(map[string]any) error { return stopped }}
	if _, err := sink.Write([]byte("data: {}\n\n")); !errors.Is(err, stopped) || ctx.Err() == nil {
		t.Fatalf("callback failure did not cancel: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	sink = &compatChatSink{header: make(http.Header), cancel: cancel, onChunk: func(map[string]any) error { t.Fatal("invalid data accepted"); return nil }}
	if _, err := sink.Write([]byte("data: null\n\n")); err == nil || ctx.Err() == nil {
		t.Fatal("invalid JSON object must fail and cancel")
	}
}

func TestAnthropicInferenceAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, key, bearer string
		want              int
	}{
		{"x-api-key", "inference-key", "", 204},
		{"bearer", "", "Bearer inference-key", 204},
		{"both matching", "inference-key", "Bearer inference-key", 204},
		{"missing", "", "", 401},
		{"wrong key", "wrong", "", 401},
		{"conflict", "inference-key", "Bearer wrong", 401},
		{"private management key", "management-key", "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{cfg: Config{APIKey: "inference-key", ManagementKey: "management-key", DesktopMode: true}}
			r := httptest.NewRequest("POST", "/v1/messages", nil)
			r.Header.Set("X-Api-Key", tc.key)
			r.Header.Set("Authorization", tc.bearer)
			w := httptest.NewRecorder()
			h.withAnthropicAuth(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
			if tc.want == 401 && !strings.Contains(w.Body.String(), `"authentication_error"`) {
				t.Fatal("missing Anthropic error envelope")
			}
		})
	}
	h := &Handler{cfg: Config{DesktopMode: true}}
	w := httptest.NewRecorder()
	h.withAnthropicAuth(func(http.ResponseWriter, *http.Request) { t.Fatal("desktop empty key allowed") })(w, httptest.NewRequest("POST", "/v1/messages", nil))
	if w.Code != 401 {
		t.Fatalf("empty desktop key status=%d", w.Code)
	}
}
