// Package contextmcp projects a durable context broker through the owned MCP
// transport. The caller retains invocation admission, candidate leases, host
// lifetime, and broker closure; constructing a server grants no model access.
package contextmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/toolbridge"
)

const (
	// MaxWireResponseBytes is the exact MCP response-body ceiling shared by
	// context-only and recorder-owned listeners.
	MaxWireResponseBytes = 1 << 20
	// MaxContentBytes reserves space for the durable receipt envelope and both
	// MCP representations. encoding/json may expand HTML-sensitive bytes
	// sixfold in each representation; the conservative factor also covers
	// string escaping.
	MaxContentBytes = (MaxWireResponseBytes - (16 << 10)) / 12
)

// New constructs an authenticated single-call-at-a-time server. It neither
// starts a listener nor closes the supplied broker. Stop the listener before
// closing the broker so no admitted call races durable closure.
// Duplicate rejection applies to preserved MCP request IDs, not semantic tool
// arguments. A new ID is a new read; callers must not implement blind retries.
// A stored response does not prove delivery after HTTP cancellation/write loss.
func New(broker *contextbroker.Broker, bearer string, observe toolbridge.ObserveFunc) (*toolbridge.Server, error) {
	return NewWithQueue(broker, bearer, observe, 0)
}

// NewWithQueue constructs the same context-only server with a bounded serial
// FIFO waiting queue for simultaneous calls. Queuing is transport admission
// only: at most one callback executes at a time, queued callers wait for a
// running slot, and capacity beyond maxQueuedCalls keeps the exact immediate
// rejection New applies. The served catalog is identical to New, so queue
// depth changes transport behavior without granting any new tool authority.
func NewWithQueue(broker *contextbroker.Broker, bearer string, observe toolbridge.ObserveFunc, maxQueuedCalls int) (*toolbridge.Server, error) {
	projection, err := Projection(broker)
	if err != nil {
		return nil, err
	}
	return toolbridge.New(toolbridge.Config{
		Token: bearer, Observe: observe, MaxConcurrentCalls: 1,
		MaxQueuedCalls:  maxQueuedCalls,
		MaxRequestBytes: 64 << 10, MaxResponseBytes: MaxWireResponseBytes,
		CallTimeout: 15 * time.Second,
		Catalog:     projection.Catalog, Call: projection.Call,
	})
}

// Projection exposes the same read-only broker catalog and receipt-preserving
// callback for composition with separately admitted tool owners. It does not
// create a listener, close the broker, or grant authority to another projection.
func Projection(broker *contextbroker.Broker) (toolbridge.Projection, error) {
	if broker == nil {
		return toolbridge.Projection{}, errors.New("durable context broker required")
	}
	if broker.Limits().MaxResponseBytes > MaxContentBytes {
		return toolbridge.Projection{}, errors.New("context response limit exceeds MCP envelope capacity")
	}
	return toolbridge.Projection{
		Catalog: func() ([]toolbridge.ToolDefinition, error) {
			catalog := broker.Catalog()
			tools := make([]toolbridge.ToolDefinition, 0, len(catalog))
			for _, definition := range catalog {
				schema, err := canonical.Bytes(definition.InputSchema)
				if err != nil {
					return nil, err
				}
				tools = append(tools, toolbridge.ToolDefinition{Name: definition.Name, Description: definition.Description, InputSchema: schema})
			}
			return tools, nil
		},
		Call: func(ctx context.Context, call toolbridge.Call) (toolbridge.Result, error) {
			// The transport provides normalized typed JSON-RPC IDs. Hash those
			// bytes rather than coercing numbers and strings to the same ID.
			if len(call.RequestID) == 0 || !json.Valid(call.RequestID) {
				return toolbridge.Result{}, errors.New("MCP request identity required")
			}
			hash := sha256.New()
			_, _ = hash.Write([]byte("harness.context-mcp-call.v1\x00"))
			_, _ = hash.Write(call.RequestID)
			response, err := broker.Call(ctx, hex.EncodeToString(hash.Sum(nil)), call.Tool, call.Arguments)
			if err != nil {
				return toolbridge.Result{}, err
			}
			// Provider tool-call IDs differ from MCP request IDs. Carry the full
			// durable receipt so transcript admission can bind both namespaces
			// without ambiguous tool/argument/content matching.
			encoded, err := canonical.Bytes(response)
			if err != nil {
				return toolbridge.Result{}, err
			}
			return toolbridge.Result{JSON: encoded, IsError: !response.Success}, nil
		},
	}, nil
}
