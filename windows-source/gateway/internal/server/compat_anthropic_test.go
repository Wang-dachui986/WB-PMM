package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

const anthropicAnswer = "data: {\"id\":\"msg-upstream\",\"model\":\"hy3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":2,\"total_tokens\":10}}\n\n" +
	"data: [DONE]\n\n"

func anthropicTestHandler(t *testing.T, transport http.RoundTripper) *Handler {
	t.Helper()
	p := testPoolWith(&auth.Auth{UID: "only", AccessToken: "offline", ExpiresAt: 9999999999})
	t.Cleanup(p.Close)
	return NewHandler(Config{Pool: p, Upstream: &upstream.Client{
		HTTP: &http.Client{Transport: transport}, ChatBaseCN: "https://offline.invalid", BillingBaseCN: "https://offline.invalid",
	}})
}

func anthropicUpstreamResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestAnthropicMessagesTextSystemAndImage(t *testing.T) {
	var outgoing map[string]any
	h := anthropicTestHandler(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&outgoing); err != nil {
			t.Errorf("outgoing JSON: %v", err)
		}
		return anthropicUpstreamResponse(anthropicAnswer), nil
	}))
	body := `{"model":"hy3","max_tokens":128,"system":[{"type":"text","text":"be concise"}],"messages":[{"role":"user","content":[{"type":"text","text":"see "},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}},{"type":"image","source":{"type":"url","url":"https://example.test/p.png"}}]}]}`
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "message" || got["stop_reason"] != "end_turn" || got["model"] != "hy3" {
		t.Fatalf("bad Messages response: %#v", got)
	}
	usage := got["usage"].(map[string]any)
	if usage["input_tokens"] != float64(8) || usage["output_tokens"] != float64(2) {
		t.Fatalf("usage=%v", usage)
	}
	msgs := outgoing["messages"].([]any)
	if msgs[0].(map[string]any)["content"] != "be concise" {
		t.Fatalf("system=%v", msgs[0])
	}
	parts := msgs[1].(map[string]any)["content"].([]any)
	if len(parts) != 3 || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,QUJD" || parts[2].(map[string]any)["image_url"].(map[string]any)["url"] != "https://example.test/p.png" {
		t.Fatalf("images=%v", parts)
	}
}

func TestAnthropicToolRoundTrip(t *testing.T) {
	var outgoing map[string]any
	answer := "data: {\"id\":\"msg-tool\",\"model\":\"hy3\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"next\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"city\\\":\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"SF\\\"}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"
	h := anthropicTestHandler(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&outgoing); err != nil {
			t.Errorf("outgoing JSON: %v", err)
		}
		return anthropicUpstreamResponse(answer), nil
	}))
	body := `{"model":"hy3","max_tokens":128,"tools":[{"name":"lookup","description":"find weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"tool_choice":{"type":"tool","name":"lookup"},"messages":[{"role":"user","content":"weather"},{"role":"assistant","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"prev","name":"lookup","input":{"city":"LA"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"prev","content":[{"type":"text","text":"sunny"}]},{"type":"text","text":"again"}]}]}`
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason=%v", got["stop_reason"])
	}
	block := got["content"].([]any)[0].(map[string]any)
	if block["type"] != "tool_use" || block["id"] != "next" || block["name"] != "lookup" || block["input"].(map[string]any)["city"] != "SF" {
		t.Fatalf("tool block=%v", block)
	}
	msgs := outgoing["messages"].([]any)
	if len(msgs) != 4 || msgs[1].(map[string]any)["role"] != "assistant" || msgs[2].(map[string]any)["role"] != "tool" || msgs[2].(map[string]any)["content"] != "sunny" || msgs[3].(map[string]any)["role"] != "user" {
		t.Fatalf("tool result pairing=%v", msgs)
	}
	if outgoing["tool_choice"] != "lookup" {
		t.Fatalf("tool_choice=%v", outgoing["tool_choice"])
	}
}

func TestAnthropicRejectsUnsupportedWithoutUpstream(t *testing.T) {
	calls := 0
	h := anthropicTestHandler(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return anthropicUpstreamResponse(anthropicAnswer), nil
	}))
	for _, body := range []string{
		`{"model":"hy3","max_tokens":128,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"hy3","max_tokens":128,"stream":null,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"hy3","max_tokens":128,"temperature":null,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"hy3","max_tokens":128,"messages":[{"role":"user","content":null}]}`,
		`{"model":"hy3","max_tokens":128,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"sig"}]}]}`,
		`{"model":"hy3","max_tokens":128,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]}]}`,
		`{"model":"hy3","max_tokens":128,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"f1"}}]}]}`,
	} {
		rec := httptest.NewRecorder()
		h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"invalid_request_error"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
	}
	if calls != 0 {
		t.Fatalf("unsupported requests reached upstream: %d", calls)
	}
}

func TestAnthropicStreamingInterleavedToolsStaySequential(t *testing.T) {
	answer := "data: {\"id\":\"msg-tools\",\"model\":\"hy3\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"one\",\"arguments\":\"{\\\"x\\\":\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"b\",\"function\":{\"name\":\"two\",\"arguments\":\"{\\\"y\\\":2}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":6}}\n\ndata: [DONE]\n\n"
	h := anthropicTestHandler(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return anthropicUpstreamResponse(answer), nil }))
	rec := httptest.NewRecorder()
	h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"hy3","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"tools"}]}`)))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	firstStart := strings.Index(body, `"id":"a"`)
	firstStop := strings.Index(body, `"index":0,"type":"content_block_stop"`)
	secondStart := strings.Index(body, `"id":"b"`)
	secondStop := strings.Index(body, `"index":1,"type":"content_block_stop"`)
	if firstStart < 0 || firstStop < firstStart || secondStart < firstStop || secondStop < secondStart || !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Fatalf("tool events not sequential: %s", body)
	}
}

func TestAnthropicCacheUsageIsPartitioned(t *testing.T) {
	s := &anthropicResponse{usage: map[string]any{
		"prompt_tokens": float64(100), "completion_tokens": float64(7),
		"prompt_cache_hit_tokens": float64(60), "prompt_cache_write_tokens": float64(10),
	}}
	got := s.anthropicUsage()
	if got["input_tokens"] != float64(30) || got["cache_read_input_tokens"] != float64(60) || got["cache_creation_input_tokens"] != float64(10) || got["output_tokens"] != float64(7) {
		t.Fatalf("usage=%v", got)
	}
}

func TestAnthropicMissingUsageKeepsToolResult(t *testing.T) {
	answer := "data: {\"id\":\"msg-no-usage\",\"model\":\"hy3\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"tool-1\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	h := anthropicTestHandler(t, roundTripFunc(func(*http.Request) (*http.Response, error) { return anthropicUpstreamResponse(answer), nil }))
	for _, stream := range []bool{false, true} {
		body := `{"model":"hy3","max_tokens":128,"messages":[{"role":"user","content":"use lookup"}],"stream":false}`
		if stream {
			body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
		}
		rec := httptest.NewRecorder()
		h.anthropicMessages(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"stop_reason":"tool_use"`) || !strings.Contains(rec.Body.String(), `"input_tokens":0`) || !strings.Contains(rec.Body.String(), `"output_tokens":0`) {
			t.Fatalf("stream=%v status=%d body=%s", stream, rec.Code, rec.Body)
		}
		if stream && (!strings.Contains(rec.Body.String(), "event: message_stop") || strings.Contains(rec.Body.String(), "event: error")) {
			t.Fatalf("stream=%v did not finish tool call: %s", stream, rec.Body)
		}
	}
}

func TestAnthropicStreamingFlushesBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	h := anthropicTestHandler(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			_, _ = io.WriteString(writer, "data: {\"id\":\"msg-live\",\"model\":\"hy3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first\"}}]}\n\n")
			<-release
			_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
	}))
	server := httptest.NewServer(http.HandlerFunc(h.anthropicMessages))
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL+"/v1/messages", strings.NewReader(`{"model":"hy3","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if line != "event: message_start\n" {
		close(release)
		t.Fatalf("first event=%q", line)
	}
	var prelude strings.Builder
	for !strings.Contains(prelude.String(), `"text":"first"`) {
		line, err = reader.ReadString('\n')
		if err != nil {
			close(release)
			t.Fatal(err)
		}
		prelude.WriteString(line)
	}
	close(release)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	all := prelude.String() + string(rest)
	for _, event := range []string{"content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"} {
		if !strings.Contains(all, "event: "+event) {
			t.Fatalf("missing %s: %s", event, all)
		}
	}
	if !strings.Contains(all, `"input_tokens":3`) || !strings.Contains(all, `"output_tokens":1`) {
		t.Fatalf("final usage=%s", all)
	}
}
