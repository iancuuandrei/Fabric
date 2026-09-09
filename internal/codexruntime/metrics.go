package codexruntime

import (
	"errors"
	"sort"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
)

// ToolUsage accounts exact journaled UTF-8 bytes, not estimated model tokens.
type ToolUsage struct {
	Tool          string `json:"tool"`
	Requests      int    `json:"requests"`
	Responses     int    `json:"responses"`
	Failures      int    `json:"failures"`
	ArgumentBytes int    `json:"argument_bytes"`
	ContentBytes  int    `json:"content_bytes"`
}

// ContextUsage reports one journal-bound invocation without exposing prompt/body
// content. ProviderUsage remains unknown until a result reports those quantities.
type ContextUsage struct {
	JournalHead      string          `json:"journal_head"`
	InvocationID     string          `json:"invocation_id"`
	Requested        runtime.Profile `json:"requested"`
	Completed        bool            `json:"completed"`
	InputBytes       int             `json:"input_bytes"`
	OutputBytes      int             `json:"output_bytes"`
	ToolContentBytes int             `json:"tool_content_bytes"`
	PendingCalls     int             `json:"pending_calls"`
	Tools            []ToolUsage     `json:"tools"`
	ProviderUsage    runtime.Usage   `json:"provider_usage"`
}

// MeasureContext computes reproducible accounting from one validated journal read.
// Content bytes include structured tool result wrappers and base64 representations.
// They exclude provider-added prompts, hidden context, tokenizer effects and cost.
func MeasureContext(path string) (ContextUsage, error) {
	events, err := journal.Read(path)
	if err != nil {
		return ContextUsage{}, err
	}
	s, err := replay(events)
	if err != nil {
		return ContextUsage{}, err
	}
	if s.Intent == nil || len(events) == 0 {
		return ContextUsage{}, errors.New("runtime invocation required for context accounting")
	}
	report := ContextUsage{JournalHead: events[len(events)-1].Hash, InvocationID: s.Intent.Invocation.ID, Requested: s.Intent.Invocation.Profile, Completed: s.Result != nil, InputBytes: len(s.Intent.Invocation.Input), Tools: []ToolUsage{}}
	byTool := map[string]*ToolUsage{}
	byCall := map[string]string{}
	for _, e := range events {
		if e.Kind != "runtime.tool-request" {
			continue
		}
		var request ToolRequest
		if err := canonical.Decode(e.Payload, &request); err != nil {
			return ContextUsage{}, err
		}
		byCall[request.CallID] = request.Tool
		counter := byTool[request.Tool]
		if counter == nil {
			counter = &ToolUsage{Tool: request.Tool}
			byTool[request.Tool] = counter
		}
		counter.Requests++
		counter.ArgumentBytes += len(request.Arguments)
	}
	for _, response := range s.ToolResponses {
		counter := byTool[byCall[response.CallID]]
		if counter == nil {
			return ContextUsage{}, errors.New("tool response lacks a request")
		}
		counter.Responses++
		if !response.Success {
			counter.Failures++
		}
		counter.ContentBytes += len(response.Content)
		report.ToolContentBytes += len(response.Content)
	}
	if s.PendingTool != nil {
		report.PendingCalls = 1
	}
	if s.Result != nil {
		report.OutputBytes = len(s.Result.Output)
		report.ProviderUsage = s.Result.Usage
	}
	for _, counter := range byTool {
		report.Tools = append(report.Tools, *counter)
	}
	sort.Slice(report.Tools, func(i, j int) bool { return report.Tools[i].Tool < report.Tools[j].Tool })
	return report, nil
}
