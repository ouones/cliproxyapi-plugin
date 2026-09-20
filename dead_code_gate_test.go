package main

import (
	"os"
	"strings"
	"testing"
)

func TestProvenFeatureDeadCodeGate(t *testing.T) {
	forbidden := []struct {
		file  string
		token string
	}{
		{file: "anthropic_executor.go", token: "type AnthropicExecutor ="},
		{file: "anthropic_executor.go", token: "func NewAnthropicExecutor("},
		{file: "chat_executor.go", token: "type OpenAIChatExecutor ="},
		{file: "chat_executor.go", token: "type OpenAIChatCompletionsExecutor ="},
		{file: "chat_executor.go", token: "func NewOpenAIChatExecutor("},
		{file: "chat_executor.go", token: "func NewOpenAIChatCompletionsExecutor("},
		{file: "responses_executor.go", token: "type OpenAIResponsesExecutor ="},
		{file: "responses_executor.go", token: "func NewOpenAIResponsesExecutor("},
		{file: "contract.go", token: "verifiedCLIProxyAPIRelease"},
		{file: "executor_core.go", token: "HasUsage"},
		{file: "executor_core.go", token: "func (r executorRuntime) upstreamRequest("},
		{file: "identity.go", token: "newUUID"},
		{file: "executor_core.go", token: "encodeBase64"},
		{file: "executor_core.go", token: "func base64Encode("},
	}
	for _, check := range forbidden {
		raw, err := os.ReadFile(check.file)
		if err != nil {
			t.Fatalf("read %s: %v", check.file, err)
		}
		if strings.Contains(string(raw), check.token) {
			t.Errorf("%s still contains forbidden dead-code marker %q", check.file, check.token)
		}
	}

	core, err := os.ReadFile("executor_core.go")
	if err != nil {
		t.Fatalf("read executor_core.go: %v", err)
	}
	if !strings.Contains(string(core), `"encoding/base64"`) || !strings.Contains(string(core), "base64.StdEncoding.EncodeToString") {
		t.Error("thinking signatures must use the standard-library base64 encoder")
	}

	mainSource, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	mainText := string(mainSource)
	start := strings.Index(mainText, "func cliproxy_plugin_init(")
	if start < 0 {
		t.Fatal("cliproxy_plugin_init was not found")
	}
	endOffset := strings.Index(mainText[start:], "\n}\n\n//export commandCodePluginCall")
	if endOffset < 0 {
		t.Fatal("cliproxy_plugin_init boundary was not found")
	}
	abiInit := mainText[start : start+endOffset]
	if strings.Contains(abiInit, "warn-only") || strings.Contains(abiInit, "keeping the verified ABI") {
		t.Error("ABI mismatch diagnostics must not claim a warn-only load policy")
	}
}
