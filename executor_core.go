package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const commandCodeGeneratePath = "/alpha/generate"

type executorRuntime struct {
	apiBase   string
	apiKey    func() string
	identity  *identityStateStore
	now       func() time.Time
	idFactory func() string
	client    pluginapi.HostHTTPClient
}

type ccToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type ccUsage struct {
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
	CacheWriteTokens  int
	NoCacheTokens     int
	HasNoCacheTokens  bool
	hasUsage          bool
}

type ccCollected struct {
	LastEvent     string
	FullText      string
	ReasoningText string
	ToolCalls     []ccToolCall
	FinishReason  string
	SawFinish     bool
	Usage         ccUsage
	UpstreamError *pluginabi.Error
}

func defaultExecutorRuntime() executorRuntime {
	return executorRuntime{
		apiBase:   commandCodeAPIBase(),
		apiKey:    configuredAPIKey,
		identity:  commandCodeIdentityStates,
		now:       time.Now,
		idFactory: func() string { return newRandomID() },
	}
}

func commandCodeAPIBase() string {
	base := strings.TrimSpace(os.Getenv("CC_API_BASE"))
	if base == "" {
		base = defaultCommandCodeAPIBase
	}
	return strings.TrimRight(base, "/")
}

func (r executorRuntime) normalized() executorRuntime {
	if strings.TrimSpace(r.apiBase) == "" {
		r.apiBase = commandCodeAPIBase()
	}
	r.apiBase = strings.TrimRight(r.apiBase, "/")
	if r.apiKey == nil {
		r.apiKey = configuredAPIKey
	}
	if r.identity == nil {
		r.identity = commandCodeIdentityStates
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.idFactory == nil {
		r.idFactory = func() string { return newRandomID() }
	}
	return r
}

func newRandomID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(raw[0:4]),
		hex.EncodeToString(raw[4:6]),
		hex.EncodeToString(raw[6:8]),
		hex.EncodeToString(raw[8:10]),
		hex.EncodeToString(raw[10:16]))
}

func requestPayload(request pluginapi.ExecutorRequest) []byte {
	if len(request.Payload) > 0 {
		return request.Payload
	}
	return request.OriginalRequest
}

func decodeRequestObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, pluginabi.NewError("invalid_request_error", "request body is required", http.StatusBadRequest)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, pluginabi.NewError("invalid_request_error", "invalid JSON request body", http.StatusBadRequest)
	}
	return value, nil
}

func (r executorRuntime) initialize(ctx context.Context, request pluginapi.ExecutorRequest, promptCacheKey string) (pluginapi.HostHTTPClient, string, string, error) {
	r = r.normalized()
	apiKey := strings.TrimSpace(r.apiKey())
	if apiKey == "" {
		return nil, "", "", pluginabi.NewError("authentication_error", "Command Code API key is not configured", http.StatusUnauthorized)
	}

	callbackID := metadataString(request.Metadata, "host_callback_id")
	client := request.HTTPClient
	if client == nil {
		client = r.client
	}
	if client == nil {
		client = NewCommandCodeHostHTTPClient(callHost, callbackID)
	}

	r.identity.ensureInitialized(ctx, apiKey, identityHostHTTPClient{client: client}, func(level, message string, fields map[string]any) {
		logHost(level, message, fields)
	})

	sessionID, err := r.identity.sessionID(map[string][]string(request.Headers), apiKey, promptCacheKey)
	if err != nil {
		return nil, "", "", pluginabi.NewError("identity_error", "Command Code session could not be created", http.StatusBadGateway)
	}
	return client, apiKey, sessionID, nil
}

func (r executorRuntime) makeHTTPRequest(request pluginapi.ExecutorRequest, body map[string]any, apiKey, sessionID string) pluginapi.HTTPRequest {
	if isUUID(sessionID) {
		ordered := make(map[string]any, len(body)+1)
		for _, key := range []string{"config", "memory", "taste", "skills", "permissionMode"} {
			if value, ok := body[key]; ok {
				ordered[key] = value
			}
		}
		ordered["threadId"] = sessionID
		for _, key := range []string{"mode", "promptCache", "params"} {
			if value, ok := body[key]; ok {
				ordered[key] = value
			}
		}
		body = ordered
	}
	bodyBytes, _ := json.Marshal(body)
	headers := http.Header{
		"Content-Type":           {"application/json"},
		"User-Agent":             {"cli"},
		"x-command-code-version": {verifiedCommandCodeWireVersion},
		"x-cli-environment":      {"production"},
		"x-project-slug":         {"c-users-dev-projects-app"},
		"x-taste-learning":       {"false"},
		"x-session-id":           {sessionID},
		"Authorization":          {"Bearer " + apiKey},
		"traceparent":            {newTraceparent()},
	}
	if request.Headers.Get("x-cmd-zdr") == "1" {
		headers.Set("x-cmd-zdr", "1")
	}
	return pluginapi.HTTPRequest{
		Method:  http.MethodPost,
		URL:     r.apiBase + commandCodeGeneratePath,
		Headers: headers,
		Body:    bodyBytes,
	}
}

func newTraceparent() string {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "00-00000000000000000000000000000000-0000000000000001-01"
	}
	return "00-" + hex.EncodeToString(raw[:16]) + "-" + hex.EncodeToString(raw[16:])[:16] + "-01"
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return value
}

func mapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func sliceValue(value any) []any {
	result, _ := value.([]any)
	return result
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func boolValue(value any) (bool, bool) {
	result, ok := value.(bool)
	return result, ok
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func intValue(value any, fallback int) int {
	number, ok := numberValue(value)
	if !ok {
		return fallback
	}
	return int(number)
}

func jsonString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func textOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if parts, ok := value.([]any); ok {
		var result strings.Builder
		for _, part := range parts {
			block := mapValue(part)
			if block == nil {
				continue
			}
			if text := stringValue(block["text"]); text != "" {
				result.WriteString(text)
			} else if text := stringValue(block["content"]); text != "" {
				result.WriteString(text)
			}
		}
		return result.String()
	}
	return ""
}

func convertResponsesRequest(value map[string]any, idFactory func() string) map[string]any {
	messages := make([]any, 0)
	if instructions, ok := value["instructions"]; ok && instructions != nil {
		if text := textOf(instructions); text != "" {
			messages = append(messages, map[string]any{"role": "system", "content": text})
		}
	}
	var pending map[string]any
	flush := func() {
		if pending == nil {
			return
		}
		toolCalls, _ := pending["tool_calls"].([]any)
		if len(toolCalls) == 0 {
			delete(pending, "tool_calls")
		}
		if pending["content"] == nil && pending["reasoning_content"] == nil && len(toolCalls) == 0 {
			pending = nil
			return
		}
		messages = append(messages, pending)
		pending = nil
	}
	ensurePending := func() map[string]any {
		if pending == nil {
			pending = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{}}
		}
		return pending
	}

	switch input := value["input"].(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": input})
	case []any:
		for _, rawItem := range input {
			item := mapValue(rawItem)
			if item == nil {
				continue
			}
			itemType := stringValue(item["type"])
			if itemType == "" && item["role"] != nil {
				itemType = "message"
			}
			switch itemType {
			case "reasoning":
				if text := responsesReasoningText(item); text != "" {
					ensurePending()["reasoning_content"] = text
				}
			case "message":
				text := textOf(item["content"])
				role := stringValue(item["role"])
				if role == "assistant" {
					if text != "" {
						ensurePending()["content"] = text
					}
				} else {
					flush()
					if role == "system" || role == "developer" {
						messages = append(messages, map[string]any{"role": "system", "content": text})
					} else {
						messages = append(messages, map[string]any{"role": "user", "content": text})
					}
				}
			case "function_call":
				id := stringValue(item["call_id"])
				if id == "" {
					id = stringValue(item["id"])
				}
				if id == "" {
					id = "call_" + shortID(idFactory())
				}
				calls, _ := ensurePending()["tool_calls"].([]any)
				calls = append(calls, map[string]any{
					"id": id, "type": "function",
					"function": map[string]any{"name": stringValue(item["name"]), "arguments": stringValue(item["arguments"])},
				})
				ensurePending()["tool_calls"] = calls
			case "function_call_output":
				flush()
				messages = append(messages, map[string]any{
					"role": "tool", "tool_call_id": stringValue(item["call_id"]), "content": jsonString(item["output"]),
				})
			}
		}
	}
	flush()

	result := map[string]any{
		"model":    stringValue(value["model"]),
		"messages": messages,
		"stream":   value["stream"] == true,
	}
	if tools := normalizeResponseTools(value["tools"]); len(tools) > 0 {
		result["tools"] = tools
	}
	if toolChoice := value["tool_choice"]; toolChoice != nil {
		if choice, ok := toolChoice.(map[string]any); ok && stringValue(choice["name"]) != "" {
			result["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": choice["name"]}}
		} else {
			result["tool_choice"] = toolChoice
		}
	}
	for _, key := range []string{"temperature", "top_p", "parallel_tool_calls"} {
		if item, ok := value[key]; ok {
			result[key] = item
		}
	}
	if maxTokens, ok := value["max_output_tokens"]; ok {
		result["max_tokens"] = maxTokens
	}
	if reasoning := mapValue(value["reasoning"]); reasoning != nil && stringValue(reasoning["effort"]) != "" {
		result["reasoning_effort"] = reasoning["effort"]
	}
	if cacheKey, ok := value["prompt_cache_key"]; ok {
		result["prompt_cache_key"] = cacheKey
	}
	return result
}

func responsesReasoningText(item map[string]any) string {
	if text := textOf(item["summary"]); text != "" {
		return text
	}
	if text := textOf(item["content"]); text != "" {
		return text
	}
	return stringValue(item["text"])
}

func normalizeResponseTools(value any) []any {
	items := sliceValue(value)
	result := make([]any, 0, len(items))
	for _, rawItem := range items {
		item := mapValue(rawItem)
		if item == nil || (stringValue(item["type"]) != "function" && stringValue(item["name"]) == "") {
			continue
		}
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        stringValue(item["name"]),
				"description": stringValue(item["description"]),
				"parameters":  defaultObject(item["parameters"]),
			},
		})
	}
	return result
}

func convertAnthropicRequest(value map[string]any) map[string]any {
	openaiMessages := make([]any, 0)
	if system := value["system"]; system != nil {
		if text, ok := system.(string); ok {
			openaiMessages = append(openaiMessages, map[string]any{"role": "system", "content": text})
		} else if blocks := sliceValue(system); len(blocks) > 0 {
			openaiMessages = append(openaiMessages, map[string]any{"role": "system", "content": blocks})
		}
	}
	toolNames := make(map[string]string)
	for _, rawMessage := range sliceValue(value["messages"]) {
		message := mapValue(rawMessage)
		if message == nil {
			continue
		}
		role := stringValue(message["role"])
		blocks := sliceValue(message["content"])
		if role == "assistant" {
			textParts := make([]any, 0)
			var text strings.Builder
			var reasoning strings.Builder
			toolCalls := make([]any, 0)
			if len(blocks) == 0 && message["content"] != nil {
				text.WriteString(stringValue(message["content"]))
			}
			for _, rawBlock := range blocks {
				block := mapValue(rawBlock)
				if block == nil {
					continue
				}
				switch stringValue(block["type"]) {
				case "text":
					part := map[string]any{"type": "text", "text": stringValue(block["text"])}
					if block["cache_control"] != nil {
						part["cache_control"] = block["cache_control"]
					}
					textParts = append(textParts, part)
					text.WriteString(stringValue(block["text"]))
				case "thinking":
					reasoning.WriteString(stringValue(block["thinking"]))
				case "tool_use":
					id := stringValue(block["id"])
					toolNames[id] = stringValue(block["name"])
					toolCalls = append(toolCalls, map[string]any{
						"id": id, "type": "function",
						"function": map[string]any{"name": stringValue(block["name"]), "arguments": jsonString(defaultObject(block["input"]))},
					})
				}
			}
			assistant := map[string]any{"role": "assistant", "content": nil}
			if len(textParts) > 1 || (len(textParts) == 1 && mapValue(textParts[0])["cache_control"] != nil) {
				assistant["content"] = textParts
			} else if text.Len() > 0 {
				assistant["content"] = text.String()
			}
			if reasoning.Len() > 0 {
				assistant["reasoning_content"] = reasoning.String()
			}
			if len(toolCalls) > 0 {
				assistant["tool_calls"] = toolCalls
			}
			openaiMessages = append(openaiMessages, assistant)
			continue
		}
		if role != "user" {
			continue
		}
		if content, ok := message["content"].(string); ok {
			openaiMessages = append(openaiMessages, map[string]any{"role": "user", "content": content})
			continue
		}
		textParts := make([]any, 0)
		var text strings.Builder
		for _, rawBlock := range blocks {
			block := mapValue(rawBlock)
			if block == nil {
				continue
			}
			switch stringValue(block["type"]) {
			case "text":
				part := map[string]any{"type": "text", "text": stringValue(block["text"])}
				if block["cache_control"] != nil {
					part["cache_control"] = block["cache_control"]
				}
				textParts = append(textParts, part)
				text.WriteString(stringValue(block["text"]))
			case "image":
				source := mapValue(block["source"])
				if stringValue(source["type"]) == "base64" && stringValue(source["data"]) != "" {
					textParts = append(textParts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + stringValue(source["media_type"]) + ";base64," + stringValue(source["data"])}})
				}
			case "tool_result":
				content := textOf(block["content"])
				if content == "" {
					content = stringValue(block["content"])
				}
				toolMessage := map[string]any{"role": "tool", "tool_call_id": stringValue(block["tool_use_id"]), "content": content}
				if name := toolNames[stringValue(block["tool_use_id"])]; name != "" {
					toolMessage["name"] = name
				}
				openaiMessages = append(openaiMessages, toolMessage)
			}
		}
		if len(textParts) > 0 || text.Len() > 0 {
			content := any(text.String())
			if len(textParts) > 1 || (len(textParts) == 1 && mapValue(textParts[0])["cache_control"] != nil) {
				content = textParts
			}
			openaiMessages = append(openaiMessages, map[string]any{"role": "user", "content": content})
		}
	}

	result := map[string]any{
		"model":      stringValue(value["model"]),
		"messages":   openaiMessages,
		"max_tokens": value["max_tokens"],
		"stream":     value["stream"] == true,
	}
	if result["model"] == "" {
		result["model"] = "deepseek/deepseek-v4-flash"
	}
	if result["max_tokens"] == nil {
		result["max_tokens"] = 64000
	}
	if tools := sliceValue(value["tools"]); len(tools) > 0 {
		normalized := make([]any, 0, len(tools))
		for _, rawTool := range tools {
			tool := mapValue(rawTool)
			if tool == nil {
				continue
			}
			normalized = append(normalized, map[string]any{"type": "function", "function": map[string]any{
				"name": stringValue(tool["name"]), "description": stringValue(tool["description"]), "parameters": defaultObject(tool["input_schema"]),
			}})
		}
		result["tools"] = normalized
	}
	if choice := mapValue(value["tool_choice"]); choice != nil {
		switch stringValue(choice["type"]) {
		case "any":
			result["tool_choice"] = "required"
		case "tool":
			result["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": choice["name"]}}
		default:
			result["tool_choice"] = "auto"
		}
	}
	for _, key := range []string{"temperature", "top_p", "stop_sequences"} {
		if item, ok := value[key]; ok {
			if key == "stop_sequences" {
				result["stop"] = item
			} else {
				result[key] = item
			}
		}
	}
	if metadata := mapValue(value["metadata"]); metadata != nil && metadata["user_id"] != nil {
		result["user"] = metadata["user_id"]
	}
	if thinking := mapValue(value["thinking"]); thinking != nil {
		switch stringValue(thinking["type"]) {
		case "adaptive":
			result["reasoning_effort"] = "medium"
		case "enabled":
			budget := intValue(thinking["budget_tokens"], 0)
			if budget >= 10000 {
				result["reasoning_effort"] = "high"
			} else if budget >= 5000 {
				result["reasoning_effort"] = "medium"
			} else if budget > 0 {
				result["reasoning_effort"] = "low"
			}
		}
	}
	return result
}

func defaultObject(value any) map[string]any {
	if result := mapValue(value); result != nil {
		return result
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func buildCommandCodeRequest(openai map[string]any) map[string]any {
	messages := sliceValue(openai["messages"])
	systemBlocks := make([]any, 0)
	chatMessages := make([]any, 0, len(messages))
	for _, rawMessage := range messages {
		message := mapValue(rawMessage)
		if message == nil {
			continue
		}
		role := stringValue(message["role"])
		if role == "system" || role == "developer" {
			content := message["content"]
			if text, ok := content.(string); ok {
				if text != "" {
					systemBlocks = append(systemBlocks, map[string]any{"type": "text", "text": text})
				}
			} else {
				for _, rawPart := range sliceValue(content) {
					part := mapValue(rawPart)
					if part == nil {
						continue
					}
					text := stringValue(part["text"])
					if text == "" {
						text = stringValue(part["content"])
					}
					if text == "" && part["cache_control"] == nil {
						continue
					}
					block := map[string]any{"type": "text", "text": text}
					if part["cache_control"] != nil {
						block["cache_control"] = part["cache_control"]
					}
					systemBlocks = append(systemBlocks, block)
				}
			}
			continue
		}
		chatMessages = append(chatMessages, message)
	}
	for index := 0; index < len(systemBlocks)-1; index++ {
		block := mapValue(systemBlocks[index])
		block["text"] = stringValue(block["text"]) + "\n"
	}

	toolNames := make(map[string]string)
	for _, rawMessage := range chatMessages {
		message := mapValue(rawMessage)
		if stringValue(message["role"]) != "assistant" {
			continue
		}
		for _, rawCall := range sliceValue(message["tool_calls"]) {
			call := mapValue(rawCall)
			function := mapValue(call["function"])
			toolNames[stringValue(call["id"])] = stringValue(function["name"])
		}
	}

	ccMessages := make([]any, 0, len(chatMessages))
	for _, rawMessage := range chatMessages {
		message := mapValue(rawMessage)
		role := stringValue(message["role"])
		switch role {
		case "user":
			ccMessages = append(ccMessages, map[string]any{"role": "user", "content": buildUserContent(message["content"])})
		case "assistant":
			parts := make([]any, 0)
			if reasoning := stringValue(message["reasoning_content"]); reasoning != "" {
				parts = append(parts, map[string]any{"type": "reasoning", "text": reasoning})
			}
			if content, ok := message["content"].(string); ok && content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": content})
			} else {
				for _, rawPart := range sliceValue(message["content"]) {
					part := mapValue(rawPart)
					if part == nil {
						continue
					}
					if stringValue(part["type"]) == "text" || stringValue(part["type"]) == "reasoning" {
						parts = append(parts, part)
					}
				}
			}
			for _, rawCall := range sliceValue(message["tool_calls"]) {
				call := mapValue(rawCall)
				function := mapValue(call["function"])
				arguments := function["arguments"]
				if _, ok := arguments.(string); !ok {
					arguments = jsonString(arguments)
				}
				var input any
				if err := json.Unmarshal([]byte(stringValue(arguments)), &input); err == nil {
					if input == nil {
						input = map[string]any{}
					}
				} else {
					input = map[string]any{}
				}
				parts = append(parts, map[string]any{"type": "tool-call", "toolCallId": stringValue(call["id"]), "toolName": stringValue(function["name"]), "input": input})
			}
			ccMessages = append(ccMessages, map[string]any{"role": "assistant", "content": parts})
		case "tool":
			output := map[string]any{"type": "text", "value": textOf(message["content"])}
			if text, ok := message["content"].(string); ok {
				output["value"] = text
			}
			ccMessages = append(ccMessages, map[string]any{"role": "tool", "content": []any{map[string]any{
				"type": "tool-result", "toolCallId": stringValue(message["tool_call_id"]), "toolName": toolNames[stringValue(message["tool_call_id"])], "output": output,
			}}})
		default:
			ccMessages = append(ccMessages, map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": textOf(message["content"])}}})
		}
	}

	maxTokens := intValue(openai["max_tokens"], 64000)
	if maxTokens <= 0 {
		maxTokens = 64000
	}
	if maxTokens > 200000 {
		maxTokens = 200000
	}
	params := map[string]any{
		"model":      stringValue(openai["model"]),
		"messages":   ccMessages,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if params["model"] == "" {
		params["model"] = "deepseek/deepseek-v4-flash"
	}
	if len(systemBlocks) > 0 {
		params["system"] = systemBlocks
	} else {
		params["system"] = []any{map[string]any{"type": "text", "text": " "}}
	}
	for _, key := range []string{"temperature", "reasoning_effort", "parallel_tool_calls"} {
		if value, ok := openai[key]; ok {
			params[key] = value
		}
	}
	tools := normalizeOpenAITools(openai["tools"])
	params["tools"] = tools
	if choice, ok := openai["tool_choice"]; ok {
		switch selected := choice.(type) {
		case string:
			mapping := map[string]string{"auto": "auto", "none": "none", "required": "any"}
			wireChoice, ok := mapping[selected]
			if !ok {
				wireChoice = "auto"
			}
			params["tool_choice"] = map[string]any{"type": wireChoice}
		case map[string]any:
			if function := mapValue(selected["function"]); function != nil {
				params["tool_choice"] = map[string]any{"type": "tool", "name": function["name"]}
			} else {
				params["tool_choice"] = selected
			}
		}
	}

	if cacheKey := stringValue(openai["prompt_cache_key"]); cacheKey != "" && len(systemBlocks) > 0 {
		marked := false
		for _, rawBlock := range systemBlocks {
			if mapValue(rawBlock)["cache_control"] != nil {
				marked = true
				break
			}
		}
		if !marked {
			mapValue(systemBlocks[len(systemBlocks)-1])["cache_control"] = map[string]any{"type": "ephemeral"}
		}
	}

	return map[string]any{
		"config": map[string]any{
			"workingDir": "C:\\Users\\dev\\projects\\app", "date": time.Now().UTC().Format("2006-01-02"), "environment": "win32",
			"structure": []any{}, "isGitRepo": false, "currentBranch": "", "mainBranch": "", "gitStatus": "", "recentCommits": []any{},
		},
		"memory": nil, "taste": nil, "skills": nil, "permissionMode": "standard", "mode": "agent", "params": params,
	}
}

func buildUserContent(value any) []any {
	if text, ok := value.(string); ok {
		return []any{map[string]any{"type": "text", "text": text}}
	}
	parts := make([]any, 0)
	for _, rawPart := range sliceValue(value) {
		part := mapValue(rawPart)
		if part == nil {
			continue
		}
		if stringValue(part["type"]) == "image_url" {
			imageURL := mapValue(part["image_url"])
			parts = append(parts, map[string]any{"type": "image", "image": stringValue(imageURL["url"])})
		} else {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return []any{map[string]any{"type": "text", "text": textOf(value)}}
	}
	return parts
}

func normalizeOpenAITools(value any) []any {
	result := make([]any, 0)
	for _, rawTool := range sliceValue(value) {
		tool := mapValue(rawTool)
		if tool == nil {
			continue
		}
		function := mapValue(tool["function"])
		if function == nil {
			function = tool
		}
		name := stringValue(function["name"])
		switch name {
		case "bash_output", "task_output":
			name = "shell_output"
		case "tool_search":
			name = "search_tools"
		case "read_multiple_files":
			name = "read_file"
		}
		result = append(result, map[string]any{
			"name": name, "description": stringValue(function["description"]), "input_schema": defaultObject(function["parameters"]),
		})
	}
	return result
}

func shortID(value string) string {
	value = strings.ReplaceAll(value, "-", "")
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func parseCCEvent(line []byte) map[string]any {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" || trimmed == "[DONE]" || strings.HasPrefix(trimmed, ":") {
		return nil
	}
	var event map[string]any
	if json.Unmarshal([]byte(trimmed), &event) != nil || stringValue(event["type"]) == "" {
		return nil
	}
	return event
}

func (c *ccCollected) consume(event map[string]any, idFactory func() string) {
	if event == nil {
		return
	}
	c.LastEvent = stringValue(event["type"])
	switch c.LastEvent {
	case "text-delta":
		text := stringValue(event["text"])
		if text == "" {
			text = stringValue(event["delta"])
		}
		c.FullText += text
	case "reasoning-delta":
		c.ReasoningText += stringValue(event["text"])
	case "tool-call":
		id := stringValue(event["toolCallId"])
		if id == "" {
			id = "call_" + shortID(idFactory())
		}
		arguments := event["input"]
		if text, ok := arguments.(string); ok {
			arguments = text
		} else {
			arguments = jsonString(arguments)
		}
		c.ToolCalls = append(c.ToolCalls, ccToolCall{ID: id, Name: stringValue(event["toolName"]), Arguments: stringValue(arguments)})
	case "finish-step", "finish":
		c.SawFinish = true
		c.FinishReason = mapFinishReason(stringValue(event["finishReason"]))
		usage := event["totalUsage"]
		if usage == nil {
			usage = event["usage"]
		}
		if usage != nil {
			c.Usage = parseUsage(mapValue(usage))
			c.Usage.hasUsage = true
		}
	case "error":
		c.UpstreamError = mapCCEventError(event)
	}
}

func collectCCBody(body []byte, idFactory func() string) ccCollected {
	var collected ccCollected
	for _, line := range strings.Split(string(body), "\n") {
		collected.consume(parseCCEvent([]byte(line)), idFactory)
	}
	return collected
}

func parseUsage(value map[string]any) ccUsage {
	if value == nil {
		return ccUsage{}
	}
	usage := ccUsage{
		InputTokens:       intValue(value["inputTokens"], 0),
		OutputTokens:      intValue(value["outputTokens"], 0),
		CachedInputTokens: intValue(value["cachedInputTokens"], 0),
	}
	details := mapValue(value["inputTokenDetails"])
	if details != nil {
		usage.CacheWriteTokens = intValue(details["cacheWriteTokens"], 0)
		if _, ok := numberValue(details["noCacheTokens"]); ok {
			usage.NoCacheTokens = intValue(details["noCacheTokens"], 0)
			usage.HasNoCacheTokens = true
		}
	}
	if usage.OutputTokens == 0 {
		usage.InputTokens = 0
		usage.CachedInputTokens = 0
	}
	return usage
}

func mapFinishReason(reason string) string {
	value := strings.ToLower(strings.TrimSpace(reason))
	switch value {
	case "", "stop":
		return "stop"
	case "tool-calls", "tool_calls", "tool_use":
		return "tool_calls"
	case "length", "max_tokens", "max_output_tokens", "model_context_window_exceeded":
		return "length"
	case "network-error", "network_error", "connection-error", "connection_error", "upstream-error", "upstream_error":
		return "upstream_error"
	default:
		return value
	}
}

func pluginErrorForHTTP(status int, body []byte) *pluginabi.Error {
	mappedStatus := status
	code := "upstream_error"
	typeName := "upstream_error"
	switch status {
	case 400, 422:
		mappedStatus, typeName = 400, "invalid_request_error"
	case 401, 403:
		mappedStatus, typeName = 401, "authentication_error"
	case 402, 429:
		mappedStatus, typeName = 429, "rate_limit_error"
	case 404:
		mappedStatus, typeName = 404, "not_found"
	case 503:
		mappedStatus, typeName = 503, "temporarily_unavailable"
	case 500, 502:
		mappedStatus, typeName = 502, "upstream_error"
	default:
		if mappedStatus < 400 {
			mappedStatus = 502
		}
	}
	message := fmt.Sprintf("Command Code API error (%d)", status)
	var decoded map[string]any
	if json.Unmarshal(body, &decoded) == nil {
		if nested := mapValue(decoded["error"]); nested != nil {
			if value := stringValue(nested["message"]); value != "" {
				message = value
			}
			if value := stringValue(nested["code"]); value != "" {
				code = value
			}
		} else if value := stringValue(decoded["message"]); value != "" {
			message = value
		}
	} else if text := strings.TrimSpace(string(body)); text != "" {
		message = text
	}
	if code == "upstream_error" {
		code = typeName
	}
	return pluginabi.NewError(code, message, mappedStatus)
}

func mapCCEventError(event map[string]any) *pluginabi.Error {
	message := stringValue(event["message"])
	code := stringValue(event["code"])
	status := 502
	if nested := mapValue(event["error"]); nested != nil {
		if value := stringValue(nested["message"]); value != "" {
			message = value
		}
		if value := stringValue(nested["code"]); value != "" {
			code = value
		}
		status = intValue(nested["statusCode"], status)
	}
	if strings.HasPrefix(message, "<") && len(message) >= 5 {
		if parsed, err := strconv.Atoi(message[1:4]); err == nil {
			status = parsed
		}
	}
	if message == "" {
		message = "Unknown Command Code error"
	}
	result := pluginErrorForHTTP(status, []byte(fmt.Sprintf(`{"error":{"message":%q,"code":%q}}`, message, code)))
	if code != "" {
		result.Code = code
	}
	return result
}

func protocolErrorType(err error) string {
	var pluginErr *pluginabi.Error
	if !errors.As(err, &pluginErr) || pluginErr == nil {
		return "upstream_error"
	}
	switch pluginErr.HTTPStatus {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable:
		return "temporarily_unavailable"
	default:
		return "upstream_error"
	}
}

func incompleteError(detail string) *pluginabi.Error {
	if detail == "" {
		detail = "no finish event"
	}
	return pluginabi.NewError("upstream_error", "Upstream stream ended without a completion finish ("+detail+") — response was truncated", http.StatusBadGateway)
}

func incompleteDetail(collected ccCollected) string {
	if !collected.SawFinish {
		return "no finish event"
	}
	if collected.FinishReason == "upstream_error" {
		return "provider reported an upstream connection failure"
	}
	return ""
}

func anthropicInputTokens(usage ccUsage) int {
	if usage.HasNoCacheTokens {
		return usage.NoCacheTokens
	}
	return maxInt(0, usage.InputTokens-usage.CachedInputTokens-usage.CacheWriteTokens)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func fakeThinkingSignature(text string) string {
	seed := sha256.Sum256([]byte(text))
	raw := append([]byte{0x12, byte(len(seed))}, seed[:]...)
	return base64.StdEncoding.EncodeToString(raw)
}

func anthropicStopReason(reason string) string {
	switch reason {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "pause_turn":
		return "pause_turn"
	case "refusal":
		return "refusal"
	default:
		return "end_turn"
	}
}

func toOpenAIFinishReason(reason string) string {
	if reason == "pause_turn" {
		return "length"
	}
	return reason
}

func formatSSE(event string, data any) []byte {
	payload, _ := json.Marshal(data)
	return []byte("event: " + event + "\ndata: " + string(payload) + "\n\n")
}

func formatChatSSE(id string, created int64, model string, delta map[string]any, finishReason string, usage map[string]any) []byte {
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{choice}}
	if usage != nil {
		chunk["usage"] = usage
	}
	payload, _ := json.Marshal(chunk)
	return []byte("data: " + string(payload) + "\n\n")
}
