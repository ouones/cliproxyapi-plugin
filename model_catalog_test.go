package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type fakeModelCatalogHTTPClient struct {
	response pluginapi.HTTPResponse
	err      error
	request  pluginapi.HTTPRequest
	wait     bool
}

func (f *fakeModelCatalogHTTPClient) Do(ctx context.Context, request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	f.request = request
	if f.wait {
		<-ctx.Done()
		return pluginapi.HTTPResponse{}, ctx.Err()
	}
	return f.response, f.err
}

func (f *fakeModelCatalogHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, errors.New("streaming is not used for model catalog requests")
}

func TestModelRegistrationUsesLiveCatalogAndDeduplicatesIDs(t *testing.T) {
	t.Setenv("CC_API_BASE", "https://command-code.test/")
	const apiKey = "user_catalog_secret"
	lifecycle, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("api-key: " + apiKey + "\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyLifecycleRequest(lifecycle); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clearCredentialState)

	client := &fakeModelCatalogHTTPClient{response: pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body: []byte(`{"object":"list","data":[
			{"id":"deepseek/deepseek-v4.1-flash","object":"model","owned_by":"command-code","name":"DeepSeek V4.1 Flash","context_length":1000000},
			{"id":"duplicate","name":"first","context_length":128},
			{"id":"duplicate","name":"second","context_length":256}
		]}`),
	}}

	registration := modelRegistrationContext(context.Background(), client)
	if registration.Provider != pluginProvider {
		t.Fatalf("provider = %q, want %q", registration.Provider, pluginProvider)
	}
	if len(registration.Models) != 2 {
		t.Fatalf("model count = %d, want 2", len(registration.Models))
	}
	if got := registration.Models[0]; got.ID != "deepseek/deepseek-v4.1-flash" || got.Name != got.ID || got.DisplayName != "DeepSeek V4.1 Flash" || got.ContextLength != 1000000 {
		t.Fatalf("live model was not mapped completely: %#v", got)
	}
	if got := registration.Models[1]; got.ID != "duplicate" || got.DisplayName != "first" || got.ContextLength != 128 {
		t.Fatalf("duplicate ID was not deterministically deduplicated: %#v", got)
	}

	if client.request.Method != http.MethodGet || client.request.URL != "https://command-code.test/provider/v1/models" {
		t.Fatalf("unexpected model catalog request: %#v", client.request)
	}
	if got := client.request.Headers.Get("Authorization"); got != "Bearer "+apiKey {
		t.Fatalf("authorization header = %q", got)
	}
	if got := client.request.Headers["x-command-code-version"]; len(got) != 1 || got[0] != verifiedCommandCodeWireVersion {
		t.Fatalf("command code version header = %#v", got)
	}

	encoded, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || containsSecret(string(encoded), apiKey) {
		t.Fatalf("model registration exposed API key: %s", encoded)
	}

	snapshot := modelRegistration()
	if !reflect.DeepEqual(snapshot.Models, registration.Models) {
		t.Fatalf("successful live catalog was not persisted: got %#v, want %#v", snapshot.Models, registration.Models)
	}
}

func TestPluginReconfigureRefreshesAndReplacesCompleteModelSnapshot(t *testing.T) {
	t.Setenv("CC_API_BASE", "https://command-code.test")
	clearCredentialState()
	t.Cleanup(clearCredentialState)

	client := &fakeModelCatalogHTTPClient{response: pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"data":[{"id":"kept"},{"id":"removed"}]}`),
	}}
	previousClientFactory := newCommandCodeModelCatalogHTTPClient
	newCommandCodeModelCatalogHTTPClient = func() pluginapi.HostHTTPClient { return client }
	t.Cleanup(func() { newCommandCodeModelCatalogHTTPClient = previousClientFactory })

	request, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("api-key: user_catalog_secret\n")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleMethodContext(context.Background(), pluginabi.MethodPluginReconfigure, request); err != nil {
		t.Fatalf("plugin.reconfigure failed: %v", err)
	}

	client.response = pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"data":[{"id":"kept"},{"id":"added"}]}`),
	}
	if _, err := handleMethodContext(context.Background(), pluginabi.MethodPluginReconfigure, request); err != nil {
		t.Fatalf("second plugin.reconfigure failed: %v", err)
	}

	raw, err := handleMethodContext(context.Background(), pluginabi.MethodModelRegister, nil)
	if err != nil {
		t.Fatalf("model.register failed: %v", err)
	}
	var result envelope
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	var registration pluginapi.ModelRegistrationResponse
	if err := json.Unmarshal(result.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if got := modelIDs(registration.Models); !reflect.DeepEqual(got, []string{"kept", "added"}) {
		t.Fatalf("model.register returned an incremental or stale catalog: %v", got)
	}
}

func TestPluginReconfigureKeepsLastValidModelSnapshotAfterFailure(t *testing.T) {
	t.Setenv("CC_API_BASE", "https://command-code.test")
	clearCredentialState()
	t.Cleanup(clearCredentialState)

	client := &fakeModelCatalogHTTPClient{response: pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"data":[{"id":"last-valid"}]}`),
	}}
	previousClientFactory := newCommandCodeModelCatalogHTTPClient
	newCommandCodeModelCatalogHTTPClient = func() pluginapi.HostHTTPClient { return client }
	t.Cleanup(func() { newCommandCodeModelCatalogHTTPClient = previousClientFactory })

	request, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("api-key: user_catalog_secret\n")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleMethodContext(context.Background(), pluginabi.MethodPluginReconfigure, request); err != nil {
		t.Fatalf("initial plugin.reconfigure failed: %v", err)
	}
	client.response = pluginapi.HTTPResponse{StatusCode: http.StatusBadGateway, Body: []byte(`{"data":[{"id":"bad"}]}`)}
	if _, err := handleMethodContext(context.Background(), pluginabi.MethodPluginReconfigure, request); err != nil {
		t.Fatalf("failed refresh should not fail plugin.reconfigure: %v", err)
	}

	registration := modelRegistration()
	if got := modelIDs(registration.Models); !reflect.DeepEqual(got, []string{"last-valid"}) {
		t.Fatalf("failed refresh discarded the last valid catalog: %v", got)
	}
}

func TestModelRegistrationKeepsACompleteSnapshotDuringRefresh(t *testing.T) {
	t.Setenv("CC_API_BASE", "https://command-code.test")
	clearCredentialState()
	t.Cleanup(clearCredentialState)

	client := &fakeModelCatalogHTTPClient{response: pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"data":[{"id":"before"}]}`),
	}}
	previousClientFactory := newCommandCodeModelCatalogHTTPClient
	newCommandCodeModelCatalogHTTPClient = func() pluginapi.HostHTTPClient { return client }
	t.Cleanup(func() { newCommandCodeModelCatalogHTTPClient = previousClientFactory })
	request, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("api-key: user_catalog_secret\n")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleMethodContext(context.Background(), pluginabi.MethodPluginReconfigure, request); err != nil {
		t.Fatalf("initial plugin.reconfigure failed: %v", err)
	}

	refreshStarted := make(chan struct{})
	refreshRelease := make(chan struct{})
	blockingClient := &blockingModelCatalogHTTPClient{
		started: refreshStarted,
		release: refreshRelease,
		response: pluginapi.HTTPResponse{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"data":[{"id":"after"}]}`),
		},
	}
	newCommandCodeModelCatalogHTTPClient = func() pluginapi.HostHTTPClient { return blockingClient }

	refreshDone := make(chan error, 1)
	go func() {
		_, err := handleMethodContext(context.Background(), pluginabi.MethodPluginReconfigure, request)
		refreshDone <- err
	}()
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("reconfigure did not start the upstream refresh")
	}

	if got := modelIDs(modelRegistration().Models); !reflect.DeepEqual(got, []string{"before"}) {
		t.Fatalf("refresh exposed a partial or empty snapshot: %v", got)
	}
	close(refreshRelease)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("plugin.reconfigure failed after upstream release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconfigure did not finish after upstream release")
	}
	if got := modelIDs(modelRegistration().Models); !reflect.DeepEqual(got, []string{"after"}) {
		t.Fatalf("successful refresh did not atomically publish the new snapshot: %v", got)
	}
}

func TestModelRegistrationFallsBackWhenLiveCatalogIsUnusable(t *testing.T) {
	t.Setenv("CC_API_BASE", "https://command-code.test")
	const apiKey = "user_catalog_secret"
	lifecycle, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("api-key: " + apiKey + "\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyLifecycleRequest(lifecycle); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clearCredentialState)

	fallback := modelRegistration()
	cases := []struct {
		name     string
		response pluginapi.HTTPResponse
		err      error
		wait     bool
	}{
		{name: "non-success", response: pluginapi.HTTPResponse{StatusCode: http.StatusUnauthorized}},
		{name: "malformed JSON", response: pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte("not-json")}},
		{name: "empty catalog", response: pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"data":[]}`)}},
		{name: "empty ID", response: pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"data":[{"id":"valid"},{"id":""}]}`)}},
		{name: "HTTP error", err: errors.New("upstream request failed: " + apiKey)},
		{name: "timeout", wait: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := &fakeModelCatalogHTTPClient{response: testCase.response, err: testCase.err, wait: testCase.wait}
			ctx := context.Background()
			if testCase.wait {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Millisecond)
				defer cancel()
			}

			registration := modelRegistrationContext(ctx, client)
			if !reflect.DeepEqual(registration.Models, fallback.Models) {
				t.Fatalf("unusable live catalog replaced embedded models: got %d models, want %d", len(registration.Models), len(fallback.Models))
			}
			encoded, err := json.Marshal(registration)
			if err != nil {
				t.Fatal(err)
			}
			if containsSecret(string(encoded), apiKey) {
				t.Fatalf("fallback registration exposed API key: %s", encoded)
			}
		})
	}
}

func TestModelRegistrationWithoutAPIKeyUsesEmbeddedCatalog(t *testing.T) {
	clearCredentialState()
	client := &fakeModelCatalogHTTPClient{}
	registration := modelRegistrationContext(context.Background(), client)
	if !reflect.DeepEqual(registration.Models, modelRegistration().Models) {
		t.Fatal("registration without an API key did not use embedded models")
	}
	if client.request.Method != "" {
		t.Fatalf("model catalog was queried without an API key: %#v", client.request)
	}
}

func containsSecret(value, secret string) bool {
	return secret != "" && strings.Contains(value, secret)
}

func modelIDs(models []pluginapi.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

type blockingModelCatalogHTTPClient struct {
	started  chan<- struct{}
	release  <-chan struct{}
	response pluginapi.HTTPResponse
}

func (f *blockingModelCatalogHTTPClient) Do(ctx context.Context, request pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-f.release:
		return f.response, nil
	case <-ctx.Done():
		return pluginapi.HTTPResponse{}, ctx.Err()
	}
}

func (f *blockingModelCatalogHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, errors.New("streaming is not used for model catalog requests")
}
