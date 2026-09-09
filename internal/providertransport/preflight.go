package providertransport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/safepath"
)

const (
	maximumPreflightRequestBytes  = 1 << 20
	maximumPreflightArtifactBytes = 2 << 20
	preflightValidatePhase        = "validate-request"
	preflightBeginPhase           = "begin"
)

type preflightJournalObservation struct {
	state providergateway.State
	head  string
}

// preflightRejectionEvidence retains a rejected request only when the
// controller can prove it belonged to the existing binding and journal head.
// It intentionally has no provider call ID: no call intent was admitted.
type preflightRejectionEvidence struct {
	Version           int    `json:"version"`
	DiagnosticID      string `json:"diagnostic_id"`
	Phase             string `json:"phase"`
	BindingID         string `json:"binding_id"`
	InvocationID      string `json:"invocation_id"`
	JournalHeadSHA256 string `json:"journal_head_sha256"`
	RequestSHA256     string `json:"request_sha256"`
	RequestBytes      int64  `json:"request_bytes"`
	Cause             string `json:"cause"`
	Body              []byte `json:"body"`
}

type preflightDiagnosticIdentity struct {
	Version       int    `json:"version"`
	BindingID     string `json:"binding_id"`
	InvocationID  string `json:"invocation_id"`
	JournalHead   string `json:"journal_head_sha256"`
	RequestSHA256 string `json:"request_sha256"`
	RequestBytes  int64  `json:"request_bytes"`
	Phase         string `json:"phase"`
}

func inspectPreflightJournal(path string, binding providergateway.Binding) (preflightJournalObservation, error) {
	if err := validateGatewayJournalDestination(path); err != nil {
		return preflightJournalObservation{}, err
	}
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		if err == nil {
			err = errors.New("provider gateway journal has no bound head")
		}
		return preflightJournalObservation{}, err
	}
	state, err := providergateway.Inspect(path)
	if err != nil || state.Binding == nil || !reflect.DeepEqual(*state.Binding, binding) {
		if err == nil {
			err = errors.New("provider gateway journal binding differs from request")
		}
		return preflightJournalObservation{}, err
	}
	// journal.Read and Inspect each take their own validated snapshot. Re-read
	// the journal after Inspect so a concurrent append cannot be mistaken for
	// a stable no-effect state.
	latest, err := journal.Read(path)
	if err != nil || len(latest) == 0 || latest[len(latest)-1].Hash != events[len(events)-1].Hash {
		return preflightJournalObservation{}, errors.New("provider gateway journal changed during inspection")
	}
	head := latest[len(latest)-1].Hash
	if safepath.RequireDigest(head) != nil {
		return preflightJournalObservation{}, errors.New("provider gateway journal head is invalid")
	}
	bindingID, err := binding.ID()
	if err != nil || state.Binding.AccessInvocationID != binding.AccessInvocationID || safepath.RequireDigest(bindingID) != nil {
		return preflightJournalObservation{}, errors.New("provider gateway binding identity is invalid")
	}
	return preflightJournalObservation{state: state, head: head}, nil
}

func validateGatewayJournalDestination(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("provider gateway journal path must be absolute")
	}
	parent := filepath.Dir(path)
	if err := safepath.Directory(parent); err != nil {
		return err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	return safepath.Check(root, filepath.Base(path), false)
}

func recordPreflightRejection(request Request, journalState preflightJournalObservation, phase string, cause error) error {
	if cause == nil || phase != preflightValidatePhase && phase != preflightBeginPhase || len(request.Body) == 0 || len(request.Body) > maximumPreflightRequestBytes || len(cause.Error()) == 0 || len(cause.Error()) > maximumDiagnosticBytes {
		return errors.New("invalid provider preflight rejection evidence")
	}
	if !utf8SafeCause(cause.Error()) {
		return errors.New("invalid provider preflight rejection diagnostic")
	}
	current, err := inspectPreflightJournal(request.GatewayJournalPath, request.Binding)
	if err != nil || current.state.Pending != nil || journalState.state.Pending != nil || current.head != journalState.head || !reflect.DeepEqual(current.state, journalState.state) {
		return errors.New("provider gateway journal changed before preflight evidence")
	}
	bindingID, err := request.Binding.ID()
	stateBindingID, stateBindingErr := current.state.Binding.ID()
	if err != nil || stateBindingErr != nil || bindingID != stateBindingID {
		return errors.New("provider preflight binding identity differs")
	}
	digest := sha256.Sum256(request.Body)
	requestSHA256 := hex.EncodeToString(digest[:])
	identity, err := canonical.Hash("harness.provider-preflight-diagnostic.v1", preflightDiagnosticIdentity{
		Version: 1, BindingID: bindingID, InvocationID: request.Binding.AccessInvocationID,
		JournalHead: current.head, RequestSHA256: requestSHA256, RequestBytes: int64(len(request.Body)), Phase: phase,
	})
	if err != nil || safepath.RequireDigest(identity) != nil {
		return errors.New("provider preflight diagnostic identity unavailable")
	}
	record := preflightRejectionEvidence{
		Version: 1, DiagnosticID: identity, Phase: phase, BindingID: bindingID,
		InvocationID: request.Binding.AccessInvocationID, JournalHeadSHA256: current.head,
		RequestSHA256: requestSHA256, RequestBytes: int64(len(request.Body)), Cause: cause.Error(),
		Body: append([]byte(nil), request.Body...),
	}
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumPreflightArtifactBytes {
		return errors.New("provider preflight rejection evidence exceeds bound")
	}
	root, err := openPreflightEvidenceDirectory(request.GatewayJournalPath)
	if err != nil {
		return err
	}
	defer root.Close()
	artifact := "preflight-" + identity + ".request.json"
	_, err = publishPrivateEvidence(root, artifact, encoded, maximumPreflightArtifactBytes)
	if err != nil {
		return fmt.Errorf("provider preflight rejection evidence: %w", err)
	}
	return nil
}

// tryRecordPreflightRejection succeeds only when the gateway can be inspected
// as the same bound, non-pending journal observed by the caller. A failure is
// deliberately reported to the caller so it can fail closed as pending.
func tryRecordPreflightRejection(request Request, phase string, cause error) bool {
	observed, err := inspectPreflightJournal(request.GatewayJournalPath, request.Binding)
	if err != nil {
		return false
	}
	return recordPreflightRejection(request, observed, phase, cause) == nil
}

func tryRecordPreflightRejectionAtState(request Request, phase string, cause error, expected providergateway.State) bool {
	observed, err := inspectPreflightJournal(request.GatewayJournalPath, request.Binding)
	if err != nil || !reflect.DeepEqual(observed.state, expected) {
		return false
	}
	return recordPreflightRejection(request, observed, phase, cause) == nil
}

func openPreflightEvidenceDirectory(gatewayPath string) (*os.Root, error) {
	if err := validateGatewayJournalDestination(gatewayPath); err != nil {
		return nil, err
	}
	parent := filepath.Dir(gatewayPath)
	name := filepath.Base(gatewayPath) + ".preflight-evidence"
	if err := safepath.Directory(parent); err != nil {
		return nil, err
	}
	if err := safepath.Relative(name); err != nil {
		return nil, err
	}
	if err := safepath.EnsureDirectory(parent, name); err != nil {
		return nil, err
	}
	directory := filepath.Join(parent, name)
	if err := safepath.Directory(directory); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	return root, err
}

func utf8SafeCause(cause string) bool {
	if !utf8.ValidString(cause) {
		return false
	}
	for _, r := range cause {
		if r == '\x00' || r == '\r' || r == '\n' {
			return false
		}
	}
	return true
}
