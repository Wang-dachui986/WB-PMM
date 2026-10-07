package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// anthropicMessages translates the supported Messages subset through the
// existing chat handler, so account selection, retries and cancellation stay
// in one place. Unsupported fields are rejected before reaching the upstream
// payload normalizer, which may otherwise silently discard them.
func (h *Handler) anthropicMessages(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", "read body: "+err.Error())
		return
	}
	body, model, stream, err := anthropicRequest(raw)
	if err != nil {
		writeAnthropicError(w, 400, "invalid_request_error", err.Error())
		return
	}
	state := &anthropicResponse{model: model, stream: stream, writer: w, tools: map[int]*anthropicTool{}}
	status, errorBody, err := h.streamCompatibleChat(r, body, state.chunk)
	if status >= 400 {
		message := string(errorBody)
		var envelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(errorBody, &envelope) == nil && envelope.Error.Message != "" {
			message = envelope.Error.Message
		}
		writeAnthropicError(w, status, anthropicErrorType(status), message)
		return
	}
	if err == nil {
		err = state.finish()
	}
	if err != nil {
		if stream && state.started {
			_ = state.event("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": err.Error()}})
		} else {
			writeAnthropicError(w, 502, "api_error", err.Error())
		}
		return
	}
	if !stream {
		writeJSON(w, 200, map[string]any{
			"id": state.id, "type": "message", "role": "assistant", "model": state.model,
			"content": state.content(), "stop_reason": state.stopReason(), "stop_sequence": nil,
			"usage": state.anthropicUsage(),
		})
	}
}

func anthropicErrorType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func writeAnthropicError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
}

func anthropicRequest(raw []byte) (map[string]any, string, bool, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(raw, &req); err != nil || req == nil {
		return nil, "", false, errors.New("request must be a JSON object")
	}
	if err := anthropicAllowed(req, "request", "model", "max_tokens", "messages", "system", "stream", "tools", "tool_choice", "stop_sequences", "temperature", "top_p"); err != nil {
		return nil, "", false, err
	}
	var model string
	if json.Unmarshal(req["model"], &model) != nil || strings.TrimSpace(model) == "" {
		return nil, "", false, errors.New("model is required")
	}
	var maxTokens int
	if json.Unmarshal(req["max_tokens"], &maxTokens) != nil || maxTokens <= 0 {
		return nil, "", false, errors.New("max_tokens must be a positive integer")
	}
	stream := false
	if v, ok := req["stream"]; ok && (string(v) == "null" || json.Unmarshal(v, &stream) != nil) {
		return nil, "", false, errors.New("stream must be a boolean")
	}
	out := map[string]any{"model": model, "max_tokens": maxTokens, "stream": stream}
	if v, ok := req["system"]; ok {
		if string(v) == "null" {
			return nil, "", false, errors.New("system cannot be null")
		}
		system, err := anthropicSystem(v)
		if err != nil {
			return nil, "", false, err
		}
		out["messages"] = []any{map[string]any{"role": "system", "content": system}}
	}
	var messages []json.RawMessage
	if json.Unmarshal(req["messages"], &messages) != nil || len(messages) == 0 {
		return nil, "", false, errors.New("messages must be a nonempty array")
	}
	converted, err := anthropicMessagesToChat(messages)
	if err != nil {
		return nil, "", false, err
	}
	if prior, ok := out["messages"].([]any); ok {
		out["messages"] = append(prior, converted...)
	} else {
		out["messages"] = converted
	}
	if v, ok := req["tools"]; ok {
		if string(v) == "null" {
			return nil, "", false, errors.New("tools cannot be null")
		}
		tools, err := anthropicTools(v)
		if err != nil {
			return nil, "", false, err
		}
		out["tools"] = tools
	}
	if v, ok := req["tool_choice"]; ok {
		choice, err := anthropicToolChoice(v)
		if err != nil {
			return nil, "", false, err
		}
		out["tool_choice"] = choice
	}
	if v, ok := req["stop_sequences"]; ok {
		if string(v) == "null" {
			return nil, "", false, errors.New("stop_sequences cannot be null")
		}
		var items []json.RawMessage
		if json.Unmarshal(v, &items) != nil {
			return nil, "", false, errors.New("stop_sequences must be an array of strings")
		}
		stop := make([]string, 0, len(items))
		for i, item := range items {
			var sequence string
			if string(item) == "null" || json.Unmarshal(item, &sequence) != nil || sequence == "" {
				return nil, "", false, fmt.Errorf("stop_sequences[%d] must be nonempty text", i)
			}
			stop = append(stop, sequence)
		}
		out["stop"] = stop
	}
	for _, field := range []string{"temperature", "top_p"} {
		if v, ok := req[field]; ok {
			if string(v) == "null" {
				return nil, "", false, fmt.Errorf("%s cannot be null", field)
			}
			var n float64
			if json.Unmarshal(v, &n) != nil || n < 0 || n > 1 {
				return nil, "", false, fmt.Errorf("%s must be a number between 0 and 1", field)
			}
			out[field] = n
		}
	}
	return out, model, stream, nil
}

func anthropicAllowed(obj map[string]json.RawMessage, path string, names ...string) error {
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	for name := range obj {
		if !known[name] {
			return fmt.Errorf("unsupported %s.%s", path, name)
		}
	}
	return nil
}

func anthropicSystem(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", errors.New("system must be text or an array of text blocks")
	}
	parts := make([]string, 0, len(blocks))
	for i, rawBlock := range blocks {
		var block map[string]json.RawMessage
		if json.Unmarshal(rawBlock, &block) != nil || block == nil {
			return "", fmt.Errorf("system[%d] must be an object", i)
		}
		if err := anthropicAllowed(block, fmt.Sprintf("system[%d]", i), "type", "text"); err != nil {
			return "", err
		}
		var typ, value string
		if json.Unmarshal(block["type"], &typ) != nil || typ != "text" || string(block["text"]) == "null" || json.Unmarshal(block["text"], &value) != nil {
			return "", fmt.Errorf("system[%d] must be a text block", i)
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, "\n\n"), nil
}

func anthropicMessagesToChat(messages []json.RawMessage) ([]any, error) {
	out := make([]any, 0, len(messages))
	var pending map[string]bool
	for i, raw := range messages {
		path := fmt.Sprintf("messages[%d]", i)
		var msg map[string]json.RawMessage
		if json.Unmarshal(raw, &msg) != nil || msg == nil {
			return nil, fmt.Errorf("%s must be an object", path)
		}
		if err := anthropicAllowed(msg, path, "role", "content"); err != nil {
			return nil, err
		}
		var role string
		if json.Unmarshal(msg["role"], &role) != nil || (role != "user" && role != "assistant") {
			return nil, fmt.Errorf("%s.role must be user or assistant", path)
		}
		if len(msg["content"]) == 0 {
			return nil, fmt.Errorf("%s.content is required", path)
		}
		if string(msg["content"]) == "null" {
			return nil, fmt.Errorf("%s.content cannot be null", path)
		}
		if msg["content"][0] == '"' {
			var content string
			if json.Unmarshal(msg["content"], &content) != nil || pending != nil {
				return nil, fmt.Errorf("%s.content cannot skip pending tool results", path)
			}
			out = append(out, map[string]any{"role": role, "content": content})
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(msg["content"], &blocks) != nil {
			return nil, fmt.Errorf("%s.content must be text or an array of blocks", path)
		}
		if role == "user" && len(blocks) == 0 {
			return nil, fmt.Errorf("%s.content cannot be empty", path)
		}
		if role == "assistant" {
			if pending != nil {
				return nil, fmt.Errorf("%s follows tool_use without matching tool_result", path)
			}
			chat, calls, err := anthropicAssistantBlocks(blocks, path)
			if err != nil {
				return nil, err
			}
			out = append(out, chat)
			if len(calls) > 0 {
				pending = calls
			}
			continue
		}
		chat, results, err := anthropicUserBlocks(blocks, path)
		if err != nil {
			return nil, err
		}
		if pending == nil && len(results) > 0 {
			return nil, fmt.Errorf("%s contains orphan tool_result", path)
		}
		if pending != nil {
			if len(results) != len(pending) {
				return nil, fmt.Errorf("%s must return every pending tool_use", path)
			}
			for _, result := range results {
				id := result["tool_call_id"].(string)
				if !pending[id] {
					return nil, fmt.Errorf("%s has unmatched tool_result %q", path, id)
				}
				delete(pending, id)
				out = append(out, result)
			}
			pending = nil
		}
		if chat != nil {
			out = append(out, chat)
		}
	}
	if pending != nil {
		return nil, errors.New("last assistant tool_use has no matching tool_result")
	}
	return out, nil
}

func anthropicAssistantBlocks(blocks []json.RawMessage, path string) (map[string]any, map[string]bool, error) {
	var text strings.Builder
	var calls []any
	ids := map[string]bool{}
	seenTool := false
	for j, raw := range blocks {
		block, typ, err := anthropicBlock(raw, fmt.Sprintf("%s.content[%d]", path, j))
		if err != nil {
			return nil, nil, err
		}
		switch typ {
		case "text":
			if seenTool {
				return nil, nil, fmt.Errorf("%s text after tool_use cannot be represented", path)
			}
			if err := anthropicAllowed(block, path+".text", "type", "text"); err != nil {
				return nil, nil, err
			}
			var s string
			if string(block["text"]) == "null" || json.Unmarshal(block["text"], &s) != nil {
				return nil, nil, fmt.Errorf("%s text block needs text", path)
			}
			text.WriteString(s)
		case "tool_use":
			seenTool = true
			if err := anthropicAllowed(block, path+".tool_use", "type", "id", "name", "input"); err != nil {
				return nil, nil, err
			}
			var id, name string
			var input map[string]any
			if json.Unmarshal(block["id"], &id) != nil || id == "" || json.Unmarshal(block["name"], &name) != nil || name == "" || json.Unmarshal(block["input"], &input) != nil || input == nil || ids[id] {
				return nil, nil, fmt.Errorf("%s has invalid or duplicate tool_use", path)
			}
			ids[id] = true
			args, _ := json.Marshal(input)
			calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}})
		default:
			return nil, nil, fmt.Errorf("%s unsupported assistant block %q", path, typ)
		}
	}
	chat := map[string]any{"role": "assistant", "content": text.String()}
	if len(calls) > 0 {
		chat["tool_calls"] = calls
	}
	return chat, ids, nil
}

func anthropicUserBlocks(blocks []json.RawMessage, path string) (map[string]any, []map[string]any, error) {
	var parts []any
	var results []map[string]any
	seenNormal := false
	for j, raw := range blocks {
		blockPath := fmt.Sprintf("%s.content[%d]", path, j)
		block, typ, err := anthropicBlock(raw, blockPath)
		if err != nil {
			return nil, nil, err
		}
		switch typ {
		case "text":
			seenNormal = true
			if err := anthropicAllowed(block, blockPath, "type", "text"); err != nil {
				return nil, nil, err
			}
			var value string
			if string(block["text"]) == "null" || json.Unmarshal(block["text"], &value) != nil {
				return nil, nil, fmt.Errorf("%s.text is required", blockPath)
			}
			parts = append(parts, map[string]any{"type": "text", "text": value})
		case "image":
			seenNormal = true
			if err := anthropicAllowed(block, blockPath, "type", "source"); err != nil {
				return nil, nil, err
			}
			imageURL, err := anthropicImage(block["source"], blockPath)
			if err != nil {
				return nil, nil, err
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": imageURL}})
		case "tool_result":
			if seenNormal {
				return nil, nil, fmt.Errorf("%s tool_result must precede text and images", blockPath)
			}
			if err := anthropicAllowed(block, blockPath, "type", "tool_use_id", "content", "is_error"); err != nil {
				return nil, nil, err
			}
			var id string
			if json.Unmarshal(block["tool_use_id"], &id) != nil || id == "" {
				return nil, nil, fmt.Errorf("%s.tool_use_id is required", blockPath)
			}
			content, err := anthropicToolResultContent(block["content"], blockPath)
			if err != nil {
				return nil, nil, err
			}
			if rawError, ok := block["is_error"]; ok {
				var isError bool
				if string(rawError) == "null" || json.Unmarshal(rawError, &isError) != nil {
					return nil, nil, fmt.Errorf("%s.is_error must be boolean", blockPath)
				}
				if isError {
					content = "Tool error: " + content
				}
			}
			results = append(results, map[string]any{"role": "tool", "tool_call_id": id, "content": content})
		default:
			return nil, nil, fmt.Errorf("%s unsupported user block %q", blockPath, typ)
		}
	}
	if len(parts) == 0 {
		return nil, results, nil
	}
	return map[string]any{"role": "user", "content": parts}, results, nil
}

func anthropicBlock(raw json.RawMessage, path string) (map[string]json.RawMessage, string, error) {
	var block map[string]json.RawMessage
	if json.Unmarshal(raw, &block) != nil || block == nil {
		return nil, "", fmt.Errorf("%s must be an object", path)
	}
	var typ string
	if json.Unmarshal(block["type"], &typ) != nil || typ == "" {
		return nil, "", fmt.Errorf("%s.type is required", path)
	}
	return block, typ, nil
}

func anthropicImage(raw json.RawMessage, path string) (string, error) {
	var source map[string]json.RawMessage
	if json.Unmarshal(raw, &source) != nil || source == nil {
		return "", fmt.Errorf("%s.source must be an object", path)
	}
	var typ string
	if json.Unmarshal(source["type"], &typ) != nil {
		return "", fmt.Errorf("%s.source.type is required", path)
	}
	if typ == "url" {
		if err := anthropicAllowed(source, path+".source", "type", "url"); err != nil {
			return "", err
		}
		var value string
		if json.Unmarshal(source["url"], &value) != nil {
			return "", fmt.Errorf("%s.source.url is required", path)
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return "", fmt.Errorf("%s.source.url must be HTTP or HTTPS", path)
		}
		return value, nil
	}
	if typ != "base64" {
		return "", fmt.Errorf("%s unsupported image source %q", path, typ)
	}
	if err := anthropicAllowed(source, path+".source", "type", "media_type", "data"); err != nil {
		return "", err
	}
	var mediaType, data string
	if json.Unmarshal(source["media_type"], &mediaType) != nil || json.Unmarshal(source["data"], &data) != nil || data == "" {
		return "", fmt.Errorf("%s.source needs media_type and data", path)
	}
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return "", fmt.Errorf("%s unsupported image media_type %q", path, mediaType)
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return "", fmt.Errorf("%s.source.data is invalid base64", path)
	}
	return "data:" + mediaType + ";base64," + data, nil
}

func anthropicToolResultContent(raw json.RawMessage, path string) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if string(raw) == "null" {
		return "", fmt.Errorf("%s.content cannot be null", path)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", fmt.Errorf("%s.content must be text or text blocks", path)
	}
	var result strings.Builder
	for i, rawBlock := range blocks {
		blockPath := fmt.Sprintf("%s.content[%d]", path, i)
		block, typ, err := anthropicBlock(rawBlock, blockPath)
		if err != nil {
			return "", err
		}
		if typ != "text" {
			return "", fmt.Errorf("%s unsupported tool result block %q", blockPath, typ)
		}
		if err := anthropicAllowed(block, blockPath, "type", "text"); err != nil {
			return "", err
		}
		var value string
		if string(block["text"]) == "null" || json.Unmarshal(block["text"], &value) != nil {
			return "", fmt.Errorf("%s.text is required", blockPath)
		}
		result.WriteString(value)
	}
	return result.String(), nil
}

func anthropicTools(raw json.RawMessage) ([]any, error) {
	var source []json.RawMessage
	if json.Unmarshal(raw, &source) != nil {
		return nil, errors.New("tools must be an array")
	}
	out := make([]any, 0, len(source))
	for i, item := range source {
		path := fmt.Sprintf("tools[%d]", i)
		var tool map[string]json.RawMessage
		if json.Unmarshal(item, &tool) != nil || tool == nil {
			return nil, fmt.Errorf("%s must be an object", path)
		}
		if err := anthropicAllowed(tool, path, "name", "description", "input_schema"); err != nil {
			return nil, err
		}
		var name, description string
		var schema map[string]any
		if json.Unmarshal(tool["name"], &name) != nil || name == "" || json.Unmarshal(tool["input_schema"], &schema) != nil || schema == nil {
			return nil, fmt.Errorf("%s needs name and input_schema", path)
		}
		if v, ok := tool["description"]; ok && (string(v) == "null" || json.Unmarshal(v, &description) != nil) {
			return nil, fmt.Errorf("%s.description must be text", path)
		}
		fn := map[string]any{"name": name, "parameters": schema}
		if _, ok := tool["description"]; ok {
			fn["description"] = description
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out, nil
}

func anthropicToolChoice(raw json.RawMessage) (any, error) {
	var choice map[string]json.RawMessage
	if json.Unmarshal(raw, &choice) != nil || choice == nil {
		return nil, errors.New("tool_choice must be an object")
	}
	var typ string
	if json.Unmarshal(choice["type"], &typ) != nil {
		return nil, errors.New("tool_choice.type is required")
	}
	switch typ {
	case "auto":
		if err := anthropicAllowed(choice, "tool_choice", "type"); err != nil {
			return nil, err
		}
		return "auto", nil
	case "any":
		if err := anthropicAllowed(choice, "tool_choice", "type"); err != nil {
			return nil, err
		}
		return "required", nil
	case "none":
		if err := anthropicAllowed(choice, "tool_choice", "type"); err != nil {
			return nil, err
		}
		return "none", nil
	case "tool":
		if err := anthropicAllowed(choice, "tool_choice", "type", "name"); err != nil {
			return nil, err
		}
		var name string
		if json.Unmarshal(choice["name"], &name) != nil || name == "" {
			return nil, errors.New("tool_choice.name is required")
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
	default:
		return nil, fmt.Errorf("unsupported tool_choice.type %q", typ)
	}
}

type anthropicTool struct {
	id, name   string
	args       strings.Builder
	blockIndex int
	started    bool
}

type anthropicResponse struct {
	writer                  http.ResponseWriter
	stream                  bool
	started                 bool
	id, model, finishReason string
	text                    strings.Builder
	textIndex               int
	textStarted             bool
	textStopped             bool
	tools                   map[int]*anthropicTool
	toolOrder               []int
	activeTool              *anthropicTool
	nextBlock               int
	usage                   map[string]any
}

func (s *anthropicResponse) event(name string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(s.writer, "event: %s\ndata: %s\n\n", name, encoded); err != nil {
		return err
	}
	if f, ok := s.writer.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func (s *anthropicResponse) start() error {
	if !s.stream || s.started {
		return nil
	}
	if s.id == "" {
		return errors.New("upstream message ID is missing")
	}
	s.writer.Header().Set("Content-Type", "text/event-stream")
	s.writer.Header().Set("Cache-Control", "no-cache")
	s.started = true
	return s.event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": s.id, "type": "message", "role": "assistant", "model": s.model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		// Upstream reports usage only at the end. These are provisional zeroes;
		// message_delta below sends the authoritative counts.
		"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
	}})
}

func (s *anthropicResponse) chunk(frame map[string]any) error {
	if rawError, ok := frame["error"]; ok && rawError != nil {
		return fmt.Errorf("upstream stream error: %v", rawError)
	}
	if id, _ := frame["id"].(string); id != "" && s.id == "" {
		s.id = id
	}
	if model, _ := frame["model"].(string); model != "" {
		s.model = model
	}
	if usage, ok := frame["usage"].(map[string]any); ok {
		if s.usage == nil {
			s.usage = map[string]any{}
		}
		for name, value := range usage {
			if value != nil {
				s.usage[name] = value
			}
		}
	}
	choices, _ := frame["choices"].([]any)
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		if index, ok := choice["index"].(float64); ok && index != 0 {
			continue
		}
		if reason, _ := choice["finish_reason"].(string); reason != "" {
			s.finishReason = reason
		}
		msg, _ := choice["delta"].(map[string]any)
		if msg == nil {
			msg, _ = choice["message"].(map[string]any)
		}
		if msg == nil {
			continue
		}
		// Models may include internal reasoning even when the client did not
		// request thinking. A signed Anthropic thinking block cannot be made
		// from that text, so only user-facing content and tools are emitted.
		if value, _ := msg["content"].(string); value != "" {
			if err := s.textDelta(value); err != nil {
				return err
			}
		}
		if calls, ok := msg["tool_calls"].([]any); ok {
			for _, rawCall := range calls {
				if err := s.toolDelta(rawCall); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *anthropicResponse) textDelta(value string) error {
	if s.textStopped || len(s.tools) > 0 {
		return errors.New("upstream text after tool call cannot be represented")
	}
	s.text.WriteString(value)
	if !s.stream {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	if !s.textStarted {
		s.textIndex = s.nextBlock
		s.nextBlock++
		s.textStarted = true
		if err := s.event("content_block_start", map[string]any{"type": "content_block_start", "index": s.textIndex, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
			return err
		}
	}
	return s.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": s.textIndex, "delta": map[string]any{"type": "text_delta", "text": value}})
}

func (s *anthropicResponse) toolDelta(raw any) error {
	call, ok := raw.(map[string]any)
	if !ok {
		return errors.New("invalid upstream tool call")
	}
	if typ, present := call["type"]; present && typ != nil && typ != "function" {
		return fmt.Errorf("unsupported upstream tool call type %v", typ)
	}
	index := -1
	if n, ok := call["index"].(float64); ok && n >= 0 && n == float64(int(n)) {
		index = int(n)
	}
	if index < 0 {
		if id, _ := call["id"].(string); id != "" {
			for known, tool := range s.tools {
				if tool.id == id {
					index = known
					break
				}
			}
			if index < 0 {
				index = 0
				for s.tools[index] != nil {
					index++
				}
			}
		}
		if index < 0 && len(s.toolOrder) > 0 {
			index = s.toolOrder[len(s.toolOrder)-1]
		}
		if index < 0 {
			index = 0
			for s.tools[index] != nil {
				index++
			}
		}
	}
	tool := s.tools[index]
	if tool == nil {
		tool = &anthropicTool{blockIndex: -1}
		s.tools[index] = tool
		s.toolOrder = append(s.toolOrder, index)
	}
	if rawID, present := call["id"]; present && rawID != nil {
		id, ok := rawID.(string)
		if !ok {
			return fmt.Errorf("upstream tool call %d has invalid id", index)
		}
		if id == "" {
			return fmt.Errorf("upstream tool call %d has empty id", index)
		}
		if tool.id != "" && tool.id != id {
			return fmt.Errorf("upstream tool call %d changed id", index)
		}
		tool.id = id
	}
	fn, _ := call["function"].(map[string]any)
	if rawName, present := fn["name"]; present && rawName != nil {
		name, ok := rawName.(string)
		if !ok {
			return fmt.Errorf("upstream tool call %d has invalid name", index)
		}
		if name == "" {
			return fmt.Errorf("upstream tool call %d has empty name", index)
		}
		if tool.name != "" && tool.name != name {
			return fmt.Errorf("upstream tool call %d changed name", index)
		}
		tool.name = name
	}
	args := ""
	if rawArgs, present := fn["arguments"]; present && rawArgs != nil {
		var ok bool
		args, ok = rawArgs.(string)
		if !ok {
			return fmt.Errorf("upstream tool call %d has invalid arguments", index)
		}
	}
	tool.args.WriteString(args)
	if !s.stream || tool.id == "" || tool.name == "" {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	// Anthropic blocks are sequential. Buffer a later call while an earlier
	// tool block is open; OpenAI can interleave their argument fragments.
	if s.activeTool != nil && s.activeTool != tool {
		return nil
	}
	if s.textStarted && !s.textStopped {
		if err := s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.textIndex}); err != nil {
			return err
		}
		s.textStopped = true
	}
	if !tool.started {
		tool.blockIndex = s.nextBlock
		s.nextBlock++
		tool.started = true
		s.activeTool = tool
		if err := s.event("content_block_start", map[string]any{"type": "content_block_start", "index": tool.blockIndex, "content_block": map[string]any{"type": "tool_use", "id": tool.id, "name": tool.name, "input": map[string]any{}}}); err != nil {
			return err
		}
		args = tool.args.String()
	}
	if args != "" {
		return s.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": tool.blockIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": args}})
	}
	return nil
}

func (s *anthropicResponse) stopReason() string {
	if len(s.tools) > 0 && s.finishReason != "length" {
		return "tool_use"
	}
	if s.finishReason == "length" {
		return "max_tokens"
	}
	return "end_turn"
}

func (s *anthropicResponse) anthropicUsage() map[string]any {
	// Anthropic requires numeric fields. Missing upstream usage falls back to
	// protocol zeroes only; it does not mean the request was free. Gateway
	// accounting still records unknown usage through the existing chat path.
	prompt := anthropicCountOrZero(s.usage["prompt_tokens"])
	usage := map[string]any{"input_tokens": prompt, "output_tokens": anthropicCountOrZero(s.usage["completion_tokens"])}
	read, hasRead := s.usage["prompt_cache_hit_tokens"].(float64)
	write, hasWrite := s.usage["prompt_cache_write_tokens"].(float64)
	if (!hasRead || anthropicTokenCount(read)) && (!hasWrite || anthropicTokenCount(write)) && read+write <= prompt {
		usage["input_tokens"] = prompt - read - write
		if hasRead {
			usage["cache_read_input_tokens"] = read
		}
		if hasWrite {
			usage["cache_creation_input_tokens"] = write
		}
	}
	return usage
}

func anthropicTokenCount(value any) bool {
	n, ok := value.(float64)
	return ok && n >= 0 && n == float64(int64(n))
}

func anthropicCountOrZero(value any) float64 {
	if anthropicTokenCount(value) {
		return value.(float64)
	}
	return 0
}

func (s *anthropicResponse) content() []any {
	content := make([]any, 0, len(s.tools)+1)
	if s.text.Len() > 0 {
		content = append(content, map[string]any{"type": "text", "text": s.text.String()})
	}
	for _, index := range s.toolOrder {
		tool := s.tools[index]
		var input map[string]any
		_ = json.Unmarshal([]byte(tool.args.String()), &input)
		content = append(content, map[string]any{"type": "tool_use", "id": tool.id, "name": tool.name, "input": input})
	}
	return content
}

func (s *anthropicResponse) finish() error {
	if s.id == "" {
		return errors.New("upstream message ID is missing")
	}
	if s.finishReason != "stop" && s.finishReason != "length" && s.finishReason != "tool_calls" {
		return fmt.Errorf("unsupported upstream finish_reason %q", s.finishReason)
	}
	if s.finishReason == "tool_calls" && len(s.tools) == 0 {
		return errors.New("upstream reported tool_calls without a tool call")
	}
	for _, index := range s.toolOrder {
		tool := s.tools[index]
		var input map[string]any
		if tool.id == "" || tool.name == "" || json.Unmarshal([]byte(tool.args.String()), &input) != nil || input == nil {
			return fmt.Errorf("upstream tool call %d is incomplete", index)
		}
	}
	if !s.stream {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	if s.textStarted && !s.textStopped {
		if err := s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.textIndex}); err != nil {
			return err
		}
	}
	if s.activeTool != nil {
		if err := s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.activeTool.blockIndex}); err != nil {
			return err
		}
	}
	for _, index := range s.toolOrder {
		tool := s.tools[index]
		if tool == s.activeTool {
			continue
		}
		if !tool.started {
			tool.blockIndex = s.nextBlock
			s.nextBlock++
			if err := s.event("content_block_start", map[string]any{"type": "content_block_start", "index": tool.blockIndex, "content_block": map[string]any{"type": "tool_use", "id": tool.id, "name": tool.name, "input": map[string]any{}}}); err != nil {
				return err
			}
			if err := s.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": tool.blockIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": tool.args.String()}}); err != nil {
				return err
			}
		}
		if err := s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": tool.blockIndex}); err != nil {
			return err
		}
	}
	if err := s.event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": s.stopReason(), "stop_sequence": nil}, "usage": s.anthropicUsage()}); err != nil {
		return err
	}
	return s.event("message_stop", map[string]any{"type": "message_stop"})
}
