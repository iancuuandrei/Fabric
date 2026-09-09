package codexruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/ri"
)

// ToolSession owns persistent lexical transport for one runtime host lifetime.
// Its requests still pass through the adapter's durable intent/response broker.
type ToolSession struct {
	adapter   *Adapter
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	stream    *ri.Stream
	binding   string
	attempted bool
	closed    bool
}

// NewToolSession creates an owner without launching a process. The first admitted
// lexical request starts the pinned reader from the recorded journal binding.
func NewToolSession(ctx context.Context, adapter *Adapter) *ToolSession {
	ctx, cancel := context.WithCancel(ctx)
	return &ToolSession{adapter: adapter, ctx: ctx, cancel: cancel}
}

// Close cancels active IO and reaps any reader; it is safe to call repeatedly.
func (s *ToolSession) Close() error {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.stream != nil {
		return s.stream.Close()
	}
	return nil
}

// HandleTool serializes journal admission and uses one immutable lexical binding.
// A failed launch or transport is never replaced automatically in this session.
func (s *ToolSession) HandleTool(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("tool session closed")
	}
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	return s.adapter.handleTool(callCtx, raw, s.readLexical)
}

func (s *ToolSession) readLexical(ctx context.Context, binding LexicalBinding, raw json.RawMessage) (any, error) {
	base, err := binding.Base.Compact()
	if err != nil {
		return nil, err
	}
	var overlay *LexicalOverlayRecord
	if o := binding.Overlay; o != nil {
		manifest, err := o.Manifest.Reference(o.ManifestPath)
		if err != nil {
			return nil, err
		}
		overlay = &LexicalOverlayRecord{Candidate: o.Candidate, Manifest: manifest, SourceRoot: o.SourceRoot, Deleted: o.Deleted}
	}
	id, err := canonical.Hash("harness.runtime-lexical-session.v2", LexicalRecord{Version: 1, Base: base, Overlay: overlay, Executable: binding.Executable, ExecutableSHA256: binding.ExecutableSHA256})
	if err != nil {
		return nil, err
	}
	if s.attempted && s.binding != id {
		return nil, errors.New("lexical session binding changed")
	}
	if !s.attempted {
		s.attempted = true
		s.binding = id
		s.stream, err = (ri.Client{Executable: binding.Executable, ExecutableHash: binding.ExecutableSHA256}).OpenStream(s.ctx)
		if err != nil {
			return nil, err
		}
	}
	if s.stream == nil {
		return nil, errors.New("lexical session launch previously failed")
	}
	return lexicalReadWith(ctx, binding, raw, s.stream)
}
