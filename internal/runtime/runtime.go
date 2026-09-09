package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexusage"
)

// Profile is the immutable requested routing for one role. It is not a fallback list.
type Profile struct {
	Runtime  string `toml:"runtime" json:"runtime"`
	Provider string `toml:"provider" json:"provider"`
	Model    string `toml:"model" json:"model"`
	Effort   string `toml:"effort" json:"effort"`
	Role     string `toml:"role" json:"role"`
}

// Validate rejects incomplete routing and unknown roles before dispatch.
func (p Profile) Validate() error {
	for _, s := range []string{p.Runtime, p.Provider, p.Model, p.Effort} {
		if strings.TrimSpace(s) == "" || len(s) > 128 {
			return errors.New("invalid runtime profile identifier")
		}
	}
	switch p.Role {
	case "planner", "explorer", "writer", "fixer", "reviewer":
		return nil
	default:
		return errors.New("unknown agent role")
	}
}

// Capabilities describes supported operations; false means unsupported, not
// independently disproven. Sandbox evidence is deliberately absent from the fake.
type Capabilities struct {
	ModelSelection     bool `json:"model_selection"`
	EffortSelection    bool `json:"effort_selection"`
	StructuredOutput   bool `json:"structured_output"`
	Continuation       bool `json:"continuation"`
	Cancellation       bool `json:"cancellation"`
	TokenAccounting    bool `json:"token_accounting"`
	CostAccounting     bool `json:"cost_accounting"`
	ObservedModel      bool `json:"observed_model"`
	WorkspaceIsolation bool `json:"workspace_isolation"`
	ProcessVisibility  bool `json:"process_visibility"`
	NetworkBoundary    bool `json:"network_boundary"`
	ExternalEffects    bool `json:"external_effects"`
}

// Usage preserves unavailable token counts as null. Counts represent provider
// evidence and must not be invented from input length by a fake runtime.
type Usage struct {
	Accounting     *codexusage.Receipt `json:"accounting,omitempty"`
	InputTokens    *int64              `json:"input_tokens"`
	OutputTokens   *int64              `json:"output_tokens"`
	CostMinorUnits *int64              `json:"cost_minor_units"`
}

// Invocation binds exact text and requested routing. ID must match its contents.
type Invocation struct {
	Version int     `json:"version"`
	ID      string  `json:"id"`
	Profile Profile `json:"profile"`
	Input   string  `json:"input"`
}

// NewInvocation constructs an immutable content identity after validation.
func NewInvocation(p Profile, input string) (Invocation, error) {
	i := Invocation{Version: 1, Profile: p, Input: input}
	if err := p.Validate(); err != nil {
		return i, err
	}
	if strings.TrimSpace(input) == "" || len(input) > 256<<10 {
		return i, errors.New("input size invalid")
	}
	id, err := canonical.Hash("harness.invocation.v1", struct {
		Version int     `json:"version"`
		Profile Profile `json:"profile"`
		Input   string  `json:"input"`
	}{1, p, input})
	i.ID = id
	return i, err
}

// Result retains exact invocation/profile identity and optional model observation.
type Result struct {
	Version          int     `json:"version"`
	InvocationID     string  `json:"invocation_id"`
	Requested        Profile `json:"requested"`
	ObservedModel    *string `json:"observed_model"`
	ObservedProvider *string `json:"observed_provider"`
	ObservedEffort   *string `json:"observed_effort"`
	Output           string  `json:"output"`
	Usage            Usage   `json:"usage"`
}

// ValidateResult rejects substitution and identity drift. requireObserved is
// selected by admitted runtime capabilities, never by an untrusted result.
func ValidateResult(i Invocation, r Result, requireObserved bool) error {
	expected, err := NewInvocation(i.Profile, i.Input)
	if err != nil {
		return err
	}
	if i.Version != 1 || i.ID != expected.ID || r.Version != 1 || r.InvocationID != i.ID || r.Requested != i.Profile {
		return errors.New("runtime identity mismatch")
	}
	if r.ObservedModel == nil {
		if requireObserved {
			return errors.New("observed model required")
		}
	} else if *r.ObservedModel != i.Profile.Model {
		return errors.New("model substitution")
	}
	if r.ObservedProvider != nil && *r.ObservedProvider != i.Profile.Provider {
		return errors.New("provider substitution")
	}
	if r.ObservedEffort != nil && *r.ObservedEffort != i.Profile.Effort {
		return errors.New("effort substitution")
	}
	if strings.TrimSpace(r.Output) == "" || len(r.Output) > 256<<10 {
		return errors.New("output size invalid")
	}
	for _, n := range []*int64{r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CostMinorUnits} {
		if n != nil && (*n < 0 || *n > 9007199254740991) {
			return errors.New("invalid usage")
		}
	}
	// Currency/scale are not yet in v1; do not admit ambiguous monetary values.
	if r.Usage.CostMinorUnits != nil {
		return errors.New("cost accounting unsupported in v1 kernel")
	}
	return nil
}

// AgentRuntime owns one invocation lifecycle. Close releases runtime resources;
// unsupported continuation/cancellation targets return errors rather than success.
type AgentRuntime interface {
	Capabilities() Capabilities
	Execute(context.Context, Invocation) (Result, error)
	Resume(context.Context, string) (Result, error)
	Cancel(context.Context, string) error
	Close() error
	Usage() Usage
}

// Fake is a concurrent-safe deterministic test runtime without tools or effects.
// Its output is a fixture plan, not engineering judgment or a production model.
type Fake struct {
	mu     sync.Mutex
	closed bool
}

// Capabilities reports only behavior implemented by Fake.
func (*Fake) Capabilities() Capabilities {
	return Capabilities{ModelSelection: true, EffortSelection: true, StructuredOutput: true, ObservedModel: true}
}

// Execute honors context cancellation before producing a deterministic fixture.
func (f *Fake) Execute(ctx context.Context, i Invocation) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if f.closed {
		return Result{}, errors.New("runtime closed")
	}
	expected, err := NewInvocation(i.Profile, i.Input)
	if err != nil {
		return Result{}, err
	}
	if i.Version != 1 || i.ID != expected.ID || i.Profile.Runtime != "fake" || i.Profile.Provider != "deterministic" {
		return Result{}, errors.New("fake invocation mismatch")
	}
	model := i.Profile.Model
	provider, effort := i.Profile.Provider, i.Profile.Effort
	r := Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: "Fixture plan: inspect the objective, propose one bounded change, then execute required verification. Input identity: " + i.ID}
	return r, ValidateResult(i, r, true)
}

// Resume is unsupported because fake executions complete synchronously.
func (*Fake) Resume(context.Context, string) (Result, error) {
	return Result{}, errors.New("continuation unsupported")
}

// Cancel cannot cancel a completed/unknown ID; use Execute's context for cancellation.
func (*Fake) Cancel(context.Context, string) error {
	return errors.New("no active asynchronous invocation")
}

// Close is idempotent and prevents future Execute calls.
func (f *Fake) Close() error { f.mu.Lock(); defer f.mu.Unlock(); f.closed = true; return nil }

// Usage returns unknown counts; fixture execution is not model token accounting.
func (*Fake) Usage() Usage { return Usage{} }
