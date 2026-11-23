package control

import (
	"context"
	"errors"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/agentcontrol"
	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

type agentDispatchBinding struct {
	JournalPath    string
	ControllerPath string
	AdmissionID    string
	Node           agenttree.Node
	InvocationID   string
	AgentTurn      *taskscheduler.AgentTurnBinding
	Recovering     bool
}

// AgentDispatchAdmission is the controller-ordered authority to start one
// exact invocation through its durably registered agent node.
type AgentDispatchAdmission struct {
	Version    int                             `json:"version"`
	RunID      string                          `json:"run_id"`
	TreeID     string                          `json:"tree_id"`
	Node       agenttree.Node                  `json:"node"`
	Invocation runtime.Invocation              `json:"invocation"`
	AgentTurn  *taskscheduler.AgentTurnBinding `json:"agent_turn,omitempty"`
}

// ID returns the content identity used by the terminal observation.
func (a AgentDispatchAdmission) ID() (string, error) {
	return canonical.Hash("harness.agent-dispatch-admission.v1", a)
}

// AgentDispatchObservation closes one admission with either an accepted exact
// result or explicit unresolved runtime uncertainty.
type AgentDispatchObservation struct {
	Version      int              `json:"version"`
	AdmissionID  string           `json:"admission_id"`
	InvocationID string           `json:"invocation_id"`
	Status       agenttree.Status `json:"status"`
	ResultSHA256 string           `json:"result_sha256,omitempty"`
}

// AgentDispatchState is the replayed admission and optional terminal fact.
type AgentDispatchState struct {
	Admission   AgentDispatchAdmission    `json:"admission"`
	Observation *AgentDispatchObservation `json:"observation,omitempty"`
}

func executeOpenCodeProvider(ctx context.Context, controllerPath string, snapshot Snapshot, invocation runtime.Invocation, lease opencoderuntime.CandidateLease) (runtime.Result, providerDispatchReceipt, error) {
	current, err := Inspect(controllerPath)
	if err != nil || current.RunID != snapshot.RunID || !sameCanonical(current.Creation.Config, snapshot.Creation.Config) {
		return runtime.Result{}, providerDispatchReceipt{}, errors.Join(errors.New("OpenCode controller state changed"), err)
	}
	if err := requireCurrentHostAdmission(ctx, current); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	snapshot = current
	binding, err := beginAgentDispatchForTurn(controllerPath, snapshot, invocation, scheduledAgentTurn(ctx))
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	if err := ensureOpenCodeDispatchCapacity(snapshot, invocation); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	result, receipt, dispatchErr := executeOpenCodeProviderRuntime(ctx, controllerPath, snapshot, invocation, lease)
	if dispatchErr != nil {
		return result, receipt, errors.Join(dispatchErr, finishAgentDispatchUnknown(binding))
	}
	if err := finishAgentDispatch(binding, receipt.ResultHash); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	return result, receipt, nil
}

func executeDirectProvider(ctx context.Context, controllerPath string, configuration config.Config, runID string, invocation runtime.Invocation) (runtime.Result, providerDispatchReceipt, error) {
	snapshot, err := Inspect(controllerPath)
	if err != nil || snapshot.RunID != runID || !sameCanonical(snapshot.Creation.Config, configuration) {
		return runtime.Result{}, providerDispatchReceipt{}, errors.Join(errors.New("direct provider controller state changed"), err)
	}
	if err := requireCurrentHostAdmission(ctx, snapshot); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	binding, err := beginAgentDispatchForTurn(controllerPath, snapshot, invocation, scheduledAgentTurn(ctx))
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	if err := ensureDirectDispatchCapacity(snapshot, invocation); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	result, receipt, dispatchErr := executeDirectProviderRuntime(ctx, controllerPath, snapshot, invocation)
	if dispatchErr != nil {
		return result, receipt, errors.Join(dispatchErr, finishAgentDispatchUnknown(binding))
	}
	if err := finishAgentDispatch(binding, receipt.ResultHash); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	return result, receipt, nil
}

func beginAgentDispatch(controllerPath string, snapshot Snapshot, invocation runtime.Invocation) (agentDispatchBinding, error) {
	return beginAgentDispatchForTurn(controllerPath, snapshot, invocation, nil)
}

type scheduledAgentTurnContextKey struct{}

func withScheduledAgentTurn(ctx context.Context, turn *taskscheduler.AgentTurnBinding) context.Context {
	if turn == nil {
		return ctx
	}
	copy := *turn
	return context.WithValue(ctx, scheduledAgentTurnContextKey{}, &copy)
}

func scheduledAgentTurn(ctx context.Context) *taskscheduler.AgentTurnBinding {
	if ctx == nil {
		return nil
	}
	turn, _ := ctx.Value(scheduledAgentTurnContextKey{}).(*taskscheduler.AgentTurnBinding)
	return turn
}

func providerRoleJournalStem(role string, turn *taskscheduler.AgentTurnBinding) string {
	if turn == nil {
		return role
	}
	return role + ".turn-" + turn.TurnID
}

// A role may perform several independent turns in one engineering run. Keep
// the historical first-turn path for exact recovery, but never bind another
// invocation to that first turn's gateway or runtime state.
func providerInvocationJournalStem(controllerPath string, invocation runtime.Invocation, turn *taskscheduler.AgentTurnBinding) (string, error) {
	stem := providerRoleJournalStem(invocation.Profile.Role, turn)
	if turn != nil {
		return stem, nil
	}
	events, err := journal.Read(controllerPath + "." + stem + ".opencode-runtime.jsonl")
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return stem, nil
	}
	var prior opencoderuntime.Intent
	if events[0].Kind != "opencode-runtime.intent" || canonical.Decode(events[0].Payload, &prior) != nil {
		return "", errors.Join(opencoderuntime.ErrRecoveryRequired, errors.New("provider runtime first intent is invalid"))
	}
	if id, err := prior.ID(); err != nil || id != prior.IntentID {
		return "", errors.Join(opencoderuntime.ErrRecoveryRequired, errors.New("provider runtime intent identity is invalid"))
	}
	if prior.Invocation.ID == invocation.ID {
		return stem, nil
	}
	return stem + ".invocation-" + invocation.ID, nil
}

// isolatedWriterJournalStem returns the collision-free deterministic journal
// namespace for one controller-admitted static isolated/staged OpenCode task
// writer turn: `<role>.invocation-<invocationID>`. It is derived from the
// admitted task/invocation binding before any journal read or write, so two
// distinct valid task invocations derive distinct journals even when neither
// journal exists yet; concurrent writers never share a journal by racing the
// legacy claimant read below. Serial, scheduled (turn-bound) and Codex paths
// are untouched: this applies only to turn-free writer/fixer invocations with
// the opencode-http runtime admitted by the frozen isolated cohort. Legacy
// absent-field serialization is unchanged and old journals are never renamed.
// The second return value reports whether the deterministic namespace applies;
// callers fall back to providerInvocationJournalStem when it does not.
func isolatedWriterJournalStem(s Snapshot, invocation runtime.Invocation, turn *taskscheduler.AgentTurnBinding) (string, bool) {
	if turn != nil {
		return "", false
	}
	if invocation.Profile.Role != "writer" && invocation.Profile.Role != "fixer" {
		return "", false
	}
	if invocation.Profile.Runtime != "opencode-http" {
		return "", false
	}
	if !isolatedImplementationEnabled(s) {
		return "", false
	}
	// Controller-admitted binding only: the exact frozen child invocation must
	// equal the candidate invocation. Model prose, scheduler hints and
	// journal contents never grant this namespace.
	admitted, _, err := isolatedWriterInvocationForReceiptID(s, invocation.ID)
	if err != nil || admitted != invocation {
		return "", false
	}
	return invocation.Profile.Role + ".invocation-" + invocation.ID, true
}

// providerInvocationJournalStemForSnapshot resolves the same journal stem as
// providerInvocationJournalStem for serial and scheduled turns, but consults
// the controller-admitted isolated task/invocation binding first. Execution,
// usage verification, scheduler reconciliation and resumed observation must all
// resolve through this snapshot-aware helper with their own current snapshot
// so every path derives the identical deterministic namespace.
func providerInvocationJournalStemForSnapshot(controllerPath string, s Snapshot, invocation runtime.Invocation, turn *taskscheduler.AgentTurnBinding) (string, error) {
	if stem, ok := isolatedWriterJournalStem(s, invocation, turn); ok {
		return stem, nil
	}
	return providerInvocationJournalStem(controllerPath, invocation, turn)
}

func beginAgentDispatchForTurn(controllerPath string, snapshot Snapshot, invocation runtime.Invocation, turn *taskscheduler.AgentTurnBinding) (agentDispatchBinding, error) {
	contextHash, err := access.InputID(invocation.Input)
	if err != nil {
		return agentDispatchBinding{}, err
	}
	authority, err := agentAuthority(invocation.Profile.Role)
	if err != nil {
		return agentDispatchBinding{}, err
	}
	journalPath := controllerPath + ".agent-tree"
	state, err := agenttree.Inspect(journalPath)
	if err != nil {
		return agentDispatchBinding{}, err
	}
	var node agenttree.Node
	if turn != nil {
		if safepath.RequireDigest(turn.ParentAgentID) != nil || safepath.RequireDigest(turn.AgentID) != nil || safepath.RequireDigest(turn.TurnID) != nil || turn.TurnSequence < 1 {
			return agentDispatchBinding{}, errors.New("invalid scheduled agent turn")
		}
		var ok bool
		node, ok = agentNodeByID(state, turn.AgentID)
		if !ok || node.ParentAgentID != turn.ParentAgentID || node.Role != invocation.Profile.Role || node.Authority != authority || turn.TurnSequence == 1 && (node.InvocationID != invocation.ID || node.ContextSHA256 != contextHash) {
			return agentDispatchBinding{}, errors.New("scheduled agent turn binding differs")
		}
	} else if invocation.Profile.Role == "planner" {
		spec := agenttree.NodeSpec{Name: "root", Role: "planner", Authority: authority, InvocationID: invocation.ID, ContextSHA256: contextHash}
		if state.TreeID == "" {
			node, err = agenttree.Create(journalPath, snapshot.RunID, spec)
		} else {
			node, err = exactAgentNode(state, "", "/root", spec)
		}
	} else {
		if state.TreeID == "" {
			state, err = bootstrapAgentTreeRoot(journalPath, snapshot)
			if err != nil {
				return agentDispatchBinding{}, err
			}
		}
		if state.TreeID != snapshot.RunID {
			return agentDispatchBinding{}, errors.New("agent tree root unavailable")
		}
		root, ok := agentNodeByID(state, state.RootAgentID)
		if !ok {
			return agentDispatchBinding{}, errors.New("agent tree root unavailable")
		}
		name := invocation.Profile.Role + "-" + invocation.ID[:12]
		spec := agenttree.NodeSpec{ParentAgentID: root.AgentID, Name: name, Role: invocation.Profile.Role, Authority: authority, InvocationID: invocation.ID, ContextSHA256: contextHash}
		node, err = ensureAgentChild(journalPath, snapshot.RunID, state, root, spec)
	}
	if err != nil {
		return agentDispatchBinding{}, err
	}
	admissionNode := node
	admissionNode.Status = agenttree.StatusQueued
	admissionNode.ResultSHA256 = ""
	admission := AgentDispatchAdmission{Version: 1, RunID: snapshot.RunID, TreeID: snapshot.RunID, Node: admissionNode, Invocation: invocation, AgentTurn: turn}
	admissionID, err := admission.ID()
	if err != nil {
		return agentDispatchBinding{}, err
	}
	if err := appendAgentDispatchAdmission(controllerPath, admission); err != nil {
		return agentDispatchBinding{}, err
	}
	recovering := false
	latest, inspectErr := Inspect(controllerPath)
	if inspectErr != nil {
		return agentDispatchBinding{}, inspectErr
	}
	if prior, ok := latest.AgentDispatch[invocation.ID]; ok && prior.Observation != nil && prior.Observation.Status == agenttree.StatusUnknown {
		recovering = true
	}
	binding := agentDispatchBinding{JournalPath: journalPath, ControllerPath: controllerPath, AdmissionID: admissionID, Node: node, InvocationID: invocation.ID, AgentTurn: turn, Recovering: recovering}
	return binding, nil
}

func markAgentDispatchRunning(binding *agentDispatchBinding) error {
	if binding.Recovering {
		return nil
	}
	if binding.AgentTurn != nil && binding.AgentTurn.TurnSequence > 1 {
		return observeAgentTurn(*binding, agenttree.StatusRunning, "")
	}
	switch binding.Node.Status {
	case agenttree.StatusQueued:
		if err := agenttree.ObserveStatus(binding.JournalPath, binding.Node.AgentID, agenttree.StatusRunning); err != nil {
			return err
		}
		binding.Node.Status = agenttree.StatusRunning
	case agenttree.StatusRunning, agenttree.StatusSucceeded, agenttree.StatusUnknown:
	default:
		return errors.New("agent dispatch requires reconciliation")
	}
	return observeAgentTurn(*binding, agenttree.StatusRunning, "")
}

func ensureOpenCodeDispatchCapacity(snapshot Snapshot, invocation runtime.Invocation) error {
	selected, err := resolveProviderRoutingForSnapshot(snapshot, invocation, 1)
	if err != nil || selected.Profile != invocation.Profile {
		return errors.Join(errors.New("OpenCode provider selection changed"), err)
	}
	_, err = ensureTaskPool(snapshot.Creation.Config, snapshot.RunID, selected.Intent)
	return err
}

func ensureDirectDispatchCapacity(snapshot Snapshot, invocation runtime.Invocation) error {
	selected, err := resolveProviderRoutingForSnapshot(snapshot, invocation, 1)
	if err != nil || selected.Profile != invocation.Profile {
		return errors.Join(errors.New("direct provider selection changed"), err)
	}
	_, err = ensureTaskPool(snapshot.Creation.Config, snapshot.RunID, selected.Intent)
	return err
}

// prepareManagedExplorerRoot registers the already accepted planner result for
// opt-in dynamic explorers. It does not dispatch a planner or grant new scope.
func prepareManagedExplorerRoot(controllerPath string, snapshot Snapshot) error {
	if !managedExplorerDispatchEnabled(snapshot) {
		return nil
	}
	if err := autonomousDispatchBlocked(snapshot); err != nil {
		return err
	}
	path := controllerPath + ".agent-tree"
	tree, err := agenttree.Inspect(path)
	if err != nil {
		return err
	}
	if tree.TreeID == "" {
		_, err = bootstrapAgentTreeRoot(path, snapshot)
		return err
	}
	planner, err := plannerInvocationForSnapshot(snapshot)
	if err != nil {
		return err
	}
	contextHash, err := access.InputID(planner.Input)
	if err != nil {
		return err
	}
	root, ok := agentNodeByID(tree, tree.RootAgentID)
	if tree.TreeID != snapshot.RunID || !ok || !exactNodeFields(root, "", "/root", agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: planner.ID, ContextSHA256: contextHash}) {
		return errors.New("managed explorer planner root differs")
	}
	if snapshot.Plan == nil {
		return errors.New("managed explorer accepted planner result unavailable")
	}
	if err := runtime.ValidateResult(planner, *snapshot.Plan, true); err != nil {
		return err
	}
	resultHash, err := canonical.Hash("harness.planner-result.v1", *snapshot.Plan)
	if err != nil || root.Status != agenttree.StatusSucceeded || root.ResultSHA256 != resultHash {
		return errors.Join(errors.New("managed explorer planner result binding differs"), err)
	}
	return nil
}

func bootstrapAgentTreeRoot(journalPath string, snapshot Snapshot) (agenttree.Snapshot, error) {
	if snapshot.Plan == nil {
		return agenttree.Snapshot{}, errors.New("agent tree planner root unavailable")
	}
	planner, err := plannerInvocationForSnapshot(snapshot)
	if err != nil {
		return agenttree.Snapshot{}, err
	}
	if err := runtime.ValidateResult(planner, *snapshot.Plan, true); err != nil {
		return agenttree.Snapshot{}, err
	}
	contextHash, err := access.InputID(planner.Input)
	if err != nil {
		return agenttree.Snapshot{}, err
	}
	root, err := agenttree.Create(journalPath, snapshot.RunID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: planner.ID, ContextSHA256: contextHash})
	if err != nil {
		return agenttree.Snapshot{}, err
	}
	if err := agenttree.ObserveStatus(journalPath, root.AgentID, agenttree.StatusRunning); err != nil {
		return agenttree.Snapshot{}, err
	}
	resultHash, err := canonical.Hash("harness.planner-result.v1", *snapshot.Plan)
	if err != nil {
		return agenttree.Snapshot{}, err
	}
	if err := agenttree.ObserveResult(journalPath, root.AgentID, resultHash); err != nil {
		return agenttree.Snapshot{}, err
	}
	return agenttree.Inspect(journalPath)
}

func ensureAgentChild(journalPath, treeID string, state agenttree.Snapshot, root agenttree.Node, spec agenttree.NodeSpec) (agenttree.Node, error) {
	wantPath, err := agenttree.ChildPath(root.Path, spec.Name)
	if err != nil {
		return agenttree.Node{}, err
	}
	if node, exactErr := exactAgentNode(state, root.AgentID, wantPath, spec); exactErr == nil {
		return node, nil
	}
	for _, reservation := range state.Reservations {
		if exactNodeFields(reservation.Node, root.AgentID, wantPath, spec) {
			return agenttree.CommitChild(journalPath, reservation.ReservationID)
		}
	}
	reservation, err := agenttree.ReserveChild(journalPath, treeID, spec)
	if err != nil {
		return agenttree.Node{}, err
	}
	return agenttree.CommitChild(journalPath, reservation.ReservationID)
}

func exactAgentNode(state agenttree.Snapshot, parentID, nodePath string, spec agenttree.NodeSpec) (agenttree.Node, error) {
	for _, node := range state.Nodes {
		if node.InvocationID == spec.InvocationID {
			if exactNodeFields(node, parentID, nodePath, spec) {
				return node, nil
			}
			return agenttree.Node{}, errors.New("agent invocation binding differs")
		}
	}
	return agenttree.Node{}, errors.New("agent invocation binding unavailable")
}

func exactNodeFields(node agenttree.Node, parentID, nodePath string, spec agenttree.NodeSpec) bool {
	return node.ParentAgentID == parentID && node.Path == nodePath && node.Name == spec.Name && node.Role == spec.Role && node.Authority == spec.Authority && node.InvocationID == spec.InvocationID && node.ContextSHA256 == spec.ContextSHA256
}

func agentNodeByID(state agenttree.Snapshot, agentID string) (agenttree.Node, bool) {
	for _, node := range state.Nodes {
		if node.AgentID == agentID {
			return node, true
		}
	}
	return agenttree.Node{}, false
}

func agentAuthority(role string) (agenttree.Authority, error) {
	switch role {
	case "planner", "explorer", "reviewer":
		return agenttree.AuthorityReadOnly, nil
	case "writer", "fixer":
		return agenttree.AuthorityScopedWriter, nil
	default:
		return "", errors.New("unknown agent authority")
	}
}

func finishAgentDispatch(binding agentDispatchBinding, resultHash string) error {
	if binding.AgentTurn != nil && binding.AgentTurn.TurnSequence > 1 {
		if err := appendAgentDispatchObservation(binding, agenttree.StatusSucceeded, resultHash); err != nil {
			return err
		}
		return observeAgentTurn(binding, agenttree.StatusSucceeded, resultHash)
	}
	state, err := agenttree.Inspect(binding.JournalPath)
	if err != nil {
		return err
	}
	node, ok := agentNodeByID(state, binding.Node.AgentID)
	if !ok {
		return errors.New("agent dispatch binding unavailable")
	}
	if node.Status == agenttree.StatusSucceeded {
		if node.ResultSHA256 != resultHash {
			return errors.New("agent result observation differs")
		}
		if err := appendAgentDispatchObservation(binding, agenttree.StatusSucceeded, resultHash); err != nil {
			return err
		}
		return observeAgentTurn(binding, agenttree.StatusSucceeded, resultHash)
	}
	if err := agenttree.ObserveResult(binding.JournalPath, binding.Node.AgentID, resultHash); err != nil {
		return err
	}
	if err := appendAgentDispatchObservation(binding, agenttree.StatusSucceeded, resultHash); err != nil {
		return err
	}
	return observeAgentTurn(binding, agenttree.StatusSucceeded, resultHash)
}

func finishAgentDispatchUnknown(binding agentDispatchBinding) error {
	if binding.AgentTurn != nil && binding.AgentTurn.TurnSequence > 1 {
		if err := appendAgentDispatchObservation(binding, agenttree.StatusUnknown, ""); err != nil {
			return err
		}
		return observeAgentTurn(binding, agenttree.StatusUnknown, "")
	}
	state, err := agenttree.Inspect(binding.JournalPath)
	if err != nil {
		return err
	}
	node, ok := agentNodeByID(state, binding.Node.AgentID)
	if !ok {
		return errors.New("agent dispatch binding unavailable")
	}
	if node.Status == agenttree.StatusUnknown {
		if err := appendAgentDispatchObservation(binding, agenttree.StatusUnknown, ""); err != nil {
			return err
		}
		return observeAgentTurn(binding, agenttree.StatusUnknown, "")
	}
	if node.Status != agenttree.StatusRunning {
		return errors.New("agent failure observation differs")
	}
	if err := agenttree.ObserveStatus(binding.JournalPath, binding.Node.AgentID, agenttree.StatusUnknown); err != nil {
		return err
	}
	if err := appendAgentDispatchObservation(binding, agenttree.StatusUnknown, ""); err != nil {
		return err
	}
	return observeAgentTurn(binding, agenttree.StatusUnknown, "")
}

func observeAgentTurn(binding agentDispatchBinding, status agenttree.Status, resultHash string) error {
	if binding.AgentTurn == nil {
		return nil
	}
	service, err := agentcontrol.Bind(binding.JournalPath, binding.ControllerPath+".agent-control")
	if err != nil {
		return err
	}
	return service.ObserveTurn(context.Background(), *binding.AgentTurn, status, resultHash)
}

func appendAgentDispatchAdmission(controllerPath string, admission AgentDispatchAdmission) error {
	latest, err := Inspect(controllerPath)
	if err != nil {
		return err
	}
	if prior, ok := latest.AgentDispatch[admission.Invocation.ID]; ok {
		if prior.Admission == admission {
			return nil
		}
		return errors.New("agent dispatch admission differs")
	}
	if err := Append(controllerPath, "agent.dispatch-admitted", admission); err != nil {
		latest, inspectErr := Inspect(controllerPath)
		if inspectErr == nil {
			if prior, ok := latest.AgentDispatch[admission.Invocation.ID]; ok && prior.Admission == admission {
				return nil
			}
		}
		return errors.Join(err, inspectErr)
	}
	return nil
}

func appendAgentDispatchObservation(binding agentDispatchBinding, status agenttree.Status, resultHash string) error {
	observation := AgentDispatchObservation{Version: 1, AdmissionID: binding.AdmissionID, InvocationID: binding.InvocationID, Status: status, ResultSHA256: resultHash}
	latest, err := Inspect(binding.ControllerPath)
	if err != nil {
		return err
	}
	if state, ok := latest.AgentDispatch[binding.InvocationID]; ok && state.Observation != nil {
		if *state.Observation == observation {
			return nil
		}
		if state.Observation.Status == agenttree.StatusUnknown && observation.Status == agenttree.StatusSucceeded {
			return Append(binding.ControllerPath, "agent.dispatch-observed", observation)
		}
		return errors.New("agent dispatch observation differs")
	}
	return Append(binding.ControllerPath, "agent.dispatch-observed", observation)
}

func replayAgentDispatch(snapshot *Snapshot, event journal.Event) error {
	if snapshot.RunID == "" {
		return errors.New("agent dispatch requires a run")
	}
	if snapshot.AgentDispatch == nil {
		snapshot.AgentDispatch = map[string]AgentDispatchState{}
	}
	switch event.Kind {
	case "agent.dispatch-admitted":
		var admission AgentDispatchAdmission
		if err := canonical.Decode(event.Payload, &admission); err != nil {
			return err
		}
		err := admission.Invocation.Validate()
		contextHash, contextErr := access.InputID(admission.Invocation.Input)
		authority, authorityErr := agentAuthority(admission.Invocation.Profile.Role)
		_, idErr := admission.ID()
		nodeErr := agenttree.ValidateQueuedNode(admission.TreeID, admission.Node)
		turnErr := validateAdmissionAgentTurn(admission, contextHash)
		if err != nil || contextErr != nil || authorityErr != nil || idErr != nil || nodeErr != nil || turnErr != nil || admission.Version != 1 || admission.RunID != snapshot.RunID || admission.TreeID != snapshot.RunID || admission.Node.Role != admission.Invocation.Profile.Role || admission.Node.Authority != authority || safepath.RequireDigest(admission.Node.AgentID) != nil || admission.Node.Status != agenttree.StatusQueued || admission.Node.ResultSHA256 != "" {
			return errors.Join(errors.New("invalid agent dispatch admission"), err, contextErr, authorityErr, idErr, nodeErr, turnErr)
		}
		if admission.Invocation.Profile.Role == "planner" {
			if admission.Node.Path != "/root" || admission.Node.ParentAgentID != "" || admission.Node.Name != "root" {
				return errors.New("invalid planner agent binding")
			}
		} else if admission.Node.ParentAgentID == "" || admission.Node.Path == "/root" {
			return errors.New("invalid tool-role agent binding")
		}
		if _, duplicate := snapshot.AgentDispatch[admission.Invocation.ID]; duplicate {
			return errors.New("duplicate agent dispatch admission")
		}
		snapshot.AgentDispatch[admission.Invocation.ID] = AgentDispatchState{Admission: admission}
	case "agent.dispatch-observed":
		var observation AgentDispatchObservation
		if err := canonical.Decode(event.Payload, &observation); err != nil {
			return err
		}
		state, ok := snapshot.AgentDispatch[observation.InvocationID]
		admissionID, err := state.Admission.ID()
		validResult := observation.Status == agenttree.StatusSucceeded && safepath.RequireDigest(observation.ResultSHA256) == nil || observation.Status == agenttree.StatusUnknown && observation.ResultSHA256 == ""
		canObserve := state.Observation == nil || state.Observation.Status == agenttree.StatusUnknown && observation.Status == agenttree.StatusSucceeded
		if !ok || err != nil || observation.Version != 1 || observation.AdmissionID != admissionID || !canObserve || !validResult {
			return errors.Join(errors.New("invalid agent dispatch observation"), err)
		}
		copy := observation
		state.Observation = &copy
		snapshot.AgentDispatch[observation.InvocationID] = state
	default:
		return errors.New("unknown agent dispatch event")
	}
	return nil
}

func validateAdmissionAgentTurn(admission AgentDispatchAdmission, contextHash string) error {
	if admission.AgentTurn == nil {
		if admission.Node.InvocationID != admission.Invocation.ID || admission.Node.ContextSHA256 != contextHash {
			return errors.New("agent dispatch invocation binding differs")
		}
		return nil
	}
	turn := admission.AgentTurn
	if safepath.RequireDigest(turn.ParentAgentID) != nil || safepath.RequireDigest(turn.AgentID) != nil || safepath.RequireDigest(turn.TurnID) != nil || turn.TurnSequence < 1 || turn.AgentID != admission.Node.AgentID || turn.ParentAgentID != admission.Node.ParentAgentID {
		return errors.New("invalid agent dispatch turn binding")
	}
	if turn.TurnSequence == 1 && (admission.Node.InvocationID != admission.Invocation.ID || admission.Node.ContextSHA256 != contextHash) {
		return errors.New("initial agent turn invocation differs")
	}
	return nil
}
