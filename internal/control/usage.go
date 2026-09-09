package control

import (
	"errors"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
)

// RuntimeUsageEntry describes a journaled host invocation, including incomplete
// attempts. ReceiptMatched distinguishes admitted evidence from raw runtime state.
type RuntimeUsageEntry struct {
	InvocationID   string                     `json:"invocation_id"`
	Role           string                     `json:"role"`
	JournalPresent bool                       `json:"journal_present"`
	ReceiptMatched bool                       `json:"receipt_matched"`
	Usage          *codexruntime.ContextUsage `json:"usage"`
}

// RunUsage reports every journaled Codex host in controller order, not only the
// latest writer/reviewer. Fake runtime work has no provider accounting entry.
type RunUsage struct {
	Admissions     []AdmissionUsage    `json:"admissions,omitempty"`
	RunID          string              `json:"run_id"`
	ControllerHead string              `json:"controller_head"`
	Scope          string              `json:"scope"`
	Invocations    []RuntimeUsageEntry `json:"invocations"`
}

// AdmissionUsage reports controller-authorized resource reservations separately
// from measured provider usage. A missing receipt means unresolved, never free.
type AdmissionUsage struct {
	Class         access.Class    `json:"class"`
	Intent        access.Intent   `json:"intent"`
	Receipt       *access.Receipt `json:"receipt"`
	EvidenceScope string          `json:"evidence_scope"`
}

type usageTarget struct {
	invocation runtime.Invocation
	path, head string
	resultHash string
}

func admittedRuntimeTerminal(s Snapshot, invocation runtime.Invocation) (string, string) {
	index := modelAccessIndex(s, invocation.ID)
	if index >= 0 && s.ModelAccess[index].Terminal != nil && s.ModelAccess[index].Terminal.Receipt.Status == "completed" {
		return s.ModelAccess[index].Terminal.RuntimeJournalHead, s.ModelAccess[index].Terminal.Receipt.OutputHash
	}
	return "", ""
}

func admittedRuntimeHead(s Snapshot, invocation runtime.Invocation) string {
	head, _ := admittedRuntimeTerminal(s, invocation)
	return head
}

func runtimeResultDomain(role string) (string, error) {
	switch role {
	case "planner":
		return "harness.planner-result.v1", nil
	case "explorer":
		return "harness.explorer-result.v1", nil
	case "writer", "fixer":
		return "harness.writer-result.v1", nil
	case "reviewer":
		return "harness.review-result.v1", nil
	default:
		return "", errors.New("unknown runtime result role")
	}
}

func measureAdmissions(path string, s Snapshot) ([]AdmissionUsage, error) {
	accessPath := modelAccessJournal(path)
	if _, err := os.Stat(accessPath); os.IsNotExist(err) {
		if len(s.ModelAccess) > 0 {
			return nil, errors.New("controller model access lacks dedicated journal")
		}
		if s.PlannerAccess == nil {
			return nil, nil
		}
		entry := AdmissionUsage{Class: s.Creation.Config.Access.Class, Intent: *s.PlannerAccess, EvidenceScope: "deterministic_fake_planner"}
		if s.Plan != nil {
			receipt, err := planningAccessReceipt(s, *s.Plan)
			if err != nil {
				return nil, err
			}
			entry.Receipt = &receipt
		}
		return []AdmissionUsage{entry}, nil
	} else if err != nil {
		return nil, err
	}
	policy, err := s.Creation.Config.AccessPolicy(s.RunID)
	if err != nil {
		return nil, err
	}
	controllerIntents := map[string]access.Intent{}
	controllerTerminals := map[string]access.Receipt{}
	controllerScopes := map[string]string{}
	if s.PlannerAccess != nil {
		controllerIntents[s.PlannerAccess.Reservation.InvocationID] = *s.PlannerAccess
		controllerScopes[s.PlannerAccess.Reservation.InvocationID] = "deterministic_fake_planner"
		if s.Plan == nil {
			return nil, errors.New("dedicated access follows unresolved legacy planner admission")
		}
		receipt, err := planningAccessReceipt(s, *s.Plan)
		if err != nil {
			return nil, err
		}
		controllerTerminals[receipt.InvocationID] = receipt
	}
	for _, admitted := range s.ModelAccess {
		id := admitted.Intent.Reservation.InvocationID
		controllerIntents[id] = admitted.Intent
		controllerScopes[id] = "controller_admission_reservation"
		if admitted.Terminal != nil {
			controllerTerminals[id] = admitted.Terminal.Receipt
			controllerScopes[id] = "controller_admission_and_runtime_terminal"
		}
	}
	accessEvents, err := journal.Read(accessPath)
	if err != nil {
		return nil, err
	}
	intents := []access.Intent{}
	receipts := map[string]access.Receipt{}
	for _, event := range accessEvents {
		switch event.Kind {
		case "access.intent":
			var intent access.Intent
			if err := canonical.Decode(event.Payload, &intent); err != nil {
				return nil, err
			}
			intents = append(intents, intent)
		case "access.receipt":
			var receipt access.Receipt
			if err := canonical.Decode(event.Payload, &receipt); err != nil {
				return nil, err
			}
			if _, duplicate := receipts[receipt.InvocationID]; duplicate {
				return nil, errors.New("duplicate dedicated access receipt")
			}
			receipts[receipt.InvocationID] = receipt
		default:
			return nil, errors.New("unknown dedicated access event")
		}
	}
	result := make([]AdmissionUsage, 0, len(intents))
	seen := map[string]bool{}
	for _, intent := range intents {
		id := intent.Reservation.InvocationID
		if seen[id] {
			return nil, errors.New("duplicate dedicated access intent")
		}
		seen[id] = true
		if expected, ok := controllerIntents[id]; ok && !sameCanonical(expected, intent) {
			return nil, errors.New("controller and dedicated access intents differ")
		}
		entry := AdmissionUsage{Class: policy.Class, Intent: intent, EvidenceScope: controllerScopes[id]}
		if entry.EvidenceScope == "" {
			entry.EvidenceScope = "durable_access_journal_only"
		}
		if receipt, terminal := receipts[id]; terminal {
			if err := access.RequireTerminal(accessPath, policy, intent, receipt); err != nil {
				return nil, err
			}
			if expected, ok := controllerTerminals[id]; ok && !sameCanonical(expected, receipt) {
				return nil, errors.New("controller and dedicated access receipts differ")
			}
			copy := receipt
			entry.Receipt = &copy
			if _, mirrored := controllerTerminals[id]; !mirrored {
				entry.EvidenceScope = "durable_terminal_without_controller_mirror"
			}
		} else {
			if _, expectsTerminal := controllerTerminals[id]; expectsTerminal {
				return nil, errors.New("controller terminal lacks dedicated access receipt")
			}
			if err := access.RequireActive(accessPath, policy, intent); err != nil {
				return nil, err
			}
		}
		result = append(result, entry)
	}
	for id := range controllerIntents {
		if !seen[id] {
			return nil, errors.New("controller admission missing from dedicated access journal")
		}
	}
	for id := range receipts {
		if !seen[id] {
			return nil, errors.New("dedicated receipt without access intent")
		}
	}
	return result, nil
}

// MeasureRunUsage replays controller evidence and verifies each runtime receipt
// head before returning accounting. Missing or changed admitted journals fail.
func MeasureRunUsage(path string) (RunUsage, error) {
	events, err := journal.Read(path)
	if err != nil {
		return RunUsage{}, err
	}
	s, err := Replay(events)
	if err != nil {
		return RunUsage{}, err
	}
	if err := validateControllerJournalPath(path, s.Creation, s.RunID); err != nil {
		return RunUsage{}, err
	}
	if len(events) == 0 {
		return RunUsage{}, errors.New("run journal required")
	}
	report := RunUsage{RunID: s.RunID, ControllerHead: events[len(events)-1].Hash, Scope: "journaled_codex_hosts", Invocations: []RuntimeUsageEntry{}}
	report.Admissions, err = measureAdmissions(path, s)
	if err != nil {
		return RunUsage{}, err
	}
	if len(report.Admissions) > 0 {
		report.Scope = "controller_admissions_and_journaled_codex_hosts"
	}
	targets := []usageTarget{}
	indices := map[string]int{}
	add := func(i runtime.Invocation, path string) {
		indices[i.ID] = len(targets)
		head, resultHash := admittedRuntimeTerminal(s, i)
		target := usageTarget{invocation: i, path: path, head: head, resultHash: resultHash}
		targets = append(targets, target)
	}
	for _, e := range events {
		switch e.Kind {
		case "explorer.host-intent":
			var intent ExplorerHostIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return RunUsage{}, err
			}
			add(intent.Invocation, filepath.Join(intent.Launch.Root, "explorer.jsonl"))
		case "planning.host-intent":
			var l codexhost.Launch
			if err := canonical.Decode(e.Payload, &l); err != nil {
				return RunUsage{}, err
			}
			i, err := plannerInvocation(s.Creation.Config, s.Creation.Objective)
			if err != nil {
				return RunUsage{}, err
			}
			add(i, filepath.Join(l.Root, "planner.jsonl"))
		case "writer.host-intent":
			var intent WriterHostIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return RunUsage{}, err
			}
			add(intent.Invocation, filepath.Join(intent.Launch.Root, "writer.jsonl"))
		case "review.host-intent":
			var intent ReviewHostIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return RunUsage{}, err
			}
			add(intent.Invocation, filepath.Join(intent.Launch.Root, "review.jsonl"))
		case "planning.runtime-observed", "writer.runtime-observed", "review.runtime-observed", "explorer.runtime-observed":
			// These receipt schemas deliberately have the same correlation fields.
			var receipt PlannerReceipt
			if err := canonical.Decode(e.Payload, &receipt); err != nil {
				return RunUsage{}, err
			}
			index, ok := indices[receipt.InvocationID]
			if !ok {
				return RunUsage{}, errors.New("runtime receipt without host intent")
			}
			if targets[index].head != "" && (targets[index].head != receipt.JournalHead || targets[index].resultHash != receipt.ResultHash) {
				return RunUsage{}, errors.New("role runtime receipt differs from terminal access receipt")
			}
			targets[index].head = receipt.JournalHead
			targets[index].resultHash = receipt.ResultHash
		}
	}
	for _, target := range targets {
		entry := RuntimeUsageEntry{InvocationID: target.invocation.ID, Role: target.invocation.Profile.Role}
		if _, err := os.Stat(target.path); os.IsNotExist(err) && target.head == "" {
			report.Invocations = append(report.Invocations, entry)
			continue
		} else if err != nil {
			return RunUsage{}, err
		}
		if target.head != "" {
			state, observedHead, err := codexruntime.InspectWithHead(target.path)
			if err != nil {
				return RunUsage{}, err
			}
			if observedHead != target.head || state.Result == nil {
				return RunUsage{}, errors.New("runtime terminal head no longer matches journal")
			}
			domain, err := runtimeResultDomain(target.invocation.Profile.Role)
			if err != nil {
				return RunUsage{}, err
			}
			resultHash, err := canonical.Hash(domain, *state.Result)
			if err != nil || resultHash != target.resultHash {
				return RunUsage{}, errors.New("runtime terminal output no longer matches receipt")
			}
		}
		usage, err := codexruntime.MeasureContext(target.path)
		if os.IsNotExist(err) && target.head == "" {
			report.Invocations = append(report.Invocations, entry)
			continue
		}
		if err != nil {
			return RunUsage{}, err
		}
		if usage.InvocationID != target.invocation.ID || usage.Requested != target.invocation.Profile {
			return RunUsage{}, errors.New("runtime usage invocation substitution")
		}
		if target.head != "" && (target.head != usage.JournalHead || !usage.Completed) {
			return RunUsage{}, errors.New("runtime usage receipt no longer matches journal")
		}
		entry.JournalPresent = true
		entry.ReceiptMatched = target.head != ""
		entry.Usage = &usage
		report.Invocations = append(report.Invocations, entry)
	}
	return report, nil
}
