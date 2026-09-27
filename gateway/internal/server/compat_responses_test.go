package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

func responseTestHandler(t *testing.T, sse string, captured *map[string]any, calls *int) *Handler {
	t.Helper()
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sse, true })
	transport := up.HTTP.Transport
	up.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*calls++
		if captured != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body, captured); err != nil {
				t.Fatal(err)
			}
		}
		return transport.RoundTrip(r)
	})
	p := testPoolWith(&auth.Auth{UID: "responses", AccessToken: "token", ExpiresAt: 9999999999})
	return NewHandler(Config{Pool: p, Upstream: up, APIKey: "test-key"})
}

func responseRequest(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func responseSSE(chunks ...string) string {
	var out strings.Builder
	for _, chunk := range chunks {
		out.WriteString("data: " + chunk + "\n\n")
	}
	out.WriteString("data: [DONE]\n\n")
	return out.String()
}

func TestResponsesStatelessHistoryAndImage(t *testing.T) {
	sse := responseSSE(
		`{"id":"chatcmpl-test","created":1,"model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-test","created":1,"model":"glm-5.2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":3}}}`,
	)
	var outbound map[string]any
	calls := 0
	h := responseTestHandler(t, sse, &outbound, &calls)
	body := `{"model":"glm-5.2","store":false,"instructions":"Be brief","max_output_tokens":128,"input":[` +
		`{"role":"user","content":[{"type":"input_text","text":"Look"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8=","detail":"low"}]},` +
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I will check","annotations":[]}]},` +
		`{"type":"function_call","call_id":"call_prior","name":"lookup","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_prior","output":"{\"ok\":true}"},` +
		`{"role":"user","content":"Summarize"}]}`
	w := responseRequest(t, h, body)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if calls != 1 {
		t.Fatalf("upstream calls=%d want 1", calls)
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["object"] != "response" || result["status"] != "completed" || result["store"] != false || result["output_text"] != "done" {
		t.Fatalf("response=%v", result)
	}
	if !strings.HasPrefix(result["id"].(string), "resp_") {
		t.Fatalf("id=%v", result["id"])
	}
	usage := result["usage"].(map[string]any)
	if usage["input_tokens"] != float64(11) || usage["output_tokens"] != float64(2) || usage["total_tokens"] != float64(13) {
		t.Fatalf("usage=%v", usage)
	}
	if usage["input_tokens_details"].(map[string]any)["cached_tokens"] != float64(3) {
		t.Fatalf("usage details=%v", usage)
	}
	messages := outbound["messages"].([]any)
	if len(messages) != 6 || messages[0].(map[string]any)["content"] != "Be brief" {
		t.Fatalf("messages=%v", messages)
	}
	image := messages[1].(map[string]any)["content"].([]any)[1].(map[string]any)["image_url"].(map[string]any)
	if image["url"] != "data:image/png;base64,aGVsbG8=" || image["detail"] != "low" {
		t.Fatalf("image=%v", image)
	}
	call := messages[3].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "call_prior" || call["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tool call=%v", call)
	}
	if messages[4].(map[string]any)["tool_call_id"] != "call_prior" {
		t.Fatalf("tool result=%v", messages[4])
	}
	if outbound["max_tokens"] != float64(128) {
		t.Fatalf("max_tokens=%v", outbound["max_tokens"])
	}
}

func TestResponsesStreamingTextEvents(t *testing.T) {
	sse := responseSSE(
		`{"id":"chatcmpl-text","created":1,"model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-text","created":1,"model":"glm-5.2","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-text","created":1,"model":"glm-5.2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
	)
	calls := 0
	h := responseTestHandler(t, sse, nil, &calls)
	w := responseRequest(t, h, `{"model":"glm-5.2","stream":true,"input":"Hi"}`)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body)
	}
	events := decodeResponseEvents(t, w.Body.String())
	want := []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.completed"}
	if len(events) != len(want) {
		t.Fatalf("events=%v", events)
	}
	for i, event := range events {
		if event["type"] != want[i] || event["sequence_number"] != float64(i) {
			t.Fatalf("event %d=%v want %s", i, event, want[i])
		}
	}
	completed := events[len(events)-1]["response"].(map[string]any)
	if completed["status"] != "completed" || completed["output_text"] != "Hello world" {
		t.Fatalf("completed=%v", completed)
	}
	if completed["usage"].(map[string]any)["total_tokens"] != float64(6) {
		t.Fatalf("usage=%v", completed["usage"])
	}
}

func TestResponsesFunctionToolRoundTrip(t *testing.T) {
	sse := responseSSE(
		`{"id":"chatcmpl-call","created":1,"model":"glm-5.2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"lookup","arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-call","created":1,"model":"glm-5.2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"LA\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":4,"total_tokens":9}}`,
	)
	var outbound map[string]any
	calls := 0
	h := responseTestHandler(t, sse, &outbound, &calls)
	body := `{"model":"glm-5.2","stream":true,"input":"Weather?","tools":[{"type":"function","name":"lookup","description":"Look up weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}},"strict":true}],"tool_choice":{"type":"function","name":"lookup"}}`
	w := responseRequest(t, h, body)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	events := decodeResponseEvents(t, w.Body.String())
	var done, completed map[string]any
	for _, event := range events {
		if event["type"] == "response.function_call_arguments.done" {
			done = event
		}
		if event["type"] == "response.completed" {
			completed = event["response"].(map[string]any)
		}
	}
	if done == nil || done["arguments"] != `{"city":"LA"}` || done["name"] != "lookup" {
		t.Fatalf("arguments.done=%v", done)
	}
	if completed == nil {
		t.Fatal("missing response.completed")
	}
	output := completed["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output=%v", output)
	}
	call := output[0].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_abc" || call["status"] != "completed" {
		t.Fatalf("call=%v", call)
	}
	tools := outbound["tools"].([]any)
	if tools[0].(map[string]any)["function"].(map[string]any)["name"] != "lookup" {
		t.Fatalf("outbound tools=%v", tools)
	}
	if outbound["tool_choice"] != "lookup" {
		t.Fatalf("outbound tool_choice=%v", outbound["tool_choice"])
	}
}

func TestResponsesLengthMapsToIncomplete(t *testing.T) {
	sse := responseSSE(`{"id":"chatcmpl-length","created":1,"model":"glm-5.2","choices":[{"index":0,"delta":{"content":"cut"},"finish_reason":"length"}]}`)
	calls := 0
	h := responseTestHandler(t, sse, nil, &calls)
	w := responseRequest(t, h, `{"model":"glm-5.2","input":"Hi"}`)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "incomplete" || result["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("result=%v", result)
	}
}

func TestResponsesUsageOnlyFinalChunk(t *testing.T) {
	sse := responseSSE(
		`{"id":"chatcmpl-usage","created":1,"model":"glm-5.2","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-usage","created":1,"model":"glm-5.2","usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
	)
	calls := 0
	h := responseTestHandler(t, sse, nil, &calls)
	w := responseRequest(t, h, `{"model":"glm-5.2","input":"Hi"}`)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["usage"].(map[string]any)["total_tokens"] != float64(4) {
		t.Fatalf("usage=%v", result["usage"])
	}
}

func TestResponsesRejectUnsupportedWithoutUpstream(t *testing.T) {
	cases := []string{
		`{"model":"glm-5.2","input":"Hi","previous_response_id":"resp_old"}`,
		`{"model":"glm-5.2","input":"Hi","store":true}`,
		`{"model":"glm-5.2","input":"Hi","background":true}`,
		`{"model":"glm-5.2","input":"Hi","conversation":"conv_old"}`,
		`{"model":"glm-5.2","input":"Hi","tools":[{"type":"web_search"}]}`,
		`{"model":"glm-5.2","input":[{"role":"user","content":[{"type":"input_file","file_id":"file_1"}]}]}`,
		`{"model":"glm-5.2","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,not-base64"}]}]}`,
		`{"model":"glm-5.2","input":[{"type":"function_call_output","call_id":"call_missing","output":"x"}]}`,
		`{"model":"glm-5.2","input":[{"type":"function_call","call_id":"call_unfinished","name":"lookup","arguments":"{}"}]}`,
		`{"model":"glm-5.2","input":"Hi","truncation":"auto"}`,
		`{"model":"glm-5.2","input":"Hi","text":{"format":{"type":"json_schema"}}}`,
		`{"model":"glm-5.2","input":"Hi","unknown_feature":1}`,
	}
	calls := 0
	h := responseTestHandler(t, sseOK, nil, &calls)
	for _, body := range cases {
		w := responseRequest(t, h, body)
		if w.Code != 400 {
			t.Errorf("status=%d body=%s request=%s", w.Code, w.Body, body)
		}
		assertJSONErrorCode(t, w.Body.String(), "invalid_request")
	}
	if calls != 0 {
		t.Fatalf("unsupported requests reached upstream %d times", calls)
	}
}

func decodeResponseEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var data string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if data == "" {
			t.Fatalf("missing data in frame %q", frame)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("event JSON: %v, frame=%q", err, frame)
		}
		events = append(events, event)
	}
	return events
}
