package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type AnthropicMessagesExecutor struct {
	apiBase   string
	apiKey    func() string
	identity  *identityStateStore
	idFactory func() string
	client    pluginapi.HostHTTPClient
}

func NewAnthropicMessagesExecutor() *AnthropicMessagesExecutor {
	return &AnthropicMessagesExecutor{apiBase: commandCodeAPIBase(), apiKey: configuredAPIKey, identity: commandCodeIdentityStates, idFactory: newRandomID}
}

func (e *AnthropicMessagesExecutor) Identifier() string { return pluginID }

func (e *AnthropicMessagesExecutor) runtime() executorRuntime {
	runtime := defaultExecutorRuntime()
	runtime.apiBase = e.apiBase
	runtime.apiKey = e.apiKey
	runtime.identity = e.identity
	runtime.client = e.client
	if e.idFactory != nil {
		runtime.idFactory = e.idFactory
	}
	return runtime.normalized()
}

func (e *AnthropicMessagesExecutor) Execute(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	runtime := e.runtime()
	anthropicRequest, err := decodeRequestObject(requestPayload(request))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	if stringValue(anthropicRequest["model"]) == "" {
		anthropicRequest["model"] = request.Model
	}
	openaiRequest := convertAnthropicRequest(anthropicRequest)
	client, apiKey, sessionID, err := runtime.initialize(ctx, request, stringValue(anthropicRequest["prompt_cache_key"]))
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
	payload, err := json.Marshal(buildAnthropicResponse(runtime, anthropicRequest, collected))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	return pluginapi.ExecutorResponse{Payload: payload, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

func (e *AnthropicMessagesExecutor) ExecuteStream(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	runtime := e.runtime()
	anthropicRequest, err := decodeRequestObject(requestPayload(request))
	if err != nil {
		return pluginapi.ExecutorStreamResponse{}, err
	}
	if stringValue(anthropicRequest["model"]) == "" {
		anthropicRequest["model"] = request.Model
	}
	openaiRequest := convertAnthropicRequest(anthropicRequest)
	client, apiKey, sessionID, err := runtime.initialize(ctx, request, stringValue(anthropicRequest["prompt_cache_key"]))
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
	go e.translateAnthropicStream(ctx, runtime, anthropicRequest, response.Chunks, output)
	headers := response.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	return pluginapi.ExecutorStreamResponse{Headers: headers, Chunks: output}, nil
}

func (e *AnthropicMessagesExecutor) CountTokens(ctx context.Context, request pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	anthropicRequest, err := decodeRequestObject(requestPayload(request))
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	result, _ := json.Marshal(map[string]any{"input_tokens": maxInt(1, len([]rune(textOf(anthropicRequest["messages"])+textOf(anthropicRequest["system"])))/4)})
	return pluginapi.ExecutorResponse{Payload: result, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

func (e *AnthropicMessagesExecutor) HttpRequest(ctx context.Context, request pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
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

func buildAnthropicResponse(runtime executorRuntime, request map[string]any, collected ccCollected) map[string]any {
	content := make([]any, 0)
	if collected.ReasoningText != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": collected.ReasoningText, "signature": fakeThinkingSignature(collected.ReasoningText)})
	}
	if collected.FullText != "" {
		content = append(content, map[string]any{"type": "text", "text": collected.FullText})
	}
	for _, toolCall := range collected.ToolCalls {
		var input any
		if json.Unmarshal([]byte(toolCall.Arguments), &input) != nil || input == nil {
			input = map[string]any{}
		}
		content = append(content, map[string]any{"type": "tool_use", "id": toolCall.ID, "name": toolCall.Name, "input": input})
	}
	outputTokens := collected.Usage.OutputTokens
	if outputTokens == 0 {
		outputTokens = maxInt(1, (len([]rune(collected.FullText))+len([]rune(collected.ReasoningText)))/4)
	}
	return map[string]any{
		"id": "msg_" + shortID(runtime.idFactory()), "type": "message", "role": "assistant", "model": stringValue(request["model"]), "content": content,
		"stop_reason": anthropicStopReason(defaultString(collected.FinishReason, "stop")), "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": anthropicInputTokens(collected.Usage), "output_tokens": outputTokens, "cache_creation_input_tokens": collected.Usage.CacheWriteTokens, "cache_read_input_tokens": collected.Usage.CachedInputTokens},
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type anthropicStreamState struct {
	collected      ccCollected
	messageID      string
	model          string
	nextBlockIndex int
	currentIndex   int
	currentType    string
	currentText    string
	blockStarted   bool
	errorSent      bool
	fallbackTokens int
	idFactory      func() string
}

func (e *AnthropicMessagesExecutor) translateAnthropicStream(ctx context.Context, runtime executorRuntime, request map[string]any, chunks <-chan pluginapi.HTTPStreamChunk, output chan<- pluginapi.ExecutorStreamChunk) {
	defer close(output)
	state := &anthropicStreamState{messageID: "msg_" + shortID(runtime.idFactory()), model: stringValue(request["model"]), idFactory: runtime.idFactory}
	emit := func(payload []byte) bool {
		select {
		case output <- pluginapi.ExecutorStreamChunk{Payload: payload}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	closeBlock := func() bool {
		if !state.blockStarted {
			return true
		}
		if state.currentType == "thinking" {
			if !emit(formatSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": state.currentIndex, "delta": map[string]any{"type": "signature_delta", "signature": fakeThinkingSignature(state.currentText)}})) {
				return false
			}
		}
		if !emit(formatSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": state.currentIndex})) {
			return false
		}
		state.blockStarted = false
		state.currentType = ""
		state.currentText = ""
		return true
	}
	startBlock := func(kind string, contentBlock map[string]any) bool {
		if state.blockStarted && state.currentType == kind {
			return true
		}
		if !closeBlock() {
			return false
		}
		state.currentIndex = state.nextBlockIndex
		state.nextBlockIndex++
		state.currentType = kind
		state.currentText = ""
		state.blockStarted = true
		return emit(formatSSE("content_block_start", map[string]any{"type": "content_block_start", "index": state.currentIndex, "content_block": contentBlock}))
	}
	emitError := func(err error) {
		if err == nil || state.errorSent {
			return
		}
		state.errorSent = true
		emit(formatSSE("error", map[string]any{"type": "error", "error": map[string]any{"type": protocolErrorType(err), "message": err.Error()}}))
	}
	if !emit(formatSSE("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": state.messageID, "type": "message", "role": "assistant", "content": []any{}, "model": state.model, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}})) {
		return
	}
	process := func(line string) bool {
		event := parseCCEvent([]byte(line))
		if event == nil {
			return true
		}
		state.collected.consume(event, state.idFactory)
		switch stringValue(event["type"]) {
		case "reasoning-delta":
			text := stringValue(event["text"])
			if text == "" {
				return true
			}
			if !startBlock("thinking", map[string]any{"type": "thinking", "thinking": ""}) {
				return false
			}
			state.currentText += text
			if !emit(formatSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": state.currentIndex, "delta": map[string]any{"type": "thinking_delta", "thinking": text}})) {
				return false
			}
			state.fallbackTokens++
			return true
		case "text-delta":
			text := stringValue(event["text"])
			if text == "" {
				return true
			}
			if !startBlock("text", map[string]any{"type": "text", "text": ""}) {
				return false
			}
			state.currentText += text
			if !emit(formatSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": state.currentIndex, "delta": map[string]any{"type": "text_delta", "text": text}})) {
				return false
			}
			state.fallbackTokens++
			return true
		case "tool-call":
			if !closeBlock() {
				return false
			}
			id := stringValue(event["toolCallId"])
			if id == "" {
				id = "toolu_" + shortID(state.idFactory())
			}
			input := event["input"]
			if _, ok := input.(string); !ok {
				input = jsonString(input)
			}
			index := state.nextBlockIndex
			state.nextBlockIndex++
			if !emit(formatSSE("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": id, "name": stringValue(event["toolName"]), "input": map[string]any{}}})) ||
				!emit(formatSSE("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": input}})) ||
				!emit(formatSSE("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})) {
				return false
			}
			state.fallbackTokens += 20
			return true
		case "error":
			if state.collected.UpstreamError != nil {
				emitError(state.collected.UpstreamError)
			}
		}
		return true
	}
	var buffer strings.Builder
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
	if strings.TrimSpace(buffer.String()) != "" && !process(buffer.String()) {
		return
	}
	if !closeBlock() {
		return
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
	outputTokens := state.collected.Usage.OutputTokens
	if !state.collected.Usage.hasUsage {
		outputTokens = state.fallbackTokens
	}
	if outputTokens == 0 {
		err := pluginabi.NewError("rate_limit_error", "Empty response from upstream (zero output tokens)", http.StatusTooManyRequests)
		emitError(err)
		emitStreamError(output, err)
		return
	}
	usage := map[string]any{"output_tokens": outputTokens, "cache_read_input_tokens": state.collected.Usage.CachedInputTokens, "cache_creation_input_tokens": state.collected.Usage.CacheWriteTokens, "input_tokens": anthropicInputTokens(state.collected.Usage)}
	if !emit(formatSSE("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": anthropicStopReason(defaultString(state.collected.FinishReason, "stop"))}, "usage": usage})) {
		return
	}
	_ = emit(formatSSE("message_stop", map[string]any{"type": "message_stop"}))
}

var _ pluginapi.ProviderExecutor = (*AnthropicMessagesExecutor)(nil)
