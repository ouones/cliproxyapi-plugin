package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestExecutorIdleTimeoutReleasesLifecycle(t *testing.T) {
	t.Run("non-stream host call observes cancellation", func(t *testing.T) {
		previousTimeouts := executorIdleTimeouts
		executorIdleTimeouts.nonStreaming = 20 * time.Millisecond
		t.Cleanup(func() { executorIdleTimeouts = previousTimeouts })

		started := make(chan struct{})
		allowReturn := make(chan struct{})
		var startOnce sync.Once
		call := func(method string, _ []byte) ([]byte, error) {
			if method != pluginabi.MethodHostHTTPDo {
				return nil, errors.New("unexpected host method")
			}
			startOnce.Do(func() { close(started) })
			<-allowReturn
			return nil, errors.New("late host response")
		}

		client := NewCommandCodeHostHTTPClient(call, "callback-id")
		state := newPluginLifecycleState()
		release, err := state.begin()
		if err != nil {
			t.Fatal(err)
		}
		defer close(allowReturn)

		finished := make(chan error, 1)
		go func() {
			_, callErr := client.Do(context.Background(), pluginapi.HTTPRequest{Method: http.MethodGet, URL: "https://cc.test"})
			release()
			finished <- callErr
		}()

		<-started
		select {
		case <-finished:
		case <-time.After(250 * time.Millisecond):
			t.Fatal("non-stream host call did not stop after the idle deadline")
		}
		release()

		waitForLifecycleDrain(t, state)
	})

	t.Run("stream idle closes host stream", func(t *testing.T) {
		previousTimeouts := executorIdleTimeouts
		executorIdleTimeouts.streaming = 20 * time.Millisecond
		t.Cleanup(func() { executorIdleTimeouts = previousTimeouts })

		readStarted := make(chan struct{})
		allowReadReturn := make(chan struct{})
		closeCalled := make(chan struct{})
		var startOnce sync.Once
		var closeOnce sync.Once
		call := func(method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodHostHTTPDoStream:
				return hostBridgeResult(t, chatHostHTTPStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-id"}), nil
			case pluginabi.MethodHostHTTPStreamRead:
				startOnce.Do(func() { close(readStarted) })
				<-allowReadReturn
				return hostBridgeResult(t, chatHostHTTPStreamReadResponse{Payload: []byte("late")}), nil
			case pluginabi.MethodHostHTTPStreamClose:
				closeOnce.Do(func() { close(closeCalled) })
				return hostBridgeResult(t, map[string]any{}), nil
			default:
				return nil, errors.New("unexpected host method")
			}
		}

		client := NewCommandCodeHostHTTPClient(call, "callback-id")
		state := newPluginLifecycleState()
		release, err := state.begin()
		if err != nil {
			t.Fatal(err)
		}
		defer close(allowReadReturn)

		stream, err := client.DoStream(context.Background(), pluginapi.HTTPRequest{Method: http.MethodGet, URL: "https://cc.test"})
		if err != nil {
			t.Fatal(err)
		}
		<-readStarted
		finished := make(chan struct{})
		go func() {
			for range stream.Chunks {
			}
			release()
			close(finished)
		}()

		select {
		case <-finished:
		case <-time.After(250 * time.Millisecond):
			t.Fatal("stream host read did not stop after the idle deadline")
		}
		select {
		case <-closeCalled:
		case <-time.After(250 * time.Millisecond):
			t.Fatal("host stream was not closed after the idle deadline")
		}
		release()

		waitForLifecycleDrain(t, state)
	})

	t.Run("explicit cancellation closes stream and releases once", func(t *testing.T) {
		previousTimeouts := executorIdleTimeouts
		executorIdleTimeouts.streaming = time.Hour
		t.Cleanup(func() { executorIdleTimeouts = previousTimeouts })

		readStarted := make(chan struct{})
		allowReadReturn := make(chan struct{})
		closeCalled := make(chan struct{})
		var startOnce sync.Once
		var closeOnce sync.Once
		call := func(method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodHostHTTPDoStream:
				return hostBridgeResult(t, chatHostHTTPStreamResponse{StatusCode: http.StatusOK, StreamID: "stream-cancel"}), nil
			case pluginabi.MethodHostHTTPStreamRead:
				startOnce.Do(func() { close(readStarted) })
				<-allowReadReturn
				return hostBridgeResult(t, chatHostHTTPStreamReadResponse{Payload: []byte("late")}), nil
			case pluginabi.MethodHostHTTPStreamClose:
				closeOnce.Do(func() { close(closeCalled) })
				return hostBridgeResult(t, map[string]any{}), nil
			default:
				return nil, errors.New("unexpected host method")
			}
		}

		client := NewCommandCodeHostHTTPClient(call, "callback-id")
		state := newPluginLifecycleState()
		release, err := state.begin()
		if err != nil {
			t.Fatal(err)
		}
		defer close(allowReadReturn)

		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.DoStream(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: "https://cc.test"})
		if err != nil {
			t.Fatal(err)
		}
		<-readStarted
		cancel()
		finished := make(chan struct{})
		go func() {
			for range stream.Chunks {
			}
			release()
			close(finished)
		}()

		select {
		case <-finished:
		case <-time.After(250 * time.Millisecond):
			t.Fatal("stream host read did not observe explicit cancellation")
		}
		select {
		case <-closeCalled:
		case <-time.After(250 * time.Millisecond):
			t.Fatal("host stream was not closed after explicit cancellation")
		}
		release()

		waitForLifecycleDrain(t, state)
	})
}

func TestBridgeExecutorStreamEmitFailureCancelsProducerAndReleasesLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const emitError = "host stream emit failed"
	var callsMu sync.Mutex
	var emitted []hostStreamEmitRequest
	var closes []hostStreamCloseRequest
	originalHostCall := executorStreamHostCall
	executorStreamHostCall = func(method string, payload []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostStreamEmit:
			var request hostStreamEmitRequest
			if err := json.Unmarshal(payload, &request); err != nil {
				return nil, err
			}
			callsMu.Lock()
			emitted = append(emitted, request)
			emitCount := len(emitted)
			callsMu.Unlock()
			if emitCount == 1 {
				return nil, errors.New(emitError)
			}
			return nil, errors.New("unexpected stream emit after failure")
		case pluginabi.MethodHostStreamClose:
			var request hostStreamCloseRequest
			if err := json.Unmarshal(payload, &request); err != nil {
				return nil, err
			}
			callsMu.Lock()
			closes = append(closes, request)
			callsMu.Unlock()
			return nil, nil
		default:
			return nil, errors.New("unexpected host method: " + method)
		}
	}
	t.Cleanup(func() { executorStreamHostCall = originalHostCall })

	chunks := make(chan pluginapi.ExecutorStreamChunk, 8)
	producerReady := make(chan struct{})
	producerDone := make(chan struct{})
	payloadFor := func(index int) []byte {
		switch index {
		case 0:
			return []byte("data: first\n\n")
		case 1:
			return []byte("data: message_stop\n\n")
		case 2:
			return []byte("data: [DONE]\n\n")
		case 3:
			return []byte("event: response.completed\n\n")
		default:
			return []byte("data: trailing\n\n")
		}
	}
	go func() {
		defer close(chunks)
		defer close(producerDone)
		for index := 0; index < cap(chunks); index++ {
			chunks <- pluginapi.ExecutorStreamChunk{Payload: payloadFor(index)}
		}
		close(producerReady)
		for index := cap(chunks); index < cap(chunks)+16; index++ {
			select {
			case chunks <- pluginapi.ExecutorStreamChunk{Payload: payloadFor(index)}:
			case <-ctx.Done():
				return
			}
		}
	}()
	<-producerReady

	lifecycle := newPluginLifecycleState()
	release, err := lifecycle.begin()
	if err != nil {
		t.Fatal(err)
	}
	bridgeDone := make(chan struct{})
	go func() {
		defer release()
		bridgeExecutorStream(ctx, cancel, "stream-id", chunks)
		close(bridgeDone)
	}()

	select {
	case <-bridgeDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("stream bridge did not return after host emit failure")
	}
	select {
	case <-producerDone:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("translator producer remained blocked after host emit failure")
	}

	callsMu.Lock()
	emittedCopy := append([]hostStreamEmitRequest(nil), emitted...)
	closesCopy := append([]hostStreamCloseRequest(nil), closes...)
	callsMu.Unlock()
	if len(emittedCopy) != 1 {
		t.Fatalf("host stream emit calls = %d, want 1", len(emittedCopy))
	}
	if string(emittedCopy[0].Payload) != "data: first\n\n" {
		t.Fatalf("first host stream payload = %q", emittedCopy[0].Payload)
	}
	if len(closesCopy) != 1 {
		t.Fatalf("host stream close calls = %d, want 1", len(closesCopy))
	}
	if closesCopy[0].Error != emitError {
		t.Fatalf("host stream close error = %q, want %q", closesCopy[0].Error, emitError)
	}

	waitForLifecycleDrain(t, lifecycle)
}

func hostBridgeResult(t *testing.T, value any) []byte {
	t.Helper()
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(pluginabi.Envelope{OK: true, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func waitForLifecycleDrain(t *testing.T, state *pluginLifecycleState) {
	t.Helper()
	drained := make(chan struct{})
	go func() {
		state.quiesceAndWait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("plugin lifecycle did not drain after executor completion")
	}
	state.resume()
}
