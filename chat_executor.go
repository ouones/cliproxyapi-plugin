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

type chatExecutorConfig struct {
	apiBase  string
	apiKey   func() string
	identity *identityStateStore
	now      func() time.Time
	newID    func() (string, error)
	client   pluginapi.HostHTTPClient
}

type ChatCompletionsExecutor struct {
	apiBase  string
	apiKey   func() string
	identity *identityStateStore
	now      func() time.Time
	newID    func() (string, error)
	client   pluginapi.HostHTTPClient
}

func NewChatCompletionsExecutor() *ChatCompletionsExecutor {
	return newChatCompletionsExecutor(chatExecutorConfig{})
}

func newChatCompletionsExecutor(config chatExecutorConfig) *ChatCompletionsExecutor {
	return &ChatCompletionsExecutor{
		apiBase:  config.apiBase,
		apiKey:   config.apiKey,
		identity: config.identity,
		now:      config.now,
		newID:    config.newID,
		client:   config.client,
	}
}

func (e *ChatCompletionsExecutor) Identifier() string { return pluginID }

func (e *ChatCompletionsExecutor) runtime() executorRuntime {
	runtime := defaultExecutorRuntime()
	runtime.apiBase = e.apiBase
	runtime.apiKey = e.apiKey
	runtime.identity = e.identity
	runtime.now = e.now
	runtime.client = e.client
	if e.newID != nil {
		runtime.idFactory = func() string {
			value, err := e.newID()
			if err == nil && value != "" {
				return value
			}
			return newRandomID()
		}
	}
	return runtime.normalized()
}

func (e *ChatCompletionsExecutor) Execute(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	runtime := e.runtime()
	openaiRequest, err := decodeRequestObject(requestPayload(request))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	if stringValue(openaiRequest["model"]) == "" {
		openaiRequest["model"] = request.Model
	}
	client, apiKey, sessionID, err := runtime.initialize(ctx, request, stringValue(openaiRequest["prompt_cache_key"]))
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

	result, err := json.Marshal(buildChatResponseObject(runtime, openaiRequest, collected))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	return pluginapi.ExecutorResponse{
		Payload: result,
		Headers: http.Header{"Content-Type": {"application/json"}},
	}, nil
}

func (e *ChatCompletionsExecutor) ExecuteStream(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	runtime := e.runtime()
	openaiRequest, err := decodeRequestObject(requestPayload(request))
	if err != nil {
		return pluginapi.ExecutorStreamResponse{}, err
	}
	if stringValue(openaiRequest["model"]) == "" {
		openaiRequest["model"] = request.Model
	}
	client, apiKey, sessionID, err := runtime.initialize(ctx, request, stringValue(openaiRequest["prompt_cache_key"]))
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
	go e.translateChatStream(ctx, runtime, request, openaiRequest, response.Chunks, output)
	headers := response.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	return pluginapi.ExecutorStreamResponse{Headers: headers, Chunks: output}, nil
}

func (e *ChatCompletionsExecutor) CountTokens(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	openaiRequest, err := decodeRequestObject(requestPayload(request))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	text := textOf(openaiRequest["messages"])
	result, _ := json.Marshal(map[string]any{"input_tokens": maxInt(1, len([]rune(text))/4)})
	return pluginapi.ExecutorResponse{Payload: result, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

func (e *ChatCompletionsExecutor) HttpRequest(ctx context.Context, request pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
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

func buildChatResponseObject(runtime executorRuntime, request map[string]any, collected ccCollected) map[string]any {
	finishReason := collected.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	toolCalls := make([]any, 0, len(collected.ToolCalls))
	for _, toolCall := range collected.ToolCalls {
		toolCalls = append(toolCalls, map[string]any{
			"id": toolCall.ID, "type": "function", "function": map[string]any{"name": toolCall.Name, "arguments": toolCall.Arguments},
		})
	}
	message := map[string]any{"role": "assistant", "content": nil}
	if collected.FullText != "" {
		message["content"] = collected.FullText
	}
	if collected.ReasoningText != "" {
		message["reasoning_content"] = collected.ReasoningText
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	created := runtime.now().Unix()
	return map[string]any{
		"id":      "chatcmpl-" + shortID(runtime.idFactory()),
		"object":  "chat.completion",
		"created": created,
		"model":   stringValue(request["model"]),
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": toOpenAIFinishReason(finishReason)}},
		"usage": map[string]any{
			"prompt_tokens":         collected.Usage.InputTokens,
			"completion_tokens":     collected.Usage.OutputTokens,
			"total_tokens":          collected.Usage.InputTokens + collected.Usage.OutputTokens,
			"prompt_tokens_details": map[string]any{"cached_tokens": collected.Usage.CachedInputTokens},
		},
	}
}

type chatStreamState struct {
	collected     ccCollected
	completionID  string
	created       int64
	model         string
	chunkIndex    int
	toolCallIndex int
	finishEmitted bool
	errorEmitted  bool
	idFactory     func() string
}

func (e *ChatCompletionsExecutor) translateChatStream(ctx context.Context, runtime executorRuntime, request pluginapi.ExecutorRequest, openaiRequest map[string]any, chunks <-chan pluginapi.HTTPStreamChunk, output chan<- pluginapi.ExecutorStreamChunk) {
	defer close(output)
	state := &chatStreamState{
		completionID: "chatcmpl-" + shortID(runtime.idFactory()),
		created:      runtime.now().Unix(),
		model:        stringValue(openaiRequest["model"]),
		idFactory:    runtime.idFactory,
	}
	var buffer strings.Builder
	emit := func(payload []byte) bool {
		select {
		case output <- pluginapi.ExecutorStreamChunk{Payload: payload}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	emitError := func(err error) {
		if err == nil || state.errorEmitted {
			return
		}
		state.errorEmitted = true
		message := err.Error()
		payload, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": protocolErrorType(err)}})
		_ = emit(append([]byte("data: "), append(payload, []byte("\n\n")...)...))
	}
	process := func(line string) bool {
		event := parseCCEvent([]byte(line))
		if event == nil {
			return true
		}
		beforeFinish := state.collected.SawFinish
		state.collected.consume(event, state.idFactory)
		switch stringValue(event["type"]) {
		case "text-delta":
			text := stringValue(event["text"])
			if text == "" {
				text = stringValue(event["delta"])
			}
			if text != "" {
				delta := map[string]any{"content": text}
				if state.chunkIndex == 0 {
					delta["role"] = "assistant"
				}
				state.chunkIndex++
				return emit(formatChatSSE(state.completionID, state.created, state.model, delta, "", nil))
			}
		case "reasoning-delta":
			text := stringValue(event["text"])
			if text != "" {
				delta := map[string]any{"reasoning_content": text}
				if state.chunkIndex == 0 {
					delta["role"] = "assistant"
				}
				state.chunkIndex++
				return emit(formatChatSSE(state.completionID, state.created, state.model, delta, "", nil))
			}
		case "tool-call":
			id := stringValue(event["toolCallId"])
			if id == "" {
				id = "call_" + shortID(state.idFactory())
			}
			arguments := event["input"]
			if _, ok := arguments.(string); !ok {
				arguments = jsonString(arguments)
			}
			entry := map[string]any{"index": state.toolCallIndex, "id": id, "type": "function", "function": map[string]any{"name": stringValue(event["toolName"]), "arguments": stringValue(arguments)}}
			delta := map[string]any{"tool_calls": []any{entry}}
			if state.chunkIndex == 0 {
				delta["role"] = "assistant"
				delta["content"] = nil
			}
			state.chunkIndex++
			state.toolCallIndex++
			return emit(formatChatSSE(state.completionID, state.created, state.model, delta, "", nil))
		case "finish":
			state.finishEmitted = true
			usage := map[string]any{
				"prompt_tokens":         state.collected.Usage.InputTokens,
				"completion_tokens":     state.collected.Usage.OutputTokens,
				"total_tokens":          state.collected.Usage.InputTokens + state.collected.Usage.OutputTokens,
				"prompt_tokens_details": map[string]any{"cached_tokens": state.collected.Usage.CachedInputTokens},
			}
			return emit(formatChatSSE(state.completionID, state.created, state.model, map[string]any{}, toOpenAIFinishReason(state.collected.FinishReason), usage))
		}
		if state.collected.UpstreamError != nil && !beforeFinish {
			emitError(state.collected.UpstreamError)
		}
		return true
	}
	for chunk := range chunks {
		if chunk.Err != nil {
			emitError(chunk.Err)
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
	if strings.TrimSpace(buffer.String()) != "" {
		if !process(buffer.String()) {
			return
		}
	}
	if state.collected.UpstreamError != nil {
		emitStreamError(output, state.collected.UpstreamError)
		return
	}
	if detail := incompleteDetail(state.collected); detail != "" {
		err := incompleteError(detail)
		emitError(err)
		emitStreamError(output, err)
		return
	}
	if !state.finishEmitted {
		if !state.collected.SawFinish {
			err := incompleteError("no finish event")
			emitError(err)
			emitStreamError(output, err)
			return
		}
		usage := map[string]any{
			"prompt_tokens":         state.collected.Usage.InputTokens,
			"completion_tokens":     state.collected.Usage.OutputTokens,
			"total_tokens":          state.collected.Usage.InputTokens + state.collected.Usage.OutputTokens,
			"prompt_tokens_details": map[string]any{"cached_tokens": state.collected.Usage.CachedInputTokens},
		}
		if !emit(formatChatSSE(state.completionID, state.created, state.model, map[string]any{}, toOpenAIFinishReason(state.collected.FinishReason), usage)) {
			return
		}
	}
	_ = emit([]byte("data: [DONE]\n\n"))
}

func emitStreamError(output chan<- pluginapi.ExecutorStreamChunk, err error) {
	if err == nil {
		return
	}
	output <- pluginapi.ExecutorStreamChunk{Err: err}
}

var _ pluginapi.ProviderExecutor = (*ChatCompletionsExecutor)(nil)
