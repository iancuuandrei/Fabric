package control

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

// ErrPlannerCapacityRefused marks a terminal, sealed initial planner turn that
// was refused by provider capacity. It authorizes no automatic retry.
var ErrPlannerCapacityRefused = errors.New("planner provider capacity refused the request")

type plannerCapacityReceiptBinding struct {
	Code                string `json:"code"`
	FailureResponseHash string `json:"failure_response_hash"`
	InvocationID        string `json:"invocation_id"`
	ThreadID            string `json:"thread_id"`
	TurnID              string `json:"turn_id"`
	JournalHead         string `json:"journal_head"`
}

func plannerCapacityReceiptHash(receipt PlannerReceipt) (string, error) {
	if receipt.FailureCode != "serverOverloaded" || receipt.FailureResponseHash == "" {
		return "", errors.New("invalid planner capacity receipt")
	}
	return canonical.Hash("harness.planner-capacity-failure.v1", plannerCapacityReceiptBinding{
		Code: receipt.FailureCode, FailureResponseHash: receipt.FailureResponseHash,
		InvocationID: receipt.InvocationID, ThreadID: receipt.ThreadID, TurnID: receipt.TurnID,
		JournalHead: receipt.JournalHead,
	})
}

func plannerCapacityReceiptAdmitted(s Snapshot) bool {
	if s.Creation.Config.Version != 1 || s.Creation.Config.Planner.Runtime != "codex-app-server" || s.PlannerHostReceipt == nil || s.Plan != nil || len(s.PlannerCorrections) != 0 || s.PlannerReceipt == nil || s.PlannerReceipt.FailureCode != "serverOverloaded" || s.PlannerReceipt.UsagePending {
		return false
	}
	want, err := plannerCapacityReceiptHash(*s.PlannerReceipt)
	return err == nil && want == s.PlannerReceipt.ResultHash
}

func plannerCapacityFailureEligible(s Snapshot, launch codexhost.Launch, invocation runtime.Invocation, state codexruntime.State) bool {
	if s.Creation.Config.Version != 1 || s.Creation.Config.Planner.Runtime != "codex-app-server" ||
		s.PlannerHostReceipt == nil || len(s.PlannerCorrections) != 0 || s.Plan != nil ||
		state.Intent == nil || state.Intent.Invocation != invocation || state.Intent.Directory != filepath.Join(launch.Root, "workspace") ||
		state.Source == nil || *state.Source != s.Creation.Repository || state.Thread == nil || state.TurnID == "" ||
		state.TurnStatus != "failed" || !state.NotificationStreamComplete || state.PendingTool != nil ||
		state.Result != nil || state.SemanticResult != nil || state.RouteFailure != "" || state.ExecutionOutcome != "failed" ||
		state.Compaction != nil && state.Compaction.Coverage == "UNKNOWN" {
		return false
	}
	if err := state.Thread.ValidateInvocation(invocation, state.Intent.Directory); err != nil {
		return false
	}
	response := state.TerminalResponse
	if response == nil || response.Method != "turn/completed" || response.ThreadID != state.Thread.ThreadID || response.TurnID != state.TurnID || response.TurnStatus != "failed" {
		return false
	}
	var turnError struct {
		Code string `json:"codexErrorInfo"`
	}
	return json.Unmarshal(response.TurnError, &turnError) == nil && turnError.Code == "serverOverloaded"
}

func makePlannerCapacityFailureReceipt(state codexruntime.State, head string) (PlannerReceipt, error) {
	if state.TerminalResponse == nil || head == "" || state.Thread == nil {
		return PlannerReceipt{}, errors.New("planner capacity evidence is incomplete")
	}
	responseHash, err := canonical.Hash("harness.planner-capacity-response.v1", *state.TerminalResponse)
	if err != nil {
		return PlannerReceipt{}, err
	}
	receipt := PlannerReceipt{
		FailureCode: "serverOverloaded", FailureResponseHash: responseHash,
		InvocationID: state.Intent.Invocation.ID, ThreadID: state.Thread.ThreadID, TurnID: state.TurnID,
		JournalHead: head,
	}
	receipt.ResultHash, err = plannerCapacityReceiptHash(receipt)
	return receipt, err
}

// observePlannerCapacityFailure seals only an exact completed Codex capacity
// refusal. Every other terminal/runtime condition remains on the normal path.
func observePlannerCapacityFailure(path string, s Snapshot, launch codexhost.Launch, invocation runtime.Invocation, runtimePath string) (Snapshot, bool, error) {
	var err error
	s, err = Inspect(path)
	if err != nil {
		return s, false, err
	}
	if s.PlannerReceipt != nil {
		return s, false, nil
	}
	state, head, err := codexruntime.InspectWithHead(runtimePath)
	if err != nil {
		return s, false, err
	}
	if !plannerCapacityFailureEligible(s, launch, invocation, state) {
		return s, false, nil
	}
	receipt, err := makePlannerCapacityFailureReceipt(state, head)
	if err != nil {
		return s, false, err
	}
	if err := Append(path, "planning.runtime-observed", receipt); err != nil {
		return s, false, err
	}
	observed, err := Inspect(path)
	if err != nil {
		return s, false, err
	}
	return observed, true, nil
}

func validatePlannerCapacityFailureRuntime(s Snapshot, path string) error {
	fresh, err := Inspect(path)
	if err != nil {
		return err
	}
	if s.PlannerReceipt == nil || fresh.PlannerReceipt == nil || !sameCanonical(*fresh.PlannerReceipt, *s.PlannerReceipt) {
		return errors.New("planner capacity controller receipt changed")
	}
	s = fresh
	receipt := s.PlannerReceipt
	if receipt == nil || receipt.FailureCode != "serverOverloaded" {
		return errors.New("planner capacity receipt is missing")
	}
	wantHash, err := plannerCapacityReceiptHash(*receipt)
	if err != nil || wantHash != receipt.ResultHash {
		return errors.New("planner capacity receipt binding is invalid")
	}
	launch, err := expectedPlannerHost(s)
	if err != nil {
		return err
	}
	invocation, err := plannerInvocationForSnapshot(s)
	if err != nil {
		return err
	}
	runtimePath := filepath.Join(launch.Root, "planner.jsonl")
	state, head, err := codexruntime.InspectWithHead(runtimePath)
	if err != nil {
		return err
	}
	if !plannerCapacityFailureEligible(s, launch, invocation, state) || receipt.InvocationID != invocation.ID || receipt.ThreadID != state.Thread.ThreadID || receipt.TurnID != state.TurnID || receipt.JournalHead != head {
		return errors.New("planner capacity runtime no longer matches receipt")
	}
	responseHash, err := canonical.Hash("harness.planner-capacity-response.v1", *state.TerminalResponse)
	if err != nil || responseHash != receipt.FailureResponseHash {
		return errors.New("planner capacity response no longer matches receipt")
	}
	return nil
}
