package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type commandCodeHostHTTPClient struct {
	call       func(string, []byte) ([]byte, error)
	callbackID string
}

type executorHostHTTPRequest struct {
	HostCallbackID string                     `json:"host_callback_id,omitempty"`
	Method         string                     `json:"method"`
	URL            string                     `json:"url"`
	Headers        map[string][]string        `json:"headers,omitempty"`
	Body           []byte                     `json:"body,omitempty"`
	WireProfile    *pluginapi.HTTPWireProfile `json:"wire_profile,omitempty"`
}

type executorHostHTTPResponse struct {
	StatusCode      int         `json:"status_code"`
	StatusCodeCamel int         `json:"StatusCode"`
	Headers         http.Header `json:"headers,omitempty"`
	HeadersCamel    http.Header `json:"Headers,omitempty"`
	Body            []byte      `json:"body,omitempty"`
	BodyCamel       []byte      `json:"Body,omitempty"`
}

type executorHostHTTPStreamResponse struct {
	StatusCode int                   `json:"status_code"`
	Headers    http.Header           `json:"headers,omitempty"`
	StreamID   string                `json:"stream_id,omitempty"`
	Chunks     []executorStreamChunk `json:"chunks,omitempty"`
}

type executorStreamChunk struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
}

type executorHostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type executorHostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type executorHostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type chatHostHTTPStreamResponse = executorHostHTTPStreamResponse
type chatHostHTTPStreamReadResponse = executorHostHTTPStreamReadResponse

// NewCommandCodeHostHTTPClient adapts the v7 host callbacks to the SDK's
// HostHTTPClient interface. It is also useful in protocol-level tests where
// the callback is replaced with a deterministic fake.
func NewCommandCodeHostHTTPClient(call func(string, []byte) ([]byte, error), callbackID string) pluginapi.HostHTTPClient {
	return &commandCodeHostHTTPClient{call: call, callbackID: callbackID}
}

func (c *commandCodeHostHTTPClient) Do(ctx context.Context, request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	ctx = contextOrBackground(ctx)
	if err := ctxErr(ctx); err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	callContext, cancel := context.WithTimeout(ctx, executorIdleTimeouts.nonStreaming)
	defer cancel()
	payload, err := json.Marshal(executorHostHTTPRequest{
		HostCallbackID: c.callbackID,
		Method:         request.Method,
		URL:            request.URL,
		Headers:        map[string][]string(request.Headers),
		Body:           append([]byte(nil), request.Body...),
		WireProfile:    request.WireProfile,
	})
	if err != nil {
		return pluginapi.HTTPResponse{}, err
	}
	raw, err := c.invokeContext(callContext, pluginabi.MethodHostHTTPDo, payload)
	if err != nil {
		return pluginapi.HTTPResponse{}, wrapIdleTimeout(ctx, executorIdleTimeouts.nonStreaming, err)
	}
	var response executorHostHTTPResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return pluginapi.HTTPResponse{}, fmt.Errorf("decode host HTTP response: %w", err)
	}
	if response.StatusCode == 0 {
		response.StatusCode = response.StatusCodeCamel
	}
	if response.Headers == nil {
		response.Headers = response.HeadersCamel
	}
	if len(response.Body) == 0 {
		response.Body = response.BodyCamel
	}
	return pluginapi.HTTPResponse{
		StatusCode: response.StatusCode,
		Headers:    response.Headers,
		Body:       append([]byte(nil), response.Body...),
	}, nil
}

func (c *commandCodeHostHTTPClient) DoStream(ctx context.Context, request pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	ctx = contextOrBackground(ctx)
	if err := ctxErr(ctx); err != nil {
		return pluginapi.HTTPStreamResponse{}, err
	}
	callContext, cancel := context.WithTimeout(ctx, executorIdleTimeouts.streaming)
	defer cancel()
	payload, err := json.Marshal(executorHostHTTPRequest{
		HostCallbackID: c.callbackID,
		Method:         request.Method,
		URL:            request.URL,
		Headers:        map[string][]string(request.Headers),
		Body:           append([]byte(nil), request.Body...),
		WireProfile:    request.WireProfile,
	})
	if err != nil {
		return pluginapi.HTTPStreamResponse{}, err
	}
	raw, err := c.invokeContext(callContext, pluginabi.MethodHostHTTPDoStream, payload)
	if err != nil {
		return pluginapi.HTTPStreamResponse{}, wrapIdleTimeout(ctx, executorIdleTimeouts.streaming, err)
	}
	var response executorHostHTTPStreamResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return pluginapi.HTTPStreamResponse{}, fmt.Errorf("decode host HTTP stream response: %w", err)
	}

	chunks := make(chan pluginapi.HTTPStreamChunk, len(response.Chunks)+1)
	for _, chunk := range response.Chunks {
		chunks <- pluginapi.HTTPStreamChunk{Payload: append([]byte(nil), chunk.Payload...), Err: errorFromString(chunk.Error)}
	}
	if response.StreamID == "" {
		close(chunks)
		return pluginapi.HTTPStreamResponse{StatusCode: response.StatusCode, Headers: response.Headers, Chunks: chunks}, nil
	}

	go c.readHostStream(ctx, response.StreamID, chunks)
	return pluginapi.HTTPStreamResponse{StatusCode: response.StatusCode, Headers: response.Headers, Chunks: chunks}, nil
}

func (c *commandCodeHostHTTPClient) readHostStream(ctx context.Context, streamID string, chunks chan<- pluginapi.HTTPStreamChunk) {
	ctx = contextOrBackground(ctx)
	defer close(chunks)
	defer func() {
		payload, err := json.Marshal(executorHostHTTPStreamCloseRequest{StreamID: streamID})
		if err == nil {
			_, _ = c.invoke(pluginabi.MethodHostHTTPStreamClose, payload)
		}
	}()

	idleDeadline := time.Now().Add(executorIdleTimeouts.streaming)
	for {
		if err := ctxErr(ctx); err != nil {
			sendHostHTTPStreamChunk(ctx, chunks, pluginapi.HTTPStreamChunk{Err: err})
			return
		}
		remaining := time.Until(idleDeadline)
		if remaining <= 0 {
			sendHostHTTPStreamChunk(ctx, chunks, pluginapi.HTTPStreamChunk{Err: fmt.Errorf("upstream idle timeout after %s: %w", executorIdleTimeouts.streaming, context.DeadlineExceeded)})
			return
		}
		payload, err := json.Marshal(executorHostHTTPStreamReadRequest{StreamID: streamID})
		if err != nil {
			sendHostHTTPStreamChunk(ctx, chunks, pluginapi.HTTPStreamChunk{Err: err})
			return
		}
		readContext, cancel := context.WithTimeout(ctx, remaining)
		raw, err := c.invokeContext(readContext, pluginabi.MethodHostHTTPStreamRead, payload)
		cancel()
		if err != nil {
			sendHostHTTPStreamChunk(ctx, chunks, pluginapi.HTTPStreamChunk{Err: wrapIdleTimeout(ctx, executorIdleTimeouts.streaming, err)})
			return
		}
		var response executorHostHTTPStreamReadResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			sendHostHTTPStreamChunk(ctx, chunks, pluginapi.HTTPStreamChunk{Err: fmt.Errorf("decode host HTTP stream chunk: %w", err)})
			return
		}
		if response.Error != "" {
			sendHostHTTPStreamChunk(ctx, chunks, pluginapi.HTTPStreamChunk{Err: errors.New(response.Error)})
			return
		}
		if len(response.Payload) > 0 {
			if !sendHostHTTPStreamChunk(ctx, chunks, pluginapi.HTTPStreamChunk{Payload: append([]byte(nil), response.Payload...)}) {
				return
			}
			idleDeadline = time.Now().Add(executorIdleTimeouts.streaming)
		}
		if response.Done {
			return
		}
	}
}

// The host callback ABI is synchronous and has no context parameter. Keep the
// callback wait cancellable; stream callers close the host-owned stream when
// the wait ends through cancellation or the idle deadline.
func (c *commandCodeHostHTTPClient) invokeContext(ctx context.Context, method string, payload []byte) ([]byte, error) {
	return invokeBoundedHostCallback(ctx, func() ([]byte, error) {
		return c.invoke(method, payload)
	})
}

func (c *commandCodeHostHTTPClient) invoke(method string, payload []byte) ([]byte, error) {
	if c == nil || c.call == nil {
		return nil, errors.New("host HTTP callback is unavailable")
	}
	raw, err := c.call(method, payload)
	if err != nil {
		return nil, err
	}
	var envelope pluginabi.Envelope
	if json.Unmarshal(raw, &envelope) == nil && (envelope.OK || envelope.Error != nil) {
		if !envelope.OK {
			if envelope.Error == nil {
				return nil, errors.New("host callback returned an error")
			}
			return nil, envelope.Error
		}
		return append([]byte(nil), envelope.Result...), nil
	}
	return raw, nil
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func wrapIdleTimeout(parent context.Context, timeout time.Duration, err error) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || errors.Is(ctxErr(parent), context.Canceled) {
		return err
	}
	return fmt.Errorf("upstream idle timeout after %s: %w", timeout, err)
}

func sendHostHTTPStreamChunk(ctx context.Context, chunks chan<- pluginapi.HTTPStreamChunk, chunk pluginapi.HTTPStreamChunk) bool {
	select {
	case chunks <- chunk:
		return true
	case <-ctx.Done():
		select {
		case chunks <- chunk:
			return true
		default:
			return false
		}
	}
}

func errorFromString(message string) error {
	if message == "" {
		return nil
	}
	return errors.New(message)
}

type identityHostHTTPClient struct {
	client pluginapi.HostHTTPClient
}

func (c identityHostHTTPClient) Do(ctx context.Context, request identityHTTPRequest) (identityHTTPResponse, error) {
	if c.client == nil {
		return identityHTTPResponse{}, errors.New("host HTTP client is unavailable")
	}
	response, err := c.client.Do(ctx, pluginapi.HTTPRequest{
		Method:  request.Method,
		URL:     request.URL,
		Headers: http.Header(request.Headers),
		Body:    append([]byte(nil), request.Body...),
	})
	return identityHTTPResponse{StatusCode: response.StatusCode}, err
}
