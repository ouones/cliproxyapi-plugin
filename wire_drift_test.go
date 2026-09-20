//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func TestVerifiedWireVersionMatchesCandidateFixture(t *testing.T) {
	const wantVersion = "1.58.0"

	raw, err := os.ReadFile("compatibility.json")
	if err != nil {
		t.Fatalf("read compatibility fixture: %v", err)
	}
	var manifest struct {
		CommandCode struct {
			WireVersion string `json:"wireVersion"`
		} `json:"commandCode"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode compatibility fixture: %v", err)
	}
	if manifest.CommandCode.WireVersion != wantVersion {
		t.Fatalf("compatibility fixture wire version = %q, want %q", manifest.CommandCode.WireVersion, wantVersion)
	}
	if verifiedCommandCodeWireVersion != wantVersion {
		t.Fatalf("verified wire version = %q, want %q", verifiedCommandCodeWireVersion, wantVersion)
	}
}

func TestWireDriftCheckLogsInSyncOrWarnsWithoutSwitchingWireVersion(t *testing.T) {
	marshalResponse := func(version string) []byte {
		body, err := json.Marshal(struct {
			Version string `json:"version"`
		}{Version: version})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(hostHTTPResponse{StatusCode: 200, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	testCases := []struct {
		name            string
		registryVersion string
		wantLevel       string
		wantMessage     string
		wantWarning     bool
	}{
		{
			name:            "registry matches verified version",
			registryVersion: "1.58.0",
			wantLevel:       "info",
			wantMessage:     "Command Code wire version in sync",
		},
		{
			name:            "registry reports a higher version",
			registryVersion: "1.59.0",
			wantLevel:       "warn",
			wantMessage:     "Command Code wire version drift detected; keeping the verified wire format",
			wantWarning:     true,
		},
		{
			name:            "registry reports a different version",
			registryVersion: "1.57.0",
			wantLevel:       "warn",
			wantMessage:     "Command Code wire version drift detected; keeping the verified wire format",
			wantWarning:     true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			originalLifecycle := commandCodePluginLifecycle
			testLifecycle := newPluginLifecycleState()
			commandCodePluginLifecycle = testLifecycle
			t.Cleanup(func() { commandCodePluginLifecycle = originalLifecycle })

			originalHostCall := wireDriftHostCall
			wireDriftHostCall = func(_ context.Context, payload []byte) ([]byte, error) {
				var request hostHTTPDoRequest
				if err := json.Unmarshal(payload, &request); err != nil {
					return nil, err
				}
				if request.Method != "GET" || request.URL != driftCheckURL {
					return nil, errors.New("wire drift request target changed")
				}
				accept := request.Headers["accept"]
				if len(accept) != 1 || accept[0] != "application/json" {
					return nil, errors.New("wire drift request headers changed")
				}
				return marshalResponse(testCase.registryVersion), nil
			}
			t.Cleanup(func() { wireDriftHostCall = originalHostCall })

			originalLog := wireDriftLog
			var logLevel, logMessage string
			var logFields map[string]any
			wireDriftLog = func(level, message string, fields map[string]any) {
				logLevel = level
				logMessage = message
				logFields = fields
			}
			t.Cleanup(func() { wireDriftLog = originalLog })

			checkWireDrift()
			if logLevel != testCase.wantLevel || logMessage != testCase.wantMessage {
				t.Fatalf("wire drift log = (%q, %q), want (%q, %q)", logLevel, logMessage, testCase.wantLevel, testCase.wantMessage)
			}
			if testCase.wantWarning {
				if logFields["implemented"] != verifiedCommandCodeWireVersion || logFields["latest"] != testCase.registryVersion || logFields["policy"] != "warn-only" {
					t.Fatalf("drift log fields = %#v", logFields)
				}
			} else {
				if logFields["version"] != verifiedCommandCodeWireVersion {
					t.Fatalf("in-sync log version = %#v, want %q", logFields["version"], verifiedCommandCodeWireVersion)
				}
				if _, ok := logFields["latest"]; ok {
					t.Fatalf("in-sync log unexpectedly contained drift fields: %#v", logFields)
				}
			}

			testLifecycle.mu.Lock()
			active := testLifecycle.active
			testLifecycle.mu.Unlock()
			if active != 0 {
				t.Fatalf("wire drift check left %d active lifecycle calls", active)
			}
		})
	}
}

func TestWireDriftCheckTimeoutReleasesLifecycle(t *testing.T) {
	originalLifecycle := commandCodePluginLifecycle
	testLifecycle := newPluginLifecycleState()
	commandCodePluginLifecycle = testLifecycle
	t.Cleanup(func() { commandCodePluginLifecycle = originalLifecycle })

	originalTimeout := wireDriftCheckTimeout
	wireDriftCheckTimeout = 20 * time.Millisecond
	t.Cleanup(func() { wireDriftCheckTimeout = originalTimeout })

	originalHostCall := wireDriftHostCall
	callbackStarted := make(chan struct{})
	callbackRelease := make(chan struct{})
	callbackReturned := make(chan struct{})
	var releaseOnce sync.Once
	wireDriftHostCall = func(_ context.Context, _ []byte) ([]byte, error) {
		close(callbackStarted)
		<-callbackRelease
		close(callbackReturned)
		return nil, errors.New("host callback released")
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(callbackRelease) })
		wireDriftHostCall = originalHostCall
	})

	go checkWireDrift()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("wire drift check did not start the host callback")
	}

	shutdownReturned := make(chan struct{})
	go func() {
		commandCodePluginShutdown()
		close(shutdownReturned)
	}()

	select {
	case <-shutdownReturned:
		t.Fatal("plugin shutdown returned while the wire drift host callback was still blocked")
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(callbackRelease) })
	select {
	case <-callbackReturned:
	case <-time.After(time.Second):
		t.Fatal("wire drift host callback goroutine did not return after release")
	}
	select {
	case <-shutdownReturned:
	case <-time.After(time.Second):
		t.Fatal("plugin shutdown did not return after the wire drift host callback was released")
	}

	testLifecycle.mu.Lock()
	active := testLifecycle.active
	testLifecycle.mu.Unlock()
	if active != 0 {
		t.Fatalf("wire drift check left %d active lifecycle calls", active)
	}
}

func TestWireDriftCheckResponseBranchesReleaseLifecycle(t *testing.T) {
	marshalResponse := func(response hostHTTPResponse) []byte {
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	testCases := []struct {
		name string
		raw  []byte
	}{
		{
			name: "success",
			raw:  marshalResponse(hostHTTPResponse{StatusCode: 200, Body: []byte(`{"version":"1.58.0"}`)}),
		},
		{
			name: "non-success status",
			raw:  marshalResponse(hostHTTPResponse{StatusCode: 503}),
		},
		{
			name: "invalid response JSON",
			raw:  []byte(`not-json`),
		},
		{
			name: "wire version drift",
			raw:  marshalResponse(hostHTTPResponse{StatusCode: 200, Body: []byte(`{"version":"1.59.0"}`)}),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			originalLifecycle := commandCodePluginLifecycle
			testLifecycle := newPluginLifecycleState()
			commandCodePluginLifecycle = testLifecycle
			t.Cleanup(func() { commandCodePluginLifecycle = originalLifecycle })

			originalHostCall := wireDriftHostCall
			callbackErr := make(chan error, 1)
			wireDriftHostCall = func(_ context.Context, payload []byte) ([]byte, error) {
				var request hostHTTPDoRequest
				err := json.Unmarshal(payload, &request)
				if err == nil && request.Method != "GET" {
					err = errors.New("wire drift request method changed")
				}
				if err == nil && request.URL != driftCheckURL {
					err = errors.New("wire drift request URL changed")
				}
				if err == nil {
					accept := request.Headers["accept"]
					if len(accept) != 1 || accept[0] != "application/json" {
						err = errors.New("wire drift request headers changed")
					}
				}
				callbackErr <- err
				return testCase.raw, nil
			}
			t.Cleanup(func() { wireDriftHostCall = originalHostCall })

			checkWireDrift()
			if err := <-callbackErr; err != nil {
				t.Fatalf("host callback failed: %v", err)
			}

			testLifecycle.mu.Lock()
			active := testLifecycle.active
			testLifecycle.mu.Unlock()
			if active != 0 {
				t.Fatalf("wire drift check left %d active lifecycle calls", active)
			}
		})
	}
}
