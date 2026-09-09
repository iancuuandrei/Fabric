// Package contextbroker provides an invocation-bound durable broker for
// read-only repository context tools. It grants no runtime, route, access, or
// filesystem authority; callers must supply an already authorized invocation
// identity and hold any candidate workspace lease for the broker lifetime.
package contextbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"harness.local/engorch/internal/candidatetools"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/sourcetools"
)

const (
	boundEvent    = "context.bound"
	requestEvent  = "context.request"
	responseEvent = "context.response"
	closedEvent   = "context.closed"
)

// Limits bounds durable tool calls and their canonical JSON bodies.
type Limits struct {
	MaxCalls              int `json:"max_calls"`
	MaxRequestBytes       int `json:"max_request_bytes"`
	MaxResponseBytes      int `json:"max_response_bytes"`
	MaxTotalResponseBytes int `json:"max_total_response_bytes"`
}

func (l Limits) validate() error {
	if l.MaxCalls < 1 || l.MaxCalls > 512 || l.MaxRequestBytes < 2 || l.MaxRequestBytes > 64<<10 || l.MaxResponseBytes < 2 || l.MaxResponseBytes > 768<<10 || l.MaxTotalResponseBytes < l.MaxResponseBytes || l.MaxTotalResponseBytes > 8<<20 {
		return errors.New("invalid context broker limits")
	}
	return nil
}

// Binding fixes the invocation, source, optional candidate, catalog identity,
// and resource bounds. InvocationID is caller-supplied identity, not proof of
// route or model-access authorization.
type Binding struct {
	Version      int                     `json:"version"`
	InvocationID string                  `json:"invocation_id"`
	Source       repository.Identity     `json:"source"`
	Candidate    *candidatetools.Binding `json:"candidate,omitempty"`
	CatalogID    string                  `json:"catalog_id"`
	Limits       Limits                  `json:"limits"`
}

// NewBinding validates and snapshots all caller-owned binding data.
func NewBinding(invocationID string, source repository.Identity, candidate *candidatetools.Binding, limits Limits) (Binding, error) {
	binding := Binding{Version: 1, InvocationID: invocationID, Source: source, Limits: limits}
	if candidate != nil {
		copy := *candidate
		binding.Candidate = &copy
	}
	id, err := catalogID(catalogFor(binding.Candidate != nil))
	if err != nil {
		return Binding{}, err
	}
	binding.CatalogID = id
	if _, err := binding.ID(); err != nil {
		return Binding{}, err
	}
	return cloneBinding(binding)
}

// ID validates and hashes the complete static broker binding.
func (b Binding) ID() (string, error) {
	if b.Version != 1 || safepath.RequireDigest(b.InvocationID) != nil || safepath.RequireDigest(b.CatalogID) != nil {
		return "", errors.New("invalid context broker binding identity")
	}
	if err := b.Source.Validate(); err != nil {
		return "", err
	}
	if err := b.Limits.validate(); err != nil {
		return "", err
	}
	if b.Candidate != nil {
		if err := b.Candidate.Validate(b.Source); err != nil {
			return "", err
		}
	}
	expected, err := catalogID(catalogFor(b.Candidate != nil))
	if err != nil {
		return "", err
	}
	if b.CatalogID != expected {
		return "", errors.New("context broker catalog identity mismatch")
	}
	return canonical.Hash("harness.context-broker-binding.v1", b)
}

// Request is persisted before any repository or candidate IO.
type Request struct {
	Version      int             `json:"version"`
	BindingID    string          `json:"binding_id"`
	InvocationID string          `json:"invocation_id"`
	RequestID    string          `json:"request_id"`
	CallID       string          `json:"call_id"`
	Tool         string          `json:"tool"`
	Arguments    json.RawMessage `json:"arguments"`
}

// ID validates and hashes a request without its derived RequestID.
func (r Request) ID() (string, error) {
	if r.Version != 1 || safepath.RequireDigest(r.BindingID) != nil || safepath.RequireDigest(r.InvocationID) != nil || !validCallID(r.CallID) || !validToolName(r.Tool) {
		return "", errors.New("invalid context request identity")
	}
	arguments, err := canonical.Normalize(r.Arguments)
	if err != nil || len(arguments) < 2 || arguments[0] != '{' || !bytes.Equal(arguments, r.Arguments) {
		return "", errors.New("context request arguments are not a canonical object")
	}
	r.RequestID = ""
	return canonical.Hash("harness.context-request.v1", r)
}

// Response is the exact canonical object durably recorded before it is returned.
type Response struct {
	Version      int             `json:"version"`
	BindingID    string          `json:"binding_id"`
	InvocationID string          `json:"invocation_id"`
	RequestID    string          `json:"request_id"`
	CallID       string          `json:"call_id"`
	Success      bool            `json:"success"`
	Content      json.RawMessage `json:"content"`
}

type closure struct {
	Version      int    `json:"version"`
	BindingID    string `json:"binding_id"`
	InvocationID string `json:"invocation_id"`
}

// State is reconstructed exclusively from the validated durable journal.
type State struct {
	Binding       *Binding   `json:"binding,omitempty"`
	Pending       *Request   `json:"pending,omitempty"`
	Requests      []Request  `json:"requests"`
	Responses     []Response `json:"responses"`
	Calls         int        `json:"calls"`
	ResponseBytes int        `json:"response_bytes"`
	Closed        bool       `json:"closed"`
}

type executeFunc func(context.Context, Binding, string, json.RawMessage) (any, bool, error)

// Broker serializes calls within one process. The journal protects individual
// appends; Broker does not claim a cross-process ownership lease.
type Broker struct {
	mu       sync.Mutex
	path     string
	binding  Binding
	catalog  []sourcetools.Definition
	execute  executeFunc
	shutdown bool
	closeErr error
}

// Open creates the durable binding on an empty journal or validates the exact
// existing binding. It never executes a pending request during recovery.
func Open(path string, binding Binding) (*Broker, error) {
	snapshot, err := cloneBinding(binding)
	if err != nil {
		return nil, err
	}
	if _, err := snapshot.ID(); err != nil {
		return nil, err
	}
	events, err := journal.Read(path)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		if _, err := journal.Append(path, boundEvent, snapshot, func(events []journal.Event) error {
			_, err := replay(events)
			return err
		}); err != nil {
			return nil, err
		}
	}
	state, err := Inspect(path)
	if err != nil {
		return nil, err
	}
	if state.Binding == nil || !reflect.DeepEqual(*state.Binding, snapshot) {
		return nil, errors.New("context broker binding differs from journal")
	}
	return &Broker{path: path, binding: snapshot, catalog: cloneCatalog(catalogFor(snapshot.Candidate != nil)), execute: executeTools, shutdown: state.Closed}, nil
}

// Catalog returns a deep copy of the exact catalog bound to this broker.
func (b *Broker) Catalog() []sourcetools.Definition {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneCatalog(b.catalog)
}

// Limits returns the immutable configured bounds by value. It is safe to call
// concurrently and does not expose mutable broker state.
func (b *Broker) Limits() Limits {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.binding.Limits
}

// Call persists an exact request before domain IO and persists the exact
// canonical response before returning it. Durable recording does not prove that
// a transport delivered the response: cancellation or write failure afterward
// is delivery-unknown and must not repeat domain IO. A post-request failure
// leaves the request pending and likewise forbids automatic retry.
func (b *Broker) Call(ctx context.Context, callID, tool string, arguments json.RawMessage) (Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.shutdown {
		return Response{}, errors.New("context broker closed")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	normal, err := canonical.Normalize(arguments)
	if err != nil || len(normal) < 2 || normal[0] != '{' || len(normal) > b.binding.Limits.MaxRequestBytes {
		return Response{}, errors.New("invalid context request arguments")
	}
	bindingID, _ := b.binding.ID()
	request := Request{Version: 1, BindingID: bindingID, InvocationID: b.binding.InvocationID, CallID: callID, Tool: tool, Arguments: json.RawMessage(append([]byte(nil), normal...))}
	request.RequestID, err = request.ID()
	if err != nil {
		return Response{}, err
	}
	if err := b.append(requestEvent, request); err != nil {
		return Response{}, err
	}
	content, handled, executionErr := b.execute(ctx, b.binding, tool, request.Arguments)
	if !handled && executionErr == nil {
		executionErr = errors.New("bound catalog tool was not handled")
	}
	success := executionErr == nil
	if executionErr != nil {
		content = map[string]string{"code": "context_request_failed", "message": "check arguments, supported path kind, bounds, and candidate identity"}
	}
	encoded, err := canonical.Bytes(content)
	if err != nil || len(encoded) < 2 || encoded[0] != '{' {
		return Response{}, errors.New("context response is not a canonical object")
	}
	if len(encoded) > b.binding.Limits.MaxResponseBytes {
		return Response{}, errors.New("context response exceeds bound; request remains pending")
	}
	response := Response{Version: 1, BindingID: bindingID, InvocationID: b.binding.InvocationID, RequestID: request.RequestID, CallID: callID, Success: success, Content: json.RawMessage(encoded)}
	if err := b.append(responseEvent, response); err != nil {
		return Response{}, err
	}
	return cloneResponse(response), nil
}

// Close always shuts down this in-process broker. Durable closure is attempted
// once and may fail when a request is pending; that error is retained.
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.shutdown {
		return b.closeErr
	}
	b.shutdown = true
	bindingID, _ := b.binding.ID()
	b.closeErr = b.append(closedEvent, closure{Version: 1, BindingID: bindingID, InvocationID: b.binding.InvocationID})
	return b.closeErr
}

func (b *Broker) append(kind string, payload any) error {
	_, err := journal.Append(b.path, kind, payload, func(events []journal.Event) error {
		state, err := replay(events)
		if err != nil {
			return err
		}
		if state.Binding == nil || !reflect.DeepEqual(*state.Binding, b.binding) {
			return errors.New("context broker journal binding changed")
		}
		return nil
	})
	return err
}

func executeTools(ctx context.Context, binding Binding, tool string, arguments json.RawMessage) (any, bool, error) {
	content, handled, err := sourcetools.Execute(ctx, binding.Source, tool, arguments)
	if handled || err != nil {
		return content, handled, err
	}
	if binding.Candidate != nil {
		return candidatetools.Execute(ctx, *binding.Candidate, tool, arguments)
	}
	return nil, false, nil
}

func catalogFor(candidate bool) []sourcetools.Definition {
	catalog := sourcetools.Catalog()
	if candidate {
		catalog = append(catalog, candidatetools.Catalog()...)
	}
	return catalog
}

func catalogID(catalog []sourcetools.Definition) (string, error) {
	return canonical.Hash("harness.context-catalog.v1", catalog)
}

func cloneCatalog(catalog []sourcetools.Definition) []sourcetools.Definition {
	copy := make([]sourcetools.Definition, len(catalog))
	for index, definition := range catalog {
		copy[index] = sourcetools.Definition{Name: definition.Name, Description: definition.Description, InputSchema: cloneMap(definition.InputSchema)}
	}
	return copy
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case map[string]any:
			result[key] = cloneMap(typed)
		case []string:
			result[key] = append([]string(nil), typed...)
		case []any:
			items := make([]any, len(typed))
			for index, item := range typed {
				if nested, ok := item.(map[string]any); ok {
					items[index] = cloneMap(nested)
				} else {
					items[index] = item
				}
			}
			result[key] = items
		default:
			result[key] = value
		}
	}
	return result
}

func cloneBinding(binding Binding) (Binding, error) {
	raw, err := canonical.Bytes(binding)
	if err != nil {
		return Binding{}, err
	}
	var result Binding
	if err := canonical.Decode(raw, &result); err != nil {
		return Binding{}, err
	}
	return result, nil
}

func cloneResponse(response Response) Response {
	response.Content = append(json.RawMessage(nil), response.Content...)
	return response
}

func cloneRequest(request Request) Request {
	request.Arguments = append(json.RawMessage(nil), request.Arguments...)
	return request
}

func validCallID(id string) bool {
	if id == "" || len(id) > 256 || !utf8.ValidString(id) {
		return false
	}
	for _, character := range id {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func validToolName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return !strings.ContainsAny(name, "\r\n")
}
