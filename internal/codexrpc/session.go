package codexrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/writercontract"
)

// ThreadSettings records selected server settings, not proof of OS isolation.
type ThreadSettings struct {
	ThreadID         string  `json:"thread_id"`
	Model            string  `json:"model"`
	Provider         string  `json:"provider"`
	Effort           *string `json:"effort"`
	ServiceTier      *string `json:"service_tier,omitempty"`
	Directory        string  `json:"directory"`
	Approval         string  `json:"approval"`
	Sandbox          string  `json:"sandbox"`
	Network          bool    `json:"network"`
	Role             string  `json:"role,omitempty"`
	DynamicToolsHash string  `json:"dynamic_tools_hash,omitempty"`
}

var (
	// ErrContinuationRouteMismatch blocks continuation without revising dispatch evidence.
	ErrContinuationRouteMismatch = errors.New("continuation route mismatch")
	// ErrRouteIdentityUnknown means the provider did not report enough current
	// route identity to safely recover a thread.
	ErrRouteIdentityUnknown = errors.New("route identity unknown")
	// ErrRouteIdentityContradiction means provider evidence conflicts with the
	// exact recorded route or execution identity.
	ErrRouteIdentityContradiction = errors.New("route identity contradiction")
)

// RerouteError preserves the exact provider notification that contradicted the
// recorded route. Event is provider evidence, not harness authority.
type RerouteError struct {
	Event        Message
	Continuation bool
}

func (*RerouteError) Error() string { return "provider model rerouting is not authorized" }

func (e *RerouteError) Unwrap() error {
	if e.Continuation {
		return ErrContinuationRouteMismatch
	}
	return ErrRouteIdentityContradiction
}

func rejectReroute(events []Message) error {
	for _, event := range events {
		if event.Method == "model/rerouted" {
			return &RerouteError{Event: event}
		}
	}
	return nil
}

// Validate binds observed settings to exact requested routing and read-only policy.
func (s ThreadSettings) Validate(p runtime.Profile, directory string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if s.ThreadID == "" || len(s.ThreadID) > 256 || s.Model != p.Model || s.Provider != p.Provider || s.Effort != nil && *s.Effort != p.Effort {
		return errors.New("observed thread identity or routing mismatch")
	}
	if !filepath.IsAbs(directory) || filepath.Clean(s.Directory) != filepath.Clean(directory) || s.Approval != "never" || s.Sandbox != "readOnly" || s.Network {
		return errors.New("observed thread policy mismatch")
	}
	return nil
}

// StartThread selects explicit routing and read-only tool policy. The caller must
// durably record intent first and ensure the server has no inherited connectors.
func (c *Client) StartThread(ctx context.Context, p runtime.Profile, directory string) (ThreadSettings, error) {
	return c.StartThreadWithTools(ctx, p, directory, nil)
}

// StartThreadWithTools registers caller-owned dynamic tools under the same
// read-only policy. Tool specifications do not grant native tool authority.
func (c *Client) StartThreadWithTools(ctx context.Context, p runtime.Profile, directory string, tools []any) (ThreadSettings, error) {
	var observed ThreadSettings
	if err := p.Validate(); err != nil {
		return observed, err
	}
	if !filepath.IsAbs(directory) {
		return observed, errors.New("absolute thread directory required")
	}
	if err := c.validateThreadConstraint(p, directory, tools); err != nil {
		return observed, err
	}
	response, events, err := c.callConstrained(ctx, "thread/start", map[string]any{
		"model": p.Model, "modelProvider": p.Provider, "cwd": directory,
		"approvalPolicy": "never", "sandbox": "read-only",
		"config":       map[string]any{"model_reasoning_effort": p.Effort, "agents.enabled": false, "features.multi_agent": false, "features.multi_agent_v2": false, "features.code_mode": map[string]any{"enabled": false, "direct_only_tool_namespaces": []string{"functions"}}},
		"environments": []any{}, "selectedCapabilityRoots": []any{},
		"allowProviderModelFallback": false,
		"dynamicTools":               tools,
	})
	if err != nil {
		return observed, err
	}
	if err := rejectReroute(events); err != nil {
		return observed, err
	}
	if len(response.Error) != 0 {
		return observed, errors.New("provider rejected thread creation")
	}
	for _, event := range events {
		if len(event.ID) != 0 {
			return observed, errors.New("unexpected server request while creating thread")
		}
	}
	var wire struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model       string  `json:"model"`
		Provider    string  `json:"modelProvider"`
		Effort      *string `json:"reasoningEffort"`
		ServiceTier *string `json:"serviceTier"`
		Directory   string  `json:"cwd"`
		Approval    string  `json:"approvalPolicy"`
		Sandbox     struct {
			Type    string `json:"type"`
			Network bool   `json:"networkAccess"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(response.Result, &wire); err != nil {
		return observed, err
	}
	toolsHash, err := canonical.Hash("harness.codex-dynamic-tools.v1", tools)
	if err != nil {
		return observed, err
	}
	observed = ThreadSettings{ThreadID: wire.Thread.ID, Model: wire.Model, Provider: wire.Provider, Effort: wire.Effort, ServiceTier: wire.ServiceTier, Directory: wire.Directory, Approval: wire.Approval, Sandbox: wire.Sandbox.Type, Network: wire.Sandbox.Network, Role: p.Role, DynamicToolsHash: toolsHash}
	if err := observed.Validate(p, directory); err != nil {
		return observed, err
	}
	if err := c.bindConstrainedThread(observed.ThreadID); err != nil {
		return observed, err
	}
	return observed, nil
}

// ResumeThread reloads an exact recorded thread without supplying route
// overrides. Its top-level response is current configuration evidence only;
// it does not prove how historical turns executed.
func (c *Client) ResumeThread(ctx context.Context, settings ThreadSettings) (ThreadSettings, error) {
	var observed ThreadSettings
	if settings.ThreadID == "" || len(settings.ThreadID) > 256 || !filepath.IsAbs(settings.Directory) {
		return observed, fmt.Errorf("%w: invalid recorded thread or workspace identity", ErrRouteIdentityUnknown)
	}
	effort := ""
	if settings.Effort != nil {
		effort = *settings.Effort
	}
	resumeProfile := runtime.Profile{Runtime: "codex-app-server", Provider: settings.Provider, Model: settings.Model, Effort: effort, Role: settings.Role}
	if err := c.validateContinuationConstraint(resumeProfile, settings.Directory, settings.DynamicToolsHash); err != nil {
		return observed, err
	}
	if err := c.bindConstrainedThread(settings.ThreadID); err != nil {
		return observed, err
	}
	r, events, err := c.callConstrained(ctx, "thread/resume", map[string]any{"threadId": settings.ThreadID})
	if err != nil {
		return observed, err
	}
	if err := rejectReroute(events); err != nil {
		var reroute *RerouteError
		if errors.As(err, &reroute) {
			reroute.Continuation = true
		}
		return observed, err
	}
	if len(r.Error) != 0 {
		return observed, errors.New("provider could not resume persisted thread")
	}
	for _, event := range events {
		if len(event.ID) != 0 {
			return observed, errors.New("unexpected server request during resume")
		}
	}
	var wire struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model       string  `json:"model"`
		Provider    string  `json:"modelProvider"`
		Effort      *string `json:"reasoningEffort"`
		ServiceTier *string `json:"serviceTier"`
		Directory   string  `json:"cwd"`
		Approval    string  `json:"approvalPolicy"`
		Sandbox     struct {
			Type    string `json:"type"`
			Network bool   `json:"networkAccess"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(r.Result, &wire); err != nil {
		return observed, err
	}
	observed = ThreadSettings{ThreadID: wire.Thread.ID, Model: wire.Model, Provider: wire.Provider, Effort: wire.Effort, ServiceTier: wire.ServiceTier, Directory: wire.Directory, Approval: wire.Approval, Sandbox: wire.Sandbox.Type, Network: wire.Sandbox.Network}
	if wire.Thread.ID != settings.ThreadID || filepath.Clean(wire.Directory) != filepath.Clean(settings.Directory) {
		return observed, fmt.Errorf("%w: resumed thread or workspace differs from recorded identity", ErrContinuationRouteMismatch)
	}
	if settings.Model == "" || settings.Provider == "" || settings.Effort == nil {
		return observed, fmt.Errorf("%w: recorded model, provider, and effort are required", ErrRouteIdentityUnknown)
	}
	if wire.Model != "" && wire.Model != settings.Model || wire.Provider != "" && wire.Provider != settings.Provider || wire.Effort != nil && *wire.Effort != *settings.Effort {
		return observed, fmt.Errorf("%w: resumed route differs from recorded route", ErrContinuationRouteMismatch)
	}
	if wire.Model == "" || wire.Provider == "" || wire.Effort == nil {
		return observed, fmt.Errorf("%w: resumed model, provider, and effort are required", ErrRouteIdentityUnknown)
	}
	if wire.Approval != settings.Approval || wire.Sandbox.Type != settings.Sandbox || wire.Sandbox.Network != settings.Network {
		return observed, fmt.Errorf("%w: resumed policy differs from recorded policy", ErrContinuationRouteMismatch)
	}
	observed.Role = settings.Role
	observed.DynamicToolsHash = settings.DynamicToolsHash
	return observed, nil
}

// Turn is a bounded provider observation. Its content must be translated before
// admission into the harness journal; raw provider items are not canonical v1.
type Turn struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Items     []json.RawMessage `json:"items"`
	ItemsView string            `json:"itemsView"`
	Error     json.RawMessage   `json:"error"`
}

func (t Turn) validate() error {
	if t.ID == "" || len(t.ID) > 256 || len(t.Items) > 4096 {
		return errors.New("invalid provider turn identity or item bound")
	}
	switch t.Status {
	case "inProgress", "completed", "interrupted", "failed":
	default:
		return errors.New("unknown provider turn status")
	}
	return nil
}

// StartTurn sends the exact input once. clientUserMessageId is a correlation key,
// not a claim of provider idempotency. Callers must persist intent before dispatch.
func (c *Client) StartTurn(ctx context.Context, thread ThreadSettings, i runtime.Invocation) (Turn, []Message, error) {
	var wire struct {
		Turn Turn `json:"turn"`
	}
	expected, err := runtime.NewInvocation(i.Profile, i.Input)
	if err != nil {
		return wire.Turn, nil, err
	}
	if expected.ID != i.ID || i.Version != 1 {
		return wire.Turn, nil, errors.New("invocation identity mismatch")
	}
	if err := thread.Validate(i.Profile, thread.Directory); err != nil {
		return wire.Turn, nil, err
	}
	if err := c.validateContinuationConstraint(i.Profile, thread.Directory, thread.DynamicToolsHash); err != nil {
		return wire.Turn, nil, err
	}
	if err := c.bindConstrainedThread(thread.ThreadID); err != nil {
		return wire.Turn, nil, err
	}
	params := map[string]any{
		"threadId": thread.ThreadID, "clientUserMessageId": i.ID,
		"model": i.Profile.Model, "effort": i.Profile.Effort,
		"approvalPolicy": "never", "sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": false},
		"input":        []any{map[string]any{"type": "text", "text": i.Input}},
		"environments": []any{},
	}
	// The optional schema is part of immutable invocation input, not inferred
	// from the role during recovery or supplied as mutable adapter configuration.
	if i.Profile.Role == "writer" || i.Profile.Role == "fixer" {
		var envelope struct {
			OutputSchema json.RawMessage `json:"output_schema"`
		}
		if json.Unmarshal([]byte(i.Input), &envelope) == nil && len(envelope.OutputSchema) > 0 {
			got, e := canonical.Hash("writer-output-schema", envelope.OutputSchema)
			want, _ := canonical.Hash("writer-output-schema", writercontract.Schema())
			utf8, _ := canonical.Hash("writer-output-schema", writercontract.UTF8Schema())
			v2, _ := canonical.Hash("writer-output-schema", writercontract.ChangesJSONSchema())
			if e != nil || (got != want && got != utf8 && got != v2) {
				return wire.Turn, nil, errors.New("writer output schema substitution")
			}
			params["outputSchema"] = envelope.OutputSchema
		}
	}
	r, events, err := c.callConstrained(ctx, "turn/start", params)
	if err != nil {
		return wire.Turn, events, err
	}
	if err := rejectReroute(events); err != nil {
		return wire.Turn, events, err
	}
	if len(r.Error) != 0 {
		return wire.Turn, events, errors.New("provider rejected turn creation")
	}
	if err := json.Unmarshal(r.Result, &wire); err != nil {
		return wire.Turn, events, err
	}
	return wire.Turn, events, wire.Turn.validate()
}

// ReadTurn observes a known persisted turn without starting or resubmitting work.
func (c *Client) ReadTurn(ctx context.Context, settings ThreadSettings, turnID string) (Turn, error) {
	threadID := settings.ThreadID
	effort := ""
	if settings.Effort != nil {
		effort = *settings.Effort
	}
	profile := runtime.Profile{Runtime: "codex-app-server", Provider: settings.Provider, Model: settings.Model, Effort: effort, Role: settings.Role}
	if err := c.validateContinuationConstraint(profile, settings.Directory, settings.DynamicToolsHash); err != nil {
		return Turn{}, err
	}
	if err := c.bindConstrainedThread(threadID); err != nil {
		return Turn{}, err
	}
	var wire struct {
		Thread struct {
			ID        string  `json:"id"`
			Turns     []Turn  `json:"turns"`
			Model     *string `json:"model"`
			Provider  string  `json:"modelProvider"`
			Effort    *string `json:"reasoningEffort"`
			Directory string  `json:"cwd"`
		} `json:"thread"`
	}
	r, events, err := c.callConstrained(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
	if err != nil {
		return Turn{}, err
	}
	if err := rejectReroute(events); err != nil {
		return Turn{}, err
	}
	if len(r.Error) != 0 {
		return Turn{}, errors.New("provider could not read persisted thread")
	}
	for _, event := range events {
		if len(event.ID) != 0 {
			return Turn{}, errors.New("unexpected server request during readback")
		}
	}
	if err := json.Unmarshal(r.Result, &wire); err != nil {
		return Turn{}, err
	}
	if wire.Thread.ID != threadID {
		return Turn{}, fmt.Errorf("%w: readback thread substitution", ErrRouteIdentityContradiction)
	}
	if filepath.Clean(wire.Thread.Directory) != filepath.Clean(settings.Directory) {
		return Turn{}, fmt.Errorf("%w: readback workspace mismatch", ErrRouteIdentityContradiction)
	}
	var found *Turn
	for n := range wire.Thread.Turns {
		if wire.Thread.Turns[n].ID == turnID {
			if found != nil {
				return Turn{}, errors.New("duplicate readback turn")
			}
			found = &wire.Thread.Turns[n]
		}
	}
	if found == nil {
		return Turn{}, errors.New("recorded turn absent from readback")
	}
	return *found, found.validate()
}

// Completion recognizes only a completion event for the exact thread and turn.
func Completion(m Message, threadID, turnID string) (*Turn, error) {
	if m.toolHandled && m.Method == "item/tool/call" {
		return nil, nil
	}
	if len(m.ID) != 0 {
		return nil, errors.New("server requested ungranted authority")
	}
	if m.Method == "model/rerouted" {
		return nil, &RerouteError{Event: m}
	}
	if m.Method != "turn/completed" {
		return nil, nil
	}
	var wire struct {
		ThreadID string `json:"threadId"`
		Turn     Turn   `json:"turn"`
	}
	if err := json.Unmarshal(m.Params, &wire); err != nil {
		return nil, err
	}
	if wire.ThreadID != threadID || wire.Turn.ID != turnID {
		return nil, errors.New("completion identity mismatch")
	}
	return &wire.Turn, wire.Turn.validate()
}

// Result translates only final assistant output from a complete successful turn.
// Missing token accounting remains null; tool output cannot masquerade as an answer.
func (t Turn) Result(i runtime.Invocation, thread ThreadSettings) (runtime.Result, error) {
	var result runtime.Result
	if err := t.validate(); err != nil {
		return result, err
	}
	if err := thread.Validate(i.Profile, thread.Directory); err != nil {
		return result, err
	}
	if t.Status != "completed" || len(t.Error) != 0 && string(t.Error) != "null" || t.ItemsView != "" && t.ItemsView != "full" {
		return result, fmt.Errorf("turn completion or full output unavailable (status=%s, itemsView=%s)", t.Status, t.ItemsView)
	}
	var final []string
	seen := map[string]bool{}
	for _, raw := range t.Items {
		var item struct {
			Type  string  `json:"type"`
			ID    string  `json:"id"`
			Text  string  `json:"text"`
			Phase *string `json:"phase"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return result, err
		}
		if item.ID == "" || seen[item.ID] {
			return result, errors.New("invalid or duplicate turn item identity")
		}
		seen[item.ID] = true
		if item.Type == "agentMessage" && (item.Phase == nil || *item.Phase == "final_answer") {
			final = append(final, item.Text)
		}
	}
	result = runtime.Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, ObservedModel: &thread.Model, ObservedProvider: &thread.Provider, ObservedEffort: thread.Effort, Output: strings.Join(final, "\n\n")}
	return result, runtime.ValidateResult(i, result, true)
}
