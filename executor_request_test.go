package main

import "testing"

func TestCommandCodeRequestNormalizesToolNamesAcrossFormats(t *testing.T) {
	toolNames := []string{"bash_output", "task_output", "tool_search", "read_multiple_files", "lookup"}
	wireNames := map[string]string{
		"bash_output":         "shell_output",
		"task_output":         "shell_output",
		"tool_search":         "search_tools",
		"read_multiple_files": "read_file",
		"lookup":              "lookup",
	}

	chatTools := make([]any, 0, len(toolNames))
	responseTools := make([]any, 0, len(toolNames))
	anthropicTools := make([]any, 0, len(toolNames))
	for _, name := range toolNames {
		chatTools = append(chatTools, map[string]any{
			"type": "function", "function": map[string]any{"name": name},
		})
		responseTools = append(responseTools, map[string]any{"type": "function", "name": name})
		anthropicTools = append(anthropicTools, map[string]any{"name": name})
	}

	formats := []struct {
		name   string
		openai map[string]any
	}{
		{
			name: "Chat",
			openai: map[string]any{
				"model": "test-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "tools": chatTools,
			},
		},
		{
			name: "Responses",
			openai: convertResponsesRequest(map[string]any{
				"model": "test-model", "input": "hello", "tools": responseTools,
			}, func() string { return "test-id" }),
		},
		{
			name: "Anthropic",
			openai: convertAnthropicRequest(map[string]any{
				"model": "test-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "tools": anthropicTools,
			}),
		},
	}

	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			params := mapValue(buildCommandCodeRequest(format.openai)["params"])
			tools := sliceValue(params["tools"])
			if len(tools) != len(toolNames) {
				t.Fatalf("upstream tool count = %d, want %d: %#v", len(tools), len(toolNames), tools)
			}
			for index, rawTool := range tools {
				tool := mapValue(rawTool)
				if got, want := stringValue(tool["name"]), wireNames[toolNames[index]]; got != want {
					t.Errorf("upstream tool %d name = %q, want %q", index, got, want)
				}
			}
		})
	}
}

func TestCommandCodeRequestFallsBackUnknownToolChoiceToAuto(t *testing.T) {
	formats := []struct {
		name  string
		build func(string) map[string]any
	}{
		{
			name: "Chat",
			build: func(choice string) map[string]any {
				return map[string]any{
					"model": "test-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "tool_choice": choice,
				}
			},
		},
		{
			name: "Responses",
			build: func(choice string) map[string]any {
				return convertResponsesRequest(map[string]any{
					"model": "test-model", "input": "hello", "tool_choice": choice,
				}, func() string { return "test-id" })
			},
		},
	}

	choices := []struct {
		input string
		want  string
	}{
		{input: "auto", want: "auto"},
		{input: "none", want: "none"},
		{input: "required", want: "any"},
		{input: "unsupported", want: "auto"},
	}
	for _, format := range formats {
		for _, choice := range choices {
			t.Run(format.name+"/"+choice.input, func(t *testing.T) {
				params := mapValue(buildCommandCodeRequest(format.build(choice.input))["params"])
				toolChoice := mapValue(params["tool_choice"])
				if got := stringValue(toolChoice["type"]); got != choice.want {
					t.Fatalf("upstream tool_choice.type = %q, want %q: %#v", got, choice.want, toolChoice)
				}
			})
		}
	}

	params := mapValue(buildCommandCodeRequest(map[string]any{
		"model": "test-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "original_name"}},
	})["params"])
	toolChoice := mapValue(params["tool_choice"])
	if toolChoice["type"] != "tool" || toolChoice["name"] != "original_name" {
		t.Fatalf("object tool_choice changed function selection: %#v", toolChoice)
	}
}
