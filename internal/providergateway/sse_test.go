package providergateway

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseChatCompletionSSEStopWithCompleteUsage(t *testing.T) {
	raw := stopSSE(`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}`)
	observation, err := ParseChatCompletionSSE(raw, len(raw), 10)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if observation.SHA256 != hex.EncodeToString(hash[:]) || observation.SizeBytes != len(raw) || observation.ResponseID != "chatcmpl-fixture" || observation.Model != "wire-model" || observation.FinishReason != "stop" || len(observation.ToolCalls) != 0 {
		t.Fatal("unexpected stop observation", observation)
	}
	if observation.Usage.InputTokens != 7 || observation.Usage.OutputTokens != 3 || observation.Usage.CacheReadTokens == nil || *observation.Usage.CacheReadTokens != 2 || observation.Usage.ReasoningTokens == nil || *observation.Usage.ReasoningTokens != 1 || observation.Usage.CacheWriteTokens != nil {
		t.Fatal("usage categories were not preserved", observation.Usage)
	}
}

func TestParseChatCompletionSSEToolCallDeltas(t *testing.T) {
	raw := toolSSE()
	observation, err := ParseChatCompletionSSE(raw, 64<<10, 13)
	if err != nil {
		t.Fatal(err)
	}
	if observation.FinishReason != "tool_calls" || len(observation.ToolCalls) != 1 {
		t.Fatal("tool finish was not admitted", observation)
	}
	call := observation.ToolCalls[0]
	if call.Index != 0 || call.ID != "call_fixture_1" || call.Name != "engorch_source_read" || string(call.Arguments) != `{"limit":64,"path":"source.txt"}` {
		t.Fatal("fragmented tool call was not reconstructed exactly", call)
	}
	if observation.Usage.CacheReadTokens != nil || observation.Usage.ReasoningTokens != nil {
		t.Fatal("missing usage details were normalized to zero", observation.Usage)
	}
}

func TestParseChatCompletionSSEPreservesExplicitZeroDetails(t *testing.T) {
	raw := stopSSE(`{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}`)
	observation, err := ParseChatCompletionSSE(raw, len(raw), 1)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Usage.CacheReadTokens == nil || *observation.Usage.CacheReadTokens != 0 || observation.Usage.ReasoningTokens == nil || *observation.Usage.ReasoningTokens != 0 {
		t.Fatal("explicit zero details were lost", observation.Usage)
	}
}

func TestParseChatCompletionSSEAcceptsCRLFOnlyAsCompleteLineEndings(t *testing.T) {
	raw := []byte(strings.ReplaceAll(string(stopSSE(`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`)), "\n", "\r\n"))
	if _, err := ParseChatCompletionSSE(raw, len(raw), 10); err != nil {
		t.Fatal(err)
	}
}

func TestParseChatCompletionSSERejectsIncompleteAmbiguousAndSubstitutedStreams(t *testing.T) {
	validUsage := `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`
	valid := string(stopSSE(validUsage))
	tests := map[string][]byte{
		"missing usage":         []byte(strings.Replace(valid, sseData(usageChunk(validUsage)), "", 1)),
		"missing done":          []byte(strings.Replace(valid, "data: [DONE]\n\n", "", 1)),
		"truncated terminator":  []byte(strings.TrimSuffix(valid, "\n")),
		"duplicate usage":       []byte(strings.Replace(valid, "data: [DONE]", sseData(usageChunk(validUsage))+"data: [DONE]", 1)),
		"trailing event":        []byte(valid + sseData(`{"x":1}`)),
		"response substitution": []byte(strings.Replace(valid, `"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[],"usage"`, `"chatcmpl-foreign","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[],"usage"`, 1)),
		"model substitution":    []byte(strings.Replace(valid, `"model":"wire-model","choices":[]`, `"model":"foreign-model","choices":[]`, 1)),
		"created substitution":  []byte(strings.Replace(valid, `"created":1,"model":"wire-model","choices":[]`, `"created":2,"model":"wire-model","choices":[]`, 1)),
		"duplicate finish":      []byte(strings.Replace(valid, sseData(usageChunk(validUsage)), sseData(finishChunk("stop"))+sseData(usageChunk(validUsage)), 1)),
		"unsupported finish":    []byte(strings.Replace(valid, `"finish_reason":"stop"`, `"finish_reason":"length"`, 1)),
		"unknown chunk member":  []byte(strings.Replace(valid, `"choices":[`, `"foreign":true,"choices":[`, 1)),
		"duplicate JSON member": []byte(strings.Replace(valid, `"model":"wire-model"`, `"model":"wire-model","model":"wire-model"`, 1)),
		"SSE comment":           []byte(strings.Replace(valid, "data: ", ": comment\ndata: ", 1)),
		"bare carriage return":  []byte(strings.Replace(valid, "data: ", "data:\r ", 1)),
		"empty ordinary delta":  stream(roleChunk(), ordinaryChunk(`{}`), finishChunk("stop"), usageChunk(validUsage)),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseChatCompletionSSE(raw, 1<<20, 100); err == nil {
				t.Fatal("invalid provider stream admitted")
			}
		})
	}
}

func TestParseChatCompletionSSERejectsInvalidUsageAndBounds(t *testing.T) {
	tests := map[string]string{
		"missing prompt":           `{"completion_tokens":3,"total_tokens":3}`,
		"missing completion":       `{"prompt_tokens":7,"total_tokens":7}`,
		"missing total":            `{"prompt_tokens":7,"completion_tokens":3}`,
		"negative":                 `{"prompt_tokens":-1,"completion_tokens":3,"total_tokens":2}`,
		"fraction":                 `{"prompt_tokens":7.5,"completion_tokens":3,"total_tokens":10}`,
		"string":                   `{"prompt_tokens":"7","completion_tokens":3,"total_tokens":10}`,
		"total mismatch":           `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":11}`,
		"cached exceeds input":     `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":8}}`,
		"reasoning exceeds output": `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"completion_tokens_details":{"reasoning_tokens":4}}`,
		"unknown detail":           `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_tokens_details":{"audio_tokens":1}}`,
	}
	for name, usage := range tests {
		t.Run(name, func(t *testing.T) {
			raw := stopSSE(usage)
			if _, err := ParseChatCompletionSSE(raw, len(raw), 100); err == nil {
				t.Fatal("invalid usage admitted")
			}
		})
	}
	t.Run("inclusive total bound", func(t *testing.T) {
		raw := stopSSE(`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`)
		if _, err := ParseChatCompletionSSE(raw, len(raw), 9); err == nil {
			t.Fatal("usage above inclusive total bound admitted")
		}
	})
	t.Run("byte bound", func(t *testing.T) {
		raw := stopSSE(`{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`)
		if _, err := ParseChatCompletionSSE(raw, len(raw)-1, 10); err == nil {
			t.Fatal("oversized stream admitted")
		}
	})
}

func TestParseChatCompletionSSERejectsMalformedToolCalls(t *testing.T) {
	base := string(toolSSE())
	tests := map[string][]byte{
		"stop mismatch":      []byte(strings.Replace(base, `"finish_reason":"tool_calls"`, `"finish_reason":"stop"`, 1)),
		"missing call":       stream(roleChunk(), finishChunk("tool_calls"), usageChunk(`{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}`)),
		"bad index":          []byte(strings.Replace(base, `"index":0,"id":"call_fixture_1"`, `"index":1,"id":"call_fixture_1"`, 1)),
		"ID substitution":    []byte(strings.Replace(base, `"index":0,"function":{"arguments":"64,`, `"index":0,"id":"foreign","function":{"arguments":"64,`, 1)),
		"duplicate name":     []byte(strings.Replace(base, `"function":{"arguments":"64,`, `"function":{"name":"engorch_source_read","arguments":"64,`, 1)),
		"invalid name":       []byte(strings.Replace(base, "engorch_source_read", "engorch.source/read", 1)),
		"invalid arguments":  []byte(strings.Replace(base, `64,\"path\":\"source.txt\"}`, `64,}`, 1)),
		"duplicate argument": []byte(strings.Replace(base, `64,\"path\":\"source.txt\"}`, `64,\"path\":\"source.txt\",\"path\":\"other\"}`, 1)),
		"mixed text":         []byte(strings.Replace(base, `"content":null`, `"content":"text"`, 1)),
		"text after tool":    []byte(strings.Replace(base, sseData(finishChunk("tool_calls")), sseData(ordinaryChunk(`{"content":"text"}`))+sseData(finishChunk("tool_calls")), 1)),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseChatCompletionSSE(raw, 1<<20, 100); err == nil {
				t.Fatal("malformed tool stream admitted")
			}
		})
	}
}

func TestParseChatCompletionSSEValidatesEscapedSurrogates(t *testing.T) {
	usage := `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}`
	validStop := string(stopSSE(usage))
	invalid := map[string][]byte{
		"response ID high": []byte(strings.ReplaceAll(validStop, "chatcmpl-fixture", `chatcmpl-\ud800`)),
		"model low":        []byte(strings.ReplaceAll(validStop, "wire-model", `wire-\udc00`)),
		"content reversed": []byte(strings.Replace(validStop, "synthetic provider response", `\udc00\ud800`, 1)),
		"tool arguments": stream(
			ordinaryChunk(`{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_fixture_1","type":"function","function":{"name":"engorch_source_read","arguments":"{\"value\":\"\\ud800\"}"}}]}`),
			finishChunk("tool_calls"), usageChunk(usage),
		),
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseChatCompletionSSE(raw, len(raw), 2); err == nil {
				t.Fatal("unpaired escaped surrogate was admitted")
			}
		})
	}

	t.Run("paired identifier and content", func(t *testing.T) {
		raw := []byte(strings.ReplaceAll(strings.Replace(validStop, "synthetic provider response", `paired \ud83d\ude00`, 1), "chatcmpl-fixture", `chatcmpl-\ud83d\ude00`))
		observation, err := ParseChatCompletionSSE(raw, len(raw), 2)
		if err != nil || observation.ResponseID != "chatcmpl-😀" {
			t.Fatal("valid surrogate pair was rejected", observation, err)
		}
	})
	t.Run("paired tool argument", func(t *testing.T) {
		raw := stream(
			ordinaryChunk(`{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_fixture_1","type":"function","function":{"name":"engorch_source_read","arguments":"{\"value\":\"\\ud83d\\ude00\"}"}}]}`),
			finishChunk("tool_calls"), usageChunk(usage),
		)
		observation, err := ParseChatCompletionSSE(raw, len(raw), 2)
		if err != nil || len(observation.ToolCalls) != 1 || string(observation.ToolCalls[0].Arguments) != `{"value":"\ud83d\ude00"}` {
			t.Fatal("valid surrogate pair in arguments was not preserved", observation, err)
		}
	})
}

func TestParseChatCompletionSSERejectsCaseInsensitiveSchemaAliases(t *testing.T) {
	usage := `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}`
	validStop := string(stopSSE(usage))
	tool := string(toolSSE())
	tests := map[string][]byte{
		"top-level alias":        []byte(strings.Replace(validStop, `"model":"wire-model"`, `"Model":"wire-model"`, 1)),
		"alias overwrites exact": []byte(strings.Replace(validStop, `"model":"wire-model"`, `"model":"wire-model","Model":"foreign"`, 1)),
		"choice alias":           []byte(strings.Replace(validStop, `"finish_reason":null`, `"Finish_Reason":null`, 1)),
		"delta alias":            []byte(strings.Replace(validStop, `"content":"synthetic`, `"Content":"synthetic`, 1)),
		"usage alias":            []byte(strings.Replace(validStop, `"prompt_tokens":1`, `"Prompt_Tokens":1`, 1)),
		"tool-call alias":        []byte(strings.Replace(tool, `"tool_calls":[`, `"Tool_Calls":[`, 1)),
		"function alias":         []byte(strings.Replace(tool, `"name":"engorch_source_read"`, `"Name":"engorch_source_read"`, 1)),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseChatCompletionSSE(raw, len(raw), 100); err == nil {
				t.Fatal("case-insensitive provider schema alias was admitted")
			}
		})
	}
}

func stopSSE(usage string) []byte {
	return stream(
		ordinaryChunk(`{"role":"assistant","content":"synthetic provider response"}`),
		finishChunk("stop"),
		usageChunk(usage),
	)
}

func toolSSE() []byte {
	return stream(
		ordinaryChunk(`{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_fixture_1","type":"function","function":{"name":"engorch_source_read","arguments":"{\"limit\":"}}]}`),
		ordinaryChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"64,\"path\":\"source.txt\"}"}}]}`),
		finishChunk("tool_calls"),
		usageChunk(`{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}`),
	)
}

func roleChunk() string { return ordinaryChunk(`{"role":"assistant"}`) }

func ordinaryChunk(delta string) string {
	return `{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}`
}

func finishChunk(reason string) string {
	return `{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[{"index":0,"delta":{},"finish_reason":"` + reason + `"}]}`
}

func usageChunk(usage string) string {
	return `{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"wire-model","choices":[],"usage":` + usage + `}`
}

func stream(chunks ...string) []byte {
	var builder strings.Builder
	for _, chunk := range chunks {
		builder.WriteString(sseData(chunk))
	}
	builder.WriteString("data: [DONE]\n\n")
	return []byte(builder.String())
}

func sseData(value string) string { return "data: " + value + "\n\n" }
