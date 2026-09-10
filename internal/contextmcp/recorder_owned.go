package contextmcp

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

// RecorderOwnedConfig binds a composite transport receipt journal to one
// invocation, controller-derived caller, expected catalog, and agent tool
// projection. AgentProjection supplies no caller identity of its own.
type RecorderOwnedConfig struct {
	Path                string
	InvocationID        string
	CallerBindingSHA256 string
	CatalogSHA256       string
	AgentProjection     toolbridge.Projection
	MaxQueuedCalls      int
}

// RecorderOwnedBridgeCallTimeout is the ceiling for admitted agent tool callbacks on the recorder-owned MCP bridge.
//
// It must exceed the wait_agent controller cap with margin: the bridge abandons
// and cancels the callback at this deadline, so a wait admitted near the cap
// must resolve to its own finite timeout first. The server maximum is one minute.
const RecorderOwnedBridgeCallTimeout = 60 * time.Second

type recorderOwner struct {
	value   *toolreceipts.Recorder
	binding *toolreceipts.Binding
}

// RecorderCatalogSHA256 computes the immutable combined context-and-agent
// catalog without opening a receipt journal or granting callback authority.
func RecorderCatalogSHA256(broker *contextbroker.Broker, agent toolbridge.Projection) (string, error) {
	contextProjection, err := Projection(broker)
	if err != nil {
		return "", err
	}
	return toolreceipts.CatalogSHA256([]toolreceipts.OwnedProjection{
		{Owner: toolreceipts.OwnerContext, Projection: contextProjection},
		{Owner: toolreceipts.OwnerAgent, Projection: agent},
	})
}

// PrepareRecorderBinding derives the exact immutable receipt binding without
// opening or changing a journal. Catalog callbacks are inspected, but tool
// callbacks are never invoked.
func PrepareRecorderBinding(broker *contextbroker.Broker, invocationID, callerBindingSHA256 string, agent toolbridge.Projection) (toolreceipts.Binding, error) {
	return prepareRecorderBinding(broker, invocationID, callerBindingSHA256, agent, 0)
}

// PrepareRecorderBindingWithQueue derives the v2 binding for one serial,
// bounded FIFO transport without opening a receipt journal.
func PrepareRecorderBindingWithQueue(broker *contextbroker.Broker, invocationID, callerBindingSHA256 string, agent toolbridge.Projection, maxQueuedCalls int) (toolreceipts.Binding, error) {
	if maxQueuedCalls < 0 || maxQueuedCalls > 32 {
		return toolreceipts.Binding{}, errors.New("invalid recorder-owned MCP queue capacity")
	}
	return prepareRecorderBinding(broker, invocationID, callerBindingSHA256, agent, maxQueuedCalls)
}

func prepareRecorderBinding(broker *contextbroker.Broker, invocationID, callerBindingSHA256 string, agent toolbridge.Projection, maxQueuedCalls int) (toolreceipts.Binding, error) {
	if broker == nil || safepath.RequireDigest(invocationID) != nil || safepath.RequireDigest(callerBindingSHA256) != nil {
		return toolreceipts.Binding{}, errors.New("invalid recorder-owned MCP binding input")
	}
	state, err := contextbroker.Inspect(broker.JournalPath())
	if err != nil || state.Binding == nil || state.Binding.InvocationID != invocationID {
		return toolreceipts.Binding{}, errors.Join(errors.New("recorder invocation differs from context broker"), err)
	}
	brokerBindingID, err := broker.BindingID()
	if err != nil {
		return toolreceipts.Binding{}, err
	}
	boundBrokerID, err := state.Binding.ID()
	if err != nil || boundBrokerID != brokerBindingID {
		return toolreceipts.Binding{}, errors.New("context broker binding changed before recorder construction")
	}
	contextProjection, err := Projection(broker)
	if err != nil {
		return toolreceipts.Binding{}, err
	}
	owners := []toolreceipts.OwnedProjection{
		{Owner: toolreceipts.OwnerContext, Projection: contextProjection},
		{Owner: toolreceipts.OwnerAgent, Projection: agent},
	}
	catalogID, err := toolreceipts.CatalogSHA256(owners)
	if err != nil {
		return toolreceipts.Binding{}, err
	}
	tools := make([]toolreceipts.ToolOwner, 0)
	for _, owner := range owners {
		frozen, freezeErr := toolbridge.Compose(owner.Projection)
		if freezeErr != nil {
			return toolreceipts.Binding{}, freezeErr
		}
		catalog, catalogErr := frozen.Catalog()
		if catalogErr != nil {
			return toolreceipts.Binding{}, catalogErr
		}
		for _, definition := range catalog {
			tools = append(tools, toolreceipts.ToolOwner{Tool: definition.Name, Owner: owner.Owner})
		}
	}
	version := 1
	policy := ""
	if maxQueuedCalls > 0 {
		version = 2
		policy = toolreceipts.QueuePolicySerialFIFO
	}
	binding := toolreceipts.Binding{
		Version: version, InvocationID: invocationID, CallerBindingSHA256: callerBindingSHA256,
		CatalogSHA256: catalogID, Tools: tools,
		QueuePolicy: policy, MaxQueuedCalls: maxQueuedCalls,
	}
	binding.BindingID, err = binding.ID()
	if err != nil {
		return toolreceipts.Binding{}, err
	}
	return binding, nil
}

// NewRecorderOwned constructs an owned MCP server whose combined projection
// records transport intents and receipts before and after the exact context or
// agent callback. The context projection is derived here from broker so a
// caller cannot substitute a foreign context owner.
func NewRecorderOwned(broker *contextbroker.Broker, bearer string, config RecorderOwnedConfig, observe toolbridge.ObserveFunc) (*OwnedServer, error) {
	if broker == nil || !cleanRecorderPath(config.Path) || !validRecorderBearer(bearer) || safepath.RequireDigest(config.InvocationID) != nil || safepath.RequireDigest(config.CallerBindingSHA256) != nil || safepath.RequireDigest(config.CatalogSHA256) != nil || config.MaxQueuedCalls < 0 || config.MaxQueuedCalls > 32 {
		return nil, errors.New("invalid recorder-owned context bridge configuration")
	}
	prepared, err := PrepareRecorderBindingWithQueue(broker, config.InvocationID, config.CallerBindingSHA256, config.AgentProjection, config.MaxQueuedCalls)
	if err != nil || prepared.CatalogSHA256 != config.CatalogSHA256 {
		return nil, errors.Join(errors.New("recorder-owned MCP catalog differs"), err)
	}
	brokerBindingID, err := broker.BindingID()
	if err != nil {
		return nil, err
	}
	contextProjection, err := Projection(broker)
	if err != nil {
		return nil, err
	}
	owners := []toolreceipts.OwnedProjection{
		{Owner: toolreceipts.OwnerContext, Projection: contextProjection},
		{Owner: toolreceipts.OwnerAgent, Projection: config.AgentProjection},
	}
	recorder, err := toolreceipts.Open(toolreceipts.Config{
		Path: config.Path, InvocationID: config.InvocationID,
		CallerBindingSHA256: config.CallerBindingSHA256,
		CatalogSHA256:       prepared.CatalogSHA256, Owners: owners,
		QueuePolicy: prepared.QueuePolicy, MaxQueuedCalls: config.MaxQueuedCalls,
	})
	if err != nil {
		return nil, err
	}
	binding := recorder.Binding()
	if !reflect.DeepEqual(binding, prepared) {
		return nil, errors.New("recorder-owned MCP binding differs from prepared binding")
	}
	server, err := toolbridge.New(toolbridge.Config{
		Token: bearer, Observe: observe, MaxConcurrentCalls: 1,
		MaxQueuedCalls:  config.MaxQueuedCalls,
		MaxRequestBytes: 64 << 10, MaxResponseBytes: MaxWireResponseBytes,
		// R46: ceiling for admitted agent tool callbacks. Must exceed the
		// wait_agent controller cap (45s) with margin: the bridge abandons
		// and cancels the callback at this deadline, so a wait admitted
		// near the cap must resolve to its own finite timeout first.
		// Server maximum is one minute; fast tools are unaffected.
		CallTimeout: RecorderOwnedBridgeCallTimeout,
		Catalog:     recorder.Projection().Catalog, Call: recorder.Projection().Call,
	})
	if err != nil {
		return nil, err
	}
	return &OwnedServer{
		server: server, broker: broker, brokerPath: broker.JournalPath(), brokerBindingID: brokerBindingID,
		bearerSHA256: sha256.Sum256([]byte(bearer)), recorderPath: config.Path,
		recorder: recorderOwner{value: recorder, binding: &binding},
	}, nil
}

func cleanRecorderPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && len(path) <= 4096
}

func validRecorderBearer(bearer string) bool {
	if len(bearer) < 32 || len(bearer) > 512 || strings.TrimSpace(bearer) != bearer || !utf8.ValidString(bearer) {
		return false
	}
	for _, character := range bearer {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

// RecorderBinding returns a defensive copy of the exact composite recorder
// binding served by this listener, or false for a context-only listener.
func (r *OwnedRunning) RecorderBinding() (toolreceipts.Binding, bool) {
	if r == nil || r.owner == nil || r.owner.recorder.value == nil || r.owner.recorder.binding == nil {
		return toolreceipts.Binding{}, false
	}
	return r.owner.recorder.value.Binding(), true
}

// ValidateRecorderOwner proves that this handle retains the exact broker,
// bearer, receipt path, full recorder binding, and served catalog expected by
// its controller. It grants no callback or journal authority.
func (r *OwnedRunning) ValidateRecorderOwner(broker *contextbroker.Broker, bearer, recorderPath string, binding toolreceipts.Binding) error {
	if r == nil || r.running == nil || r.owner == nil || r.owner.server == nil || r.owner.recorder.value == nil || r.owner.recorder.binding == nil || broker == nil || broker != r.owner.broker {
		return errors.New("foreign recorder-owned context bridge")
	}
	brokerBindingID, err := broker.BindingID()
	if err != nil || brokerBindingID != r.owner.brokerBindingID || broker.JournalPath() != r.owner.brokerPath {
		return errors.New("recorder-owned context broker identity changed")
	}
	digest := sha256.Sum256([]byte(bearer))
	if subtle.ConstantTimeCompare(digest[:], r.owner.bearerSHA256[:]) != 1 {
		return errors.New("recorder-owned context bridge credential mismatch")
	}
	current := r.owner.recorder.value.Binding()
	if recorderPath == "" || recorderPath != r.owner.recorderPath || !reflect.DeepEqual(current, *r.owner.recorder.binding) || !reflect.DeepEqual(current, binding) || r.CatalogHash() != current.CatalogSHA256 {
		return errors.New("recorder-owned context bridge binding changed")
	}
	return nil
}
