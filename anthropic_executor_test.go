package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type fakeAnthropicHTTPClient struct {
	mu sync.Mutex

	doRequests     []pluginapi.HTTPRequest
	streamRequests []pluginapi.HTTPRequest
	doResponse     pluginapi.HTTPResponse
	doError        error
	streamResponse pluginapi.HTTPStreamResponse
	streamError    error
}

func (f *fakeAnthropicHTTPClient) Do(_ context.Context, request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	f.mu.Lock()
	f.doRequests = append(f.doRequests, cloneAnthropicHTTPRequest(request))
	response, err := f.doResponse, f.doError
	f.mu.Unlock()
	if strings.HasSuffix(request.URL, "/alpha/fingerprint/record") || strings.HasSuffix(request.URL, "/alpha/lifecycle-events") {
		return pluginapi.HTTPResponse{StatusCode: http.StatusOK}, nil
	}
	return response, err
}

func (f *fakeAnthropicHTTPClient) DoStream(_ context.Context, request pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	f.mu.Lock()
	f.streamRequests = append(f.streamRequests, cloneAnthropicHTTPRequest(request))
	response, err := f.streamResponse, f.streamError
	f.mu.Unlock()
	return response, err
}

func (f *fakeAnthropicHTTPClient) doSnapshot() []pluginapi.HTTPRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]pluginapi.HTTPRequest, len(f.doRequests))
	copy(result, f.doRequests)
	return result
}

func (f *fakeAnthropicHTTPClient) streamSnapshot() []pluginapi.HTTPRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]pluginapi.HTTPRequest, len(f.streamRequests))
	copy(result, f.streamRequests)
	return result
}

func cloneAnthropicHTTPRequest(request pluginapi.HTTPRequest) pluginapi.HTTPRequest {
	return pluginapi.HTTPRequest{
		Method:  request.Method,
		URL:     request.URL,
		Headers: request.Headers.Clone(),
		Body:    append([]byte(nil), request.Body...),
	}
}

func newAnthropicTestExecutor() *AnthropicMessagesExecutor {
	return &AnthropicMessagesExecutor{
		apiBase: "https://command-code.test",
		identity: newIdentityStateStore(identityStateConfig{
			fingerprintSalt: "anthropic-test-salt",
			apiBase:         "https://command-code.test",
			now:             func() time.Time { return time.Unix(100, 0) },
			randomDuration:  func(time.Duration) time.Duration { return 0 },
			newSessionID:    func() (string, error) { return "11111111-1111-4111-8111-111111111111", nil },
			newLifecycleID:  func() (string, error) { return "sess_test", nil },
			cleanupInterval: -1,
		}),
		idFactory: func() string { return "22222222-2222-4222-8222-222222222222" },
	}
}

func setAnthropicTestAPIKey(t *testing.T) {
	t.Helper()
	credentialState.Lock()
	credentialState.apiKey = "user_anthropic_test"
	credentialState.Unlock()
	t.Cleanup(func() {
		credentialState.Lock()
		credentialState.apiKey = ""
		credentialState.Unlock()
		commandCodeIdentityStates.reset()
	})
}

func anthropicExecutionRequest(host pluginapi.HostHTTPClient, body string) pluginapi.ExecutorRequest {
	return pluginapi.ExecutorRequest{
		Model:      "claude-sonnet-4-6",
		Format:     "anthropic",
		Headers:    http.Header{"x-session-id": {"explicit-session-1234"}},
		Payload:    []byte(body),
		HTTPClient: host,
	}
}

func findAnthropicRequest(requests []pluginapi.HTTPRequest, suffix string) (pluginapi.HTTPRequest, bool) {
	for _, request := range requests {
		if strings.HasSuffix(request.URL, suffix) {
			return request, true
		}
	}
	return pluginapi.HTTPRequest{}, false
}

func decodeAnthropicJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode JSON response: %v", err)
	}
	return value
}

func TestAnthropicMessagesExecuteConvertsThinkingToolsAndCacheUsage(t *testing.T) {
	setAnthropicTestAPIKey(t)
	host := &fakeAnthropicHTTPClient{
		doResponse: pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body: []byte(strings.Join([]string{
				`{"type":"reasoning-delta","text":"plan"}`,
				`{"type":"text-delta","text":"answer"}`,
				`{"type":"tool-call","toolCallId":"call_1","toolName":"lookup","input":{"q":"x"}}`,
				`{"type":"finish","finishReason":"tool_calls","totalUsage":{"inputTokens":100,"outputTokens":20,"cachedInputTokens":30,"inputTokenDetails":{"cacheWriteTokens":10,"noCacheTokens":60}}}`,
			}, "\n")),
		},
	}

	executor := newAnthropicTestExecutor()
	response, err := executor.Execute(context.Background(), anthropicExecutionRequest(host, `{
		"model":"claude-sonnet-4-6",
		"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"old thought"},{"type":"text","text":"old answer"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"old"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done"}]}
		],
		"tools":[{"name":"lookup","description":"lookup data","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],
		"thinking":{"type":"enabled","budget_tokens":5000},
		"stream":false
	}`))
	if err != nil {
		t.Fatalf("execute Anthropic request: %v", err)
	}

	result := decodeAnthropicJSON(t, response.Payload)
	if result["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", result["stop_reason"])
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 3 {
		t.Fatalf("content = %#v, want thinking/text/tool_use blocks", result["content"])
	}
	if content[0].(map[string]any)["type"] != "thinking" || content[1].(map[string]any)["type"] != "text" || content[2].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("content block order = %#v", content)
	}
	usage := result["usage"].(map[string]any)
	if usage["input_tokens"] != float64(60) || usage["output_tokens"] != float64(20) || usage["cache_creation_input_tokens"] != float64(10) || usage["cache_read_input_tokens"] != float64(30) {
		t.Fatalf("usage = %#v", usage)
	}

	request, ok := findAnthropicRequest(host.doSnapshot(), "/alpha/generate")
	if !ok {
		t.Fatal("host did not receive the Command Code generate request")
	}
	if request.Headers.Get("Authorization") != "Bearer user_anthropic_test" {
		t.Fatal("generate request did not use the configured credential")
	}
	var ccBody map[string]any
	if err := json.Unmarshal(request.Body, &ccBody); err != nil {
		t.Fatalf("decode Command Code request: %v", err)
	}
	params := ccBody["params"].(map[string]any)
	if params["stream"] != true || params["reasoning_effort"] != "medium" {
		t.Fatalf("Command Code params = %#v", params)
	}
	system := params["system"].([]any)
	if system[0].(map[string]any)["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("system cache marker was not preserved: %#v", system)
	}
}

func TestAnthropicMessagesExecuteStreamEmitsThinkingToolUsageAndValidTail(t *testing.T) {
	setAnthropicTestAPIKey(t)
	chunks := make(chan pluginapi.HTTPStreamChunk, 2)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte("{\"type\":\"reasoning-delta\",\"text\":\"think\"}\n{\"type\":\"text-delta\",\"text\":\"hello\"}\n")}
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte("{\"type\":\"tool-call\",\"toolCallId\":\"call_1\",\"toolName\":\"lookup\",\"input\":{\"q\":\"x\"}}\n{\"type\":\"finish\",\"finishReason\":\"tool_calls\",\"totalUsage\":{\"inputTokens\":8,\"outputTokens\":12,\"cachedInputTokens\":3,\"inputTokenDetails\":{\"cacheWriteTokens\":2,\"noCacheTokens\":3}}}\n")}
	close(chunks)
	host := &fakeAnthropicHTTPClient{streamResponse: pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"text/event-stream"}}, Chunks: chunks}}

	response, err := newAnthropicTestExecutor().ExecuteStream(context.Background(), anthropicExecutionRequest(host, `{"messages":[{"role":"user","content":"hello"}],"stream":true}`))
	if err != nil {
		t.Fatalf("start Anthropic stream: %v", err)
	}
	var output strings.Builder
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	stream := output.String()
	if !strings.Contains(stream, "event: message_start") || !strings.Contains(stream, "thinking_delta") || !strings.Contains(stream, "input_json_delta") || !strings.Contains(stream, `"stop_reason":"tool_use"`) {
		t.Fatalf("stream omitted expected Anthropic events: %s", stream)
	}
	if strings.Count(stream, "event: message_stop") != 1 {
		t.Fatalf("message_stop count = %d, want 1", strings.Count(stream, "event: message_stop"))
	}
	if !strings.Contains(stream, `"cache_read_input_tokens":3`) || !strings.Contains(stream, `"cache_creation_input_tokens":2`) || !strings.Contains(stream, `"input_tokens":3`) {
		t.Fatalf("stream usage omitted cache accounting: %s", stream)
	}
}

func TestAnthropicStreamClosesThinkingWithSignatureBeforeText(t *testing.T) {
	setAnthropicTestAPIKey(t)
	chunks := make(chan pluginapi.HTTPStreamChunk, 1)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(strings.Join([]string{
		`{"type":"reasoning-delta","text":"think"}`,
		`{"type":"text-delta","text":"answer"}`,
		`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":1,"outputTokens":2}}`,
	}, "\n") + "\n")}
	close(chunks)
	host := &fakeAnthropicHTTPClient{streamResponse: pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: chunks}}

	response, err := newAnthropicTestExecutor().ExecuteStream(context.Background(), anthropicExecutionRequest(host, `{"messages":[{"role":"user","content":"hello"}],"stream":true}`))
	if err != nil {
		t.Fatalf("start Anthropic stream: %v", err)
	}
	var output strings.Builder
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	stream := output.String()
	thinkingDelta := strings.Index(stream, `"type":"thinking_delta"`)
	signatureDelta := strings.Index(stream, `"type":"signature_delta"`)
	firstBlockStop := strings.Index(stream, "event: content_block_stop")
	firstTextStart := strings.Index(stream[firstBlockStop+1:], "event: content_block_start")
	if firstTextStart >= 0 {
		firstTextStart += firstBlockStop + 1
	}
	textDelta := strings.Index(stream, `"type":"text_delta"`)
	if thinkingDelta < 0 || signatureDelta < 0 || firstBlockStop < 0 || firstTextStart < 0 || textDelta < 0 {
		t.Fatalf("stream omitted thinking/text block events: %s", stream)
	}
	if !(thinkingDelta < signatureDelta && signatureDelta < firstBlockStop && firstBlockStop < firstTextStart && firstTextStart < textDelta) {
		t.Fatalf("thinking block did not close with signature before text: %s", stream)
	}
}

func TestAnthropicStreamWithoutUsageCompletesWithFallbackTokens(t *testing.T) {
	setAnthropicTestAPIKey(t)
	chunks := make(chan pluginapi.HTTPStreamChunk, 1)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(strings.Join([]string{
		`{"type":"reasoning-delta","text":"think"}`,
		`{"type":"text-delta","text":"answer"}`,
		`{"type":"tool-call","toolCallId":"call_1","toolName":"lookup","input":{"q":"x"}}`,
		`{"type":"finish","finishReason":"tool_calls"}`,
	}, "\n") + "\n")}
	close(chunks)
	host := &fakeAnthropicHTTPClient{streamResponse: pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: chunks}}

	response, err := newAnthropicTestExecutor().ExecuteStream(context.Background(), anthropicExecutionRequest(host, `{"messages":[{"role":"user","content":"hello"}],"stream":true}`))
	if err != nil {
		t.Fatalf("start Anthropic stream: %v", err)
	}
	var output strings.Builder
	var streamErr error
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		output.Write(chunk.Payload)
	}
	stream := output.String()
	if streamErr != nil {
		t.Fatalf("stream ended with an error despite a normal finish: %v; stream=%s", streamErr, stream)
	}
	if strings.Contains(stream, "event: error") {
		t.Fatalf("stream emitted an error despite a normal finish: %s", stream)
	}
	if strings.Count(stream, "event: message_delta") != 1 || strings.Count(stream, "event: message_stop") != 1 {
		t.Fatalf("stream tail count is not normal: %s", stream)
	}
	messageDelta := strings.Index(stream, "event: message_delta")
	messageStop := strings.Index(stream, "event: message_stop")
	if messageDelta < 0 || messageStop < 0 || !strings.Contains(stream[messageDelta:messageStop], `"output_tokens":22`) {
		t.Fatalf("stream did not use the expected fallback output token count: %s", stream)
	}
}

func TestAnthropicMessagesStreamErrorDoesNotEmitSuccessfulTail(t *testing.T) {
	tests := []struct {
		name    string
		chunks  func() chan pluginapi.HTTPStreamChunk
		wantErr bool
	}{
		{
			name: "upstream SSE error",
			chunks: func() chan pluginapi.HTTPStreamChunk {
				chunks := make(chan pluginapi.HTTPStreamChunk, 1)
				chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"error","error":{"message":"provider failed","statusCode":503}}\n`)}
				close(chunks)
				return chunks
			},
		},
		{
			name: "normal disconnect without finish",
			chunks: func() chan pluginapi.HTTPStreamChunk {
				chunks := make(chan pluginapi.HTTPStreamChunk, 1)
				chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"text-delta","text":"partial"}\n`)}
				close(chunks)
				return chunks
			},
		},
		{
			name: "transport disconnect",
			chunks: func() chan pluginapi.HTTPStreamChunk {
				chunks := make(chan pluginapi.HTTPStreamChunk, 2)
				chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"text-delta","text":"partial"}\n`)}
				chunks <- pluginapi.HTTPStreamChunk{Err: errors.New("stream disconnected")}
				close(chunks)
				return chunks
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setAnthropicTestAPIKey(t)
			host := &fakeAnthropicHTTPClient{streamResponse: pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: test.chunks()}}
			response, err := newAnthropicTestExecutor().ExecuteStream(context.Background(), anthropicExecutionRequest(host, `{"messages":[{"role":"user","content":"hello"}],"stream":true}`))
			if err != nil {
				t.Fatalf("start Anthropic stream: %v", err)
			}
			var output strings.Builder
			var streamErr error
			for chunk := range response.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
					continue
				}
				output.Write(chunk.Payload)
			}
			stream := output.String()
			if strings.Contains(stream, "event: message_stop") || strings.Contains(stream, "event: message_delta") {
				t.Fatalf("incomplete stream emitted successful tail: %s", stream)
			}
			if test.name == "upstream SSE error" && !strings.Contains(stream, "event: error") {
				t.Fatalf("upstream SSE error was not forwarded: %s", stream)
			}
			if test.name == "normal disconnect without finish" && !strings.Contains(stream, "event: error") {
				t.Fatalf("incomplete normal close did not emit an error event: %s", stream)
			}
			if test.wantErr && streamErr == nil {
				t.Fatal("transport disconnect did not propagate a stream error")
			}
		})
	}
}

func TestAnthropicMessagesExecuteMapsUpstreamHTTPError(t *testing.T) {
	setAnthropicTestAPIKey(t)
	host := &fakeAnthropicHTTPClient{
		doResponse: pluginapi.HTTPResponse{
			StatusCode: http.StatusTooManyRequests,
			Body:       []byte(`{"error":{"message":"quota exceeded","code":"quota"}}`),
		},
	}
	_, err := newAnthropicTestExecutor().Execute(context.Background(), anthropicExecutionRequest(host, `{"messages":[{"role":"user","content":"hello"}]}`))
	if err == nil {
		t.Fatal("expected upstream HTTP error")
	}
	var pluginErr *pluginabi.Error
	if !errors.As(err, &pluginErr) || pluginErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want plugin error with HTTP 429", err)
	}
}

func TestAnthropicMessagesExecutorUsesProviderExecutorContract(t *testing.T) {
	var _ pluginapi.ProviderExecutor = NewAnthropicMessagesExecutor()
	if got := NewAnthropicMessagesExecutor().Identifier(); got != pluginID {
		t.Fatalf("executor identifier = %q, want %q", got, pluginID)
	}
}

func TestAnthropicMessagesTestRequestDoesNotExposeCredentialInErrorText(t *testing.T) {
	setAnthropicTestAPIKey(t)
	host := &fakeAnthropicHTTPClient{doError: fmt.Errorf("host unavailable")}
	_, err := newAnthropicTestExecutor().Execute(context.Background(), anthropicExecutionRequest(host, `{"messages":[]}`))
	if err == nil || strings.Contains(err.Error(), "user_anthropic_test") {
		t.Fatalf("unexpected credential exposure in error: %v", err)
	}
}
