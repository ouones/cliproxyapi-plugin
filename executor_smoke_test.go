package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type executorSmokeHTTPClient struct {
	mu          sync.Mutex
	responses   []pluginapi.HTTPResponse
	stream      pluginapi.HTTPStreamResponse
	requests    []pluginapi.HTTPRequest
	streamCalls int
}

func (c *executorSmokeHTTPClient) Do(_ context.Context, request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request)
	if strings.HasSuffix(request.URL, "/alpha/fingerprint/record") || strings.HasSuffix(request.URL, "/alpha/lifecycle-events") {
		return pluginapi.HTTPResponse{StatusCode: http.StatusOK}, nil
	}
	if len(c.responses) == 0 {
		return pluginapi.HTTPResponse{}, errors.New("no smoke response")
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func (c *executorSmokeHTTPClient) DoStream(_ context.Context, request pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	c.mu.Lock()
	c.streamCalls++
	c.requests = append(c.requests, request)
	response := c.stream
	c.mu.Unlock()
	return response, nil
}

func newSmokeIdentity() *identityStateStore {
	return newIdentityStateStore(identityStateConfig{
		apiBase:         "https://cc.test",
		fingerprintSalt: "smoke",
		now:             func() time.Time { return time.Unix(100, 0).UTC() },
		randomDuration:  func(time.Duration) time.Duration { return 0 },
		newSessionID:    func() (string, error) { return "11111111-1111-4111-8111-111111111111", nil },
		newLifecycleID:  func() (string, error) { return "sess_smoke", nil },
		cleanupInterval: -1,
	})
}

func TestChatExecutorSmokePreservesReasoningToolsAndUsage(t *testing.T) {
	client := &executorSmokeHTTPClient{responses: []pluginapi.HTTPResponse{{
		StatusCode: http.StatusOK,
		Body: []byte(strings.Join([]string{
			`{"type":"reasoning-delta","text":"think"}`,
			`{"type":"text-delta","text":"answer"}`,
			`{"type":"tool-call","toolCallId":"call_1","toolName":"lookup","input":{"q":"x"}}`,
			`{"type":"finish","finishReason":"tool-calls","totalUsage":{"inputTokens":9,"outputTokens":3,"cachedInputTokens":2}}`,
		}, "\n")),
	}}}
	executor := newChatCompletionsExecutor(chatExecutorConfig{
		apiBase:  "https://cc.test",
		apiKey:   func() string { return "user_smoke" },
		identity: newSmokeIdentity(),
		now:      func() time.Time { return time.Unix(100, 0).UTC() },
		newID:    func() (string, error) { return "22222222-2222-4222-8222-222222222222", nil },
	})
	response, err := executor.Execute(context.Background(), pluginapi.ExecutorRequest{
		Model: "smoke-model", Format: "chat-completions", HTTPClient: client,
		Headers: http.Header{"X-Session-Id": {"11111111-1111-4111-8111-111111111111"}},
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Payload, &body); err != nil {
		t.Fatal(err)
	}
	choice := body["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "answer" || message["reasoning_content"] != "think" || choice["finish_reason"] != "tool_calls" {
		t.Fatalf("unexpected Chat result: %#v", body)
	}
	usage := body["usage"].(map[string]any)
	if usage["total_tokens"] != float64(12) || usage["prompt_tokens_details"].(map[string]any)["cached_tokens"] != float64(2) {
		t.Fatalf("unexpected Chat usage: %#v", usage)
	}
}

func TestChatExecutorSmokeCompletesStreamWithUsage(t *testing.T) {
	chunks := make(chan pluginapi.HTTPStreamChunk, 2)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"reasoning-delta","text":"think"}` + "\n" + `{"type":"text-delta","text":"answer"}` + "\n")}
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":4,"outputTokens":2,"cachedInputTokens":1}}` + "\n")}
	close(chunks)

	client := &executorSmokeHTTPClient{stream: pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: chunks}}
	executor := newChatCompletionsExecutor(chatExecutorConfig{apiBase: "https://cc.test", apiKey: func() string { return "user_smoke" }, identity: newSmokeIdentity()})
	response, err := executor.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{
		Model: "smoke-model", Format: "chat-completions", HTTPClient: client,
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var stream strings.Builder
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		if !json.Valid([]byte(strings.TrimSpace(string(chunk.Payload)))) {
			t.Fatalf("Chat stream chunk must be raw JSON for CPA SSE framing: %q", chunk.Payload)
		}
		stream.Write(chunk.Payload)
	}
	if !strings.Contains(stream.String(), "reasoning_content") || !strings.Contains(stream.String(), `"finish_reason":"stop"`) || !strings.Contains(stream.String(), `"total_tokens":6`) || strings.Contains(stream.String(), "data:") || strings.Contains(stream.String(), "[DONE]") {
		t.Fatalf("unexpected Chat stream: %s", stream.String())
	}
}

func TestResponsesExecutorSmokeReportsIncompleteLength(t *testing.T) {
	client := &executorSmokeHTTPClient{responses: []pluginapi.HTTPResponse{{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"type":"text-delta","text":"partial"}` + "\n" + `{"type":"finish","finishReason":"max_output_tokens","totalUsage":{"inputTokens":4,"outputTokens":2}}` + "\n"),
	}}}
	executor := &ResponsesExecutor{
		identity: newSmokeIdentity(), apiBase: "https://cc.test", client: client,
		apiKey: func() string { return "user_smoke" }, now: func() time.Time { return time.Unix(100, 0).UTC() },
		newID: func(prefix string) (string, error) { return prefix + "smoke", nil },
	}
	response, err := executor.Execute(context.Background(), pluginapi.ExecutorRequest{
		Model: "gpt-smoke", Format: "openai-response", HTTPClient: client,
		Payload: []byte(`{"model":"gpt-smoke","instructions":"system","input":"hello","max_output_tokens":2}`),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "incomplete" || body["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("unexpected Responses result: %#v", body)
	}
}

func TestResponsesExecutorSmokeCompletesStream(t *testing.T) {
	chunks := make(chan pluginapi.HTTPStreamChunk, 2)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"reasoning-delta","text":"think"}` + "\n" + `{"type":"text-delta","text":"answer"}` + "\n")}
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":4,"outputTokens":2}}` + "\n")}
	close(chunks)

	client := &executorSmokeHTTPClient{stream: pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: chunks}}
	executor := &ResponsesExecutor{
		identity: newSmokeIdentity(), apiBase: "https://cc.test", client: client,
		apiKey: func() string { return "user_smoke" }, now: func() time.Time { return time.Unix(100, 0).UTC() },
		newID: func(prefix string) (string, error) { return prefix + "smoke", nil },
	}
	response, err := executor.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{
		Model: "gpt-smoke", Format: "openai-response", HTTPClient: client,
		Payload: []byte(`{"model":"gpt-smoke","input":"hello","stream":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var stream strings.Builder
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		stream.Write(chunk.Payload)
	}
	if !strings.Contains(stream.String(), "response.completed") || !strings.Contains(stream.String(), `"status":"completed"`) || strings.Contains(stream.String(), "response.incomplete") {
		t.Fatalf("unexpected Responses stream: %s", stream.String())
	}
}

func TestResponsesExecuteRejectsFinishedEmptyOutput(t *testing.T) {
	client := &executorSmokeHTTPClient{responses: []pluginapi.HTTPResponse{{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":4,"outputTokens":0}}` + "\n"),
	}}}
	executor := newResponsesSmokeExecutor(client)
	_, err := executor.Execute(context.Background(), pluginapi.ExecutorRequest{
		Model: "gpt-smoke", Format: "openai-response", HTTPClient: client,
		Payload: []byte(`{"model":"gpt-smoke","input":"hello"}`),
	})
	if err == nil {
		t.Fatal("Execute() returned a completed response for an empty finished output")
	}
	var pluginErr *pluginabi.Error
	if !errors.As(err, &pluginErr) || pluginErr.Code != "rate_limit_error" || pluginErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want rate_limit_error with HTTP 429", err)
	}
}

func TestResponsesUsageFallsBackToObservedOutput(t *testing.T) {
	t.Run("non-stream", func(t *testing.T) {
		client := &executorSmokeHTTPClient{responses: []pluginapi.HTTPResponse{{
			StatusCode: http.StatusOK,
			Body: []byte(strings.Join([]string{
				`{"type":"reasoning-delta","text":"think"}`,
				`{"type":"text-delta","text":"answer"}`,
				`{"type":"tool-call","toolCallId":"call_1","toolName":"lookup","input":{"q":"x"}}`,
				`{"type":"finish","finishReason":"stop"}`,
			}, "\n")),
		}}}
		executor := newResponsesSmokeExecutor(client)
		response, err := executor.Execute(context.Background(), pluginapi.ExecutorRequest{
			Model: "gpt-smoke", Format: "openai-response", HTTPClient: client,
			Payload: []byte(`{"model":"gpt-smoke","input":"hello"}`),
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(response.Payload, &body); err != nil {
			t.Fatal(err)
		}
		usage := body["usage"].(map[string]any)
		if usage["output_tokens"].(float64) <= 0 {
			t.Fatalf("output_tokens = %v, want a positive fallback", usage["output_tokens"])
		}
	})

	t.Run("provided usage wins", func(t *testing.T) {
		client := &executorSmokeHTTPClient{responses: []pluginapi.HTTPResponse{{
			StatusCode: http.StatusOK,
			Body: []byte(strings.Join([]string{
				`{"type":"text-delta","text":"answer"}`,
				`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":4,"outputTokens":0}}`,
			}, "\n")),
		}}}
		executor := newResponsesSmokeExecutor(client)
		response, err := executor.Execute(context.Background(), pluginapi.ExecutorRequest{
			Model: "gpt-smoke", Format: "openai-response", HTTPClient: client,
			Payload: []byte(`{"model":"gpt-smoke","input":"hello"}`),
		})
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(response.Payload, &body); err != nil {
			t.Fatal(err)
		}
		usage := body["usage"].(map[string]any)
		if usage["output_tokens"].(float64) != 0 {
			t.Fatalf("output_tokens = %v, want upstream value 0", usage["output_tokens"])
		}
		if usage["input_tokens"].(float64) != 0 || usage["total_tokens"].(float64) != 0 {
			t.Fatalf("usage = %v, want input_tokens=0 and total_tokens=0", usage)
		}
	})

	t.Run("stream", func(t *testing.T) {
		chunks := make(chan pluginapi.HTTPStreamChunk, 1)
		chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(strings.Join([]string{
			`{"type":"reasoning-delta","text":"think"}`,
			`{"type":"text-delta","text":"answer"}`,
			`{"type":"tool-call","toolCallId":"call_1","toolName":"lookup","input":{"q":"x"}}`,
			`{"type":"finish","finishReason":"stop"}`,
		}, "\n") + "\n")}
		close(chunks)
		client := &executorSmokeHTTPClient{stream: pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: chunks}}
		executor := newResponsesSmokeExecutor(client)
		response, err := executor.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{
			Model: "gpt-smoke", Format: "openai-response", HTTPClient: client,
			Payload: []byte(`{"model":"gpt-smoke","input":"hello","stream":true}`),
		})
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		var stream strings.Builder
		for chunk := range response.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error: %v", chunk.Err)
			}
			stream.Write(chunk.Payload)
		}
		if !strings.Contains(stream.String(), `"output_tokens":`) || strings.Contains(stream.String(), `"output_tokens":0`) {
			t.Fatalf("stream did not use a positive fallback output token count: %s", stream.String())
		}
	})
}

func newResponsesSmokeExecutor(client *executorSmokeHTTPClient) *ResponsesExecutor {
	return &ResponsesExecutor{
		identity: newSmokeIdentity(), apiBase: "https://cc.test", client: client,
		apiKey: func() string { return "user_smoke" }, now: func() time.Time { return time.Unix(100, 0).UTC() },
		newID: func(prefix string) (string, error) { return prefix + "smoke", nil },
	}
}

func TestExecutorSmokeDoesNotSendDoneAfterTruncatedChatStream(t *testing.T) {
	chunks := make(chan pluginapi.HTTPStreamChunk, 1)
	chunks <- pluginapi.HTTPStreamChunk{Payload: []byte(`{"type":"text-delta","text":"partial"}` + "\n")}
	close(chunks)
	client := &executorSmokeHTTPClient{stream: pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: chunks}}
	executor := newChatCompletionsExecutor(chatExecutorConfig{apiBase: "https://cc.test", apiKey: func() string { return "user_smoke" }, identity: newSmokeIdentity()})
	response, err := executor.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{Model: "smoke", Format: "chat-completions", HTTPClient: client, Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var stream strings.Builder
	var streamErr error
	for chunk := range response.Chunks {
		stream.Write(chunk.Payload)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if strings.Contains(stream.String(), "[DONE]") || streamErr == nil {
		t.Fatalf("truncated stream was reported as successful: body=%s err=%v", stream.String(), streamErr)
	}
	var abiErr *pluginabi.Error
	if !errors.As(streamErr, &abiErr) || abiErr.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("stream error = %v, want 502 plugin error", streamErr)
	}
}

func TestCommandCodeHostHTTPClientSmoke(t *testing.T) {
	var methods []string
	reads := 0
	call := func(method string, payload []byte) ([]byte, error) {
		methods = append(methods, method)
		var result any
		switch method {
		case pluginabi.MethodHostHTTPDo:
			result = pluginapi.HTTPResponse{StatusCode: http.StatusCreated, Body: []byte("ok")}
		case pluginabi.MethodHostHTTPDoStream:
			result = chatHostHTTPStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-smoke"}
		case pluginabi.MethodHostHTTPStreamRead:
			reads++
			if reads == 1 {
				result = chatHostHTTPStreamReadResponse{Payload: []byte("part")}
			} else {
				result = chatHostHTTPStreamReadResponse{Done: true}
			}
		case pluginabi.MethodHostHTTPStreamClose:
			result = map[string]any{}
		default:
			return nil, errors.New("unexpected host method")
		}
		encoded, err := json.Marshal(pluginabi.Envelope{OK: true, Result: mustMarshalSmoke(t, result)})
		return encoded, err
	}
	client := NewCommandCodeHostHTTPClient(call, "callback-smoke")
	response, err := client.Do(context.Background(), pluginapi.HTTPRequest{Method: http.MethodPost, URL: "https://cc.test"})
	if err != nil || response.StatusCode != http.StatusCreated || string(response.Body) != "ok" {
		t.Fatalf("host HTTP Do() = %#v, %v", response, err)
	}
	stream, err := client.DoStream(context.Background(), pluginapi.HTTPRequest{Method: http.MethodPost, URL: "https://cc.test"})
	if err != nil {
		t.Fatalf("host HTTP DoStream() error = %v", err)
	}
	var body strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		body.Write(chunk.Payload)
	}
	if body.String() != "part" || len(methods) != 5 || methods[0] != pluginabi.MethodHostHTTPDo || methods[4] != pluginabi.MethodHostHTTPStreamClose {
		t.Fatalf("host bridge calls/body mismatch: methods=%v body=%q", methods, body.String())
	}
}

func TestCommandCodeHostHTTPClientStreamCancellationClosesHostStream(t *testing.T) {
	readStarted := make(chan struct{})
	allowReadReturn := make(chan struct{})
	closeCalled := make(chan struct{})
	reads := 0
	call := func(method string, payload []byte) ([]byte, error) {
		var result any
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			result = chatHostHTTPStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-cancel"}
		case pluginabi.MethodHostHTTPStreamRead:
			reads++
			if reads != 1 {
				return nil, errors.New("unexpected extra host stream read")
			}
			close(readStarted)
			<-allowReadReturn
			result = chatHostHTTPStreamReadResponse{Payload: []byte("part")}
		case pluginabi.MethodHostHTTPStreamClose:
			close(closeCalled)
			result = map[string]any{}
		default:
			return nil, errors.New("unexpected host method")
		}
		encoded, err := json.Marshal(pluginabi.Envelope{OK: true, Result: mustMarshalSmoke(t, result)})
		return encoded, err
	}

	client := NewCommandCodeHostHTTPClient(call, "callback-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.DoStream(ctx, pluginapi.HTTPRequest{Method: http.MethodPost, URL: "https://cc.test"})
	if err != nil {
		t.Fatalf("DoStream() error = %v", err)
	}
	<-readStarted
	cancel()
	close(allowReadReturn)

	var streamErr error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("stream error = %v, want context.Canceled", streamErr)
	}
	select {
	case <-closeCalled:
	case <-time.After(time.Second):
		t.Fatal("host HTTP stream was not closed after cancellation")
	}
}

func mustMarshalSmoke(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
