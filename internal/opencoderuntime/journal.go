package opencoderuntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/toolreceipts"
)

const (
	intentEvent           = "opencode-runtime.intent"
	boundEvent            = "opencode-runtime.bound"
	resultEvent           = "opencode-runtime.result"
	metadataEvent         = "opencode-runtime.metadata"
	transportFailureEvent = "opencode-runtime.transport-failure.v1"
)

var errCompositeAPIRequired = errors.New("composite OpenCode runtime API required")

// Intent binds generic runtime work to exact source/candidate context and the
// tool-session policy before session creation. An optional gateway digest is
// provenance only and grants no provider authority.
type Intent struct {
	Version    int                         `json:"version"`
	IntentID   string                      `json:"intent_id"`
	Invocation runtime.Invocation          `json:"invocation"`
	Directory  string                      `json:"directory"`
	Project    opencode.ProjectExpectation `json:"project"`
	Context    contextbroker.Binding       `json:"context"`
	Session    opencode.ToolSessionBinding `json:"session"`
	// SessionPlan is the v2 pre-effect session shape. It deliberately excludes
	// the OpenCode ProjectID, which is observable only after process startup.
	// Version 1 retains Session byte-for-byte and leaves SessionPlan nil.
	SessionPlan              *SessionPlan          `json:"session_plan,omitempty"`
	ToolReceipts             *toolreceipts.Binding `json:"tool_receipts,omitempty"`
	ProviderGatewayBindingID string                `json:"provider_gateway_binding_id,omitempty"`
	// StructuredOutput is an explicit, role-scoped native terminal contract.
	// It is absent on legacy, read-only, and composite intents.
	StructuredOutput *opencode.StructuredOutputExpectation `json:"structured_output,omitempty"`
}

// SessionPlan binds every session choice known before OpenCode starts, while
// leaving its generated/observed project locator to the post-start Bound.
type SessionPlan struct {
	Agent         string   `json:"agent"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Variant       string   `json:"variant"`
	ToolNames     []string `json:"tool_names"`
	CatalogSHA256 string   `json:"catalog_sha256"`
}

// ID validates and hashes the complete immutable runtime intent.
func (i Intent) ID() (string, error) {
	if (i.Version == 2 || i.Version == 3) && i.Session.ToolNames == nil {
		// canonical.Decode rejects null for this legacy nonnullable slice. V2
		// retains the field as an explicit empty shell for v1 byte compatibility.
		i.Session.ToolNames = []string{}
	}
	exact, err := runtime.NewInvocation(i.Invocation.Profile, i.Invocation.Input)
	contextID, contextErr := i.Context.ID()
	plan, planErr := i.effectiveSessionPlan()
	if err != nil || (i.Version != 1 && i.Version != 2 && i.Version != 3) || i.Invocation != exact || i.Invocation.Profile.Runtime != "opencode-http" || !cleanAbsolute(i.Directory) || validateProjectExpectation(i.Project, i.Directory) != nil || contextErr != nil || contextID == "" || i.Context.InvocationID != i.Invocation.ID || planErr != nil || validateToolReceiptIntent(i, plan) != nil || validateStructuredOutputIntent(i) != nil {
		return "", errors.New("invalid OpenCode runtime intent")
	}
	if plan.Provider != i.Invocation.Profile.Provider || plan.Model != i.Invocation.Profile.Model || plan.Variant != i.Invocation.Profile.Effort {
		return "", errors.New("OpenCode runtime session differs from invocation")
	}
	if i.Version == 1 && (i.Session.Session.IntentID != i.Invocation.ID || i.Session.Session.Directory != i.Directory) {
		return "", errors.New("OpenCode runtime session differs from invocation")
	}
	if i.Version == 1 && i.Project.Mode == opencode.ProjectModeGlobal && i.Session.Session.ProjectID != "global" {
		return "", errors.New("OpenCode runtime session differs from project expectation")
	}
	if i.ProviderGatewayBindingID != "" && safepath.RequireDigest(i.ProviderGatewayBindingID) != nil {
		return "", errors.New("invalid optional provider gateway identity")
	}
	i.IntentID = ""
	return canonical.Hash("harness.opencode-runtime-intent.v1", i)
}

// EffectiveSessionPlan returns a detached copy of the pre-effect session plan.
// It projects legacy v1 Session fields without changing their durable bytes.
func (i Intent) EffectiveSessionPlan() (SessionPlan, error) {
	plan, err := i.effectiveSessionPlan()
	plan.ToolNames = append([]string(nil), plan.ToolNames...)
	return plan, err
}

// ResolveToolSessionBinding binds an observed OpenCode project locator to the
// pre-effect session plan. It performs no I/O and does not guess the locator.
func (i Intent) ResolveToolSessionBinding(projectID string) (opencode.ToolSessionBinding, error) {
	if !projectLocator(projectID) {
		return opencode.ToolSessionBinding{}, errors.New("invalid observed OpenCode project locator")
	}
	if err := validateStructuredOutputIntent(i); err != nil {
		return opencode.ToolSessionBinding{}, err
	}
	plan, err := i.effectiveSessionPlan()
	if err != nil {
		return opencode.ToolSessionBinding{}, err
	}
	binding := opencode.ToolSessionBinding{
		Session: opencode.SessionBinding{
			IntentID: i.Invocation.ID, ProjectID: projectID, Directory: i.Directory,
			Agent: plan.Agent, Provider: plan.Provider, Model: plan.Model, Variant: plan.Variant,
		},
		ToolNames: append([]string(nil), plan.ToolNames...), CatalogSHA256: plan.CatalogSHA256,
	}
	if i.StructuredOutput != nil {
		expectation := *i.StructuredOutput
		expectation.Schema = append([]byte(nil), i.StructuredOutput.Schema...)
		binding.StructuredOutput = &expectation
	}
	if err := binding.Validate(); err != nil {
		return opencode.ToolSessionBinding{}, errors.New("invalid resolved OpenCode tool session binding")
	}
	if i.Version == 1 && !equalCanonical(binding, i.Session) {
		return opencode.ToolSessionBinding{}, errors.New("observed OpenCode project differs from legacy session intent")
	}
	return binding, nil
}

func (i Intent) effectiveSessionPlan() (SessionPlan, error) {
	if i.Version == 1 {
		if i.SessionPlan != nil || i.Session.Validate() != nil {
			return SessionPlan{}, errors.New("invalid legacy OpenCode session intent")
		}
		s := i.Session
		return SessionPlan{Agent: s.Session.Agent, Provider: s.Session.Provider, Model: s.Session.Model, Variant: s.Session.Variant, ToolNames: s.ToolNames, CatalogSHA256: s.CatalogSHA256}, nil
	}
	if (i.Version != 2 && i.Version != 3) || i.SessionPlan == nil || i.Session.Session != (opencode.SessionBinding{}) || len(i.Session.ToolNames) != 0 || i.Session.CatalogSHA256 != "" {
		return SessionPlan{}, errors.New("invalid planned OpenCode session intent")
	}
	plan := *i.SessionPlan
	probe := opencode.ToolSessionBinding{
		Session:   opencode.SessionBinding{IntentID: i.Invocation.ID, ProjectID: "planned", Directory: i.Directory, Agent: plan.Agent, Provider: plan.Provider, Model: plan.Model, Variant: plan.Variant},
		ToolNames: plan.ToolNames, CatalogSHA256: plan.CatalogSHA256,
	}
	if probe.Validate() != nil {
		return SessionPlan{}, errors.New("invalid planned OpenCode session intent")
	}
	return plan, nil
}

func validateToolReceiptIntent(intent Intent, plan SessionPlan) error {
	if intent.Version != 3 {
		if intent.ToolReceipts != nil {
			return errors.New("legacy OpenCode runtime intent contains composite tool receipts")
		}
		return nil
	}
	if intent.ToolReceipts == nil {
		return errors.New("composite OpenCode runtime intent lacks tool receipts")
	}
	bindingID, err := intent.ToolReceipts.ID()
	if err != nil || bindingID != intent.ToolReceipts.BindingID || intent.ToolReceipts.InvocationID != intent.Invocation.ID || intent.ToolReceipts.CatalogSHA256 != plan.CatalogSHA256 {
		return errors.New("composite tool receipts differ from OpenCode runtime intent")
	}
	if len(intent.ToolReceipts.Tools) != len(plan.ToolNames) {
		return errors.New("composite tool receipt catalog differs from session plan")
	}
	receiptNames := make([]string, len(intent.ToolReceipts.Tools))
	for index, tool := range intent.ToolReceipts.Tools {
		receiptNames[index] = tool.Tool
	}
	sort.Strings(receiptNames)
	if !slices.Equal(receiptNames, plan.ToolNames) {
		return errors.New("composite tool receipt catalog differs from session plan")
	}
	return nil
}

func validateIntentPaths(intent Intent, paths Paths) error {
	if intent.Version == 3 {
		if paths.Version != 2 {
			return errors.New("composite OpenCode runtime requires tool receipt paths")
		}
		return nil
	}
	if paths.Version != 1 {
		return errors.New("legacy OpenCode runtime cannot use composite tool receipt paths")
	}
	return nil
}

// Paths fixes every subordinate journal used to prove one runtime result.
type Paths struct {
	Version      int    `json:"version"`
	Session      string `json:"session"`
	Dispatch     string `json:"dispatch"`
	Broker       string `json:"broker"`
	Seal         string `json:"seal"`
	Gateway      string `json:"gateway,omitempty"`
	ToolReceipts string `json:"tool_receipts,omitempty"`
}

func (p Paths) validate() error {
	if (p.Version != 1 && p.Version != 2) || (p.Version == 1 && p.ToolReceipts != "") || (p.Version == 2 && !cleanAbsolute(p.ToolReceipts)) {
		return errors.New("invalid OpenCode runtime journal paths")
	}
	seen := []string{}
	paths := []string{p.Session, p.Dispatch, p.Broker, p.Seal}
	if p.Version == 2 {
		paths = append(paths, p.ToolReceipts)
	}
	for _, path := range paths {
		if !cleanAbsolute(path) {
			return errors.New("invalid or duplicate OpenCode runtime journal path")
		}
		for _, prior := range seen {
			if strings.EqualFold(prior, path) {
				return errors.New("invalid or duplicate OpenCode runtime journal path")
			}
		}
		seen = append(seen, path)
	}
	if p.Gateway != "" {
		if !cleanAbsolute(p.Gateway) {
			return errors.New("invalid or duplicate OpenCode runtime journal path")
		}
		for _, prior := range seen {
			if strings.EqualFold(prior, p.Gateway) {
				return errors.New("invalid or duplicate OpenCode runtime journal path")
			}
		}
	}
	return nil
}

// GatewayBound fixes the exact unused provider gateway prefix observed before
// OpenCode dispatch. It contains no request body or credential.
type GatewayBound struct {
	Binding        providergateway.Binding `json:"binding"`
	BindingID      string                  `json:"binding_id"`
	InitialHead    string                  `json:"initial_head"`
	InitialStateID string                  `json:"initial_state_id"`
}

// Bound is derived from an observed project, tool session and unused context
// broker. SealExpected retains the executable, host, tools and MCP admission
// receipts.
type Bound struct {
	Version              int                                      `json:"version"`
	IntentID             string                                   `json:"intent_id"`
	Paths                Paths                                    `json:"paths"`
	Project              opencode.ProjectReceipt                  `json:"project"`
	Session              opencode.ToolSessionRecord               `json:"session"`
	ContextBindingID     string                                   `json:"context_binding_id"`
	ContextCatalogID     string                                   `json:"context_catalog_id"`
	BrokerInitialHead    string                                   `json:"broker_initial_head"`
	BrokerInitialStateID string                                   `json:"broker_initial_state_id"`
	SealExpected         opencode.SynchronousToolTurnSealExpected `json:"seal_expected"`
	Gateway              *GatewayBound                            `json:"gateway,omitempty"`
	Composite            *CompositeBound                          `json:"composite,omitempty"`
}

// CompositeBound fixes the pre-effect composite dispatch and the unused tool
// receipt journal prefix observed before OpenCode dispatch.
type CompositeBound struct {
	Dispatch              opencode.CompositeDispatchIntent `json:"dispatch"`
	ReceiptInitialHead    string                           `json:"receipt_initial_head"`
	ReceiptInitialStateID string                           `json:"receipt_initial_state_id"`
}

// ResultRecord binds a generic result to exact current heads of all subordinate
// journals and to the seal receipt used by the OpenCode result mapper.
type ResultRecord struct {
	Version              int                                        `json:"version"`
	IntentID             string                                     `json:"intent_id"`
	SessionHead          string                                     `json:"session_head"`
	DispatchHead         string                                     `json:"dispatch_head"`
	BrokerHead           string                                     `json:"broker_head"`
	SealHead             string                                     `json:"seal_head"`
	SealReceipt          opencode.ToolTurnTerminalReceipt           `json:"seal_receipt"`
	CompositeSealReceipt *opencode.CompositeToolTurnTerminalReceipt `json:"composite_seal_receipt,omitempty"`
	GatewayHead          string                                     `json:"gateway_head,omitempty"`
	GatewayStateID       string                                     `json:"gateway_state_id,omitempty"`
	GatewayUsage         *providergateway.Usage                     `json:"gateway_usage,omitempty"`
	ToolReceiptsHead     string                                     `json:"tool_receipts_head,omitempty"`
	ToolReceiptsStateID  string                                     `json:"tool_receipts_state_id,omitempty"`
	Result               runtime.Result                             `json:"result"`
}

// RuntimeMetadataRecord preserves the classified, seal-bound OpenCode patch
// snapshot evidence even when its authority signal blocks result success.
type RuntimeMetadataRecord struct {
	Version           int                             `json:"version"`
	SHA256            string                          `json:"sha256"`
	IntentID          string                          `json:"intent_id"`
	SessionID         string                          `json:"session_id"`
	MessageID         string                          `json:"message_id"`
	Authority         string                          `json:"authority"`
	WorkspaceRoot     string                          `json:"workspace_root"`
	TranscriptSHA256  string                          `json:"transcript_sha256"`
	ObservationSHA256 string                          `json:"observation_sha256"`
	Receipts          []opencode.PatchSnapshotReceipt `json:"receipts"`
}

// TransportFailureRecord preserves an unresolved local OpenCode transport
// failure without converting it into a semantic result. The exact runtime
// intent, session and dispatch binding remain durable even when a later
// GET-only readback proves that the original POST completed.
type TransportFailureRecord struct {
	Version         int                               `json:"version"`
	IntentID        string                            `json:"intent_id"`
	SessionID       string                            `json:"session_id"`
	DispatchBinding opencode.Binding                  `json:"dispatch_binding"`
	Outcome         string                            `json:"outcome"`
	Failure         opencode.TransportFailureEvidence `json:"failure"`
}

// State is reconstructed from the runtime journal; Inspect additionally
// revalidates every referenced subordinate journal when a bound/result exists.
type State struct {
	Intent           *Intent                 `json:"intent,omitempty"`
	Bound            *Bound                  `json:"bound,omitempty"`
	TransportFailure *TransportFailureRecord `json:"transport_failure,omitempty"`
	Metadata         *RuntimeMetadataRecord  `json:"metadata,omitempty"`
	Result           *ResultRecord           `json:"result,omitempty"`
}

// RecordTransportFailure durably binds one classified HTTP failure to the
// already-recorded runtime and dispatch. Repeating the exact record is
// idempotent; conflicting evidence is rejected.
func RecordTransportFailure(path string, expected Intent, failure opencode.TransportFailureEvidence) (TransportFailureRecord, error) {
	state, err := inspectRuntimeJournal(path)
	if err != nil || state.Intent == nil || state.Bound == nil || !equalCanonical(*state.Intent, normalizedIntent(expected)) {
		return TransportFailureRecord{}, errors.New("OpenCode runtime binding unavailable for transport failure")
	}
	record := TransportFailureRecord{
		Version: 1, IntentID: state.Intent.IntentID,
		SessionID:       state.Bound.Session.SessionID,
		DispatchBinding: state.Bound.SealExpected.Dispatch.Dispatch.Binding,
		Outcome:         opencode.TransportDispatchStateUnknown, Failure: failure,
	}
	if err := validateTransportFailureStatic(record, *state.Intent, *state.Bound); err != nil {
		return TransportFailureRecord{}, err
	}
	if state.TransportFailure != nil {
		if equalCanonical(*state.TransportFailure, record) {
			return *state.TransportFailure, nil
		}
		return TransportFailureRecord{}, errors.New("OpenCode runtime transport failure already differs")
	}
	if state.Result != nil {
		return TransportFailureRecord{}, errors.New("completed OpenCode runtime cannot record transport failure")
	}
	if _, err := journal.Append(path, transportFailureEvent, record, validateRuntimeEvents); err != nil {
		return TransportFailureRecord{}, err
	}
	return record, nil
}

// RecordIntent writes the exact pre-session runtime intent. Repeating the same
// call only validates the existing record and creates no new transition.
func RecordIntent(path string, intent Intent) error {
	normalized := normalizedIntent(intent)
	if normalized.Version == 0 {
		return errors.New("invalid OpenCode runtime intent identity")
	}
	intent = normalized
	state, err := inspectRuntimeJournal(path)
	if err != nil {
		return err
	}
	if state.Intent != nil {
		if equalCanonical(*state.Intent, intent) {
			return nil
		}
		return errors.New("OpenCode runtime intent already differs")
	}
	_, err = journal.Append(path, intentEvent, intent, validateRuntimeEvents)
	return err
}

// RecordBound derives and persists the post-session/pre-dispatch binding. It
// requires an exact project receipt, offline session record, unused open
// broker, and empty dispatch/seal journals. It never creates or reconciles a
// project or session.
func RecordBound(path string, expected Intent, project opencode.ProjectReceipt, paths Paths, sealExpected opencode.SynchronousToolTurnSealExpected) (Bound, error) {
	state, err := inspectRuntimeJournal(path)
	if err != nil || state.Intent == nil || !equalCanonical(*state.Intent, normalizedIntent(expected)) {
		return Bound{}, errors.New("OpenCode runtime intent mismatch")
	}
	bound, err := deriveBound(*state.Intent, project, paths, sealExpected)
	if err != nil {
		return Bound{}, err
	}
	if state.Bound != nil {
		if equalCanonical(*state.Bound, bound) {
			return *state.Bound, nil
		}
		return Bound{}, errors.New("OpenCode runtime binding already differs")
	}
	if _, err := journal.Append(path, boundEvent, bound, validateRuntimeEvents); err != nil {
		return Bound{}, err
	}
	return bound, nil
}

// Complete performs journal-only recovery of the sealed tool-turn evidence,
// derives the generic result, and records exact subordinate heads. It never
// repeats a POST, host observation, shutdown, or provider call.
func Complete(path string, expected Intent) (ResultRecord, error) {
	if normalized := normalizedIntent(expected); normalized.Version == 3 {
		return ResultRecord{}, errCompositeAPIRequired
	}
	state, err := inspectRuntimeJournal(path)
	if err != nil || state.Intent == nil || state.Bound == nil || !equalCanonical(*state.Intent, normalizedIntent(expected)) {
		return ResultRecord{}, errors.New("OpenCode runtime binding unavailable")
	}
	if state.Bound.SealExpected.RuntimeMetadata != nil {
		metadata, metadataErr := deriveRuntimeMetadata(*state.Intent, *state.Bound)
		if metadataErr != nil {
			return ResultRecord{}, metadataErr
		}
		if state.Metadata != nil && !equalCanonical(*state.Metadata, metadata) {
			return ResultRecord{}, errors.New("OpenCode runtime metadata already differs")
		}
		if state.Metadata == nil {
			if _, metadataErr = journal.Append(path, metadataEvent, metadata, validateRuntimeEvents); metadataErr != nil {
				return ResultRecord{}, metadataErr
			}
			state.Metadata = &metadata
		}
	}
	record, err := deriveResult(*state.Intent, *state.Bound)
	if err != nil {
		return ResultRecord{}, err
	}
	if state.Result != nil {
		if equalCanonical(*state.Result, record) {
			return *state.Result, nil
		}
		return ResultRecord{}, errors.New("OpenCode runtime result already differs")
	}
	if _, err := journal.Append(path, resultEvent, record, validateRuntimeEvents); err != nil {
		return ResultRecord{}, err
	}
	return record, nil
}

// CompleteComposite performs journal-only recovery for a sealed composite
// tool turn. The verifier reads the authoritative owner journals and grants no
// callback, network, or process authority.
func CompleteComposite(path string, expected Intent, verify opencode.CompositeBackendVerifier) (ResultRecord, error) {
	if verify == nil {
		return ResultRecord{}, errors.New("composite backend verifier required")
	}
	state, err := inspectRuntimeJournal(path)
	if err != nil || state.Intent == nil || state.Bound == nil || state.Intent.Version != 3 || !equalCanonical(*state.Intent, normalizedIntent(expected)) {
		return ResultRecord{}, errors.New("composite OpenCode runtime binding unavailable")
	}
	if state.Bound.SealExpected.RuntimeMetadata != nil {
		metadata, metadataErr := deriveCompositeRuntimeMetadata(*state.Intent, *state.Bound, verify)
		if metadataErr != nil {
			return ResultRecord{}, metadataErr
		}
		if state.Metadata != nil && !equalCanonical(*state.Metadata, metadata) {
			return ResultRecord{}, errors.New("composite OpenCode runtime metadata already differs")
		}
		if state.Metadata == nil {
			if _, metadataErr = journal.Append(path, metadataEvent, metadata, validateRuntimeEvents); metadataErr != nil {
				return ResultRecord{}, metadataErr
			}
			state.Metadata = &metadata
		}
	}
	record, err := deriveCompositeResult(*state.Intent, *state.Bound, verify)
	if err != nil {
		return ResultRecord{}, err
	}
	if state.Result != nil {
		if equalCanonical(*state.Result, record) {
			return *state.Result, nil
		}
		return ResultRecord{}, errors.New("OpenCode runtime result already differs")
	}
	if _, err := journal.Append(path, resultEvent, record, validateRuntimeEvents); err != nil {
		return ResultRecord{}, err
	}
	return record, nil
}

// Inspect validates the runtime journal and rereads referenced subordinate
// journals. A result is accepted only when the current sealed evidence derives
// the exact same record; terminal booleans in this journal are never sufficient.
func Inspect(path string, expected Intent) (State, error) {
	state, err := inspectRuntimeJournal(path)
	if err != nil || state.Intent == nil || !equalCanonical(*state.Intent, normalizedIntent(expected)) {
		return State{}, errors.New("OpenCode runtime intent mismatch")
	}
	if state.Bound == nil {
		return cloneState(state)
	}
	if state.Intent.Version == 3 {
		return State{}, errCompositeAPIRequired
	}
	if err := validateBoundCurrent(*state.Intent, *state.Bound); err != nil {
		return State{}, err
	}
	if state.Metadata != nil {
		exact, metadataErr := deriveRuntimeMetadata(*state.Intent, *state.Bound)
		if metadataErr != nil || !equalCanonical(*state.Metadata, exact) {
			return State{}, errors.New("OpenCode runtime metadata lacks exact sealed evidence")
		}
	} else if state.Bound.SealExpected.RuntimeMetadata != nil && state.Result != nil {
		return State{}, errors.New("OpenCode runtime result lacks metadata evidence")
	}
	if state.Result != nil {
		exact, err := deriveResult(*state.Intent, *state.Bound)
		if err != nil || !equalCanonical(*state.Result, exact) {
			return State{}, errors.New("OpenCode runtime result lacks exact sealed evidence")
		}
	}
	return cloneState(state)
}

// InspectComposite validates a v3 runtime and rechecks composite transcript,
// owner backend, seal, and result evidence without repeating any effect.
func InspectComposite(path string, expected Intent, verify opencode.CompositeBackendVerifier) (State, error) {
	if verify == nil {
		return State{}, errors.New("composite backend verifier required")
	}
	state, err := inspectRuntimeJournal(path)
	if err != nil || state.Intent == nil || state.Intent.Version != 3 || !equalCanonical(*state.Intent, normalizedIntent(expected)) {
		return State{}, errors.New("OpenCode runtime intent mismatch")
	}
	if state.Bound == nil {
		return cloneState(state)
	}
	if err := validateBoundCurrent(*state.Intent, *state.Bound); err != nil {
		return State{}, err
	}
	if state.Metadata != nil {
		exact, metadataErr := deriveCompositeRuntimeMetadata(*state.Intent, *state.Bound, verify)
		if metadataErr != nil || !equalCanonical(*state.Metadata, exact) {
			return State{}, errors.New("composite OpenCode runtime metadata lacks exact sealed evidence")
		}
	} else if state.Bound.SealExpected.RuntimeMetadata != nil && state.Result != nil {
		return State{}, errors.New("composite OpenCode runtime result lacks metadata evidence")
	}
	if state.Result != nil {
		exact, err := deriveCompositeResult(*state.Intent, *state.Bound, verify)
		if err != nil || !equalCanonical(*state.Result, exact) {
			return State{}, errors.New("composite OpenCode runtime result lacks exact sealed evidence")
		}
	}
	return cloneState(state)
}

func deriveBound(intent Intent, project opencode.ProjectReceipt, paths Paths, expected opencode.SynchronousToolTurnSealExpected) (Bound, error) {
	if err := paths.validate(); err != nil {
		return Bound{}, err
	}
	if err := validateIntentPaths(intent, paths); err != nil {
		return Bound{}, err
	}
	if err := validateProjectReceipt(project, intent); err != nil {
		return Bound{}, err
	}
	plannedSession, err := intent.ResolveToolSessionBinding(project.ID)
	if err != nil || !equalCanonical(expected.Session, plannedSession) {
		return Bound{}, errors.New("seal expectation differs from planned runtime session")
	}
	session, err := opencode.ReadToolSession(paths.Session, plannedSession)
	if err != nil {
		return Bound{}, err
	}
	brokerEventsBefore, err := journal.Read(paths.Broker)
	if err != nil || len(brokerEventsBefore) != 1 {
		if err == nil {
			err = errors.New("unused context broker must contain only its binding")
		}
		return Bound{}, err
	}
	brokerHeadBefore := brokerEventsBefore[0].Hash
	broker, err := contextbroker.Inspect(paths.Broker)
	if err != nil || broker.Binding == nil || broker.Closed || broker.Pending != nil || broker.Calls != 0 || len(broker.Requests) != 0 || len(broker.Responses) != 0 || broker.ResponseBytes != 0 || !equalCanonical(*broker.Binding, intent.Context) {
		return Bound{}, errors.New("unused bound context broker required")
	}
	contextID, _ := intent.Context.ID()
	workingDirectory := expected.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = expected.HostRoot
	}
	if expected.Dispatch.Invocation != intent.Invocation || expected.Dispatch.BrokerBindingID != contextID || expected.Dispatch.BrokerCatalogID != intent.Context.CatalogID || expected.Dispatch.Dispatch.Binding.SessionID != session.SessionID || workingDirectory != intent.Directory || expected.Session.Session.Directory != intent.Directory {
		return Bound{}, errors.New("seal expectation differs from runtime intent")
	}
	dispatch := expected.Dispatch.Dispatch
	exactDispatch, err := opencode.DispatchForInvocationWithStructuredOutput(intent.Invocation, session.SessionID, dispatch.Binding.ParentID, dispatch.Binding.Agent, dispatch.Binding.Directory, dispatch.Binding.Root, intent.StructuredOutput)
	if err != nil || !equalCanonical(exactDispatch, dispatch) {
		return Bound{}, errors.New("seal dispatch differs from exact invocation")
	}
	for _, emptyPath := range []string{paths.Dispatch, paths.Seal} {
		events, err := journal.Read(emptyPath)
		if err != nil || len(events) != 0 {
			return Bound{}, errors.New("dispatch and seal journals must be empty before binding")
		}
	}
	brokerHead, err := journalHead(paths.Broker)
	if err != nil || brokerHead != brokerHeadBefore {
		if err == nil {
			err = errors.New("context broker changed during runtime binding")
		}
		return Bound{}, err
	}
	brokerID, err := canonical.Hash("harness.opencode-runtime-initial-broker.v1", broker)
	if err != nil {
		return Bound{}, err
	}
	gateway, err := deriveGatewayBound(intent, paths, expected)
	if err != nil {
		return Bound{}, err
	}
	composite, err := deriveCompositeBound(intent, paths, expected)
	if err != nil {
		return Bound{}, err
	}
	version := 1
	if composite != nil {
		version = 2
	}
	bound := Bound{Version: version, IntentID: intent.IntentID, Paths: paths, Project: project, Session: session, ContextBindingID: contextID, ContextCatalogID: intent.Context.CatalogID, BrokerInitialHead: brokerHead, BrokerInitialStateID: brokerID, SealExpected: expected, Gateway: gateway, Composite: composite}
	if err := validateBoundStatic(bound, intent); err != nil {
		return Bound{}, err
	}
	return bound, nil
}

func validateBoundCurrent(intent Intent, bound Bound) error {
	if err := validateBoundStatic(bound, intent); err != nil {
		return err
	}
	session, err := opencode.ReadToolSession(bound.Paths.Session, bound.Session.Binding)
	if err != nil || !equalCanonical(session, bound.Session) {
		return errors.New("OpenCode runtime session evidence changed")
	}
	events, err := journal.Read(bound.Paths.Broker)
	if err != nil || len(events) == 0 || events[0].Hash != bound.BrokerInitialHead {
		return errors.New("OpenCode runtime initial broker journal changed")
	}
	initial := contextbroker.State{Binding: &intent.Context, Requests: []contextbroker.Request{}, Responses: []contextbroker.Response{}}
	initialID, err := canonical.Hash("harness.opencode-runtime-initial-broker.v1", initial)
	if err != nil || initialID != bound.BrokerInitialStateID {
		return errors.New("OpenCode runtime initial broker state changed")
	}
	broker, err := contextbroker.Inspect(bound.Paths.Broker)
	if err != nil || broker.Binding == nil || !equalCanonical(*broker.Binding, intent.Context) {
		return errors.New("OpenCode runtime context binding changed")
	}
	if err := validateGatewayBoundCurrent(intent, bound); err != nil {
		return err
	}
	if err := validateCompositeBoundCurrent(intent, bound); err != nil {
		return err
	}
	return nil
}

func deriveGatewayBound(intent Intent, paths Paths, expected opencode.SynchronousToolTurnSealExpected) (*GatewayBound, error) {
	if intent.ProviderGatewayBindingID == "" {
		if paths.Gateway != "" || expected.Provider != nil {
			return nil, errors.New("unexpected provider gateway runtime binding")
		}
		return nil, nil
	}
	if paths.Gateway == "" || expected.Provider == nil {
		return nil, errors.New("provider gateway runtime binding required")
	}
	events, err := journal.Read(paths.Gateway)
	if err != nil || len(events) != 1 {
		return nil, errors.New("unused durable provider gateway required")
	}
	state, err := providergateway.Inspect(paths.Gateway)
	if err != nil || state.Binding == nil || len(state.Calls) != 0 || state.Pending != nil || state.Finished || state.Exhausted {
		return nil, errors.New("unused durable provider gateway required")
	}
	bindingID, err := state.Binding.ID()
	if err != nil || bindingID != intent.ProviderGatewayBindingID || expected.Provider.Proxy.GatewayBindingID != bindingID || expected.Provider.Process.Protocol != opencode.ProviderProtocol(state.Binding.Model.AdapterID) || expected.Provider.Process.ModelID != intent.Invocation.Profile.Model || expected.Provider.Process.ModelID != state.Binding.Model.Model {
		return nil, errors.New("provider gateway differs from runtime intent")
	}
	stateID, err := canonical.Hash("harness.opencode-runtime-initial-gateway.v1", state)
	if err != nil {
		return nil, err
	}
	return &GatewayBound{Binding: *state.Binding, BindingID: bindingID, InitialHead: events[0].Hash, InitialStateID: stateID}, nil
}

func deriveCompositeBound(intent Intent, paths Paths, expected opencode.SynchronousToolTurnSealExpected) (*CompositeBound, error) {
	if intent.Version != 3 {
		return nil, nil
	}
	state, head, err := toolreceipts.InspectWithHead(paths.ToolReceipts)
	if err != nil || state.Binding == nil || len(state.Calls) != 0 || !equalCanonical(*state.Binding, *intent.ToolReceipts) {
		return nil, errors.Join(errors.New("unused composite tool receipt journal required"), err)
	}
	stateID, err := canonical.Hash("harness.opencode-runtime-initial-tool-receipts.v1", *state.Binding)
	if err != nil {
		return nil, err
	}
	dispatch := opencode.CompositeDispatchIntent{
		Version: 1, DispatchPath: paths.Dispatch, Turn: expected.Dispatch,
		BrokerPath: paths.Broker, ReceiptPath: paths.ToolReceipts, Receipts: *intent.ToolReceipts,
	}
	dispatch.Receipts.Tools = append([]toolreceipts.ToolOwner(nil), intent.ToolReceipts.Tools...)
	return &CompositeBound{Dispatch: dispatch, ReceiptInitialHead: head, ReceiptInitialStateID: stateID}, nil
}

func validateGatewayBoundCurrent(intent Intent, bound Bound) error {
	if intent.ProviderGatewayBindingID == "" {
		if bound.Gateway != nil || bound.Paths.Gateway != "" || bound.SealExpected.Provider != nil {
			return errors.New("unexpected provider gateway runtime evidence")
		}
		return nil
	}
	if bound.Gateway == nil || bound.Paths.Gateway == "" || bound.SealExpected.Provider == nil {
		return errors.New("provider gateway runtime evidence missing")
	}
	events, err := journal.Read(bound.Paths.Gateway)
	if err != nil || len(events) == 0 || events[0].Hash != bound.Gateway.InitialHead {
		return errors.New("provider gateway initial journal changed")
	}
	state, err := providergateway.Inspect(bound.Paths.Gateway)
	if err != nil || state.Binding == nil || !equalCanonical(*state.Binding, bound.Gateway.Binding) {
		return errors.New("provider gateway binding changed")
	}
	// Inspect normalizes an empty call prefix to nil. Reconstruct the same
	// canonical state so later calls cannot change the identity of the initially
	// bound, unused gateway.
	initial := providergateway.State{Binding: &bound.Gateway.Binding}
	initialID, err := canonical.Hash("harness.opencode-runtime-initial-gateway.v1", initial)
	if err != nil || initialID != bound.Gateway.InitialStateID {
		return errors.New("provider gateway initial state changed")
	}
	return nil
}

func validateCompositeBoundCurrent(intent Intent, bound Bound) error {
	if intent.Version != 3 {
		return nil
	}
	state, head, err := toolreceipts.InspectWithHead(bound.Paths.ToolReceipts)
	if err != nil || state.Binding == nil || !equalCanonical(*state.Binding, *intent.ToolReceipts) {
		return errors.New("composite tool receipt binding changed")
	}
	events, err := journal.Read(bound.Paths.ToolReceipts)
	if err != nil || len(events) == 0 || events[0].Hash != bound.Composite.ReceiptInitialHead {
		return errors.New("composite tool receipt initial journal changed")
	}
	initialID, err := canonical.Hash("harness.opencode-runtime-initial-tool-receipts.v1", *intent.ToolReceipts)
	if err != nil || initialID != bound.Composite.ReceiptInitialStateID || head == "" {
		return errors.New("composite tool receipt initial state changed")
	}
	return nil
}

func deriveResult(intent Intent, bound Bound) (ResultRecord, error) {
	if err := validateBoundCurrent(intent, bound); err != nil {
		return ResultRecord{}, err
	}
	before, err := subjournalHeads(bound.Paths)
	if err != nil {
		return ResultRecord{}, err
	}
	observation, receipt, err := opencode.RecoverSynchronousToolTurnSealEvidence(bound.Paths.Seal, bound.Paths.Dispatch, bound.Paths.Broker, bound.SealExpected)
	if err != nil {
		return ResultRecord{}, err
	}
	result, err := opencode.ResultFromSealedToolTurn(intent.Invocation, observation, receipt)
	if err != nil {
		return ResultRecord{}, err
	}
	if result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil || result.Usage.CostMinorUnits != nil {
		return ResultRecord{}, errors.New("OpenCode runtime result invented provider usage")
	}
	var gatewayHead, gatewayStateID string
	var gatewayUsage *providergateway.Usage
	if bound.Gateway != nil {
		gateway, gatewayErr := validateFinalGateway(bound, observation)
		if gatewayErr != nil {
			return ResultRecord{}, gatewayErr
		}
		gatewayHead = before[4]
		gatewayStateID, gatewayErr = canonical.Hash("harness.opencode-runtime-final-gateway.v1", gateway)
		if gatewayErr != nil {
			return ResultRecord{}, gatewayErr
		}
		usage := gateway.Aggregate
		gatewayUsage = &usage
		input, output := usage.InputTokens, usage.OutputTokens
		result.Usage.InputTokens, result.Usage.OutputTokens = &input, &output
		if runtime.ValidateResult(intent.Invocation, result, true) != nil {
			return ResultRecord{}, errors.New("provider usage projection invalid")
		}
	}
	heads, err := subjournalHeads(bound.Paths)
	if err != nil || !reflectHeads(before, heads) {
		return ResultRecord{}, errors.New("OpenCode runtime subjournals changed during offline recovery")
	}
	if heads[0] != bound.Session.JournalHead {
		return ResultRecord{}, errors.New("OpenCode tool session journal changed")
	}
	return ResultRecord{Version: 1, IntentID: intent.IntentID, SessionHead: heads[0], DispatchHead: heads[1], BrokerHead: heads[2], SealHead: heads[3], GatewayHead: gatewayHead, GatewayStateID: gatewayStateID, GatewayUsage: gatewayUsage, SealReceipt: receipt, Result: result}, nil
}

func deriveRuntimeMetadata(intent Intent, bound Bound) (RuntimeMetadataRecord, error) {
	if bound.SealExpected.RuntimeMetadata == nil {
		return RuntimeMetadataRecord{}, errors.New("OpenCode runtime metadata expectation unavailable")
	}
	observation, receipt, err := opencode.RecoverSynchronousToolTurnSealEvidence(bound.Paths.Seal, bound.Paths.Dispatch, bound.Paths.Broker, bound.SealExpected)
	if err != nil {
		return RuntimeMetadataRecord{}, err
	}
	return newRuntimeMetadataRecord(intent, bound, observation.Final, observation.TranscriptSHA256, receipt.ObservationSHA256, observation.RuntimeMetadataExpectation, observation.RuntimeMetadata)
}

func deriveCompositeRuntimeMetadata(intent Intent, bound Bound, verify opencode.CompositeBackendVerifier) (RuntimeMetadataRecord, error) {
	if bound.SealExpected.RuntimeMetadata == nil || bound.Composite == nil || verify == nil {
		return RuntimeMetadataRecord{}, errors.New("composite OpenCode runtime metadata expectation unavailable")
	}
	observation, receipt, err := opencode.RecoverCompositeToolTurnSeal(bound.Paths.Seal, bound.Composite.Dispatch, bound.SealExpected, verify)
	if err != nil {
		return RuntimeMetadataRecord{}, err
	}
	return newRuntimeMetadataRecord(intent, bound, observation.Final, observation.TranscriptSHA256, receipt.ObservationSHA256, observation.RuntimeMetadataExpectation, observation.RuntimeMetadata)
}

func newRuntimeMetadataRecord(intent Intent, bound Bound, final opencode.Assistant, transcriptSHA256, observationSHA256 string, observed *opencode.RuntimeMetadataExpectation, receipts []opencode.PatchSnapshotReceipt) (RuntimeMetadataRecord, error) {
	expected := bound.SealExpected.RuntimeMetadata
	if expected == nil || observed == nil || !equalCanonical(*expected, *observed) || safepath.RequireDigest(transcriptSHA256) != nil || safepath.RequireDigest(observationSHA256) != nil || final.Binding.SessionID != bound.Session.SessionID || final.ID == "" {
		return RuntimeMetadataRecord{}, errors.New("OpenCode runtime metadata binding mismatch")
	}
	for _, receipt := range receipts {
		if receipt.SessionID != final.Binding.SessionID || receipt.MessageID != final.ID || opencode.ValidatePatchSnapshotReceiptAgainstExpectation(*expected, receipt) != nil {
			return RuntimeMetadataRecord{}, errors.New("OpenCode runtime metadata receipt binding mismatch")
		}
	}
	record := RuntimeMetadataRecord{Version: 1, IntentID: intent.IntentID, SessionID: final.Binding.SessionID, MessageID: final.ID, Authority: expected.Authority, WorkspaceRoot: expected.WorkspaceRoot, TranscriptSHA256: transcriptSHA256, ObservationSHA256: observationSHA256, Receipts: append([]opencode.PatchSnapshotReceipt(nil), receipts...)}
	if record.Receipts == nil {
		record.Receipts = []opencode.PatchSnapshotReceipt{}
	}
	digest, err := runtimeMetadataRecordDigest(record)
	if err != nil {
		return RuntimeMetadataRecord{}, err
	}
	record.SHA256 = digest
	return record, nil
}

func runtimeMetadataRecordDigest(record RuntimeMetadataRecord) (string, error) {
	record.SHA256 = ""
	return canonical.Hash("harness.opencode-runtime-metadata.v1", record)
}

func deriveCompositeResult(intent Intent, bound Bound, verify opencode.CompositeBackendVerifier) (ResultRecord, error) {
	if intent.Version != 3 || bound.Composite == nil || verify == nil {
		return ResultRecord{}, errors.New("composite OpenCode runtime binding unavailable")
	}
	if err := validateBoundCurrent(intent, bound); err != nil {
		return ResultRecord{}, err
	}
	before, err := compositeSubjournalHeads(bound.Paths)
	if err != nil {
		return ResultRecord{}, err
	}
	observation, receipt, err := opencode.RecoverCompositeToolTurnSeal(bound.Paths.Seal, bound.Composite.Dispatch, bound.SealExpected, verify)
	if err != nil {
		return ResultRecord{}, err
	}
	result, err := opencode.ResultFromSealedCompositeToolTurn(intent.Invocation, observation, receipt)
	if err != nil {
		return ResultRecord{}, err
	}
	if result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil || result.Usage.CostMinorUnits != nil {
		return ResultRecord{}, errors.New("OpenCode runtime result invented provider usage")
	}
	gateway, err := ValidateCompositeFinalGateway(bound, observation)
	if err != nil {
		return ResultRecord{}, err
	}
	gatewayStateID, err := canonical.Hash("harness.opencode-runtime-final-gateway.v1", gateway)
	if err != nil {
		return ResultRecord{}, err
	}
	usage := gateway.Aggregate
	input, output := usage.InputTokens, usage.OutputTokens
	result.Usage.InputTokens, result.Usage.OutputTokens = &input, &output
	if runtime.ValidateResult(intent.Invocation, result, true) != nil {
		return ResultRecord{}, errors.New("provider usage projection invalid")
	}
	after, err := compositeSubjournalHeads(bound.Paths)
	if err != nil || before != after {
		return ResultRecord{}, errors.New("composite OpenCode runtime subjournals changed during offline recovery")
	}
	if after[0] != bound.Session.JournalHead || after[5] != observation.ReceiptJournalHead {
		return ResultRecord{}, errors.New("composite OpenCode runtime journal evidence changed")
	}
	receiptCopy := receipt
	return ResultRecord{
		Version: 2, IntentID: intent.IntentID,
		SessionHead: after[0], DispatchHead: after[1], BrokerHead: after[2], SealHead: after[3],
		CompositeSealReceipt: &receiptCopy,
		GatewayHead:          after[4], GatewayStateID: gatewayStateID, GatewayUsage: &usage,
		ToolReceiptsHead: after[5], ToolReceiptsStateID: observation.ReceiptStateSHA256,
		Result: result,
	}, nil
}

func compositeSubjournalHeads(paths Paths) ([6]string, error) {
	var heads [6]string
	for index, path := range []string{paths.Session, paths.Dispatch, paths.Broker, paths.Seal, paths.Gateway, paths.ToolReceipts} {
		head, err := journalHead(path)
		if err != nil {
			return heads, err
		}
		heads[index] = head
	}
	return heads, nil
}

func subjournalHeads(paths Paths) ([5]string, error) {
	var heads [5]string
	for index, path := range []string{paths.Session, paths.Dispatch, paths.Broker, paths.Seal} {
		head, err := journalHead(path)
		if err != nil {
			return heads, err
		}
		heads[index] = head
	}
	if paths.Gateway != "" {
		head, err := journalHead(paths.Gateway)
		if err != nil {
			return heads, err
		}
		heads[4] = head
	}
	return heads, nil
}

func reflectHeads(left, right [5]string) bool { return left == right }

func validateFinalGateway(bound Bound, observation opencode.ToolTurnObservation) (providergateway.State, error) {
	state, err := providergateway.Inspect(bound.Paths.Gateway)
	if err != nil || state.Binding == nil || !equalCanonical(*state.Binding, bound.Gateway.Binding) || state.Pending != nil || !state.Finished || state.Exhausted || len(state.Calls) != len(observation.Generations) || len(state.Calls) == 0 {
		return providergateway.State{}, errors.New("provider gateway is not an exact finished tool turn")
	}
	structured := observation.StructuredOutput != nil || observation.StructuredOutputTool != nil
	if (observation.StructuredOutput == nil) != (observation.StructuredOutputTool == nil) {
		return providergateway.State{}, errors.New("structured output terminal capture is incomplete")
	}
	expectedStructured := bound.SealExpected.Dispatch.Dispatch.StructuredOutput
	if expectedStructured != nil {
		if expectedStructured.Validate() != nil || !structured {
			return providergateway.State{}, errors.New("provider structured output expectation is missing from terminal capture")
		}
	} else if structured {
		return providergateway.State{}, errors.New("unexpected provider structured output terminal")
	}
	for index, call := range state.Calls {
		generation := observation.Generations[index]
		if call.Intent.Sequence != index+1 || call.Receipt == nil || call.Receipt.Semantic == nil || call.Receipt.Semantic.OutputTextSHA256 != generation.TextSHA256 {
			return providergateway.State{}, errors.New("provider response semantics differ from OpenCode generation")
		}
		expectedToolCalls := len(generation.Calls)
		if index == len(state.Calls)-1 && structured {
			if generation.Finish != "tool-calls" || generation.StructuredOutputTool == nil || call.Receipt.Semantic.TerminalTool == nil {
				return providergateway.State{}, errors.New("provider structured output terminal differs from OpenCode generation")
			}
			expectedToolCalls++
		} else if generation.StructuredOutputTool != nil || call.Receipt.Semantic.TerminalTool != nil {
			return providergateway.State{}, errors.New("unexpected provider structured output terminal")
		}
		if len(call.Receipt.Semantic.ToolCalls) != expectedToolCalls {
			return providergateway.State{}, errors.New("provider response tool semantics differ from OpenCode generation")
		}
		finish := strings.ReplaceAll(generation.Finish, "-", "_")
		if call.Receipt.Finish != finish {
			return providergateway.State{}, errors.New("provider finish differs from OpenCode generation")
		}
		for callIndex, tool := range call.Receipt.Semantic.ToolCalls[:len(generation.Calls)] {
			observed := generation.Calls[callIndex]
			if tool.ID != observed.ProviderCallID || tool.Name != observed.Tool || tool.ArgumentsSHA256 != observed.ArgumentsSHA256 {
				return providergateway.State{}, errors.New("provider tool semantics differ from OpenCode generation")
			}
		}
		if index == len(state.Calls)-1 && structured {
			terminal := call.Receipt.Semantic.TerminalTool
			observed := observation.StructuredOutputTool
			if terminal.ID != observed.CallID || terminal.Name != opencode.StructuredOutputToolName || terminal.ArgumentsSHA256 != observed.ArgumentsSHA256 || call.Receipt.Semantic.TerminalSchemaSHA256 != digestText(string(expectedStructured.Schema)) || terminal.ArgumentsSHA256 != digestText(string(*observation.StructuredOutput)) {
				return providergateway.State{}, errors.New("provider structured output value differs from OpenCode terminal")
			}
		}
	}
	last := state.Calls[len(state.Calls)-1].Receipt
	if structured {
		if last.Finish != "tool_calls" || last.Semantic.TerminalTool == nil || last.Semantic.OutputTextSHA256 != digestText(observation.Text) {
			return providergateway.State{}, errors.New("provider structured output terminal differs from sealed OpenCode result")
		}
	} else if last.Finish != "stop" || len(last.Semantic.ToolCalls) != 0 || last.Semantic.OutputTextSHA256 != digestText(observation.Text) {
		return providergateway.State{}, errors.New("provider final text differs from sealed OpenCode result")
	}
	return state, nil
}

func validateRuntimeEvents(events []journal.Event) error {
	_, err := replay(events)
	return err
}

func replay(events []journal.Event) (State, error) {
	state := State{}
	for _, event := range events {
		switch event.Kind {
		case intentEvent:
			if state.Intent != nil {
				return State{}, errors.New("duplicate OpenCode runtime intent")
			}
			var intent Intent
			id, err := decodeIntent(event.Payload, &intent)
			if err != nil || intent.IntentID != id {
				return State{}, errors.New("invalid OpenCode runtime intent event")
			}
			state.Intent = &intent
		case boundEvent:
			if state.Intent == nil || state.Bound != nil {
				return State{}, errors.New("invalid OpenCode runtime bound transition")
			}
			var bound Bound
			if canonical.Decode(event.Payload, &bound) != nil || validateBoundStatic(bound, *state.Intent) != nil {
				return State{}, errors.New("invalid OpenCode runtime bound event")
			}
			state.Bound = &bound
		case transportFailureEvent:
			if state.Intent == nil || state.Bound == nil || state.TransportFailure != nil || state.Metadata != nil || state.Result != nil {
				return State{}, errors.New("invalid OpenCode runtime transport failure transition")
			}
			var failure TransportFailureRecord
			if canonical.Decode(event.Payload, &failure) != nil || validateTransportFailureStatic(failure, *state.Intent, *state.Bound) != nil {
				return State{}, errors.New("invalid OpenCode runtime transport failure event")
			}
			state.TransportFailure = &failure
		case metadataEvent:
			if state.Intent == nil || state.Bound == nil || state.Metadata != nil || state.Result != nil {
				return State{}, errors.New("invalid OpenCode runtime metadata transition")
			}
			var metadata RuntimeMetadataRecord
			if canonical.Decode(event.Payload, &metadata) != nil || validateRuntimeMetadataStatic(metadata, *state.Intent, *state.Bound) != nil {
				return State{}, errors.New("invalid OpenCode runtime metadata event")
			}
			state.Metadata = &metadata
		case resultEvent:
			if state.Intent == nil || state.Bound == nil || state.Result != nil || state.Bound.SealExpected.RuntimeMetadata != nil && state.Metadata == nil {
				return State{}, errors.New("invalid OpenCode runtime result transition")
			}
			var result ResultRecord
			if canonical.Decode(event.Payload, &result) != nil || validateResultStatic(result, *state.Intent, *state.Bound) != nil {
				return State{}, errors.New("invalid OpenCode runtime result event")
			}
			state.Result = &result
		default:
			return State{}, errors.New("unknown OpenCode runtime event")
		}
	}
	return state, nil
}

func validateTransportFailureStatic(record TransportFailureRecord, intent Intent, bound Bound) error {
	if record.Version != 1 || record.IntentID != intent.IntentID || record.SessionID != bound.Session.SessionID || !equalCanonical(record.DispatchBinding, bound.SealExpected.Dispatch.Dispatch.Binding) || record.Failure.Validate() != nil || record.Outcome != opencode.TransportDispatchStateUnknown {
		return errors.New("invalid OpenCode runtime transport failure projection")
	}
	if record.Failure.SessionID != record.SessionID || record.Failure.RequestMessageID != "" && record.Failure.RequestMessageID != record.DispatchBinding.ParentID {
		return errors.New("OpenCode runtime transport failure differs from dispatch")
	}
	if record.Failure.HTTPMethod != "POST" && record.Failure.HTTPMethod != "GET" {
		return errors.New("OpenCode runtime transport failure is not a dispatch or readback request")
	}
	if record.Failure.HTTPMethod == "POST" && record.Failure.Phase != opencode.TransportFailurePhaseMessagePostWait || record.Failure.HTTPMethod == "GET" && record.Failure.Phase != opencode.TransportFailurePhaseReadbackWait {
		return errors.New("unknown OpenCode runtime outcome lacks bound transport evidence")
	}
	return nil
}

func validateRuntimeMetadataStatic(record RuntimeMetadataRecord, intent Intent, bound Bound) error {
	expected := bound.SealExpected.RuntimeMetadata
	want := record.SHA256
	if expected == nil || record.Version != 1 || record.IntentID != intent.IntentID || record.Authority != expected.Authority || record.WorkspaceRoot != expected.WorkspaceRoot || record.Receipts == nil || record.SessionID != bound.Session.SessionID || record.MessageID == "" || safepath.RequireDigest(record.TranscriptSHA256) != nil || safepath.RequireDigest(record.ObservationSHA256) != nil || safepath.RequireDigest(want) != nil {
		return errors.New("invalid OpenCode runtime metadata projection")
	}
	for _, receipt := range record.Receipts {
		if receipt.SessionID != record.SessionID || receipt.MessageID != record.MessageID || opencode.ValidatePatchSnapshotReceiptAgainstExpectation(*expected, receipt) != nil {
			return errors.New("invalid OpenCode runtime metadata receipt projection")
		}
	}
	digest, err := runtimeMetadataRecordDigest(record)
	if err != nil || digest != want {
		return errors.New("invalid OpenCode runtime metadata identity")
	}
	return nil
}

func validateBoundStatic(bound Bound, intent Intent) error {
	if err := validateStructuredOutputIntent(intent); err != nil {
		return err
	}
	if err := validateIntentPaths(intent, bound.Paths); err != nil {
		return err
	}
	plannedSession, planErr := intent.ResolveToolSessionBinding(bound.Project.ID)
	wantVersion := 1
	if intent.Version == 3 {
		wantVersion = 2
	}
	if bound.Version != wantVersion || bound.IntentID != intent.IntentID || bound.Paths.validate() != nil || validateProjectReceipt(bound.Project, intent) != nil || planErr != nil || !equalCanonical(bound.Session.Binding, plannedSession) || bound.SealExpected.Tools.AllowStructuredOutput != (intent.StructuredOutput != nil) || safepath.RequireDigest(bound.Session.JournalHead) != nil || safepath.RequireDigest(bound.ContextBindingID) != nil || bound.ContextBindingID != mustContextID(intent.Context) || bound.ContextCatalogID != intent.Context.CatalogID || safepath.RequireDigest(bound.BrokerInitialHead) != nil || safepath.RequireDigest(bound.BrokerInitialStateID) != nil || !equalCanonical(bound.SealExpected.Session, plannedSession) || bound.SealExpected.Dispatch.Invocation != intent.Invocation || bound.SealExpected.Dispatch.BrokerBindingID != bound.ContextBindingID || bound.SealExpected.Dispatch.BrokerCatalogID != bound.ContextCatalogID || bound.SealExpected.Dispatch.Dispatch.Binding.SessionID != bound.Session.SessionID || !equalCanonical(bound.SealExpected.RuntimeMetadata, bound.SealExpected.Dispatch.RuntimeMetadata) || bound.SealExpected.RuntimeMetadata != nil && (opencode.ValidateRuntimeMetadataExpectation(*bound.SealExpected.RuntimeMetadata) != nil || bound.SealExpected.RuntimeMetadata.WorkspaceRoot != intent.Directory) || validateCompositeBoundStatic(bound, intent) != nil {
		return errors.New("invalid OpenCode runtime static binding")
	}
	if intent.ProviderGatewayBindingID == "" {
		if bound.Gateway != nil || bound.Paths.Gateway != "" || bound.SealExpected.Provider != nil {
			return errors.New("invalid unexpected provider gateway binding")
		}
	} else if validateGatewayBoundStatic(bound, intent) != nil {
		return errors.New("invalid provider gateway binding")
	}
	return nil
}

func validateGatewayBoundStatic(bound Bound, intent Intent) error {
	if bound.Gateway == nil || bound.Paths.Gateway == "" || bound.SealExpected.Provider == nil || bound.Gateway.BindingID != intent.ProviderGatewayBindingID || safepath.RequireDigest(bound.Gateway.InitialHead) != nil || safepath.RequireDigest(bound.Gateway.InitialStateID) != nil || opencode.ValidateProviderProcessIdentity(bound.SealExpected.Provider.Process) != nil || opencode.ValidateProviderProxyIdentity(bound.SealExpected.Provider.Proxy) != nil {
		return errors.New("invalid provider gateway binding")
	}
	bindingID, err := bound.Gateway.Binding.ID()
	if err != nil || bindingID != bound.Gateway.BindingID || bound.SealExpected.Provider.Proxy.GatewayBindingID != bindingID || bound.SealExpected.Provider.Process.ModelID != intent.Invocation.Profile.Model || bound.SealExpected.Provider.Process.ModelID != bound.Gateway.Binding.Model.Model || bound.SealExpected.Provider.Process.Protocol != opencode.ProviderProtocol(bound.Gateway.Binding.Model.AdapterID) {
		return errors.New("provider gateway binding differs from runtime")
	}
	return nil
}

func validateCompositeBoundStatic(bound Bound, intent Intent) error {
	if intent.Version != 3 {
		if bound.Composite != nil {
			return errors.New("unexpected composite runtime binding")
		}
		return nil
	}
	if bound.Composite == nil || intent.ToolReceipts == nil || safepath.RequireDigest(bound.Composite.ReceiptInitialHead) != nil || safepath.RequireDigest(bound.Composite.ReceiptInitialStateID) != nil {
		return errors.New("composite runtime binding missing")
	}
	want := opencode.CompositeDispatchIntent{
		Version: 1, DispatchPath: bound.Paths.Dispatch, Turn: bound.SealExpected.Dispatch,
		BrokerPath: bound.Paths.Broker, ReceiptPath: bound.Paths.ToolReceipts, Receipts: *intent.ToolReceipts,
	}
	want.Receipts.Tools = append([]toolreceipts.ToolOwner(nil), intent.ToolReceipts.Tools...)
	if !equalCanonical(bound.Composite.Dispatch, want) {
		return errors.New("composite runtime dispatch differs")
	}
	return nil
}

func validateProjectReceipt(project opencode.ProjectReceipt, intent Intent) error {
	if safepath.RequireDigest(project.SHA256) != nil || project.Directory != intent.Project.Directory || project.Mode != intent.Project.Mode || !projectLocator(project.ID) {
		return errors.New("OpenCode project receipt differs from runtime intent")
	}
	if intent.Version == 1 && project.ID != intent.Session.Session.ProjectID {
		return errors.New("OpenCode project receipt differs from legacy runtime intent")
	}
	switch project.Mode {
	case opencode.ProjectModeGlobal:
		if intent.Project.Worktree != "" || project.ID != "global" || project.Worktree != "/" || project.VCS != "" {
			return errors.New("invalid global OpenCode project receipt")
		}
	case opencode.ProjectModeGit:
		if project.ID == "global" || project.Worktree != intent.Project.Worktree || !cleanAbsolute(project.Worktree) || project.VCS != "git" {
			return errors.New("invalid Git OpenCode project receipt")
		}
	default:
		return errors.New("invalid OpenCode project receipt mode")
	}
	return nil
}

func validateProjectExpectation(project opencode.ProjectExpectation, directory string) error {
	if project.Directory != directory || !cleanAbsolute(project.Directory) {
		return errors.New("invalid OpenCode project expectation")
	}
	switch project.Mode {
	case opencode.ProjectModeGlobal:
		if project.Worktree != "" {
			return errors.New("invalid global OpenCode project expectation")
		}
	case opencode.ProjectModeGit:
		if !cleanAbsolute(project.Worktree) {
			return errors.New("invalid Git OpenCode project expectation")
		}
	default:
		return errors.New("invalid OpenCode project expectation mode")
	}
	return nil
}

func projectLocator(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validateResultStatic(record ResultRecord, intent Intent, bound Bound) error {
	if intent.Version == 3 {
		return validateCompositeResultStatic(record, intent, bound)
	}
	if record.Version != 1 || record.IntentID != intent.IntentID || record.SessionHead != bound.Session.JournalHead || runtime.ValidateResult(intent.Invocation, record.Result, true) != nil || record.Result.Usage.CostMinorUnits != nil {
		return errors.New("invalid OpenCode runtime result projection")
	}
	if record.CompositeSealReceipt != nil || record.ToolReceiptsHead != "" || record.ToolReceiptsStateID != "" {
		return errors.New("unexpected composite OpenCode runtime result")
	}
	for _, head := range []string{record.SessionHead, record.DispatchHead, record.BrokerHead, record.SealHead} {
		if safepath.RequireDigest(head) != nil {
			return errors.New("invalid OpenCode runtime subjournal head")
		}
	}
	if !record.SealReceipt.MCPHandlersStopped || !record.SealReceipt.RootProcessReaped || !record.SealReceipt.BrokerClosed || safepath.RequireDigest(record.SealReceipt.IntentID) != nil {
		return errors.New("invalid OpenCode runtime seal projection")
	}
	if bound.Gateway == nil {
		if record.GatewayHead != "" || record.GatewayStateID != "" || record.GatewayUsage != nil || record.Result.Usage.InputTokens != nil || record.Result.Usage.OutputTokens != nil || record.SealReceipt.ProviderProxyStopped || record.SealReceipt.ProviderProcessSHA256 != "" || record.SealReceipt.ProviderProxySHA256 != "" {
			return errors.New("unexpected provider gateway result projection")
		}
		return nil
	}
	if safepath.RequireDigest(record.GatewayHead) != nil || safepath.RequireDigest(record.GatewayStateID) != nil || record.GatewayUsage == nil || record.Result.Usage.InputTokens == nil || record.Result.Usage.OutputTokens == nil || *record.Result.Usage.InputTokens != record.GatewayUsage.InputTokens || *record.Result.Usage.OutputTokens != record.GatewayUsage.OutputTokens || record.GatewayUsage.InputTokens < 0 || record.GatewayUsage.OutputTokens < 0 || !record.SealReceipt.ProviderProxyStopped || record.SealReceipt.ProviderProcessSHA256 != bound.SealExpected.Provider.Process.SHA256 || record.SealReceipt.ProviderProxySHA256 != bound.SealExpected.Provider.Proxy.SHA256 {
		return errors.New("invalid provider gateway result projection")
	}
	for _, optional := range []*int64{record.GatewayUsage.ReasoningTokens, record.GatewayUsage.CacheReadTokens, record.GatewayUsage.CacheWriteTokens} {
		if optional != nil && (*optional < 0 || *optional > record.GatewayUsage.InputTokens+record.GatewayUsage.OutputTokens) {
			return errors.New("invalid provider gateway usage projection")
		}
	}
	return nil
}

func validateCompositeResultStatic(record ResultRecord, intent Intent, bound Bound) error {
	if record.Version != 2 || bound.Version != 2 || bound.Composite == nil || intent.ToolReceipts == nil || record.IntentID != intent.IntentID || record.SessionHead != bound.Session.JournalHead || record.CompositeSealReceipt == nil || !equalCanonical(record.SealReceipt, opencode.ToolTurnTerminalReceipt{}) || runtime.ValidateResult(intent.Invocation, record.Result, true) != nil || record.Result.Usage.CostMinorUnits != nil {
		return errors.New("invalid composite OpenCode runtime result projection")
	}
	for _, digest := range []string{record.SessionHead, record.DispatchHead, record.BrokerHead, record.SealHead, record.GatewayHead, record.GatewayStateID, record.ToolReceiptsHead, record.ToolReceiptsStateID} {
		if safepath.RequireDigest(digest) != nil {
			return errors.New("invalid composite OpenCode runtime subjournal head")
		}
	}
	receipt := record.CompositeSealReceipt
	if receipt.Version != 1 || receipt.InvocationID != intent.Invocation.ID || receipt.ReceiptBindingID != intent.ToolReceipts.BindingID || receipt.ReceiptJournalHead != record.ToolReceiptsHead || receipt.ReceiptStateSHA256 != record.ToolReceiptsStateID || !receipt.MCPHandlersStopped || !receipt.ProviderProxyStopped || !receipt.RootProcessReaped || !receipt.BrokerClosed || bound.Gateway == nil || bound.SealExpected.Provider == nil || record.GatewayUsage == nil || record.Result.Usage.InputTokens == nil || record.Result.Usage.OutputTokens == nil || *record.Result.Usage.InputTokens != record.GatewayUsage.InputTokens || *record.Result.Usage.OutputTokens != record.GatewayUsage.OutputTokens || record.GatewayUsage.InputTokens < 0 || record.GatewayUsage.OutputTokens < 0 || bound.SealExpected.Provider != nil && (receipt.ProviderProcessSHA256 != bound.SealExpected.Provider.Process.SHA256 || receipt.ProviderProxySHA256 != bound.SealExpected.Provider.Proxy.SHA256) {
		return errors.New("invalid composite OpenCode runtime terminal projection")
	}
	for _, optional := range []*int64{record.GatewayUsage.ReasoningTokens, record.GatewayUsage.CacheReadTokens, record.GatewayUsage.CacheWriteTokens} {
		if optional != nil && (*optional < 0 || *optional > record.GatewayUsage.InputTokens+record.GatewayUsage.OutputTokens) {
			return errors.New("invalid composite provider gateway usage projection")
		}
	}
	return nil
}

func inspectRuntimeJournal(path string) (State, error) {
	events, err := journal.Read(path)
	if err != nil {
		return State{}, err
	}
	return replay(events)
}

func decodeIntent(raw []byte, intent *Intent) (string, error) {
	if err := canonical.Decode(raw, intent); err != nil {
		return "", err
	}
	return intent.ID()
}

func normalizedIntent(intent Intent) Intent {
	id, err := intent.ID()
	if err != nil || (intent.IntentID != "" && intent.IntentID != id) {
		return Intent{}
	}
	if (intent.Version == 2 || intent.Version == 3) && intent.Session.ToolNames == nil {
		intent.Session.ToolNames = []string{}
	}
	if intent.SessionPlan != nil {
		plan := *intent.SessionPlan
		plan.ToolNames = append([]string(nil), intent.SessionPlan.ToolNames...)
		intent.SessionPlan = &plan
	}
	if intent.ToolReceipts != nil {
		binding := *intent.ToolReceipts
		binding.Tools = append([]toolreceipts.ToolOwner(nil), intent.ToolReceipts.Tools...)
		intent.ToolReceipts = &binding
	}
	if intent.StructuredOutput != nil {
		expectation := *intent.StructuredOutput
		expectation.Schema = append([]byte(nil), intent.StructuredOutput.Schema...)
		intent.StructuredOutput = &expectation
	}
	intent.IntentID = id
	return intent
}

func journalHead(path string) (string, error) {
	events, err := journal.Read(path)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return "", errors.New("OpenCode runtime subjournal is empty")
	}
	return events[len(events)-1].Hash, nil
}

func mustContextID(binding contextbroker.Binding) string {
	id, _ := binding.ID()
	return id
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && len(path) <= 4096
}

func equalCanonical(left, right any) bool {
	a, err := canonical.Bytes(left)
	if err != nil {
		return false
	}
	b, err := canonical.Bytes(right)
	return err == nil && bytes.Equal(a, b)
}

func digestText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func cloneState(state State) (State, error) {
	raw, err := canonical.Bytes(state)
	if err != nil {
		return State{}, err
	}
	var clone State
	if err := canonical.Decode(raw, &clone); err != nil {
		return State{}, err
	}
	return clone, nil
}
