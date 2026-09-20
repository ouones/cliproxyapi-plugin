package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestParseAPIKeyAcceptsOnlyCommandCodeKeys(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    string
		wantErr bool
	}{
		{name: "valid", yaml: "api-key: user_example\n", want: "user_example"},
		{name: "missing", yaml: "enabled: true\n"},
		{name: "wrong provider", yaml: "api-key: sk_example\n", wantErr: true},
		{name: "empty suffix", yaml: "api-key: user_\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAPIKey([]byte(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseAPIKey() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("parseAPIKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegistrationPinsCapabilitiesAndDoesNotExposeCredential(t *testing.T) {
	const secret = "user_test_secret"
	lifecycle, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("api-key: " + secret + "\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := applyLifecycleRequest(lifecycle); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(clearCredentialState)
	if configuredAPIKey() != secret {
		t.Fatalf("configured API key was not retained in memory")
	}

	registration := pluginRegistration()
	if registration.SchemaVersion != verifiedSchemaVersion {
		t.Fatalf("schema version = %d, want %d", registration.SchemaVersion, verifiedSchemaVersion)
	}
	if !registration.Capabilities.ModelRegistrar || !registration.Capabilities.Executor {
		t.Fatalf("registration must declare model registrar and executor capabilities")
	}
	if len(registration.Metadata.ConfigFields) != 1 || registration.Metadata.ConfigFields[0].Name != "api-key" {
		t.Fatalf("registration must expose only the api-key configuration field")
	}

	encoded, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "user_test_secret") {
		t.Fatalf("registration response must not contain a user API key: %s", encoded)
	}
}

func TestShutdownClearsIdentityState(t *testing.T) {
	commandCodeIdentityStates.reset()
	if _, err := commandCodeIdentityStates.sessionID(nil, "user_shutdown", ""); err != nil {
		t.Fatal(err)
	}

	clearCredentialState()

	commandCodeIdentityStates.mu.Lock()
	remaining := len(commandCodeIdentityStates.states)
	commandCodeIdentityStates.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("shutdown left %d identity states in memory", remaining)
	}
}

func TestModelRegistrationUsesUpstreamIDs(t *testing.T) {
	response := modelRegistration()
	if response.Provider != pluginProvider {
		t.Fatalf("provider = %q, want %q", response.Provider, pluginProvider)
	}
	if len(response.Models) == 0 {
		t.Fatal("model registration returned no models")
	}

	seen := make(map[string]bool, len(response.Models))
	for _, model := range response.Models {
		if model.ID == "" || model.Name != model.ID {
			t.Fatalf("model does not preserve upstream ID: %#v", model)
		}
		if strings.HasPrefix(model.ID, pluginProvider+"/") {
			t.Fatalf("model ID was prefixed: %q", model.ID)
		}
		if seen[model.ID] {
			t.Fatalf("duplicate model ID: %q", model.ID)
		}
		seen[model.ID] = true
	}
}

func TestQuiesceRejectsNewCallsUntilReconfigure(t *testing.T) {
	if err := applyLifecycleRequest(nil); err != nil {
		t.Fatal(err)
	}

	if _, err := handleMethod(pluginabi.MethodPluginQuiesce, nil); err != nil {
		t.Fatalf("plugin.quiesce failed: %v", err)
	}
	t.Cleanup(func() {
		if err := applyLifecycleRequest(nil); err != nil {
			t.Fatalf("restore plugin lifecycle state: %v", err)
		}
	})

	if _, err := handleMethod(pluginabi.MethodExecutorIdentifier, nil); err == nil || !strings.Contains(err.Error(), "quiescing") {
		t.Fatalf("executor call after quiesce = %v, want a quiescing error", err)
	}

	if err := applyLifecycleRequest(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := handleMethod(pluginabi.MethodExecutorIdentifier, nil); err != nil {
		t.Fatalf("executor call after reconfigure failed: %v", err)
	}
}
