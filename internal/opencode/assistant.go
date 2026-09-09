package opencode

import (
	"encoding/json"
	"errors"
	"strings"

	"harness.local/engorch/internal/canonical"
)

// Binding is the controller-selected identity expected from message readback.
// Empty variant means the response must not select a variant implicitly.
type Binding struct {
	SessionID string `json:"session_id"`
	ParentID  string `json:"parent_id"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Agent     string `json:"agent"`
	Directory string `json:"directory"`
	Root      string `json:"root"`
	Variant   string `json:"variant"`
}

// Assistant is a validated final-message metadata projection, not an admitted
// invocation result. Text parts and whole-turn completeness need separate checks.
// Monetary cost is deliberately excluded pending provider-specific accounting.
type Assistant struct {
	ID              string  `json:"id"`
	Binding         Binding `json:"binding"`
	Created         int64   `json:"created"`
	Completed       int64   `json:"completed"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CacheRead       int64   `json:"cache_read"`
	CacheWrite      int64   `json:"cache_write"`
}

func field(m map[string]json.RawMessage, key string, dst any) error {
	raw, ok := m[key]
	if !ok {
		return errors.New("required OpenCode field missing")
	}
	if err := canonical.Decode(raw, dst); err != nil {
		return errors.New("invalid OpenCode field")
	}
	return nil
}

// DecodeAssistant admits only a stop-finished assistant matching exact binding.
// A completion timestamp on tool-calls, length or unknown is insufficient. Unknown
// wire fields are allowed after full ambiguity checks; required keys are exact.
func DecodeAssistant(raw []byte, expected Binding) (Assistant, error) {
	var a Assistant
	for _, value := range []string{expected.SessionID, expected.ParentID, expected.Provider, expected.Model, expected.Agent, expected.Directory, expected.Root} {
		if strings.TrimSpace(value) == "" || len(value) > 4096 {
			return a, errors.New("invalid expected OpenCode binding")
		}
	}
	m, err := wireObject(raw)
	if err != nil {
		return a, err
	}
	a.Binding = expected
	for key, want := range map[string]string{"sessionID": expected.SessionID, "parentID": expected.ParentID, "providerID": expected.Provider, "modelID": expected.Model, "agent": expected.Agent, "role": "assistant", "finish": "stop"} {
		var got string
		if field(m, key, &got) != nil || got != want {
			return Assistant{}, errors.New("OpenCode message binding or finish mismatch")
		}
	}
	if field(m, "id", &a.ID) != nil || strings.TrimSpace(a.ID) == "" || len(a.ID) > 256 {
		return Assistant{}, errors.New("invalid OpenCode message ID")
	}
	if _, ok := m["error"]; ok {
		return Assistant{}, errors.New("OpenCode message carries error")
	}
	if _, ok := m["summary"]; ok {
		var summary bool
		if field(m, "summary", &summary) != nil || summary {
			return Assistant{}, errors.New("summary is not final task output")
		}
	}
	var variant string
	if _, ok := m["variant"]; ok {
		if field(m, "variant", &variant) != nil {
			return Assistant{}, errors.New("invalid variant")
		}
	}
	if variant != expected.Variant {
		return Assistant{}, errors.New("OpenCode variant substitution")
	}
	var path struct {
		Cwd  string `json:"cwd"`
		Root string `json:"root"`
	}
	if field(m, "path", &path) != nil || path.Cwd != expected.Directory || path.Root != expected.Root {
		return Assistant{}, errors.New("OpenCode workspace substitution")
	}
	var timing struct {
		Created   *int64 `json:"created"`
		Completed *int64 `json:"completed"`
	}
	if field(m, "time", &timing) != nil || timing.Created == nil || timing.Completed == nil || *timing.Created < 0 || *timing.Completed < *timing.Created || *timing.Completed > 9007199254740991 {
		return Assistant{}, errors.New("incomplete OpenCode message time")
	}
	a.Created, a.Completed = *timing.Created, *timing.Completed
	var tokens struct {
		Total     *int64 `json:"total,omitempty"`
		Input     *int64 `json:"input"`
		Output    *int64 `json:"output"`
		Reasoning *int64 `json:"reasoning"`
		Cache     *struct {
			Read  *int64 `json:"read"`
			Write *int64 `json:"write"`
		} `json:"cache"`
	}
	if field(m, "tokens", &tokens) != nil || tokens.Cache == nil {
		return Assistant{}, errors.New("invalid OpenCode token accounting")
	}
	for _, n := range []*int64{tokens.Input, tokens.Output, tokens.Reasoning, tokens.Cache.Read, tokens.Cache.Write} {
		if n == nil || *n < 0 || *n > 9007199254740991 {
			return Assistant{}, errors.New("missing or invalid token accounting")
		}
	}
	if tokens.Total != nil && (*tokens.Total < 0 || *tokens.Total > 9007199254740991) {
		return Assistant{}, errors.New("invalid total token accounting")
	}
	a.InputTokens, a.OutputTokens, a.ReasoningTokens, a.CacheRead, a.CacheWrite = *tokens.Input, *tokens.Output, *tokens.Reasoning, *tokens.Cache.Read, *tokens.Cache.Write
	return a, nil
}
