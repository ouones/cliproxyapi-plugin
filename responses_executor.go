package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type ResponsesExecutor struct {
	identity *identityStateStore
	apiBase  string
	apiKey   func() string
	now      func() time.Time
	newID    func(string) (string, error)
	client   pluginapi.HostHTTPClient
}

func NewResponsesExecutor() *ResponsesExecutor {
	return &ResponsesExecutor{identity: commandCodeIdentityStates, apiBase: commandCodeAPIBase(), apiKey: configuredAPIKey, now: time.Now}
}

func (e *ResponsesExecutor) Identifier() string { return pluginID }

func (e *ResponsesExecutor) runtime() executorRuntime {
	runtime := defaultExecutorRuntime()
	runtime.apiBase = e.apiBase
	runtime.apiKey = e.apiKey
	runtime.identity = e.identity
	runtime.now = e.now
	runtime.client = e.client
	if e.newID != nil {
		runtime.idFactory = func() string {
			value, err := e.newID("")
			if err == nil && value != "" {
				return value
			}
			return newRandomID()
		}
	}
	return runtime.normalized()
}

func (e *ResponsesExecutor) Execute(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	runtime := e.runtime()
	responsesRequest, err := decodeRequestObject(requestPayload(request))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	if stringValue(responsesRequest["model"]) == "" {
		responsesRequest["model"] = request.Model
	}
	openaiRequest := convertResponsesRequest(responsesRequest, runtime.idFactory)
	if stringValue(openaiRequest["model"]) == "" {
		openaiRequest["model"] = request.Model
	}
	client, apiKey, sessionID, err := runtime.initialize(ctx, request, stringValue(responsesRequest["prompt_cache_key"]))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	upstreamRequest := runtime.makeHTTPRequest(request, buildCommandCodeRequest(openaiRequest), apiKey, sessionID)
	response, err := client.Do(ctx, upstreamRequest)
	if err != nil {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("upstream_error", err.Error(), http.StatusBadGateway)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return pluginapi.ExecutorResponse{}, pluginErrorForHTTP(response.StatusCode, response.Body)
	}
	collected := collectCCBody(response.Body, runtime.idFactory)
	if collected.UpstreamError != nil {
		return pluginapi.ExecutorResponse{}, collected.UpstreamError
	}
	if detail := incompleteDetail(collected); detail != "" {
		return pluginapi.ExecutorResponse{}, incompleteError(detail)
	}
	if collected.FullText == "" && collected.ReasoningText == "" && len(collected.ToolCalls) == 0 {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("rate_limit_error", "Empty response from upstream (zero output tokens)", http.StatusTooManyRequests)
	}
	payload, err := json.Marshal(buildResponsesResponse(runtime, responsesRequest, collected))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	return pluginapi.ExecutorResponse{Payload: payload, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

func (e *ResponsesExecutor) ExecuteStream(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	runtime := e.runtime()
	responsesRequest, err := decodeRequestObject(requestPayload(request))
	if err != nil {
		return pluginapi.ExecutorStreamResponse{}, err
	}
	if stringValue(responsesRequest["model"]) == "" {
		responsesRequest["model"] = request.Model
	}
	openaiRequest := convertResponsesRequest(responsesRequest, runtime.idFactory)
	if stringValue(openaiRequest["model"]) == "" {
		openaiRequest["model"] = request.Model
	}
	client, apiKey, sessionID, err := runtime.initialize(ctx, request, stringValue(responsesRequest["prompt_cache_key"]))
	if err != nil {
		return pluginapi.ExecutorStreamResponse{}, err
	}
	upstreamRequest := runtime.makeHTTPRequest(request, buildCommandCodeRequest(openaiRequest), apiKey, sessionID)
	response, err := client.DoStream(ctx, upstreamRequest)
	if err != nil {
		return pluginapi.ExecutorStreamResponse{}, pluginabi.NewError("upstream_error", err.Error(), http.StatusBadGateway)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return pluginapi.ExecutorStreamResponse{}, pluginErrorForHTTP(response.StatusCode, nil)
	}
	output := make(chan pluginapi.ExecutorStreamChunk, 8)
	go e.translateResponsesStream(ctx, runtime, responsesRequest, openaiRequest, response.Chunks, output)
	headers := response.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	return pluginapi.ExecutorStreamResponse{Headers: headers, Chunks: output}, nil
}

func (e *ResponsesExecutor) CountTokens(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	openaiRequest, err := decodeRequestObject(requestPayload(request))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	result, _ := json.Marshal(map[string]any{"input_tokens": maxInt(1, len([]rune(textOf(openaiRequest["input"])+textOf(openaiRequest["instructions"])))/4)})
	return pluginapi.ExecutorResponse{Payload: result, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

func (e *ResponsesExecutor) HttpRequest(ctx context.Context, request pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	client := request.HTTPClient
	if client == nil {
		client = e.client
	}
	if client == nil {
		client = NewCommandCodeHostHTTPClient(callHost, "")
	}
	response, err := client.Do(ctx, pluginapi.HTTPRequest{Method: request.Method, URL: request.URL, Headers: request.Headers, Body: request.Body})
	if err != nil {
		return pluginapi.ExecutorHTTPResponse{}, err
	}
	return pluginapi.ExecutorHTTPResponse{StatusCode: response.StatusCode, Headers: response.Headers, Body: response.Body}, nil
}

func buildResponsesResponse(runtime executorRuntime, request map[string]any, collected ccCollected) map[string]any {
	finishReason := collected.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	status := "completed"
	var incomplete any
	if finishReason == "length" {
		status = "incomplete"
		incomplete = map[string]any{"reason": "max_output_tokens"}
	} else if finishReason == "pause_turn" {
		status = "incomplete"
		incomplete = map[string]any{"reason": "pause_turn"}
	}
	output := buildResponsesOutput(runtime, collected)
	model := stringValue(request["model"])
	result := map[string]any{
		"id": "resp_" + shortID(runtime.idFactory()), "object": "response", "created_at": runtime.now().Unix(), "status": status,
		"completed_at": runtime.now().Unix(), "error": nil, "incomplete_details": incomplete,
		"input": request["input"], "instructions": request["instructions"], "max_output_tokens": request["max_output_tokens"],
		"model": model, "output": output, "output_text": collected.FullText, "parallel_tool_calls": true,
		"previous_response_id": nil, "reasoning": request["reasoning"], "store": false,
		"temperature": numberOrDefault(request["temperature"], 1), "text": map[string]any{"format": map[string]any{"type": "text"}},
		"tool_choice": valueOrDefault(request["tool_choice"], "auto"), "tools": valueOrDefault(request["tools"], []any{}),
		"top_p": numberOrDefault(request["top_p"], 1), "truncation": "disabled", "usage": buildResponsesUsage(collected.Usage, responsesFallbackOutputTokens(collected)),
		"user": nil, "metadata": map[string]any{},
	}
	return result
}

func buildResponsesOutput(runtime executorRuntime, collected ccCollected) []any {
	output := make([]any, 0)
	if collected.ReasoningText != "" {
		output = append(output, map[string]any{"type": "reasoning", "id": "rs_" + shortID(runtime.idFactory()), "summary": []any{map[string]any{"type": "summary_text", "text": collected.ReasoningText}}, "status": "completed"})
	}
	if collected.FullText != "" {
		output = append(output, map[string]any{"type": "message", "id": "msg_" + shortID(runtime.idFactory()), "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": collected.FullText, "annotations": []any{}}}})
	}
	for _, call := range collected.ToolCalls {
		output = append(output, map[string]any{"type": "function_call", "id": "fc_" + shortID(runtime.idFactory()), "call_id": call.ID, "name": call.Name, "arguments": call.Arguments, "status": "completed"})
	}
	return output
}

func buildResponsesUsage(usage ccUsage, fallbackOutputTokens int) map[string]any {
	outputTokens := usage.OutputTokens
	if outputTokens == 0 {
		outputTokens = fallbackOutputTokens
	}
	return map[string]any{
		"input_tokens":         usage.InputTokens,
		"input_tokens_details": map[string]any{"cached_tokens": usage.CachedInputTokens, "cache_write_tokens": usage.CacheWriteTokens},
		"output_tokens":        outputTokens, "output_tokens_details": map[string]any{"reasoning_tokens": 0},
		"total_tokens": usage.InputTokens + outputTokens,
	}
}

func responsesFallbackOutputTokens(collected ccCollected) int {
	if collected.Usage.hasUsage {
		return 0
	}
	textLength := len([]rune(collected.FullText)) + len([]rune(collected.ReasoningText))
	return (textLength+3)/4 + len(collected.ToolCalls)*20
}

func valueOrDefault(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func numberOrDefault(value any, fallback float64) any {
	if value == nil {
		return fallback
	}
	return value
}

type responsesStreamState struct {
	collected    ccCollected
	responseID   string
	created      int64
	model        string
	sequence     int
	outputIndex  int
	currentKind  string
	currentIndex int
	currentItem  map[string]any
	currentText  string
	text         string
	started      bool
	doneItems    []any
	idFactory    func() string
}

func responsesSSE(event string, sequence *int, data map[string]any) []byte {
	if data == nil {
		data = map[string]any{}
	}
	data["type"] = event
	data["sequence_number"] = *sequence
	*sequence++
	payload, _ := json.Marshal(data)
	return []byte("event: " + event + "\ndata: " + string(payload) + "\n\n")
}

func (e *ResponsesExecutor) translateResponsesStream(ctx context.Context, runtime executorRuntime, request, openaiRequest map[string]any, chunks <-chan pluginapi.HTTPStreamChunk, output chan<- pluginapi.ExecutorStreamChunk) {
	defer close(output)
	state := &responsesStreamState{responseID: "resp_" + shortID(runtime.idFactory()), created: runtime.now().Unix(), model: stringValue(openaiRequest["model"]), idFactory: runtime.idFactory}
	emit := func(payload []byte) bool {
		select {
		case output <- pluginapi.ExecutorStreamChunk{Payload: payload}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	baseResponse := func(status string, items []any) map[string]any {
		return map[string]any{"id": state.responseID, "object": "response", "created_at": state.created, "status": status, "output": items, "output_text": "", "model": state.model, "error": nil, "incomplete_details": nil, "parallel_tool_calls": true, "previous_response_id": nil, "store": false, "tools": []any{}, "metadata": map[string]any{}}
	}
	start := func() bool {
		if state.started {
			return true
		}
		state.started = true
		return emit(responsesSSE("response.created", &state.sequence, map[string]any{"response": baseResponse("in_progress", []any{})})) && emit(responsesSSE("response.in_progress", &state.sequence, map[string]any{"response": baseResponse("in_progress", []any{})}))
	}
	closeItem := func() bool {
		if state.currentItem == nil {
			return true
		}
		item := state.currentItem
		if state.currentKind == "message" {
			if !emit(responsesSSE("response.output_text.done", &state.sequence, map[string]any{"item_id": item["id"], "output_index": state.currentIndex, "content_index": 0, "text": state.currentText, "logprobs": []any{}})) {
				return false
			}
			item["content"] = []any{map[string]any{"type": "output_text", "text": state.currentText, "annotations": []any{}}}
			item["status"] = "completed"
			if !emit(responsesSSE("response.content_part.done", &state.sequence, map[string]any{"item_id": item["id"], "output_index": state.currentIndex, "content_index": 0, "part": item["content"].([]any)[0]})) {
				return false
			}
		} else if state.currentKind == "reasoning" {
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": state.currentText}}
			item["status"] = "completed"
			if !emit(responsesSSE("response.reasoning_summary_text.done", &state.sequence, map[string]any{"item_id": item["id"], "output_index": state.currentIndex, "summary_index": 0, "text": state.currentText})) {
				return false
			}
		} else {
			item["status"] = "completed"
			if !emit(responsesSSE("response.function_call_arguments.done", &state.sequence, map[string]any{"item_id": item["id"], "output_index": state.currentIndex, "arguments": item["arguments"]})) {
				return false
			}
		}
		if !emit(responsesSSE("response.output_item.done", &state.sequence, map[string]any{"output_index": state.currentIndex, "item": item})) {
			return false
		}
		state.doneItems = append(state.doneItems, item)
		state.currentItem = nil
		state.currentKind = ""
		state.currentText = ""
		return true
	}
	open := func(kind string, item map[string]any) bool {
		if !closeItem() {
			return false
		}
		state.currentKind = kind
		state.currentIndex = state.outputIndex
		state.outputIndex++
		state.currentItem = item
		if !emit(responsesSSE("response.output_item.added", &state.sequence, map[string]any{"output_index": state.currentIndex, "item": item})) {
			return false
		}
		if kind == "message" {
			return emit(responsesSSE("response.content_part.added", &state.sequence, map[string]any{"item_id": item["id"], "output_index": state.currentIndex, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}}))
		}
		if kind == "reasoning" {
			return emit(responsesSSE("response.reasoning_summary_part.added", &state.sequence, map[string]any{"item_id": item["id"], "output_index": state.currentIndex, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}))
		}
		return true
	}
	process := func(line string) bool {
		event := parseCCEvent([]byte(line))
		if event == nil {
			return true
		}
		state.collected.consume(event, state.idFactory)
		switch stringValue(event["type"]) {
		case "text-delta":
			text := stringValue(event["text"])
			if text == "" {
				text = stringValue(event["delta"])
			}
			if text == "" {
				return true
			}
			if !start() || (state.currentItem == nil || state.currentKind != "message") && !open("message", map[string]any{"type": "message", "id": "msg_" + shortID(state.idFactory()), "status": "in_progress", "role": "assistant", "content": []any{}}) {
				return false
			}
			state.currentText += text
			state.text += text
			return emit(responsesSSE("response.output_text.delta", &state.sequence, map[string]any{"item_id": state.currentItem["id"], "output_index": state.currentIndex, "content_index": 0, "delta": text, "logprobs": []any{}}))
		case "reasoning-delta":
			text := stringValue(event["text"])
			if text == "" {
				return true
			}
			if !start() || (state.currentItem == nil || state.currentKind != "reasoning") && !open("reasoning", map[string]any{"type": "reasoning", "id": "rs_" + shortID(state.idFactory()), "summary": []any{}, "status": "in_progress"}) {
				return false
			}
			state.currentText += text
			return emit(responsesSSE("response.reasoning_summary_text.delta", &state.sequence, map[string]any{"item_id": state.currentItem["id"], "output_index": state.currentIndex, "summary_index": 0, "delta": text}))
		case "tool-call":
			if !start() {
				return false
			}
			args := event["input"]
			if _, ok := args.(string); !ok {
				args = jsonString(args)
			}
			if !open("function_call", map[string]any{"type": "function_call", "id": "fc_" + shortID(state.idFactory()), "call_id": stringValue(event["toolCallId"]), "name": stringValue(event["toolName"]), "arguments": "", "status": "in_progress"}) {
				return false
			}
			state.currentItem["arguments"] = stringValue(args)
			return emit(responsesSSE("response.function_call_arguments.delta", &state.sequence, map[string]any{"item_id": state.currentItem["id"], "output_index": state.currentIndex, "delta": args}))
		case "error":
			return emit(responsesSSE("error", &state.sequence, map[string]any{"code": state.collected.UpstreamError.Code, "message": state.collected.UpstreamError.Error(), "param": nil}))
		}
		return true
	}
	var buffer strings.Builder
	for chunk := range chunks {
		if chunk.Err != nil {
			if state.started {
				_ = emit(responsesSSE("response.failed", &state.sequence, map[string]any{"response": baseResponse("failed", state.doneItems)}))
			}
			emitStreamError(output, chunk.Err)
			return
		}
		buffer.Write(chunk.Payload)
		for {
			value := buffer.String()
			index := strings.IndexByte(value, '\n')
			if index < 0 {
				break
			}
			if !process(value[:index]) {
				return
			}
			buffer.Reset()
			buffer.WriteString(value[index+1:])
		}
	}
	if strings.TrimSpace(buffer.String()) != "" && !process(buffer.String()) {
		return
	}
	if state.collected.UpstreamError != nil {
		if state.started {
			_ = emit(responsesSSE("response.failed", &state.sequence, map[string]any{"response": baseResponse("failed", state.doneItems)}))
		}
		emitStreamError(output, state.collected.UpstreamError)
		return
	}
	if !state.started {
		if state.collected.SawFinish {
			_ = start()
		} else {
			err := incompleteError("no finish event")
			emitStreamError(output, err)
			return
		}
	}
	if !closeItem() {
		return
	}
	if detail := incompleteDetail(state.collected); detail != "" {
		_ = emit(responsesSSE("response.failed", &state.sequence, map[string]any{"response": map[string]any{"id": state.responseID, "object": "response", "status": "failed", "error": map[string]any{"code": "upstream_error", "message": incompleteError(detail).Error()}}}))
		emitStreamError(output, incompleteError(detail))
		return
	}
	status := "completed"
	eventName := "response.completed"
	var incomplete any
	if state.collected.FinishReason == "length" || state.collected.FinishReason == "pause_turn" {
		status = "incomplete"
		eventName = "response.incomplete"
		if state.collected.FinishReason == "length" {
			incomplete = map[string]any{"reason": "max_output_tokens"}
		} else {
			incomplete = map[string]any{"reason": "pause_turn"}
		}
	}
	response := baseResponse(status, state.doneItems)
	response["output_text"] = state.text
	response["incomplete_details"] = incomplete
	response["usage"] = buildResponsesUsage(state.collected.Usage, responsesFallbackOutputTokens(state.collected))
	_ = emit(responsesSSE(eventName, &state.sequence, map[string]any{"response": response}))
	_ = request
}

var _ pluginapi.ProviderExecutor = (*ResponsesExecutor)(nil)
