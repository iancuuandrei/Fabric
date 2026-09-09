package codexruntime

import (
	"context"
	"encoding/json"
	"errors"

	"harness.local/engorch/internal/candidatetools"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/sourcetools"
)

// ToolRequest records provider correlation and arguments before a source read.
type ToolRequest struct {
	Arguments json.RawMessage `json:"arguments"`
	CallID    string          `json:"callId"`
	Namespace *string         `json:"namespace,omitempty"`
	ThreadID  string          `json:"threadId"`
	Tool      string          `json:"tool"`
	TurnID    string          `json:"turnId"`
}

// ToolResponse records exactly the content made available to the provider.
type ToolResponse struct {
	CallID  string `json:"call_id"`
	Success bool   `json:"success"`
	Content string `json:"content"`
}

// SourceTools returns the explicit immutable-source tool catalog for this adapter.
func (a *Adapter) SourceTools() []any {
	if a.Source == nil {
		return nil
	}
	object := func(properties map[string]any, required []string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	tools := []any{}
	for _, definition := range sourcetools.Catalog() {
		tools = append(tools, map[string]any{"type": "function", "deferLoading": false, "name": definition.Name, "description": definition.Description, "inputSchema": definition.InputSchema})
	}
	if a.Lexical != nil {
		tools = append(tools, lexicalTool())
	}
	if a.RI != nil {
		tools = append(tools, semanticTools()...)
		tools = append(tools, map[string]any{"type": "function", "deferLoading": false, "name": "ri_status", "description": "Read counts and exact provenance from the fixed RI snapshot. Does not run an indexer or prove graph completeness.", "inputSchema": object(map[string]any{}, []string{})})
	}
	if a.Candidate != nil {
		for _, definition := range candidatetools.Catalog() {
			tools = append(tools, map[string]any{"type": "function", "deferLoading": false, "name": definition.Name, "description": definition.Description, "inputSchema": definition.InputSchema})
		}
	}
	return tools
}

// HandleTool binds source requests to the recorded invocation and thread. It
// records both intent and response before sending bytes, and grants no mutations.
func (a *Adapter) HandleTool(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	return a.handleTool(ctx, raw, lexicalRead)
}

func (a *Adapter) handleTool(ctx context.Context, raw json.RawMessage, readLexical func(context.Context, LexicalBinding, json.RawMessage) (any, error)) (json.RawMessage, error) {
	if len(raw) > 16<<10 {
		return nil, errors.New("tool request exceeds bound")
	}
	var request ToolRequest
	if err := canonical.Decode(raw, &request); err != nil {
		return nil, err
	}
	if err := appendEvent(a.JournalPath, "runtime.tool-request", request); err != nil {
		return nil, err
	}
	s, err := Inspect(a.JournalPath)
	if err != nil {
		return nil, err
	}
	var content any
	var readErr error
	switch request.Tool {
	case "ri_search":
		binding := s.Lexical
		if s.LexicalRecord != nil {
			var candidate string
			candidate, readErr = s.lexicalCandidate()
			if readErr == nil {
				var hydrated LexicalBinding
				hydrated, readErr = s.LexicalRecord.Hydrate(ctx, *s.Source, candidate)
				binding = &hydrated
			}
		}
		if readErr == nil {
			content, readErr = readLexical(ctx, *binding, request.Arguments)
		}
	case candidatetools.ListName, candidatetools.ReadName:
		content, _, readErr = candidatetools.Execute(ctx, *s.Candidate, request.Tool, request.Arguments)
	case "ri_locate", "ri_definition", "ri_references":
		content, readErr = semanticRead(ctx, *s.RI, request.Tool, request.Arguments)
	case "ri_status":
		var args struct{}
		readErr = canonical.Decode(request.Arguments, &args)
		if readErr == nil {
			binding := s.RI
			content, readErr = (ri.Client{Executable: binding.Executable, ExecutableHash: binding.ExecutableSHA256}).Inspect(ctx, binding.Snapshot)
		}
	case "source_list", "source_read":
		content, _, readErr = sourcetools.Execute(ctx, *s.Source, request.Tool, request.Arguments)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if readErr != nil {
		content = map[string]any{"error": "context request failed; check arguments, supported path kind and bounds"}
	}
	encoded, err := canonical.Bytes(content)
	if err != nil {
		return nil, err
	}
	response := ToolResponse{request.CallID, readErr == nil, string(encoded)}
	if err := appendEvent(a.JournalPath, "runtime.tool-response", response); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"success": response.Success, "contentItems": []any{map[string]any{"type": "inputText", "text": response.Content}}})
}

func (s *State) toolEvent(kind string, payload json.RawMessage) error {
	switch kind {
	case "runtime.lexical-record":
		if s.Intent == nil || s.Source == nil || s.Thread != nil || s.Lexical != nil || s.LexicalRecord != nil {
			return errors.New("compact runtime lexical transition rejected")
		}
		var record LexicalRecord
		if err := canonical.Decode(payload, &record); err != nil {
			return err
		}
		candidate, err := s.lexicalCandidate()
		if err != nil {
			return err
		}
		if err := record.Validate(*s.Source, candidate); err != nil {
			return err
		}
		s.LexicalRecord = &record
	case "runtime.lexical":
		if s.Intent == nil || s.Source == nil || s.Thread != nil || s.Lexical != nil || s.LexicalRecord != nil {
			return errors.New("runtime lexical binding transition rejected")
		}
		var binding LexicalBinding
		if err := canonical.Decode(payload, &binding); err != nil {
			return err
		}
		candidate := ""
		if s.Candidate != nil {
			var err error
			candidate, err = s.Candidate.Candidate.ID()
			if err != nil {
				return err
			}
		}
		if _, err := binding.ID(*s.Source, candidate); err != nil {
			return err
		}
		s.Lexical = &binding
	case "runtime.candidate":
		if s.Intent == nil || s.Source == nil || s.Thread != nil || s.Candidate != nil {
			return errors.New("runtime candidate binding transition rejected")
		}
		var binding CandidateBinding
		if err := canonical.Decode(payload, &binding); err != nil {
			return err
		}
		if err := binding.Validate(*s.Source); err != nil {
			return err
		}
		s.Candidate = &binding
	case "runtime.ri":
		if s.Intent == nil || s.Source == nil || s.Thread != nil || s.RI != nil {
			return errors.New("runtime RI binding transition rejected")
		}
		var binding RIBinding
		if err := canonical.Decode(payload, &binding); err != nil {
			return err
		}
		if err := binding.Validate(*s.Source); err != nil {
			return err
		}
		s.RI = &binding
	case "runtime.source":
		if s.Intent == nil || s.Thread != nil || s.Source != nil {
			return errors.New("source binding transition rejected")
		}
		var source repository.Identity
		if err := canonical.Decode(payload, &source); err != nil {
			return err
		}
		if err := source.Validate(); err != nil {
			return err
		}
		s.Source = &source
	case "runtime.tool-request":
		if s.Source == nil || s.Thread == nil || !s.TurnPending || s.Result != nil || s.TurnStatus != "" && s.TurnStatus != "inProgress" || s.PendingTool != nil || len(s.ToolResponses) >= 512 {
			return errors.New("tool request transition rejected")
		}
		var request ToolRequest
		if err := canonical.Decode(payload, &request); err != nil {
			return err
		}
		if request.Namespace != nil || request.ThreadID != s.Thread.ThreadID || request.CallID == "" || len(request.CallID) > 256 || request.TurnID == "" || len(request.TurnID) > 256 || request.Tool != "source_read" && request.Tool != "source_list" && !(request.Tool == "ri_search" && (s.Lexical != nil || s.LexicalRecord != nil)) && !(riTool(request.Tool) && s.RI != nil) && !((request.Tool == candidatetools.ListName || request.Tool == candidatetools.ReadName) && s.Candidate != nil) {
			return errors.New("tool scope mismatch")
		}
		if s.TurnID != "" && request.TurnID != s.TurnID || s.ToolTurnID != "" && request.TurnID != s.ToolTurnID {
			return errors.New("tool turn substitution")
		}
		for _, previous := range s.ToolResponses {
			if previous.CallID == request.CallID {
				return errors.New("duplicate tool call")
			}
		}
		s.ToolTurnID = request.TurnID
		s.PendingTool = &request
	case "runtime.tool-response":
		var response ToolResponse
		if err := canonical.Decode(payload, &response); err != nil {
			return err
		}
		if s.PendingTool == nil || s.PendingTool.CallID != response.CallID || len(response.Content) > 768<<10 {
			return errors.New("tool response identity or size mismatch")
		}
		if _, err := canonical.Normalize([]byte(response.Content)); err != nil {
			return err
		}
		total := len(response.Content)
		for _, previous := range s.ToolResponses {
			total += len(previous.Content)
		}
		if total > 8<<20 {
			return errors.New("tool response budget exceeded")
		}
		s.ToolResponses = append(s.ToolResponses, response)
		s.PendingTool = nil
	}
	return nil
}
