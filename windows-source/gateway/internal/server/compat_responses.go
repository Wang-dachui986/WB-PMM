package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// responses adapts the stateless subset of the Responses API to the existing
// Chat Completions path, which owns account selection, retries and cancellation.
func (h *Handler) responses(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil || request == nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "body must be a JSON object")
		return
	}
	chat, spec, err := responseToChat(request)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "internal_error", "could not create response ID")
		return
	}
	state := newResponseStream("resp_"+hex.EncodeToString(idBytes), spec)
	stream := request["stream"] == true
	started := false
	if stream {
		state.emit = func(kind string, event map[string]any) error {
			if !started {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("X-Accel-Buffering", "no")
				started = true
			}
			data, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
				return err
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return nil
		}
	}
	status, errorBody, callErr := h.streamCompatibleChat(r, chat, func(chunk map[string]any) error {
		if failure, ok := chunk["error"]; ok && failure != nil {
			state.upstreamError = failure
			return errors.New("upstream stream error")
		}
		if stream && !started {
			if err := state.event("response.created", map[string]any{"response": state.response}); err != nil {
				return err
			}
			if err := state.event("response.in_progress", map[string]any{"response": state.response}); err != nil {
				return err
			}
		}
		return state.consume(chunk)
	})
	if status >= 400 {
		if json.Valid(errorBody) && len(errorBody) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(errorBody)
		} else {
			writeOpenAIError(w, status, "upstream_error", string(errorBody))
		}
		return
	}
	if callErr == nil {
		callErr = state.finish()
	}
	if callErr != nil {
		if !started {
			message := callErr.Error()
			if state.upstreamError != nil {
				if detail, ok := state.upstreamError.(map[string]any); ok {
					if s, ok := detail["message"].(string); ok && s != "" {
						message = s
					}
				}
			}
			writeOpenAIError(w, http.StatusBadGateway, "upstream_error", message)
			return
		}
		state.response["status"] = "failed"
		state.response["error"] = map[string]any{"code": "upstream_error", "message": callErr.Error()}
		if state.upstreamError != nil {
			state.response["error"] = state.upstreamError
		}
		_ = state.event("response.failed", map[string]any{"response": state.response})
		return
	}
	if !stream {
		writeJSON(w, http.StatusOK, state.response)
	}
}

type responseSpec struct {
	model, instructions string
	metadata            map[string]any
	maxOutputTokens     any
	parallelToolCalls   bool
	tools               []any
	toolChoice          any
	temperature, topP   any
}

func responseToChat(req map[string]any) (map[string]any, responseSpec, error) {
	var spec responseSpec
	if err := responseKeys(req, "request", "model", "input", "instructions", "stream", "stream_options", "store", "background", "previous_response_id", "conversation", "prompt", "tools", "tool_choice", "parallel_tool_calls", "max_output_tokens", "temperature", "top_p", "reasoning", "text", "truncation", "include", "metadata"); err != nil {
		return nil, spec, err
	}
	model, ok := req["model"].(string)
	if !ok || strings.TrimSpace(model) == "" {
		return nil, spec, errors.New("model must be a nonempty string")
	}
	spec.model = model
	if err := responseFalseOrNull(req, "store", "background"); err != nil {
		return nil, spec, err
	}
	if v := req["previous_response_id"]; v != nil && v != "" {
		return nil, spec, errors.New("previous_response_id is unsupported; supply complete input history")
	}
	for _, key := range []string{"conversation", "prompt"} {
		if req[key] != nil {
			return nil, spec, fmt.Errorf("%s is unsupported; supply complete input history", key)
		}
	}
	if v, ok := req["stream"]; ok && v != nil {
		if _, ok := v.(bool); !ok {
			return nil, spec, errors.New("stream must be a boolean")
		}
	}
	if v, ok := req["stream_options"]; ok && v != nil {
		opts, ok := v.(map[string]any)
		if !ok {
			return nil, spec, errors.New("stream_options must be an object")
		}
		if err := responseKeys(opts, "stream_options", "include_obfuscation"); err != nil {
			return nil, spec, err
		}
		if req["stream"] != true {
			return nil, spec, errors.New("stream_options requires stream=true")
		}
		if v, ok := opts["include_obfuscation"]; ok && v != nil && v != false {
			return nil, spec, errors.New("stream_options.include_obfuscation is unsupported")
		}
	}
	if v, ok := req["truncation"]; ok && v != nil && v != "disabled" {
		return nil, spec, errors.New("truncation must be disabled; automatic truncation is unsupported")
	}
	if v, ok := req["include"]; ok && v != nil {
		items, ok := v.([]any)
		if !ok || len(items) != 0 {
			return nil, spec, errors.New("include is unsupported")
		}
	}
	if v, ok := req["metadata"]; ok && v != nil {
		metadata, ok := v.(map[string]any)
		if !ok || len(metadata) > 16 {
			return nil, spec, errors.New("metadata must be an object with at most 16 entries")
		}
		for key, value := range metadata {
			s, ok := value.(string)
			if !ok || len(key) > 64 || len(s) > 512 {
				return nil, spec, errors.New("metadata keys and values must be bounded strings")
			}
		}
		spec.metadata = metadata
	}
	if spec.metadata == nil {
		spec.metadata = map[string]any{}
	}
	chat := map[string]any{"model": model, "stream": req["stream"] == true, "stream_options": map[string]any{"include_usage": true}}
	if v, ok := req["instructions"]; ok && v != nil {
		instructions, ok := v.(string)
		if !ok {
			return nil, spec, errors.New("instructions must be a string")
		}
		spec.instructions = instructions
	}
	input, ok := req["input"]
	if !ok || input == nil {
		return nil, spec, errors.New("input is required for stateless responses")
	}
	messages, err := responseInput(input, spec.instructions)
	if err != nil {
		return nil, spec, err
	}
	chat["messages"] = messages
	if v, ok := req["max_output_tokens"]; ok && v != nil {
		n, ok := responsePositiveInt(v)
		if !ok {
			return nil, spec, errors.New("max_output_tokens must be a positive integer")
		}
		chat["max_completion_tokens"] = n
		spec.maxOutputTokens = n
	}
	for _, field := range []struct {
		key      string
		min, max float64
	}{{"temperature", 0, 2}, {"top_p", 0, 1}} {
		if v, ok := req[field.key]; ok && v != nil {
			n, ok := v.(float64)
			if !ok || n < field.min || n > field.max {
				return nil, spec, fmt.Errorf("%s must be a number between %g and %g", field.key, field.min, field.max)
			}
			chat[field.key] = n
			if field.key == "temperature" {
				spec.temperature = n
			} else {
				spec.topP = n
			}
		}
	}
	if v, ok := req["parallel_tool_calls"]; ok && v != nil {
		b, ok := v.(bool)
		if !ok {
			return nil, spec, errors.New("parallel_tool_calls must be a boolean")
		}
		chat["parallel_tool_calls"] = b
		spec.parallelToolCalls = b
	} else {
		spec.parallelToolCalls = true
	}
	if v, ok := req["reasoning"]; ok && v != nil {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, spec, errors.New("reasoning must be an object")
		}
		if err := responseKeys(obj, "reasoning", "effort"); err != nil {
			return nil, spec, err
		}
		if effort, ok := obj["effort"]; ok && effort != nil {
			s, ok := effort.(string)
			if !ok || !responseOneOf(s, "none", "minimal", "low", "medium", "high", "xhigh", "max") {
				return nil, spec, errors.New("reasoning.effort is invalid")
			}
			chat["reasoning_effort"] = s
		}
	}
	if v, ok := req["text"]; ok && v != nil {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, spec, errors.New("text must be an object")
		}
		if err := responseKeys(obj, "text", "format"); err != nil {
			return nil, spec, err
		}
		if format, ok := obj["format"]; ok && format != nil {
			formatObj, ok := format.(map[string]any)
			if !ok {
				return nil, spec, errors.New("text.format must be an object")
			}
			if err := responseKeys(formatObj, "text.format", "type"); err != nil {
				return nil, spec, err
			}
			if formatObj["type"] != "text" {
				return nil, spec, errors.New("text.format supports only type=text")
			}
		}
	}
	if v, ok := req["tools"]; ok && v != nil {
		converted, original, err := responseTools(v)
		if err != nil {
			return nil, spec, err
		}
		chat["tools"] = converted
		spec.tools = original
	}
	if v, ok := req["tool_choice"]; ok && v != nil {
		choice, err := responseToolChoice(v)
		if err != nil {
			return nil, spec, err
		}
		if choice == "required" && len(spec.tools) == 0 {
			return nil, spec, errors.New("tool_choice=required needs function tools")
		}
		if forced, ok := v.(map[string]any); ok {
			name := forced["name"]
			found := false
			for _, rawTool := range spec.tools {
				if tool, ok := rawTool.(map[string]any); ok && tool["name"] == name {
					found = true
					break
				}
			}
			if !found {
				return nil, spec, errors.New("tool_choice.name has no matching function tool")
			}
		}
		chat["tool_choice"] = choice
		spec.toolChoice = v
	}
	return chat, spec, nil
}

func responseInput(input any, instructions string) ([]any, error) {
	messages := make([]any, 0)
	if instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions})
	}
	if text, ok := input.(string); ok {
		return append(messages, map[string]any{"role": "user", "content": text}), nil
	}
	items, ok := input.([]any)
	if !ok {
		return nil, errors.New("input must be a string or array")
	}
	calls := map[string]bool{}
	for i, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("input[%d] must be an object", i)
		}
		kind, _ := item["type"].(string)
		if typ, present := item["type"]; present && typ != nil {
			if _, ok := typ.(string); !ok {
				return nil, fmt.Errorf("input[%d].type must be a string", i)
			}
		}
		switch kind {
		case "", "message":
			if err := responseKeys(item, fmt.Sprintf("input[%d]", i), "type", "role", "content", "id", "status", "phase"); err != nil {
				return nil, err
			}
			role, ok := item["role"].(string)
			if !ok || !responseOneOf(role, "user", "assistant", "system", "developer") {
				return nil, fmt.Errorf("input[%d].role is invalid", i)
			}
			if err := responseCompletedStatus(item, i); err != nil {
				return nil, err
			}
			if phase, ok := item["phase"]; ok && phase != nil && phase != "final_answer" {
				return nil, fmt.Errorf("input[%d].phase is unsupported", i)
			}
			content, err := responseContent(item["content"], role, i)
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": role, "content": content})
		case "function_call":
			if err := responseKeys(item, fmt.Sprintf("input[%d]", i), "type", "id", "status", "call_id", "name", "arguments"); err != nil {
				return nil, err
			}
			if err := responseCompletedStatus(item, i); err != nil {
				return nil, err
			}
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			arguments, ok := item["arguments"].(string)
			if callID == "" || name == "" || !ok || !json.Valid([]byte(arguments)) {
				return nil, fmt.Errorf("input[%d] requires call_id, name and valid JSON arguments", i)
			}
			if calls[callID] {
				return nil, fmt.Errorf("input[%d].call_id is duplicated", i)
			}
			calls[callID] = true
			var assistant map[string]any
			if len(messages) > 0 {
				assistant, _ = messages[len(messages)-1].(map[string]any)
			}
			if assistant == nil || assistant["role"] != "assistant" || assistant["tool_calls"] == nil {
				assistant = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{}}
				messages = append(messages, assistant)
			}
			assistant["tool_calls"] = append(assistant["tool_calls"].([]any), map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}})
		case "function_call_output":
			if err := responseKeys(item, fmt.Sprintf("input[%d]", i), "type", "id", "status", "call_id", "output"); err != nil {
				return nil, err
			}
			if err := responseCompletedStatus(item, i); err != nil {
				return nil, err
			}
			callID, _ := item["call_id"].(string)
			output, ok := item["output"].(string)
			if callID == "" || !ok {
				return nil, fmt.Errorf("input[%d] requires call_id and string output", i)
			}
			if !calls[callID] {
				return nil, fmt.Errorf("input[%d].call_id has no preceding function_call in stateless input", i)
			}
			delete(calls, callID)
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": callID, "content": output})
		default:
			return nil, fmt.Errorf("input[%d].type=%q is unsupported", i, kind)
		}
	}
	if len(calls) > 0 {
		return nil, errors.New("input has a function_call without a matching function_call_output")
	}
	return messages, nil
}

func responseContent(raw any, role string, index int) (any, error) {
	if s, ok := raw.(string); ok {
		return s, nil
	}
	parts, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("input[%d].content must be text or an array", index)
	}
	out := make([]any, 0, len(parts))
	for j, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("input[%d].content[%d] must be an object", index, j)
		}
		typ, _ := part["type"].(string)
		switch typ {
		case "input_text", "output_text":
			if err := responseKeys(part, fmt.Sprintf("input[%d].content[%d]", index, j), "type", "text", "annotations"); err != nil {
				return nil, err
			}
			if typ == "output_text" && role != "assistant" {
				return nil, fmt.Errorf("input[%d].content[%d] output_text requires assistant role", index, j)
			}
			if annotations, ok := part["annotations"]; ok && annotations != nil {
				array, ok := annotations.([]any)
				if !ok || len(array) != 0 {
					return nil, fmt.Errorf("input[%d].content[%d].annotations are unsupported", index, j)
				}
			}
			text, ok := part["text"].(string)
			if !ok {
				return nil, fmt.Errorf("input[%d].content[%d].text must be a string", index, j)
			}
			out = append(out, map[string]any{"type": "text", "text": text})
		case "input_image":
			if role != "user" {
				return nil, fmt.Errorf("input[%d].content[%d] image requires user role", index, j)
			}
			if err := responseKeys(part, fmt.Sprintf("input[%d].content[%d]", index, j), "type", "image_url", "detail"); err != nil {
				return nil, err
			}
			imageURL, ok := part["image_url"].(string)
			if !ok || !responseImageURL(imageURL) {
				return nil, fmt.Errorf("input[%d].content[%d].image_url must be an HTTP(S) or base64 data URL", index, j)
			}
			image := map[string]any{"url": imageURL}
			if detail, ok := part["detail"]; ok && detail != nil {
				s, ok := detail.(string)
				if !ok || !responseOneOf(s, "auto", "low", "high") {
					return nil, fmt.Errorf("input[%d].content[%d].detail is invalid", index, j)
				}
				image["detail"] = s
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": image})
		default:
			return nil, fmt.Errorf("input[%d].content[%d].type=%q is unsupported", index, j, typ)
		}
	}
	return out, nil
}

func responseImageURL(raw string) bool {
	if strings.HasPrefix(raw, "data:image/") {
		prefix, data, found := strings.Cut(raw, ",")
		if !found || !responseOneOf(prefix, "data:image/jpeg;base64", "data:image/png;base64", "data:image/gif;base64", "data:image/webp;base64") {
			return false
		}
		n, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)))
		return err == nil && n > 0
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func responseTools(raw any) ([]any, []any, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, nil, errors.New("tools must be an array")
	}
	converted := make([]any, 0, len(items))
	for i, raw := range items {
		tool, ok := raw.(map[string]any)
		if !ok || tool["type"] != "function" {
			return nil, nil, fmt.Errorf("tools[%d] must be a function tool; server tools are unsupported", i)
		}
		if err := responseKeys(tool, fmt.Sprintf("tools[%d]", i), "type", "name", "description", "parameters", "strict"); err != nil {
			return nil, nil, err
		}
		name, ok := tool["name"].(string)
		if !ok || name == "" {
			return nil, nil, fmt.Errorf("tools[%d].name is required", i)
		}
		fn := map[string]any{"name": name}
		if v, ok := tool["description"]; ok && v != nil {
			if _, ok := v.(string); !ok {
				return nil, nil, fmt.Errorf("tools[%d].description must be a string", i)
			}
			fn["description"] = v
		}
		if v, ok := tool["parameters"]; ok && v != nil {
			if _, ok := v.(map[string]any); !ok {
				return nil, nil, fmt.Errorf("tools[%d].parameters must be an object", i)
			}
			fn["parameters"] = v
		}
		if v, ok := tool["strict"]; ok && v != nil {
			if _, ok := v.(bool); !ok {
				return nil, nil, fmt.Errorf("tools[%d].strict must be a boolean", i)
			}
			fn["strict"] = v
		}
		converted = append(converted, map[string]any{"type": "function", "function": fn})
	}
	return converted, items, nil
}

func responseToolChoice(raw any) (any, error) {
	if s, ok := raw.(string); ok {
		if responseOneOf(s, "none", "auto", "required") {
			return s, nil
		}
		return nil, errors.New("tool_choice is invalid")
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("tool_choice is invalid")
	}
	if err := responseKeys(obj, "tool_choice", "type", "name"); err != nil {
		return nil, err
	}
	if obj["type"] != "function" {
		return nil, errors.New("tool_choice supports only function tools")
	}
	name, ok := obj["name"].(string)
	if !ok || name == "" {
		return nil, errors.New("tool_choice.name is required")
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
}

func responseKeys(obj map[string]any, context string, allowed ...string) error {
	known := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		known[key] = true
	}
	var unsupported []string
	for key := range obj {
		if !known[key] {
			unsupported = append(unsupported, key)
		}
	}
	if len(unsupported) == 0 {
		return nil
	}
	sort.Strings(unsupported)
	return fmt.Errorf("%s.%s is unsupported", context, unsupported[0])
}

func responseFalseOrNull(req map[string]any, keys ...string) error {
	for _, key := range keys {
		if v, ok := req[key]; ok && v != nil && v != false {
			return fmt.Errorf("%s=true is unsupported; responses are synchronous and stateless", key)
		}
	}
	return nil
}

func responseCompletedStatus(item map[string]any, index int) error {
	if status, ok := item["status"]; ok && status != nil && status != "completed" {
		return fmt.Errorf("input[%d].status must be completed", index)
	}
	return nil
}

func responsePositiveInt(v any) (int64, bool) {
	n, ok := v.(float64)
	return int64(n), ok && n >= 1 && n <= 1<<53 && float64(int64(n)) == n
}

func responseOneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

type responseTool struct {
	index                       int
	id, callID, name, arguments string
	emitted, sent               bool
}

type responseStream struct {
	response      map[string]any
	id            string
	sequence      int
	emit          func(string, map[string]any) error
	message       map[string]any
	textPart      map[string]any
	refusalPart   map[string]any
	tools         map[int]*responseTool
	toolIDs       map[string]int
	lastToolIndex int
	order         []any
	finishReason  string
	usage         map[string]any
	chunks        int
	upstreamError any
}

func newResponseStream(id string, spec responseSpec) *responseStream {
	response := map[string]any{
		"id": id, "object": "response", "created_at": time.Now().Unix(), "status": "in_progress",
		"error": nil, "incomplete_details": nil, "instructions": nil, "model": spec.model,
		"output": []any{}, "parallel_tool_calls": spec.parallelToolCalls,
		"previous_response_id": nil, "store": false, "usage": nil, "metadata": spec.metadata,
		"max_output_tokens": spec.maxOutputTokens, "temperature": spec.temperature, "top_p": spec.topP,
		"tools": spec.tools, "tool_choice": spec.toolChoice, "text": map[string]any{"format": map[string]any{"type": "text"}},
		"truncation": "disabled",
	}
	if spec.instructions != "" {
		response["instructions"] = spec.instructions
	}
	if spec.tools == nil {
		response["tools"] = []any{}
	}
	if spec.toolChoice == nil {
		response["tool_choice"] = "auto"
	}
	return &responseStream{response: response, id: id, tools: map[int]*responseTool{}, toolIDs: map[string]int{}, lastToolIndex: -1}
}

func (s *responseStream) event(kind string, fields map[string]any) error {
	if s.emit == nil {
		return nil
	}
	fields["type"] = kind
	fields["sequence_number"] = s.sequence
	s.sequence++
	return s.emit(kind, fields)
}

func (s *responseStream) appendOutput(item any) int {
	output := s.response["output"].([]any)
	index := len(output)
	s.response["output"] = append(output, item)
	return index
}

func (s *responseStream) ensureMessage() (int, error) {
	if s.message != nil {
		for index, raw := range s.response["output"].([]any) {
			if item, ok := raw.(map[string]any); ok && item["id"] == s.message["id"] {
				return index, nil
			}
		}
	}
	s.message = map[string]any{"id": "msg_" + strings.TrimPrefix(s.id, "resp_"), "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}
	index := s.appendOutput(s.message)
	if err := s.event("response.output_item.added", map[string]any{"output_index": index, "item": s.message}); err != nil {
		return 0, err
	}
	return index, nil
}

func (s *responseStream) addText(delta string, refusal bool) error {
	if delta == "" {
		return nil
	}
	index, err := s.ensureMessage()
	if err != nil {
		return err
	}
	part := s.textPart
	partType, eventType := "output_text", "response.output_text.delta"
	if refusal {
		part, partType, eventType = s.refusalPart, "refusal", "response.refusal.delta"
	}
	if part == nil {
		if refusal {
			part = map[string]any{"type": partType, "refusal": ""}
			s.refusalPart = part
		} else {
			part = map[string]any{"type": partType, "text": "", "annotations": []any{}}
			s.textPart = part
		}
		parts := s.message["content"].([]any)
		s.message["content"] = append(parts, part)
		if err := s.event("response.content_part.added", map[string]any{"item_id": s.message["id"], "output_index": index, "content_index": len(parts), "part": part}); err != nil {
			return err
		}
	}
	contentIndex := 0
	for i, p := range s.message["content"].([]any) {
		if content, ok := p.(map[string]any); ok && content["type"] == partType {
			contentIndex = i
			break
		}
	}
	key := "text"
	if refusal {
		key = "refusal"
	}
	part[key] = part[key].(string) + delta
	return s.event(eventType, map[string]any{"item_id": s.message["id"], "output_index": index, "content_index": contentIndex, "delta": delta})
}

func (s *responseStream) consume(chunk map[string]any) error {
	s.chunks++
	if usage, ok := chunk["usage"].(map[string]any); ok {
		s.usage = usage
	}
	choices, ok := chunk["choices"].([]any)
	if !ok {
		if chunk["usage"] != nil && s.usage != nil {
			return nil // Chat stream_options may send usage without choices.
		}
		return errors.New("chat chunk has no choices")
	}
	for _, raw := range choices {
		choice, ok := raw.(map[string]any)
		if !ok {
			return errors.New("chat chunk choice is invalid")
		}
		if choice["index"] != nil && choice["index"] != float64(0) {
			return errors.New("multiple chat choices are unsupported")
		}
		if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
			s.finishReason = reason
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if content, ok := delta["content"].(string); ok {
			if err := s.addText(content, false); err != nil {
				return err
			}
		}
		if refusal, ok := delta["refusal"].(string); ok {
			if err := s.addText(refusal, true); err != nil {
				return err
			}
		}
		if calls, ok := delta["tool_calls"].([]any); ok {
			for _, rawCall := range calls {
				call, ok := rawCall.(map[string]any)
				if !ok {
					return errors.New("chat tool call is invalid")
				}
				if err := s.addToolCall(call); err != nil {
					return err
				}
			}
		}
		if delta["function_call"] != nil {
			return errors.New("legacy function_call chunk is unsupported")
		}
	}
	return nil
}

func (s *responseStream) addToolCall(call map[string]any) error {
	index := s.lastToolIndex
	if n, ok := call["index"].(float64); ok {
		if n < 0 || n != float64(int(n)) {
			return errors.New("chat tool call index is invalid")
		}
		index = int(n)
	} else if id, ok := call["id"].(string); ok && id != "" {
		if known, ok := s.toolIDs[id]; ok {
			index = known
		} else {
			index = 0
			for s.tools[index] != nil {
				index++
			}
		}
	} else if index < 0 {
		index = 0
		for s.tools[index] != nil {
			index++
		}
	}
	s.lastToolIndex = index
	tool := s.tools[index]
	if tool == nil {
		tool = &responseTool{index: index, id: fmt.Sprintf("fc_%s_%d", strings.TrimPrefix(s.id, "resp_"), index)}
		s.tools[index] = tool
	}
	if id, ok := call["id"].(string); ok && id != "" {
		if !tool.emitted {
			tool.callID = id
		}
		s.toolIDs[id] = index
	}
	if tool.callID == "" {
		tool.callID = fmt.Sprintf("call_%s_%d", strings.TrimPrefix(s.id, "resp_"), index)
	}
	fn, _ := call["function"].(map[string]any)
	if fn != nil {
		if name, ok := fn["name"].(string); ok && name != "" {
			tool.name = name
		}
		if args, ok := fn["arguments"].(string); ok {
			tool.arguments += args
		}
	}
	if tool.name == "" {
		return nil
	}
	if !tool.emitted {
		item := map[string]any{"id": tool.id, "type": "function_call", "call_id": tool.callID, "name": tool.name, "arguments": "", "status": "in_progress"}
		outputIndex := s.appendOutput(item)
		s.order = append(s.order, tool)
		tool.emitted = true
		if err := s.event("response.output_item.added", map[string]any{"output_index": outputIndex, "item": item}); err != nil {
			return err
		}
	}
	if tool.arguments != "" && !tool.sent {
		outputIndex := s.toolOutputIndex(tool)
		if err := s.event("response.function_call_arguments.delta", map[string]any{"item_id": tool.id, "output_index": outputIndex, "delta": tool.arguments}); err != nil {
			return err
		}
		tool.sent = true
	} else if fn != nil {
		if args, ok := fn["arguments"].(string); ok && args != "" {
			outputIndex := s.toolOutputIndex(tool)
			if err := s.event("response.function_call_arguments.delta", map[string]any{"item_id": tool.id, "output_index": outputIndex, "delta": args}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *responseStream) toolOutputIndex(tool *responseTool) int {
	for index, raw := range s.response["output"].([]any) {
		item, ok := raw.(map[string]any)
		if ok && item["id"] == tool.id {
			return index
		}
	}
	return -1
}

func (s *responseStream) finish() error {
	if s.chunks == 0 {
		return errors.New("empty chat stream")
	}
	if s.finishReason == "" {
		s.finishReason = "stop"
	}
	status := "completed"
	switch s.finishReason {
	case "stop", "tool_calls", "function_call":
	case "length":
		status = "incomplete"
		s.response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	case "content_filter":
		status = "incomplete"
		s.response["incomplete_details"] = map[string]any{"reason": "content_filter"}
	default:
		return fmt.Errorf("unsupported chat finish_reason %q", s.finishReason)
	}
	if s.usage != nil {
		s.response["usage"] = responseUsage(s.usage)
	}
	for index, raw := range s.response["output"].([]any) {
		item := raw.(map[string]any)
		if item["type"] == "message" {
			for contentIndex, rawPart := range item["content"].([]any) {
				part := rawPart.(map[string]any)
				fields := map[string]any{"item_id": item["id"], "output_index": index, "content_index": contentIndex}
				if part["type"] == "output_text" {
					fields["text"] = part["text"]
					if err := s.event("response.output_text.done", fields); err != nil {
						return err
					}
				} else {
					fields["refusal"] = part["refusal"]
					if err := s.event("response.refusal.done", fields); err != nil {
						return err
					}
				}
				if err := s.event("response.content_part.done", map[string]any{"item_id": item["id"], "output_index": index, "content_index": contentIndex, "part": part}); err != nil {
					return err
				}
			}
		} else {
			var tool *responseTool
			for _, candidate := range s.tools {
				if candidate.id == item["id"] {
					tool = candidate
					break
				}
			}
			if tool == nil || tool.name == "" {
				return errors.New("incomplete chat function call")
			}
			if tool.arguments == "" {
				tool.arguments = "{}"
			}
			if status == "completed" && !json.Valid([]byte(tool.arguments)) {
				return errors.New("invalid chat function arguments")
			}
			item["arguments"] = tool.arguments
			if err := s.event("response.function_call_arguments.done", map[string]any{"item_id": item["id"], "output_index": index, "name": tool.name, "arguments": tool.arguments}); err != nil {
				return err
			}
		}
		item["status"] = status
		if err := s.event("response.output_item.done", map[string]any{"output_index": index, "item": item}); err != nil {
			return err
		}
	}
	for _, tool := range s.tools {
		if !tool.emitted {
			return errors.New("chat function call has no name")
		}
	}
	s.response["status"] = status
	if s.textPart != nil {
		s.response["output_text"] = s.textPart["text"]
	}
	return s.event("response."+status, map[string]any{"response": s.response})
}

func responseUsage(chat map[string]any) any {
	input, okInput := responseToken(chat["prompt_tokens"])
	output, okOutput := responseToken(chat["completion_tokens"])
	if !okInput || !okOutput {
		return nil
	}
	total, ok := responseToken(chat["total_tokens"])
	if !ok {
		total = input + output
	}
	cached, reasoning := int64(0), int64(0)
	if details, ok := chat["prompt_tokens_details"].(map[string]any); ok {
		if n, valid := responseToken(details["cached_tokens"]); valid {
			cached = n
		}
	}
	if details, ok := chat["completion_tokens_details"].(map[string]any); ok {
		if n, valid := responseToken(details["reasoning_tokens"]); valid {
			reasoning = n
		}
	}
	return map[string]any{"input_tokens": input, "output_tokens": output, "total_tokens": total,
		"input_tokens_details":  map[string]any{"cached_tokens": cached},
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoning}}
}

func responseToken(v any) (int64, bool) {
	n, ok := v.(float64)
	return int64(n), ok && n >= 0 && n <= 1<<53 && float64(int64(n)) == n
}
