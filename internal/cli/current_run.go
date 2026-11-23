package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/controllerstate"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

// runEligibility reports whether a fully validated journal snapshot may be
// selected as the latest run. Validation (replay, filename and repository
// binding) always runs before eligibility so ineligible journals never mask
// a corrupt or foreign journal.
type runEligibility func(s control.Snapshot) bool

// latestRunCandidate is one validated journal eligible for latest selection.
type latestRunCandidate struct {
	id string
	at int64
}

// selectLatestValidatedRun validates every journal bound to this checkout and
// then selects the latest eligible entry by filesystem modification time.
// Tied durable timestamps are rejected rather than silently picking an
// arbitrary run. noneErr names the empty-eligible case; tieErr names the
// timestamp-tie case so interactive and autonomous callers keep distinct
// diagnostics while sharing validation and ordering.
func selectLatestValidatedRun(root string, eligible runEligibility, noneErr, tieErr string) (string, error) {
	cfg, err := configuration(root)
	if err != nil {
		return "", err
	}
	identity, err := repository.Discover(context.Background(), root, cfg.Repository)
	if err != nil {
		return "", err
	}
	paths, err := controllerstate.Resolve(cfg.ControllerStateRoot, identity)
	if err != nil {
		return "", err
	}
	if paths.External {
		if err := controllerstate.Validate(paths, identity); err != nil {
			return "", err
		}
	}
	entries, err := filepath.Glob(filepath.Join(paths.Runs, "*.jsonl"))
	if err != nil {
		return "", err
	}
	candidates := make([]latestRunCandidate, 0, len(entries))
	for _, path := range entries {
		if runJournalSidecar(path) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		s, err := control.Inspect(path)
		if err != nil {
			return "", err
		}
		if filepath.Base(path) != s.RunID+".jsonl" || filepath.Clean(s.Creation.Repository.Root) != filepath.Clean(root) {
			return "", errors.New("run journal repository binding mismatch")
		}
		if eligible != nil && !eligible(s) {
			continue
		}
		candidates = append(candidates, latestRunCandidate{id: s.RunID, at: info.ModTime().UnixNano()})
	}
	if len(candidates) == 0 {
		return "", errors.New(noneErr)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].at == candidates[j].at {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].at > candidates[j].at
	})
	if len(candidates) > 1 && candidates[0].at == candidates[1].at {
		return "", errors.New(tieErr)
	}
	return candidates[0].id, nil
}

// Scheduler, model-access and provider runtime journals share the runs
// directory but have a separate event contract. Ignore only their canonical
// names; other entries still fail replay or repository binding validation
// rather than silently hiding corrupt runs.
func runJournalSidecar(path string) bool {
	runID, sidecar, found := strings.Cut(filepath.Base(path), ".jsonl.")
	if !found || safepath.RequireDigest(runID) != nil {
		return false
	}
	if sidecar == "model-access.jsonl" {
		return true
	}
	if isCanonicalGraphSchedulerSidecar(sidecar) {
		return true
	}
	return isCanonicalProviderSidecar(sidecar)
}

// isCanonicalGraphSchedulerSidecar reports whether sidecar is a scheduler
// journal produced by the current controller dispatch paths. Only the exact
// controller-produced prefixes with an exact 16 lowercase hex cohort suffix
// and ".jsonl" are ignored; malformed or unknown names return false so
// listing stays fail-closed. Longer prefixes sort first so the
// "isolated-graph-writers-" prefix never shadows its "-wave-" extension.
func isCanonicalGraphSchedulerSidecar(sidecar string) bool {
	for _, prefix := range []string{
		"staged-graph-writers-wave-",
		"isolated-graph-writers-wave-",
		"isolated-graph-writers-",
		"graph-schedule-",
		"graph-writers-",
	} {
		rest, ok := strings.CutPrefix(sidecar, prefix)
		if !ok {
			continue
		}
		if len(rest) != len("0123456789abcdef.jsonl") || !strings.HasSuffix(rest, ".jsonl") {
			return false
		}
		cohortID := strings.TrimSuffix(rest, ".jsonl")
		return len(cohortID) == 16 && strings.Trim(cohortID, "0123456789abcdef") == ""
	}
	return false
}

// isCanonicalProviderSidecar reports whether sidecar (the filename portion
// after "<run>.jsonl.") is a well-formed provider/runtime journal produced by
// the current controller dispatch paths. Only exact role/runtime/provider
// grammars with valid lower-case identities are ignored; malformed or unknown
// names return false so listing stays fail-closed.
func isCanonicalProviderSidecar(sidecar string) bool {
	const stateSuffix = ".opencode-runtime.jsonl.state-root.jsonl"
	if strings.HasSuffix(sidecar, stateSuffix) {
		stem := strings.TrimSuffix(sidecar, stateSuffix)
		if stem == "" || strings.Contains(stem, ".jsonl") {
			return false
		}
		return isCanonicalProviderStem(stem, ".opencode-runtime.jsonl")
	}
	for _, suffix := range []string{
		".opencode-runtime.jsonl",
		".provider-gateway.jsonl",
		".provider-runtime.jsonl",
		".claude-runtime.jsonl",
		".acp-runtime.jsonl",
		".directapi-runtime.jsonl",
	} {
		if !strings.HasSuffix(sidecar, suffix) {
			continue
		}
		stem := strings.TrimSuffix(sidecar, suffix)
		if stem == "" || strings.Contains(stem, ".jsonl") {
			return false
		}
		return isCanonicalProviderStem(stem, suffix)
	}
	return false
}

// isCanonicalProviderStem validates a journal stem for one runtime suffix.
// Bare "<role>" is accepted for every role and suffix. Turn-scoped
// "<role>.turn-<id>" is accepted only for explorer: non-nil turns only reach
// journal stems through scheduled dynamic explorer claims (OperationExplorer
// with a bound AgentTurn; planner never takes a turn and follow-up turns are
// explorer-gated), so other roles' turn names stay fail-closed. Repeated
// invocation "<role>.invocation-<id>" is accepted only for opencode-runtime
// and provider-gateway: only providerInvocationJournalStem generates it, and
// its callers bind both the runtime and gateway paths to that stem, while
// direct-provider producers (providerRoleJournalStem callers) emit bare or
// turn-scoped stems only. Each trailing identity must be a valid lower-case
// digest. A combined turn plus invocation suffix is not produced and is
// rejected to preserve fail-closed listing.
func isCanonicalProviderStem(stem, suffix string) bool {
	if validProviderSidecarRole(stem) {
		return true
	}
	role, rest, ok := strings.Cut(stem, ".")
	if !ok || !validProviderSidecarRole(role) || strings.Contains(rest, ".jsonl") {
		return false
	}
	if after, ok := strings.CutPrefix(rest, "turn-"); ok {
		return role == "explorer" && !strings.Contains(after, ".") && safepath.RequireDigest(after) == nil
	}
	if after, ok := strings.CutPrefix(rest, "invocation-"); ok {
		return suffix != ".provider-runtime.jsonl" && !strings.Contains(after, ".") && safepath.RequireDigest(after) == nil
	}
	return false
}

func validProviderSidecarRole(role string) bool {
	switch role {
	case "planner", "explorer", "writer", "fixer", "reviewer":
		return true
	default:
		return false
	}
}

// latestRunID selects only a journal which fully replays and is bound to this
// checkout. Tied durable timestamps are rejected rather than silently picking
// an arbitrary run.
func latestRunID(root string) (string, error) {
	return selectLatestValidatedRun(root, nil, "no durable runs found for this repository", "multiple durable runs have the same latest timestamp; specify RUN explicitly")
}

// latestAutonomousRunID selects the latest validated autonomous run bound to
// this checkout by explicit filesystem modification timestamp. Interactive
// runs never qualify; no new persistence machinery is introduced.
func latestAutonomousRunID(root string) (string, error) {
	return selectLatestValidatedRun(root, func(s control.Snapshot) bool {
		return s.Creation.Execution != nil && s.Creation.Execution.Mode == "autonomous-v1"
	}, "no durable autonomous runs found for this repository", "multiple durable autonomous runs have the same latest timestamp; specify RUN explicitly")
}

func inspectRunArgument(root string, args []string) (id string, export bool, err error) {
	switch len(args) {
	case 0:
		id, err = latestRunID(root)
		return id, false, err
	case 1:
		if args[0] == "--export-jsonl" {
			id, err = latestRunID(root)
			return id, true, err
		}
		return args[0], false, nil
	case 2:
		if args[1] != "--export-jsonl" {
			return "", false, errors.New("inspect accepts [RUN] [--export-jsonl]")
		}
		return args[0], true, nil
	default:
		return "", false, errors.New("inspect accepts [RUN] [--export-jsonl]")
	}
}
