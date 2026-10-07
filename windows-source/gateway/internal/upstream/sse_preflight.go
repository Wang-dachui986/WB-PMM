package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const ssePreludeLimit = 64 << 10

// probeSSEPrelude delays forwarding only non-output SSE frames. A hard-credit
// error before output can use the caller's existing account rotation path.
// Every other prelude is replayed byte-for-byte into the normal stream parser.
func probeSSEPrelude(ctx context.Context, body io.ReadCloser) (io.ReadCloser, error) {
	br := bufio.NewReaderSize(body, 4096)
	prefix := make([]byte, 0, 1024)
	lineStart := 0
	replay := func() io.ReadCloser {
		return &preludeReplay{reader: io.MultiReader(bytes.NewReader(prefix), br), body: body, ctx: ctx}
	}
	for len(prefix) < ssePreludeLimit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b, err := br.ReadByte()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if err != io.EOF {
				return nil, err
			}
			if lineStart < len(prefix) {
				if stop, hard := inspectPreludeLine(prefix[lineStart:]); hard != "" {
					return nil, &Error{Kind: ErrHardCredit, Status: http.StatusPaymentRequired, Msg: hard}
				} else if stop {
					return replay(), nil
				}
			}
			return replay(), nil
		}
		prefix = append(prefix, b)
		if b != '\n' {
			continue
		}
		stop, hard := inspectPreludeLine(prefix[lineStart:])
		lineStart = len(prefix)
		if hard != "" {
			return nil, &Error{Kind: ErrHardCredit, Status: http.StatusPaymentRequired, Msg: hard}
		}
		if stop {
			return replay(), nil
		}
	}
	return replay(), nil
}

type preludeReplay struct {
	reader io.Reader
	body   io.Closer
	ctx    context.Context
}

func (r *preludeReplay) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (r *preludeReplay) Close() error { return r.body.Close() }

// inspectPreludeLine returns stop for the first frame that cannot safely be
// discarded on account rotation. hard is nonempty only for a proven quota
// error; ordinary errors are replayed to the existing StreamHint path.
func inspectPreludeLine(line []byte) (stop bool, hard string) {
	line = bytes.TrimRight(line, "\r\n")
	if !bytes.HasPrefix(line, []byte("data:")) {
		if len(line) == 0 || line[0] == ':' ||
			bytes.HasPrefix(line, []byte("event:")) || bytes.Equal(line, []byte("event")) ||
			bytes.HasPrefix(line, []byte("id:")) || bytes.Equal(line, []byte("id")) ||
			bytes.HasPrefix(line, []byte("retry:")) || bytes.Equal(line, []byte("retry")) {
			return false, "" // blank line, comment, or standard SSE metadata
		}
		return true, "" // unknown non-SSE text must not be hidden by the probe
	}
	payload := strings.TrimPrefix(string(line[5:]), " ")
	if payload == "[DONE]" {
		return true, ""
	}
	var frame map[string]any
	if json.Unmarshal([]byte(payload), &frame) != nil || frame == nil {
		return true, ""
	}
	if failure, ok := frame["error"]; ok && failure != nil {
		if FrameKind(payload) == ErrHardCredit {
			return true, payload
		}
		return true, ""
	}
	return !preludeOnly(frame), ""
}

// preludeOnly admits the SSE fields which cannot expose answer/tool content.
// Unknown fields stop probing so the original response is streamed unchanged.
func preludeOnly(frame map[string]any) bool {
	for key, value := range frame {
		switch key {
		case "id", "object", "created", "model", "system_fingerprint", "service_tier":
		case "usage":
			if value != nil {
				if _, ok := value.(map[string]any); !ok {
					return false
				}
			}
		case "error": // null only; non-null error handled by inspectPreludeLine
		case "choices":
			choices, ok := value.([]any)
			if !ok {
				return false
			}
			for _, raw := range choices {
				choice, ok := raw.(map[string]any)
				if !ok || !preludeChoiceOnly(choice) {
					return false
				}
			}
		default:
			return false
		}
	}
	return len(frame) > 0
}

func preludeChoiceOnly(choice map[string]any) bool {
	for key, value := range choice {
		switch key {
		case "index":
		case "finish_reason":
			if value != nil {
				reason, ok := value.(string)
				if !ok || reason != "" {
					return false
				}
			}
		case "delta":
			delta, ok := value.(map[string]any)
			if !ok || !preludeDeltaOnly(delta) {
				return false
			}
		case "message":
			return false
		default:
			return false
		}
	}
	return true
}

func preludeDeltaOnly(delta map[string]any) bool {
	for key, value := range delta {
		switch key {
		case "role", "content", "reasoning_content", "refusal":
			text, ok := value.(string)
			if !ok || (key != "role" && text != "") {
				return false
			}
		case "tool_calls":
			calls, ok := value.([]any)
			if !ok || len(calls) != 0 {
				return false
			}
		case "function_call":
			if value == nil {
				continue
			}
			call, ok := value.(map[string]any)
			if !ok {
				return false
			}
			for field, part := range call {
				if field != "name" && field != "arguments" {
					return false
				}
				text, ok := part.(string)
				if !ok || text != "" {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}
