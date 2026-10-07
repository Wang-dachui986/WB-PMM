package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChatStreamTerminalAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		fail       bool
		content    string
		usage      bool
	}{
		{"CRLF UTF8 no space", "data:{\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\r\n\r\ndata:{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":0}}\r\n\r\ndata:[DONE]\r\n\r\n", false, "你好", true},
		{"finish without DONE", "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n", false, "ok", false},
		{"EOF", "data: {\"choices\":[{\"delta\":{\"content\":\"part\"}}]}\n\n", true, "part", false},
		{"half event", "data: {\"choices\":[{\"delta\":{\"content\":\"part\"}}]}\n", true, "", false},
		{"error then DONE", "data: {\"error\":{\"message\":\"synthetic failure\"}}\n\ndata: [DONE]\n\n", true, "", false},
		{"invalid", "data: not-json\n\ndata: [DONE]\n\n", true, "", false},
		{"multiline", "data: {\"choices\":\n" + "data: [{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", false, "ok", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, b := range []byte(tc.wire) {
					w.Write([]byte{b})
					w.(http.Flusher).Flush()
				}
			}))
			defer srv.Close()
			c := New(srv.URL, func() string { return "synthetic" }, time.Second)
			defer c.http.CloseIdleConnections()
			var content string
			var usage *Usage
			done := 0
			err := c.ChatStream(context.Background(), []byte(`{}`), func(d Delta) error {
				content += d.Content
				if d.Usage != nil {
					usage = d.Usage
				}
				if d.Done {
					done++
				}
				return nil
			})
			if (err != nil) != tc.fail || content != tc.content || (usage != nil) != tc.usage {
				t.Fatalf("err=%v content=%q usage=%v", err, content, usage)
			}
			if tc.fail && done != 0 || !tc.fail && done != 1 {
				t.Fatalf("done=%d", done)
			}
			if usage != nil {
				raw, _ := json.Marshal(usage)
				if string(raw) != `{"prompt_tokens":0}` {
					t.Fatalf("unknown vs zero: %s", raw)
				}
			}
		})
	}
}

func TestChatUsageMissingZeroAndPartial(t *testing.T) {
	for _, fields := range []string{"", `,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, `,"usage":{"prompt_tokens":7}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"model":"fake","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]`+fields+`}`)
		}))
		c := New(srv.URL, func() string { return "" }, time.Second)
		out, err := c.Chat(context.Background(), []byte(`{}`))
		c.http.CloseIdleConnections()
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if (out.Usage == nil) != (fields == "") {
			t.Fatalf("missing usage fabricated: %+v", out)
		}
		if strings.Contains(fields, `"prompt_tokens":0`) && (out.Usage.PromptTokens == nil || *out.Usage.PromptTokens != 0) {
			t.Fatal("observed zero lost")
		}
		if strings.Contains(fields, `"prompt_tokens":7`) && out.Usage.CompletionTokens != nil {
			t.Fatal("missing completion fabricated")
		}
	}
}
