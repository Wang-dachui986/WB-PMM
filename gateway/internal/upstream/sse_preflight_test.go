package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

const hardSSEFrame = "data: {\"error\":{\"code\":14018,\"message\":\"Credits exhausted\"}}\n\n"

func syntheticSSEClient(wire string) *Client {
	return testClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
	})
}

func openSyntheticStream(c *Client) (io.ReadCloser, int, []byte, error) {
	return c.ChatStreamContext(context.Background(), &auth.Auth{AccessToken: "synthetic"}, []byte(`{"model":"m","messages":[]}`), "", ChatMeta{})
}

func TestSSEPreludeHardCreditBeforeOutput(t *testing.T) {
	wire := ": heartbeat\n\n" +
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"\",\"reasoning_content\":\"\",\"tool_calls\":[]}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1}}\n\n" + hardSSEFrame
	rc, status, body, err := openSyntheticStream(syntheticSSEClient(wire))
	var ue *Error
	if rc != nil || status != http.StatusPaymentRequired || !errors.As(err, &ue) || ue.Kind != ErrHardCredit {
		t.Fatalf("rc=%v status=%d err=%v", rc, status, err)
	}
	if string(body) != strings.TrimSpace(strings.TrimPrefix(hardSSEFrame, "data: ")) || ue.Msg != string(body) {
		t.Fatalf("hard credit payload not preserved: %q", body)
	}
}

func TestSSEPreludeReplaysVisibleAndUnknownFrames(t *testing.T) {
	const role = "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"
	for _, tc := range []struct {
		name  string
		frame string
	}{
		{"content", `{"choices":[{"delta":{"content":"hi"}}]}`},
		{"reasoning", `{"choices":[{"delta":{"reasoning_content":"think"}}]}`},
		{"refusal", `{"choices":[{"delta":{"refusal":"cannot"}}]}`},
		{"tool call", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c"}]}}]}`},
		{"function call", `{"choices":[{"delta":{"function_call":{"name":"f"}}}]}`},
		{"message", `{"choices":[{"message":{"content":"whole"}}]}`},
		{"finish", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`},
		{"unknown", `{"future_field":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := role + "data: " + tc.frame + "\n\n" + hardSSEFrame
			rc, status, _, err := openSyntheticStream(syntheticSSEClient(wire))
			if err != nil || status != http.StatusOK || rc == nil {
				t.Fatalf("rc=%v status=%d err=%v", rc, status, err)
			}
			defer rc.Close()
			got, readErr := io.ReadAll(rc)
			if readErr != nil || string(got) != wire {
				t.Fatalf("replay err=%v equal=%v", readErr, string(got) == wire)
			}
		})
	}
	for _, line := range []string{"unknown-preface\n", "data\n"} {
		wire := line + hardSSEFrame
		rc, status, _, err := openSyntheticStream(syntheticSSEClient(wire))
		if err != nil || status != http.StatusOK {
			t.Fatalf("unknown non-data line unexpectedly rotated: status=%d err=%v", status, err)
		}
		got, readErr := io.ReadAll(rc)
		rc.Close()
		if readErr != nil || string(got) != wire {
			t.Fatalf("unknown non-data replay err=%v equal=%v", readErr, string(got) == wire)
		}
	}

	wire := role + `data: {"error":{"message":"synthetic unavailable"}}` + "\n\n"
	rc, status, _, err := openSyntheticStream(syntheticSSEClient(wire))
	if err != nil || status != http.StatusOK {
		t.Fatalf("unknown error unexpectedly rotated: status=%d err=%v", status, err)
	}
	defer rc.Close()
	if got, readErr := io.ReadAll(rc); readErr != nil || string(got) != wire {
		t.Fatalf("unknown error replay err=%v equal=%v", readErr, string(got) == wire)
	}
}

func TestSSEPreludeLimitFailsOpenWithoutLosingBytes(t *testing.T) {
	wire := strings.Repeat("x", ssePreludeLimit) + "\n" + hardSSEFrame
	rc, status, _, err := openSyntheticStream(syntheticSSEClient(wire))
	if err != nil || status != http.StatusOK || rc == nil {
		t.Fatalf("limit status=%d err=%v", status, err)
	}
	defer rc.Close()
	got, readErr := io.ReadAll(rc)
	if readErr != nil || string(got) != wire {
		t.Fatalf("limit replay err=%v equal=%v", readErr, string(got) == wire)
	}
}

func TestSSEPreludeCancellationStopsBlockedRead(t *testing.T) {
	ready := make(chan struct{})
	c := testClient(func(req *http.Request) (*http.Response, error) {
		pr, pw := io.Pipe()
		go func() {
			_, _ = pw.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
			close(ready)
			<-req.Context().Done()
			pw.CloseWithError(req.Context().Err())
		}()
		return &http.Response{StatusCode: http.StatusOK, Body: pr}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, _, err := c.ChatStreamContext(ctx, &auth.Auth{AccessToken: "synthetic"}, []byte(`{"model":"m"}`), "", ChatMeta{})
		done <- err
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("prelude did not read role frame")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prelude read did not stop on cancel")
	}
}

func TestSSEHardCreditAfterOutputIsTypedButNotReplayed(t *testing.T) {
	wire := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" + hardSSEFrame
	var ue *Error
	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(wire), nil); !errors.As(err, &ue) || ue.Kind != ErrHardCredit || !IsStreamFailure(err) {
		t.Fatalf("stream error=%v", err)
	}
	if !strings.Contains(rec.Body.String(), "partial") || !strings.Contains(rec.Body.String(), "Credits exhausted") || strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
		t.Fatalf("stream content or terminal lost: %s", rec.Body.String())
	}
	ue = nil
	if _, err := Aggregate(strings.NewReader(wire)); !errors.As(err, &ue) || ue.Kind != ErrHardCredit {
		t.Fatalf("aggregate error=%v", err)
	}
}

func TestFrameKindHardCreditAndRateLimitAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    ErrKind
	}{
		{`{"error":{"code":14018}}`, ErrHardCredit},
		{`{"error":{"code":"14018","message":"quota exceeded"}}`, ErrHardCredit},
		{`{"error":{"code":6004,"message":"credits exhausted"}}`, ErrSoftRate},
		{`{"error":{"status":429,"message":"quota exceeded"}}`, ErrSoftRate},
		{`{"error":{"code":429,"message":"quota exceeded"}}`, ErrSoftRate},
		{`{"error":{"code":"429","message":"quota exceeded"}}`, ErrSoftRate},
		{`{"error":{"statusCode":"429","message":"积分不足"}}`, ErrSoftRate},
	} {
		if got := FrameKind(tc.payload); got != tc.want {
			t.Errorf("FrameKind(%s)=%v want %v", tc.payload, got, tc.want)
		}
	}
}
