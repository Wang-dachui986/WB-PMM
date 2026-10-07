package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

type compatStreamContextKey struct{}

// streamCompatibleChat keeps scheduling, billing and cancellation on the existing
// chat path. The protocol adapters only translate its complete SSE JSON events.
func (h *Handler) streamCompatibleChat(r *http.Request, body map[string]any, onChunk func(map[string]any) error) (int, []byte, error) {
	requestedStream, _ := body["stream"].(bool)
	body["stream"] = true
	body["stream_options"] = map[string]any{"include_usage": true}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	ctx, cancel := context.WithCancel(context.WithValue(r.Context(), compatStreamContextKey{}, requestedStream))
	defer cancel()
	request := r.Clone(ctx)
	request.Body = io.NopCloser(bytes.NewReader(raw))
	request.ContentLength = int64(len(raw))
	request.Header.Set("Content-Type", "application/json")
	sink := &compatChatSink{header: make(http.Header), onChunk: onChunk, cancel: cancel}
	h.chatCompletions(sink, request)
	if sink.status == 0 {
		sink.status = http.StatusOK
	}
	if sink.status < 200 || sink.status >= 300 {
		return sink.status, sink.errorBody.Bytes(), sink.err
	}
	if sink.err != nil {
		return sink.status, nil, sink.err
	}
	if err := r.Context().Err(); err != nil {
		return sink.status, nil, err
	}
	if !sink.done {
		return sink.status, nil, io.ErrUnexpectedEOF
	}
	return sink.status, nil, nil
}

type compatChatSink struct {
	header    http.Header
	status    int
	pending   []byte
	data      []string
	errorBody bytes.Buffer
	done      bool
	err       error
	onChunk   func(map[string]any) error
	cancel    context.CancelFunc
}

func (s *compatChatSink) Header() http.Header { return s.header }
func (s *compatChatSink) Flush()              {}
func (s *compatChatSink) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
}

func (s *compatChatSink) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if s.status < 200 || s.status >= 300 {
		return s.errorBody.Write(p)
	}
	if s.done {
		return len(p), nil
	}
	s.pending = append(s.pending, p...)
	for {
		n := bytes.IndexByte(s.pending, '\n')
		if n < 0 {
			break
		}
		line := strings.TrimSuffix(string(s.pending[:n]), "\r")
		s.pending = s.pending[n+1:]
		if strings.HasPrefix(line, "data:") {
			s.data = append(s.data, strings.TrimPrefix(line[5:], " "))
		} else if line == "" && len(s.data) > 0 {
			payload := strings.Join(s.data, "\n")
			s.data = nil
			if payload == "[DONE]" {
				s.done = true
				s.pending = nil
				return len(p), nil
			}
			var chunk map[string]any
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil || chunk == nil {
				s.err = errors.New("invalid chat completion stream event")
			} else {
				s.err = s.onChunk(chunk)
			}
			if s.err != nil {
				s.cancel()
				return 0, s.err
			}
		}
	}
	return len(p), nil
}

// Only the Messages endpoint accepts x-api-key. It is the same inference key;
// management routes continue to require the private desktop management key.
func (h *Handler) withAnthropicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bearer, key := r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
		valid := !h.cfg.DesktopMode && h.cfg.APIKey == ""
		if h.cfg.APIKey != "" {
			valid = (key != "" || bearer != "") &&
				(key == "" || sameSecret(key, h.cfg.APIKey)) &&
				(bearer == "" || sameSecret(bearer, "Bearer "+h.cfg.APIKey))
		}
		if !valid {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"type": "error", "error": map[string]string{"type": "authentication_error", "message": "missing or invalid API key"},
			})
			return
		}
		next(w, r)
	}
}
