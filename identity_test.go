package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fingerprintGoldenInput struct {
	Salt   string `json:"salt"`
	APIKey string `json:"apiKey"`
}

type fingerprintGoldenCase struct {
	Salt     string            `json:"salt"`
	APIKey   string            `json:"apiKey"`
	Expected deviceFingerprint `json:"expected"`
}

type fingerprintGoldenFixture struct {
	Primary        fingerprintGoldenCase  `json:"primary"`
	IsolatedAPIKey fingerprintGoldenInput `json:"isolatedApiKey"`
	IsolatedSalt   fingerprintGoldenInput `json:"isolatedSalt"`
}

func readFingerprintGoldenFixture(t *testing.T) fingerprintGoldenFixture {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate identity_test.go")
	}
	path := filepath.Join(filepath.Dir(sourceFile), "testdata", "commandcode-fingerprint-golden.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fingerprint golden fixture: %v", err)
	}
	var fixture fingerprintGoldenFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fingerprint golden fixture: %v", err)
	}
	return fixture
}

func assertFingerprintFields(t *testing.T, got, want deviceFingerprint) {
	t.Helper()
	if got.Thumbmark != want.Thumbmark {
		t.Errorf("thumbmark = %q, want %q", got.Thumbmark, want.Thumbmark)
	}
	if got.Components.MachineIDHash != want.Components.MachineIDHash {
		t.Errorf("machineIdHash = %q, want %q", got.Components.MachineIDHash, want.Components.MachineIDHash)
	}
	if !reflect.DeepEqual(got.Components.MACHashes, want.Components.MACHashes) {
		t.Errorf("macHashes = %#v, want %#v", got.Components.MACHashes, want.Components.MACHashes)
	}
	if got.Components.OSUserHash != want.Components.OSUserHash {
		t.Errorf("osUserHash = %q, want %q", got.Components.OSUserHash, want.Components.OSUserHash)
	}
	if got.Components.HostnameHash != want.Components.HostnameHash {
		t.Errorf("hostnameHash = %q, want %q", got.Components.HostnameHash, want.Components.HostnameHash)
	}
	if got.Components.GitEmailHash != want.Components.GitEmailHash {
		t.Errorf("gitEmailHash = %q, want %q", got.Components.GitEmailHash, want.Components.GitEmailHash)
	}
	if got.Components.Platform != want.Components.Platform {
		t.Errorf("platform = %q, want %q", got.Components.Platform, want.Components.Platform)
	}
	if got.Components.Arch != want.Components.Arch {
		t.Errorf("arch = %q, want %q", got.Components.Arch, want.Components.Arch)
	}
	if got.Components.OSRelease != want.Components.OSRelease {
		t.Errorf("osRelease = %q, want %q", got.Components.OSRelease, want.Components.OSRelease)
	}
	if got.Components.CPUModel != want.Components.CPUModel {
		t.Errorf("cpuModel = %q, want %q", got.Components.CPUModel, want.Components.CPUModel)
	}
	if got.Components.CPUCount != want.Components.CPUCount {
		t.Errorf("cpuCount = %d, want %d", got.Components.CPUCount, want.Components.CPUCount)
	}
	if got.Components.MemGiB != want.Components.MemGiB {
		t.Errorf("memGiB = %d, want %d", got.Components.MemGiB, want.Components.MemGiB)
	}
	if got.Components.IsContainer != want.Components.IsContainer {
		t.Errorf("isContainer = %t, want %t", got.Components.IsContainer, want.Components.IsContainer)
	}
	if got.Components.Timezone != want.Components.Timezone {
		t.Errorf("timezone = %q, want %q", got.Components.Timezone, want.Components.Timezone)
	}
	if got.Components.Runtime != want.Components.Runtime {
		t.Errorf("runtime = %q, want %q", got.Components.Runtime, want.Components.Runtime)
	}
	if got.Components.CollectorVersion != want.Components.CollectorVersion {
		t.Errorf("collectorVersion = %d, want %d", got.Components.CollectorVersion, want.Components.CollectorVersion)
	}
}

func TestFingerprintIsStableAndIsolatedByAPIKeyAndSalt(t *testing.T) {
	fixture := readFingerprintGoldenFixture(t)
	store := newIdentityStateStore(identityStateConfig{
		fingerprintSalt: fixture.Primary.Salt,
		cleanupInterval: -1,
	})

	first, err := store.fingerprintFor(fixture.Primary.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.fingerprintFor(fixture.Primary.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, repeated) {
		t.Fatalf("same API key produced different fingerprints: %#v != %#v", first, repeated)
	}

	otherKey, err := store.fingerprintFor(fixture.IsolatedAPIKey.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(first, otherKey) {
		t.Fatal("different API keys shared a fingerprint")
	}
	if first.Thumbmark == otherKey.Thumbmark {
		t.Fatal("different API keys shared a thumbmark")
	}
	if len(first.Components.MACHashes) < 2 || len(first.Components.MACHashes) > 5 {
		t.Fatalf("MAC hash count = %d, want 2..5", len(first.Components.MACHashes))
	}

	otherSalt := newIdentityStateStore(identityStateConfig{
		fingerprintSalt: fixture.IsolatedSalt.Salt,
		cleanupInterval: -1,
	})
	changedSalt, err := otherSalt.fingerprintFor(fixture.IsolatedSalt.APIKey)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(first, changedSalt) {
		t.Fatal("changing fingerprint salt did not change the fingerprint")
	}
	if first.Thumbmark == changedSalt.Thumbmark {
		t.Fatal("changing fingerprint salt did not change the thumbmark")
	}
}

func TestFingerprintMatchesTheVerifiedWireFixture(t *testing.T) {
	fixture := readFingerprintGoldenFixture(t)
	got := generateDeviceFingerprint(fixture.Primary.Salt, fixture.Primary.APIKey)
	assertFingerprintFields(t, got, fixture.Primary.Expected)
}

func TestSessionIsReusedPerAPIKeyUntilItExpires(t *testing.T) {
	now := time.Unix(100, 0)
	sequence := 0
	store := newIdentityStateStore(identityStateConfig{
		now:            func() time.Time { return now },
		randomDuration: func(time.Duration) time.Duration { return 0 },
		newSessionID: func() (string, error) {
			sequence++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", sequence), nil
		},
		cleanupInterval: -1,
	})

	first, err := store.sessionID(nil, "user_alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	reused, err := store.sessionID(nil, "user_alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	if first != reused {
		t.Fatalf("same API key did not reuse session: %q != %q", first, reused)
	}

	other, err := store.sessionID(nil, "user_beta", "")
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("different API keys shared a session")
	}

	now = now.Add(commandCodeSessionDuration)
	expired, err := store.sessionID(nil, "user_alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	if expired == first {
		t.Fatal("expired session was reused")
	}
}

func TestSessionSelectionKeepsExistingPriority(t *testing.T) {
	store := newIdentityStateStore(identityStateConfig{
		newSessionID:    func() (string, error) { return "internal-session", nil },
		cleanupInterval: -1,
	})

	got, err := store.sessionID(map[string][]string{
		"x-session-id":             {"explicit1"},
		"x-claude-code-session-id": {"explicit2"},
		"session_id":               {"explicit3"},
	}, "user_alpha", "prompt-cache")
	if err != nil {
		t.Fatal(err)
	}
	if got != "explicit1" {
		t.Fatalf("highest-priority session = %q, want explicit1", got)
	}

	got, err = store.sessionID(map[string][]string{
		"x-session-id":             {"short"},
		"x-claude-code-session-id": {"explicit2"},
		"session_id":               {"explicit3"},
	}, "user_beta", "prompt-cache")
	if err != nil {
		t.Fatal(err)
	}
	if got != "explicit2" {
		t.Fatalf("fallback session = %q, want explicit2", got)
	}

	got, err = store.sessionID(nil, "user_gamma", "prompt-cache")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prompt-cache" {
		t.Fatalf("prompt cache session = %q, want prompt-cache", got)
	}

	got, err = store.sessionID(nil, "user_delta", "short")
	if err != nil {
		t.Fatal(err)
	}
	if got != "internal-session" {
		t.Fatalf("internal session = %q, want internal-session", got)
	}
}

type fakeIdentityHTTPClient struct {
	mu            sync.Mutex
	requests      []identityHTTPRequest
	statusByURL   map[string]int
	defaultStatus int
}

func (f *fakeIdentityHTTPClient) Do(_ context.Context, request identityHTTPRequest) (identityHTTPResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, identityHTTPRequest{
		Method: request.Method, URL: request.URL,
		Headers: cloneHeaders(request.Headers), Body: append([]byte(nil), request.Body...),
	})
	status := f.defaultStatus
	if status == 0 {
		status = 200
	}
	if configured, ok := f.statusByURL[request.URL]; ok {
		status = configured
	}
	f.mu.Unlock()
	return identityHTTPResponse{StatusCode: status}, nil
}

func (f *fakeIdentityHTTPClient) requestSnapshot() []identityHTTPRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]identityHTTPRequest, len(f.requests))
	copy(result, f.requests)
	return result
}

func TestInitializationIsPerKeyAndFailureIsSoftAndRetryable(t *testing.T) {
	now := time.Unix(200, 0)
	store := newIdentityStateStore(identityStateConfig{
		apiBase:         "https://command-code.test/",
		now:             func() time.Time { return now },
		randomDuration:  func(time.Duration) time.Duration { return 0 },
		newLifecycleID:  func() (string, error) { return "sess_fixture", nil },
		cleanupInterval: -1,
	})
	client := &fakeIdentityHTTPClient{}
	var logs []string
	logger := func(_ string, message string, fields map[string]any) {
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("marshal log fields: %v", err)
		}
		logs = append(logs, message+" "+string(encoded))
	}

	store.ensureInitialized(context.Background(), "user_alpha_secret", client, logger)
	initialRequests := client.requestSnapshot()
	if got := len(initialRequests); got != 2 {
		t.Fatalf("first initialization sent %d requests, want 2", got)
	}
	fingerprint, err := store.fingerprintFor("user_alpha_secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range initialRequests {
		if request.Headers["Authorization"][0] != "Bearer user_alpha_secret" {
			t.Fatalf("initialization did not use the configured API key: %#v", request.Headers)
		}
		if request.Headers["x-command-code-version"][0] != verifiedCommandCodeWireVersion {
			t.Fatalf("initialization used unexpected wire version: %#v", request.Headers)
		}
		switch {
		case strings.HasSuffix(request.URL, "/alpha/fingerprint/record"):
			var got deviceFingerprint
			if err := json.Unmarshal(request.Body, &got); err != nil {
				t.Fatalf("decode fingerprint request: %v", err)
			}
			if !reflect.DeepEqual(got, fingerprint) {
				t.Fatal("fingerprint request did not use the per-key fingerprint")
			}
		case strings.HasSuffix(request.URL, "/alpha/lifecycle-events"):
			var got struct {
				EventType string `json:"eventType"`
				Metadata  struct {
					SessionID string `json:"sessionId"`
					OS        string `json:"os"`
				} `json:"metadata"`
			}
			if err := json.Unmarshal(request.Body, &got); err != nil {
				t.Fatalf("decode lifecycle request: %v", err)
			}
			if got.EventType != "cli_session_exists" || got.Metadata.SessionID != "sess_fixture" || got.Metadata.OS != "win32-x64" {
				t.Fatalf("unexpected lifecycle request: %#v", got)
			}
		}
	}

	store.ensureInitialized(context.Background(), "user_alpha_secret", client, logger)
	if got := len(client.requestSnapshot()); got != 2 {
		t.Fatalf("initialization was not throttled, request count = %d", got)
	}
	store.ensureInitialized(context.Background(), "user_beta_secret", client, logger)
	if got := len(client.requestSnapshot()); got != 4 {
		t.Fatalf("second API key initialization sent %d total requests, want 4", got)
	}

	client.statusByURL = map[string]int{"https://command-code.test/alpha/fingerprint/record": 500}
	now = now.Add(commandCodeInitRefresh)
	store.ensureInitialized(context.Background(), "user_alpha_secret", client, logger)
	if got := len(client.requestSnapshot()); got != 6 {
		t.Fatalf("failed initialization sent %d total requests, want 6", got)
	}
	now = now.Add(time.Nanosecond)
	store.ensureInitialized(context.Background(), "user_alpha_secret", client, logger)
	if got := len(client.requestSnapshot()); got != 8 {
		t.Fatalf("failed initialization was not retried, request count = %d", got)
	}
	for _, logLine := range logs {
		if strings.Contains(logLine, "user_alpha_secret") {
			t.Fatalf("full API key appeared in log: %s", logLine)
		}
	}
}

func TestResetDropsSessionButRecreatesTheSameFingerprint(t *testing.T) {
	now := time.Unix(300, 0)
	sequence := 0
	store := newIdentityStateStore(identityStateConfig{
		fingerprintSalt: "restart-salt",
		now:             func() time.Time { return now },
		randomDuration:  func(time.Duration) time.Duration { return 0 },
		newSessionID: func() (string, error) {
			sequence++
			return fmt.Sprintf("session-%d", sequence), nil
		},
		cleanupInterval: -1,
	})

	before, err := store.fingerprintFor("user_alpha")
	if err != nil {
		t.Fatal(err)
	}
	oldSession, err := store.sessionID(nil, "user_alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	store.reset()
	newSession, err := store.sessionID(nil, "user_alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.fingerprintFor("user_alpha")
	if err != nil {
		t.Fatal(err)
	}
	if oldSession == newSession {
		t.Fatal("reset reused the old session")
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reset changed the deterministic device fingerprint")
	}
}

func TestCleanupRemovesInactiveIdentityState(t *testing.T) {
	now := time.Unix(400, 0)
	store := newIdentityStateStore(identityStateConfig{
		now:             func() time.Time { return now },
		randomDuration:  func(time.Duration) time.Duration { return 0 },
		sessionDuration: time.Hour,
		initRefresh:     time.Hour,
		retention:       2 * time.Hour,
		cleanupInterval: -1,
	})
	if _, err := store.sessionID(nil, "user_alpha", ""); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Hour)
	if cleaned := store.cleanupExpired(now); cleaned != 1 {
		t.Fatalf("cleaned %d states, want 1", cleaned)
	}
}
