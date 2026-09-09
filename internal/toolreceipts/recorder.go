package toolreceipts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/toolbridge"
)

const (
	boundEvent   = "mcp_tool_receipts_bound_v1"
	intentEvent  = "mcp_tool_call_intent_v1"
	receiptEvent = "mcp_tool_call_receipt_v1"

	// OwnerContext identifies tools owned by the read-only context projection.
	OwnerContext Owner = "context"
	// OwnerAgent identifies tools owned by the agent-control projection.
	OwnerAgent Owner = "agent"

	maxArgumentsBytes = 64 << 10
	maxResultBytes    = 1 << 20
)

var (
	// ErrDuplicateRequest reports an exact request already claimed by the journal.
	ErrDuplicateRequest = errors.New("MCP request already recorded")
	// ErrRequestConflict reports reuse of a request ID with different call data.
	ErrRequestConflict = errors.New("MCP request identity conflict")
	toolPattern        = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Owner is the controller family that owns a projected tool callback.
type Owner string

// OwnedProjection names the authority boundary for one immutable projection.
type OwnedProjection struct {
	Owner      Owner
	Projection toolbridge.Projection
}

// ToolOwner is the finite, catalog-order mapping frozen into a binding.
type ToolOwner struct {
	Tool  string `json:"tool"`
	Owner Owner  `json:"owner"`
}

// Binding fixes the caller, invocation, catalog, and every tool owner.
type Binding struct {
	Version             int         `json:"version"`
	InvocationID        string      `json:"invocation_id"`
	CallerBindingSHA256 string      `json:"caller_binding_sha256"`
	CatalogSHA256       string      `json:"catalog_sha256"`
	Tools               []ToolOwner `json:"tools"`
	QueuePolicy         string      `json:"queue_policy,omitempty"`
	MaxQueuedCalls      int         `json:"max_queued_calls,omitempty"`
	BindingID           string      `json:"binding_id"`
}

// QueuePolicySerialFIFO identifies the bounded transport queue implemented by
// toolbridge. The receipt recorder binds this policy but does not enforce it.
const QueuePolicySerialFIFO = "serial-fifo-v1"

// ID validates and hashes a binding without its derived identifier.
func (b Binding) ID() (string, error) {
	if b.Version != 1 && b.Version != 2 || safepath.RequireDigest(b.InvocationID) != nil || safepath.RequireDigest(b.CallerBindingSHA256) != nil || safepath.RequireDigest(b.CatalogSHA256) != nil || len(b.Tools) < 2 || len(b.Tools) > 128 {
		return "", errors.New("invalid MCP tool receipt binding")
	}
	if b.Version == 1 && (b.QueuePolicy != "" || b.MaxQueuedCalls != 0) || b.Version == 2 && (b.QueuePolicy != QueuePolicySerialFIFO || b.MaxQueuedCalls < 1 || b.MaxQueuedCalls > 32) {
		return "", errors.New("invalid MCP tool receipt queue policy")
	}
	seen := make(map[string]struct{}, len(b.Tools))
	owners := map[Owner]bool{}
	for _, item := range b.Tools {
		if !validOwner(item.Owner) || !validTool(item.Tool) {
			return "", errors.New("invalid MCP tool owner mapping")
		}
		if _, exists := seen[item.Tool]; exists {
			return "", errors.New("duplicate MCP tool owner mapping")
		}
		seen[item.Tool] = struct{}{}
		owners[item.Owner] = true
	}
	if !owners[OwnerContext] || !owners[OwnerAgent] {
		return "", errors.New("context and agent tool owners required")
	}
	b.BindingID = ""
	return canonical.Hash("harness.mcp-tool-receipt-binding.v1", b)
}

// Intent is durable before lookup or callback execution. ArgumentsSHA256 is a
// digest of canonical arguments; the argument body is intentionally absent.
type Intent struct {
	Version         int    `json:"version"`
	BindingID       string `json:"binding_id"`
	InvocationID    string `json:"invocation_id"`
	RequestID       string `json:"request_id"`
	RequestKey      string `json:"request_key"`
	Tool            string `json:"tool"`
	ArgumentsSHA256 string `json:"arguments_sha256"`
}

// Receipt records a successful callback observation without duplicating its
// body. It is transport evidence, not proof of the owner's backend effect.
type Receipt struct {
	Version      int    `json:"version"`
	BindingID    string `json:"binding_id"`
	InvocationID string `json:"invocation_id"`
	RequestKey   string `json:"request_key"`
	Tool         string `json:"tool"`
	Owner        Owner  `json:"owner"`
	ResultSHA256 string `json:"result_sha256"`
}

// Call pairs one unique intent with at most one terminal receipt.
type Call struct {
	Intent  Intent   `json:"intent"`
	Receipt *Receipt `json:"receipt,omitempty"`
}

// State is reconstructed exclusively from a fully validated journal.
type State struct {
	Binding *Binding `json:"binding,omitempty"`
	Calls   []Call   `json:"calls"`
}

// Observation is a caller-held transcript entry. Its bodies are never added to
// the receipt journal.
type Observation struct {
	Call   toolbridge.Call
	Result toolbridge.Result
}

// Config supplies a precomputed transport catalog identity and exact caller
// binding. Open validates every projection before it appends a binding.
type Config struct {
	Path                string
	InvocationID        string
	CallerBindingSHA256 string
	CatalogSHA256       string
	Owners              []OwnedProjection
	QueuePolicy         string
	MaxQueuedCalls      int
}

// Recorder owns frozen catalog and callback snapshots for one journal binding.
type Recorder struct {
	path       string
	binding    Binding
	catalog    []toolbridge.ToolDefinition
	routes     map[string]route
	projection toolbridge.Projection
}

type route struct {
	owner Owner
	call  toolbridge.CallFunc
}

// CatalogSHA256 returns the exact identity toolbridge.Server will use for the
// combined owner catalog. It performs no journal or callback operation.
func CatalogSHA256(owners []OwnedProjection) (string, error) {
	catalog, _, _, err := snapshotOwners(owners)
	if err != nil {
		return "", err
	}
	return toolbridge.CatalogIdentity(catalog)
}

// ArgumentsSHA256 hashes one bounded canonical MCP arguments object.
func ArgumentsSHA256(arguments json.RawMessage) (string, error) {
	if len(arguments) == 0 || len(arguments) > maxArgumentsBytes {
		return "", errors.New("MCP tool arguments exceed receipt bound")
	}
	normal, err := canonical.Normalize(arguments)
	if err != nil || len(normal) == 0 || normal[0] != '{' {
		return "", errors.New("invalid canonical MCP tool arguments")
	}
	return canonical.Hash("harness.mcp-tool-arguments.v1", json.RawMessage(normal))
}

// ResultSHA256 hashes one bounded canonical callback result.
func ResultSHA256(result toolbridge.Result) (string, error) { return hashResult(result) }

// Open validates and freezes the two owner projections, then creates or
// reopens their exact metadata journal binding.
func Open(config Config) (*Recorder, error) {
	if config.Path == "" {
		return nil, errors.New("MCP tool receipt journal path required")
	}
	catalog, routes, tools, err := snapshotOwners(config.Owners)
	if err != nil {
		return nil, err
	}
	catalogID, err := toolbridge.CatalogIdentity(catalog)
	if err != nil || catalogID != config.CatalogSHA256 {
		return nil, errors.New("MCP tool receipt catalog hash mismatch")
	}
	version := 1
	if config.QueuePolicy != "" || config.MaxQueuedCalls != 0 {
		version = 2
	}
	binding := Binding{Version: version, InvocationID: config.InvocationID, CallerBindingSHA256: config.CallerBindingSHA256, CatalogSHA256: catalogID, Tools: tools, QueuePolicy: config.QueuePolicy, MaxQueuedCalls: config.MaxQueuedCalls}
	binding.BindingID, err = binding.ID()
	if err != nil {
		return nil, err
	}
	if err = bind(config.Path, binding); err != nil {
		return nil, err
	}
	r := &Recorder{path: config.Path, binding: cloneBinding(binding), catalog: cloneCatalog(catalog), routes: routes}
	r.projection = toolbridge.Projection{Catalog: r.catalogSnapshot, Call: r.call}
	return r, nil
}

// Binding returns a defensive copy of the recorder's immutable binding.
func (r *Recorder) Binding() Binding {
	if r == nil {
		return Binding{}
	}
	return cloneBinding(r.binding)
}

// Projection returns the frozen catalog and recording callback.
func (r *Recorder) Projection() toolbridge.Projection {
	if r == nil {
		return toolbridge.Projection{}
	}
	return r.projection
}

func (r *Recorder) catalogSnapshot() ([]toolbridge.ToolDefinition, error) {
	return cloneCatalog(r.catalog), nil
}

func (r *Recorder) call(ctx context.Context, call toolbridge.Call) (toolbridge.Result, error) {
	if ctx == nil {
		return toolbridge.Result{}, errors.New("MCP tool call context required")
	}
	if err := ctx.Err(); err != nil {
		return toolbridge.Result{}, err
	}
	requestID, err := canonicalRequestID(call.RequestID)
	if err != nil || !validTool(call.Tool) {
		return toolbridge.Result{}, errors.New("invalid MCP tool call identity")
	}
	owned, exists := r.routes[call.Tool]
	if !exists {
		return toolbridge.Result{}, errors.New("tool absent from receipt binding")
	}
	argumentsHash, err := ArgumentsSHA256(call.Arguments)
	if err != nil {
		return toolbridge.Result{}, err
	}
	bindingID := r.binding.BindingID
	requestKey, err := canonical.Hash("harness.mcp-tool-request.v1", struct {
		BindingID string `json:"binding_id"`
		RequestID string `json:"request_id"`
	}{bindingID, requestID})
	if err != nil {
		return toolbridge.Result{}, err
	}
	intent := Intent{Version: 1, BindingID: bindingID, InvocationID: r.binding.InvocationID, RequestID: requestID, RequestKey: requestKey, Tool: call.Tool, ArgumentsSHA256: argumentsHash}
	if err = r.appendIntent(intent); err != nil {
		return toolbridge.Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return toolbridge.Result{}, err
	}
	result, callErr := owned.call(ctx, toolbridge.Call{RequestID: append(json.RawMessage(nil), call.RequestID...), Tool: call.Tool, Arguments: append(json.RawMessage(nil), call.Arguments...)})
	if callErr != nil {
		return result, callErr
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	resultHash, err := hashResult(result)
	if err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	receipt := Receipt{Version: 1, BindingID: bindingID, InvocationID: r.binding.InvocationID, RequestKey: requestKey, Tool: call.Tool, Owner: owned.owner, ResultSHA256: resultHash}
	if err = r.appendReceipt(intent, receipt); err != nil {
		return result, err
	}
	return result, nil
}

func snapshotOwners(owners []OwnedProjection) ([]toolbridge.ToolDefinition, map[string]route, []ToolOwner, error) {
	if len(owners) != 2 {
		return nil, nil, nil, errors.New("exact context and agent tool owners required")
	}
	seenOwners := map[Owner]bool{}
	routes := map[string]route{}
	var projections []toolbridge.Projection
	var tools []ToolOwner
	for _, owned := range owners {
		if !validOwner(owned.Owner) || seenOwners[owned.Owner] || owned.Projection.Catalog == nil || owned.Projection.Call == nil {
			return nil, nil, nil, errors.New("invalid or duplicate catalog owner")
		}
		seenOwners[owned.Owner] = true
		catalog, err := owned.Projection.Catalog()
		if err != nil {
			return nil, nil, nil, err
		}
		// Compose supplies the transport's catalog validation and exact ordering.
		frozen, err := toolbridge.Compose(toolbridge.Projection{Catalog: func() ([]toolbridge.ToolDefinition, error) { return catalog, nil }, Call: owned.Projection.Call})
		if err != nil {
			return nil, nil, nil, err
		}
		validated, err := frozen.Catalog()
		if err != nil {
			return nil, nil, nil, err
		}
		for _, definition := range validated {
			if _, duplicate := routes[definition.Name]; duplicate {
				return nil, nil, nil, errors.New("tool projection name collision")
			}
			routes[definition.Name] = route{owner: owned.Owner, call: owned.Projection.Call}
			tools = append(tools, ToolOwner{Tool: definition.Name, Owner: owned.Owner})
		}
		projections = append(projections, frozen)
	}
	combined, err := toolbridge.Compose(projections...)
	if err != nil {
		return nil, nil, nil, err
	}
	catalog, err := combined.Catalog()
	return catalog, routes, tools, err
}

func validOwner(owner Owner) bool { return owner == OwnerContext || owner == OwnerAgent }

func validTool(tool string) bool {
	return len(tool) >= 1 && len(tool) <= 128 && toolPattern.MatchString(tool)
}

func canonicalRequestID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || len(raw) > 256 {
		return "", errors.New("invalid MCP request ID")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return "", errors.New("invalid MCP request ID")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "", errors.New("invalid MCP request ID")
	}
	var encoded []byte
	switch id := value.(type) {
	case string:
		if id == "" || !utf8.ValidString(id) || len(id) > 256 {
			return "", errors.New("invalid MCP request ID")
		}
		encoded, _ = json.Marshal(id)
	case json.Number:
		text := string(id)
		if len(text) > 64 || strings.ContainsAny(text, ".eE") {
			return "", errors.New("invalid MCP request ID")
		}
		integer, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return "", errors.New("invalid MCP request ID")
		}
		encoded = []byte(integer.String())
	default:
		return "", errors.New("invalid MCP request ID")
	}
	if !bytes.Equal(encoded, raw) {
		return "", errors.New("noncanonical MCP request ID")
	}
	return string(encoded), nil
}

type resultDigest struct {
	JSON    json.RawMessage `json:"json,omitempty"`
	IsError bool            `json:"is_error"`
	Error   *resultError    `json:"error,omitempty"`
}

type resultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func hashResult(result toolbridge.Result) (string, error) {
	if len(result.JSON) > maxResultBytes || result.Error != nil && (len(result.Error.Code) > 64 || len(result.Error.Message) > 1024 || !utf8.ValidString(result.Error.Code) || !utf8.ValidString(result.Error.Message)) || len(result.JSON) != 0 && result.Error != nil {
		return "", errors.New("tool callback result exceeds receipt bound or is ambiguous")
	}
	if result.Error != nil && (!validTool(result.Error.Code) || result.Error.Message == "") || result.Error == nil && len(result.JSON) == 0 {
		return "", errors.New("tool callback returned invalid result")
	}
	digest := resultDigest{IsError: result.IsError}
	if len(result.JSON) != 0 {
		normal, err := canonical.Normalize(result.JSON)
		if err != nil || len(normal) == 0 || normal[0] != '{' {
			return "", errors.New("tool callback returned noncanonical result")
		}
		digest.JSON = json.RawMessage(normal)
	}
	if result.Error != nil {
		digest.Error = &resultError{Code: result.Error.Code, Message: result.Error.Message}
	}
	return canonical.Hash("harness.mcp-tool-result.v1", digest)
}

func bind(path string, binding Binding) error {
	state, err := Inspect(path)
	if err != nil {
		return err
	}
	if state.Binding != nil {
		if !reflect.DeepEqual(*state.Binding, binding) {
			return errors.New("MCP tool receipt journal binding changed")
		}
		return nil
	}
	_, err = journal.Append(path, boundEvent, binding, func(events []journal.Event) error {
		next, replayErr := replay(events)
		if replayErr != nil {
			return replayErr
		}
		if next.Binding == nil || !reflect.DeepEqual(*next.Binding, binding) {
			return errors.New("MCP tool receipt binding append changed")
		}
		return nil
	})
	if err == nil {
		return nil
	}
	// Concurrent open is acceptable only when it published the exact binding.
	state, inspectErr := Inspect(path)
	if inspectErr == nil && state.Binding != nil && reflect.DeepEqual(*state.Binding, binding) {
		return nil
	}
	return err
}

func (r *Recorder) appendIntent(intent Intent) error {
	_, err := journal.Append(r.path, intentEvent, intent, func(events []journal.Event) error {
		before, replayErr := replay(events[:len(events)-1])
		if replayErr != nil {
			return replayErr
		}
		if before.Binding == nil || !reflect.DeepEqual(*before.Binding, r.binding) {
			return errors.New("MCP tool receipt journal binding changed")
		}
		for _, call := range before.Calls {
			if call.Intent.RequestKey == intent.RequestKey {
				if reflect.DeepEqual(call.Intent, intent) {
					return ErrDuplicateRequest
				}
				return ErrRequestConflict
			}
		}
		_, replayErr = replay(events)
		return replayErr
	})
	return err
}

func (r *Recorder) appendReceipt(intent Intent, receipt Receipt) error {
	_, err := journal.Append(r.path, receiptEvent, receipt, func(events []journal.Event) error {
		next, replayErr := replay(events)
		if replayErr != nil {
			return replayErr
		}
		if next.Binding == nil || !reflect.DeepEqual(*next.Binding, r.binding) || len(next.Calls) == 0 {
			return errors.New("MCP tool receipt journal binding changed")
		}
		matched := false
		for _, recorded := range next.Calls {
			if reflect.DeepEqual(recorded.Intent, intent) && recorded.Receipt != nil && reflect.DeepEqual(*recorded.Receipt, receipt) {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("MCP tool result differs from recorded intent")
		}
		return nil
	})
	return err
}

// Inspect validates the complete journal without granting tool authority.
func Inspect(path string) (State, error) { return InspectContext(context.Background(), path) }

// InspectContext is Inspect with caller-controlled cancellation.
func InspectContext(ctx context.Context, path string) (State, error) {
	events, err := journal.ReadContext(ctx, path)
	if err != nil {
		return State{}, err
	}
	return replay(events)
}

// InspectWithHead atomically returns the validated state and exact journal
// head from one read. An empty journal has no bindable head and is rejected.
func InspectWithHead(path string) (State, string, error) {
	return InspectWithHeadContext(context.Background(), path)
}

// InspectWithHeadContext is InspectWithHead with caller-controlled cancellation.
func InspectWithHeadContext(ctx context.Context, path string) (State, string, error) {
	events, err := journal.ReadContext(ctx, path)
	if err != nil {
		return State{}, "", err
	}
	if len(events) == 0 {
		return State{}, "", errors.New("MCP tool receipt journal is empty")
	}
	state, err := replay(events)
	if err != nil {
		return State{}, "", err
	}
	return state, events[len(events)-1].Hash, nil
}

// ValidateTranscript binds a complete caller-held transcript to this state.
// It proves only request/result transport correspondence. Owner journals remain
// separately authoritative for context reads and agent-side mutation.
func (s State) ValidateTranscript(binding Binding, observations []Observation) error {
	if s.Binding == nil || !reflect.DeepEqual(*s.Binding, binding) || len(observations) != len(s.Calls) {
		return errors.New("MCP transcript binding or cardinality mismatch")
	}
	seen := map[string]bool{}
	byKey := make(map[string]Call, len(s.Calls))
	for _, recorded := range s.Calls {
		if recorded.Receipt == nil {
			return errors.New("MCP transcript has an intent-only call")
		}
		byKey[recorded.Intent.RequestKey] = recorded
	}
	for _, observation := range observations {
		requestID, err := canonicalRequestID(observation.Call.RequestID)
		if err != nil || !validTool(observation.Call.Tool) {
			return errors.New("invalid MCP transcript call")
		}
		key, err := canonical.Hash("harness.mcp-tool-request.v1", struct {
			BindingID string `json:"binding_id"`
			RequestID string `json:"request_id"`
		}{binding.BindingID, requestID})
		if err != nil || seen[key] {
			return errors.New("duplicate MCP transcript request")
		}
		seen[key] = true
		recorded, ok := byKey[key]
		argumentsHash, argumentsErr := ArgumentsSHA256(observation.Call.Arguments)
		resultHash, resultErr := ResultSHA256(observation.Result)
		if !ok || argumentsErr != nil || resultErr != nil || recorded.Intent.Tool != observation.Call.Tool || recorded.Intent.ArgumentsSHA256 != argumentsHash || recorded.Receipt.ResultSHA256 != resultHash {
			return errors.New("MCP transcript call differs from receipt")
		}
	}
	return nil
}

func replay(events []journal.Event) (State, error) {
	state := State{Calls: []Call{}}
	requests := map[string]bool{}
	receipts := map[string]bool{}
	for _, event := range events {
		switch event.Kind {
		case boundEvent:
			if state.Binding != nil || len(state.Calls) != 0 {
				return State{}, errors.New("duplicate MCP tool receipt binding")
			}
			var binding Binding
			if canonical.Decode(event.Payload, &binding) != nil {
				return State{}, errors.New("invalid MCP tool receipt binding event")
			}
			id, err := binding.ID()
			if err != nil || id != binding.BindingID {
				return State{}, errors.New("invalid MCP tool receipt binding identity")
			}
			state.Binding = &binding
		case intentEvent:
			if state.Binding == nil {
				return State{}, errors.New("MCP tool intent without binding")
			}
			var intent Intent
			if canonical.Decode(event.Payload, &intent) != nil || validateIntent(*state.Binding, intent) != nil || requests[intent.RequestKey] {
				return State{}, errors.New("invalid or duplicate MCP tool intent")
			}
			requests[intent.RequestKey] = true
			state.Calls = append(state.Calls, Call{Intent: intent})
		case receiptEvent:
			if state.Binding == nil {
				return State{}, errors.New("MCP tool receipt without binding")
			}
			var receipt Receipt
			if canonical.Decode(event.Payload, &receipt) != nil || receipts[receipt.RequestKey] {
				return State{}, errors.New("invalid or duplicate MCP tool receipt")
			}
			index := -1
			for i := range state.Calls {
				if state.Calls[i].Intent.RequestKey == receipt.RequestKey {
					index = i
					break
				}
			}
			if index < 0 || state.Calls[index].Receipt != nil || validateReceipt(*state.Binding, state.Calls[index].Intent, receipt) != nil {
				return State{}, errors.New("MCP tool receipt has no exact intent")
			}
			copy := receipt
			state.Calls[index].Receipt = &copy
			receipts[receipt.RequestKey] = true
		default:
			return State{}, fmt.Errorf("unknown MCP tool receipt event %q", event.Kind)
		}
	}
	return cloneState(state), nil
}

func validateIntent(binding Binding, intent Intent) error {
	requestID, err := canonicalRequestID(json.RawMessage(intent.RequestID))
	if err != nil || requestID != intent.RequestID || intent.Version != 1 || intent.BindingID != binding.BindingID || intent.InvocationID != binding.InvocationID || !validTool(intent.Tool) || safepath.RequireDigest(intent.ArgumentsSHA256) != nil {
		return errors.New("MCP tool intent differs from binding")
	}
	found := false
	for _, mapping := range binding.Tools {
		found = found || mapping.Tool == intent.Tool
	}
	if !found {
		return errors.New("MCP tool intent is absent from binding")
	}
	want, err := canonical.Hash("harness.mcp-tool-request.v1", struct {
		BindingID string `json:"binding_id"`
		RequestID string `json:"request_id"`
	}{binding.BindingID, intent.RequestID})
	if err != nil || want != intent.RequestKey {
		return errors.New("MCP tool request key mismatch")
	}
	return nil
}

func validateReceipt(binding Binding, intent Intent, receipt Receipt) error {
	owner := Owner("")
	for _, mapping := range binding.Tools {
		if mapping.Tool == intent.Tool {
			owner = mapping.Owner
			break
		}
	}
	if receipt.Version != 1 || receipt.BindingID != binding.BindingID || receipt.InvocationID != binding.InvocationID || receipt.RequestKey != intent.RequestKey || receipt.Tool != intent.Tool || receipt.Owner != owner || !validOwner(owner) || safepath.RequireDigest(receipt.ResultSHA256) != nil {
		return errors.New("MCP tool receipt differs from intent")
	}
	return nil
}

func cloneCatalog(source []toolbridge.ToolDefinition) []toolbridge.ToolDefinition {
	result := make([]toolbridge.ToolDefinition, len(source))
	for i, tool := range source {
		result[i] = tool
		result[i].InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
	}
	return result
}

func cloneBinding(binding Binding) Binding {
	binding.Tools = append([]ToolOwner(nil), binding.Tools...)
	return binding
}

func cloneState(state State) State {
	if state.Binding != nil {
		binding := cloneBinding(*state.Binding)
		state.Binding = &binding
	}
	state.Calls = append([]Call(nil), state.Calls...)
	for i := range state.Calls {
		if state.Calls[i].Receipt != nil {
			receipt := *state.Calls[i].Receipt
			state.Calls[i].Receipt = &receipt
		}
	}
	return state
}
