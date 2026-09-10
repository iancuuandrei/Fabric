package contextmcp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"

	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/toolbridge"
)

// OwnedServer retains the exact broker and credential used to construct its
// generic MCP transport. These identities are established here rather than
// supplied later as caller labels.
type OwnedServer struct {
	server          *toolbridge.Server
	broker          *contextbroker.Broker
	brokerPath      string
	brokerBindingID string
	bearerSHA256    [sha256.Size]byte
	recorderPath    string
	recorder        recorderOwner
}

// NewOwned constructs a context bridge whose later running handle can prove
// in-process ownership of the exact broker pointer and copied bearer.
func NewOwned(broker *contextbroker.Broker, bearer string, observe toolbridge.ObserveFunc) (*OwnedServer, error) {
	return NewOwnedWithQueue(broker, bearer, observe, 0)
}

// NewOwnedWithQueue constructs the same context-only bridge with a bounded
// serial FIFO waiting queue. The served catalog and owner proof are identical
// to NewOwned; only simultaneous-call transport admission changes.
func NewOwnedWithQueue(broker *contextbroker.Broker, bearer string, observe toolbridge.ObserveFunc, maxQueuedCalls int) (*OwnedServer, error) {
	if broker == nil {
		return nil, errors.New("durable context broker required")
	}
	bindingID, err := broker.BindingID()
	if err != nil {
		return nil, err
	}
	server, err := NewWithQueue(broker, bearer, observe, maxQueuedCalls)
	if err != nil {
		return nil, err
	}
	return &OwnedServer{
		server: server, broker: broker, brokerPath: broker.JournalPath(),
		brokerBindingID: bindingID, bearerSHA256: sha256.Sum256([]byte(bearer)),
	}, nil
}

// OwnedRunning is the exact listener returned by one OwnedServer. It does not
// claim operating-system containment or PID-to-port attestation.
type OwnedRunning struct {
	running *toolbridge.Running
	owner   *OwnedServer
}

// Listen starts this owned bridge once and retains its exact server linkage.
func (s *OwnedServer) Listen() (*OwnedRunning, error) {
	if s == nil || s.server == nil || s.broker == nil {
		return nil, errors.New("owned context bridge required")
	}
	running, err := s.server.Listen()
	if err != nil {
		return nil, err
	}
	return &OwnedRunning{running: running, owner: s}, nil
}

// URL returns the exact loopback endpoint selected by the owned listener.
func (r *OwnedRunning) URL() string {
	if r == nil || r.running == nil {
		return ""
	}
	return r.running.URL
}

// CatalogHash identifies the exact immutable catalog served by this listener.
func (r *OwnedRunning) CatalogHash() string {
	if r == nil || r.running == nil {
		return ""
	}
	return r.running.CatalogHash()
}

// ValidateOwner proves that this handle was constructed over the supplied
// broker pointer and bearer and that the broker's immutable identities agree.
func (r *OwnedRunning) ValidateOwner(broker *contextbroker.Broker, bearer string) error {
	if r == nil || r.running == nil || r.owner == nil || r.owner.server == nil || r.owner.recorder.binding != nil || broker == nil || broker != r.owner.broker {
		return errors.New("foreign owned context bridge")
	}
	bindingID, err := broker.BindingID()
	if err != nil || bindingID != r.owner.brokerBindingID || broker.JournalPath() != r.owner.brokerPath {
		return errors.New("owned context bridge broker identity changed")
	}
	digest := sha256.Sum256([]byte(bearer))
	if subtle.ConstantTimeCompare(digest[:], r.owner.bearerSHA256[:]) != 1 {
		return errors.New("owned context bridge credential mismatch")
	}
	return nil
}

// Close stops admission, cancels active callbacks and waits for HTTP handlers
// subject to ctx through the underlying bridge.
func (r *OwnedRunning) Close(ctx context.Context) error {
	if r == nil || r.running == nil {
		return errors.New("owned running context bridge required")
	}
	return r.running.Close(ctx)
}

// Wait returns after the exact owned listener exits.
func (r *OwnedRunning) Wait() error {
	if r == nil || r.running == nil {
		return errors.New("owned running context bridge required")
	}
	return r.running.Wait()
}
