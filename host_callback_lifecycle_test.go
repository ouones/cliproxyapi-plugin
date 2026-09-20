//go:build cgo

package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestSynchronousHostCallbackTimeoutsStayBoundedAndShutdownWaits(t *testing.T) {
	previousLifecycle := commandCodePluginLifecycle
	commandCodePluginLifecycle = newPluginLifecycleState()
	t.Cleanup(func() { commandCodePluginLifecycle = previousLifecycle })

	previousTimeouts := executorIdleTimeouts
	executorIdleTimeouts.nonStreaming = 5 * time.Millisecond
	t.Cleanup(func() { executorIdleTimeouts = previousTimeouts })

	callbackStarted := make(chan struct{})
	callbackRelease := make(chan struct{})
	var releaseOnce sync.Once
	var callbackCount atomic.Int32
	var startOnce sync.Once
	call := func(method string, _ []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return nil, errors.New("unexpected host method")
		}
		callbackCount.Add(1)
		startOnce.Do(func() { close(callbackStarted) })
		<-callbackRelease
		return nil, errors.New("host callback released")
	}
	releaseCallback := func() { releaseOnce.Do(func() { close(callbackRelease) }) }
	t.Cleanup(releaseCallback)

	client := NewCommandCodeHostHTTPClient(call, "callback-id")
	request := pluginapi.HTTPRequest{Method: http.MethodGet, URL: "https://cc.test"}
	firstCallDone := make(chan struct{})
	go func() {
		defer close(firstCallDone)
		_, _ = client.Do(context.Background(), request)
	}()
	<-callbackStarted

	const attempts = 16
	for attempt := 0; attempt < attempts; attempt++ {
		_, err := client.Do(context.Background(), request)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("attempt %d error = %v, want a deadline error", attempt, err)
		}
	}
	if got := callbackCount.Load(); got > 8 {
		t.Errorf("started host callbacks = %d, want at most 8 bounded callback slots", got)
	}

	shutdownReturned := make(chan struct{})
	go func() {
		commandCodePluginShutdown()
		close(shutdownReturned)
	}()
	select {
	case <-shutdownReturned:
		t.Error("plugin shutdown returned while a host callback was still blocked")
	case <-time.After(50 * time.Millisecond):
	}

	releaseCallback()
	select {
	case <-shutdownReturned:
	case <-time.After(time.Second):
		t.Fatal("plugin shutdown did not return after the host callback was released")
	}
	select {
	case <-firstCallDone:
	case <-time.After(time.Second):
		t.Fatal("initial host callback caller did not return after release")
	}
}
