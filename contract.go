package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginID                               = "command-code"
	pluginVersion                          = "0.1.0"
	pluginProvider                         = pluginID
	verifiedABIVersion              uint32 = 1
	verifiedSchemaVersion           uint32 = 6
	verifiedCommandCodeWireVersion         = "1.58.0"
	driftCheckURL                          = "https://registry.npmjs.org/command-code/latest"
	commandCodeModelCatalogPath            = "/provider/v1/models"
	commandCodeModelCatalogTimeout         = 10 * time.Second
	executorNonStreamingIdleTimeout        = 90 * time.Second
	executorStreamingIdleTimeout           = 30 * time.Second
)

var executorIdleTimeouts = struct {
	nonStreaming time.Duration
	streaming    time.Duration
}{
	nonStreaming: executorNonStreamingIdleTimeout,
	streaming:    executorStreamingIdleTimeout,
}

// These compile-time checks keep a dependency upgrade from silently changing the
// wire contract used by this plugin. A mismatch makes the plugin fail to build;
// it never selects a newer unverified ABI or schema automatically.
var (
	_ [int(verifiedABIVersion) - int(pluginabi.ABIVersion)]struct{}
	_ [int(pluginabi.ABIVersion) - int(verifiedABIVersion)]struct{}
	_ [int(verifiedSchemaVersion) - int(pluginabi.SchemaVersion)]struct{}
	_ [int(pluginabi.SchemaVersion) - int(verifiedSchemaVersion)]struct{}
)

//go:embed models.json
var modelsJSON []byte

type modelDefinition struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type commandCodeModelCatalog struct {
	Data []commandCodeModel `json:"data"`
}

type commandCodeModel struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Created       int64  `json:"created"`
	OwnedBy       string `json:"owned_by"`
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	ContextLength int64  `json:"context_length"`
}

var commandCodeModels = loadModels()

var commandCodeModelCatalogState = struct {
	sync.RWMutex
	models []pluginapi.ModelInfo
}{
	models: append([]pluginapi.ModelInfo(nil), commandCodeModels...),
}

var commandCodeModelCatalogRefreshMu sync.Mutex

var newCommandCodeModelCatalogHTTPClient = func() pluginapi.HostHTTPClient {
	return NewCommandCodeHostHTTPClient(callHost, "")
}

func loadModels() []pluginapi.ModelInfo {
	var definitions []modelDefinition
	if err := json.Unmarshal(modelsJSON, &definitions); err != nil {
		panic("invalid embedded model catalog: " + err.Error())
	}

	models := make([]pluginapi.ModelInfo, 0, len(definitions))
	for _, definition := range definitions {
		models = append(models, pluginapi.ModelInfo{
			ID:          definition.ID,
			Name:        definition.ID,
			Object:      "model",
			OwnedBy:     pluginProvider,
			DisplayName: definition.DisplayName,
		})
	}
	return models
}

func modelCatalogSnapshot() []pluginapi.ModelInfo {
	commandCodeModelCatalogState.RLock()
	defer commandCodeModelCatalogState.RUnlock()
	return append([]pluginapi.ModelInfo(nil), commandCodeModelCatalogState.models...)
}

func replaceModelCatalog(models []pluginapi.ModelInfo) {
	commandCodeModelCatalogState.Lock()
	commandCodeModelCatalogState.models = append([]pluginapi.ModelInfo(nil), models...)
	commandCodeModelCatalogState.Unlock()
}

func resetModelCatalog() {
	commandCodeModelCatalogRefreshMu.Lock()
	defer commandCodeModelCatalogRefreshMu.Unlock()
	replaceModelCatalog(commandCodeModels)
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRegistrar        bool                         `json:"model_registrar"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type executorRequestEnvelope struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type executorHTTPRequestEnvelope struct {
	pluginapi.ExecutorHTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type executorStreamResponseEnvelope struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type hostStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type hostStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

var credentialState struct {
	sync.RWMutex
	apiKey string
}

var executorStreamHostCall = callHost

func applyLifecycleRequest(raw []byte) error {
	var request lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &request); err != nil {
			return errors.New("invalid plugin lifecycle request")
		}
	}

	apiKey, err := parseAPIKey(request.ConfigYAML)
	if err != nil {
		return err
	}

	credentialState.Lock()
	credentialState.apiKey = apiKey
	credentialState.Unlock()
	commandCodePluginLifecycle.resume()
	return nil
}

func parseAPIKey(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}

	var config struct {
		APIKey string `yaml:"api-key"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return "", errors.New("invalid plugin configuration")
	}

	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return "", nil
	}
	if !strings.HasPrefix(apiKey, "user_") || len(apiKey) <= len("user_") {
		return "", errors.New("api-key must start with user_")
	}
	return apiKey, nil
}

func configuredAPIKey() string {
	credentialState.RLock()
	defer credentialState.RUnlock()
	return credentialState.apiKey
}

func clearCredentialState() {
	credentialState.Lock()
	credentialState.apiKey = ""
	credentialState.Unlock()
	resetModelCatalog()
	commandCodeIdentityStates.reset()
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: verifiedSchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Command Code",
			Version:          pluginVersion,
			Author:           "MAXeaglet",
			GitHubRepository: "https://github.com/MAXeaglet/commandcode-proxy",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "api-key",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Command Code API key; must start with user_. It is kept in memory and never logged.",
				},
			},
		},
		Capabilities: registrationCapability{
			ModelRegistrar:        true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeStatic,
			ExecutorInputFormats:  []string{"chat-completions", "responses", "anthropic"},
			ExecutorOutputFormats: []string{"chat-completions", "responses", "anthropic"},
		},
	}
}

func modelRegistration() pluginapi.ModelRegistrationResponse {
	return modelRegistrationFor(modelCatalogSnapshot())
}

func modelRegistrationContext(ctx context.Context, client pluginapi.HostHTTPClient) pluginapi.ModelRegistrationResponse {
	refreshModelCatalog(ctx, client)
	return modelRegistration()
}

func refreshModelCatalog(ctx context.Context, client pluginapi.HostHTTPClient) bool {
	commandCodeModelCatalogRefreshMu.Lock()
	defer commandCodeModelCatalogRefreshMu.Unlock()

	apiKey := configuredAPIKey()
	if apiKey == "" || client == nil {
		return false
	}

	requestContext, cancel := context.WithTimeout(contextOrBackground(ctx), commandCodeModelCatalogTimeout)
	defer cancel()
	liveModels, ok := fetchCommandCodeModels(requestContext, apiKey, client)
	if !ok {
		return false
	}
	replaceModelCatalog(liveModels)
	return true
}

func modelRegistrationFor(models []pluginapi.ModelInfo) pluginapi.ModelRegistrationResponse {
	return pluginapi.ModelRegistrationResponse{
		Provider: pluginProvider,
		Models:   append([]pluginapi.ModelInfo(nil), models...),
	}
}

func fetchCommandCodeModels(ctx context.Context, apiKey string, client pluginapi.HostHTTPClient) ([]pluginapi.ModelInfo, bool) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || client == nil {
		return nil, false
	}

	response, err := client.Do(contextOrBackground(ctx), pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    commandCodeAPIBase() + commandCodeModelCatalogPath,
		Headers: http.Header{
			"Authorization":          {"Bearer " + apiKey},
			"User-Agent":             {"cli"},
			"x-cli-environment":      {"production"},
			"x-command-code-version": {verifiedCommandCodeWireVersion},
		},
	})
	if err != nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, false
	}

	var catalog commandCodeModelCatalog
	if err := json.Unmarshal(response.Body, &catalog); err != nil {
		return nil, false
	}
	return modelInfosFromCatalog(catalog.Data)
}

func modelInfosFromCatalog(definitions []commandCodeModel) ([]pluginapi.ModelInfo, bool) {
	if len(definitions) == 0 {
		return nil, false
	}

	models := make([]pluginapi.ModelInfo, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if strings.TrimSpace(definition.ID) == "" {
			return nil, false
		}
		if definition.ContextLength < 0 {
			return nil, false
		}
		if _, ok := seen[definition.ID]; ok {
			continue
		}
		seen[definition.ID] = struct{}{}

		object := definition.Object
		if object == "" {
			object = "model"
		}
		ownedBy := definition.OwnedBy
		if ownedBy == "" {
			ownedBy = pluginProvider
		}
		displayName := definition.DisplayName
		if displayName == "" {
			displayName = definition.Name
		}
		if displayName == "" {
			displayName = definition.ID
		}
		models = append(models, pluginapi.ModelInfo{
			ID:            definition.ID,
			Name:          definition.ID,
			Object:        object,
			Created:       definition.Created,
			OwnedBy:       ownedBy,
			DisplayName:   displayName,
			ContextLength: definition.ContextLength,
		})
	}
	if len(models) == 0 {
		return nil, false
	}
	return models, true
}

func handleMethod(method string, request []byte) ([]byte, error) {
	return handleMethodContext(context.Background(), method, request)
}

func handleMethodContext(parent context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := applyLifecycleRequest(request); err != nil {
			return nil, err
		}
		refreshModelCatalog(parent, newCommandCodeModelCatalogHTTPClient())
		scheduleWireDriftCheck()
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginQuiesce:
		commandCodePluginLifecycle.quiesce()
		return okEnvelope(map[string]any{})
	case pluginabi.MethodPluginShutdown:
		commandCodePluginLifecycle.quiesceAndWait()
		commandCodeHostCallbackBoundary.wait()
		clearCredentialState()
		return okEnvelope(map[string]any{})
	}

	release, err := commandCodePluginLifecycle.begin()
	if err != nil {
		return nil, err
	}
	releaseOnReturn := true
	defer func() {
		if releaseOnReturn {
			release()
		}
	}()

	switch method {
	case pluginabi.MethodModelRegister:
		refreshModelCatalog(parent, newCommandCodeModelCatalogHTTPClient())
		return okEnvelope(modelRegistration())
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: pluginProvider})
	case pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream, pluginabi.MethodExecutorCountTokens:
		var envelope executorRequestEnvelope
		if err := json.Unmarshal(request, &envelope); err != nil {
			return nil, errors.New("invalid executor request")
		}
		if envelope.ExecutorRequest.Metadata == nil {
			envelope.ExecutorRequest.Metadata = make(map[string]any)
		}
		if envelope.HostCallbackID != "" {
			envelope.ExecutorRequest.Metadata["host_callback_id"] = envelope.HostCallbackID
		}
		executor := executorForRequest(envelope.ExecutorRequest)
		switch method {
		case pluginabi.MethodExecutorExecute:
			executionContext, cancel := newExecutorContext(parent, false)
			defer cancel()
			response, err := executor.Execute(executionContext, envelope.ExecutorRequest)
			if err != nil {
				return nil, err
			}
			return okEnvelope(response)
		case pluginabi.MethodExecutorCountTokens:
			executionContext, cancel := newExecutorContext(parent, false)
			defer cancel()
			response, err := executor.CountTokens(executionContext, envelope.ExecutorRequest)
			if err != nil {
				return nil, err
			}
			return okEnvelope(response)
		default:
			executionContext, cancel := newExecutorContext(parent, true)
			cancelOnReturn := true
			defer func() {
				if cancelOnReturn {
					cancel()
				}
			}()
			response, err := executor.ExecuteStream(executionContext, envelope.ExecutorRequest)
			if err != nil {
				return nil, err
			}
			if envelope.StreamID != "" {
				releaseOnReturn = false
				cancelOnReturn = false
				go func() {
					defer release()
					defer cancel()
					bridgeExecutorStream(executionContext, cancel, envelope.StreamID, response.Chunks)
				}()
				return okEnvelope(executorStreamResponseEnvelope{Headers: response.Headers})
			}
			streamResponse := executorStreamResponseEnvelope{Headers: response.Headers}
			for chunk := range response.Chunks {
				streamResponse.Chunks = append(streamResponse.Chunks, chunk)
			}
			return okEnvelope(streamResponse)
		}
	case pluginabi.MethodExecutorHTTPRequest:
		var envelope executorHTTPRequestEnvelope
		if err := json.Unmarshal(request, &envelope); err != nil {
			return nil, errors.New("invalid executor HTTP request")
		}
		if envelope.HostCallbackID != "" {
			if envelope.Metadata == nil {
				envelope.Metadata = make(map[string]any)
			}
			envelope.Metadata["host_callback_id"] = envelope.HostCallbackID
		}
		if envelope.ExecutorHTTPRequest.HTTPClient == nil {
			envelope.ExecutorHTTPRequest.HTTPClient = NewCommandCodeHostHTTPClient(callHost, envelope.HostCallbackID)
		}
		executionContext, cancel := newExecutorContext(parent, false)
		defer cancel()
		response, err := NewChatCompletionsExecutor().HttpRequest(executionContext, envelope.ExecutorHTTPRequest)
		if err != nil {
			return nil, err
		}
		return okEnvelope(response)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func newExecutorContext(parent context.Context, streaming bool) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if streaming {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, executorIdleTimeouts.nonStreaming)
}

func bridgeExecutorStream(ctx context.Context, cancel context.CancelFunc, streamID string, chunks <-chan pluginapi.ExecutorStreamChunk) {
	streamError := ""
	if chunks == nil {
		streamError = "executor stream is unavailable"
	} else {
		for chunk := range chunks {
			if ctx != nil {
				select {
				case <-ctx.Done():
					streamError = ctx.Err().Error()
					break
				default:
				}
				if streamError != "" {
					break
				}
			}
			if chunk.Err != nil {
				streamError = chunk.Err.Error()
				break
			}
			payload, err := json.Marshal(hostStreamEmitRequest{StreamID: streamID, Payload: chunk.Payload})
			if err != nil {
				streamError = err.Error()
				break
			}
			if _, err := executorStreamHostCall(pluginabi.MethodHostStreamEmit, payload); err != nil {
				streamError = err.Error()
				break
			}
		}
	}
	if streamError == "" && ctx != nil {
		if err := ctx.Err(); err != nil {
			streamError = err.Error()
		}
	}
	if streamError != "" && cancel != nil {
		cancel()
	}
	payload, err := json.Marshal(hostStreamCloseRequest{StreamID: streamID, Error: streamError})
	if err == nil {
		_, _ = executorStreamHostCall(pluginabi.MethodHostStreamClose, payload)
	}
}

func executorForRequest(request pluginapi.ExecutorRequest) pluginapi.ProviderExecutor {
	format := strings.ToLower(request.Format + " " + request.SourceFormat)
	if strings.Contains(format, "anthropic") || strings.Contains(format, "claude") {
		return NewAnthropicMessagesExecutor()
	}
	if strings.Contains(format, "response") {
		return NewResponsesExecutor()
	}
	return NewChatCompletionsExecutor()
}

func okEnvelope(value any) ([]byte, error) {
	result, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string, httpStatus ...int) []byte {
	status := 0
	if len(httpStatus) > 0 {
		status = httpStatus[0]
	}
	result, _ := json.Marshal(envelope{
		OK: false,
		Error: &envelopeError{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
		},
	})
	return result
}
