package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iancuuandrei/fabric/internal/buildinfo"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/controllerstate"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

// Command describes an implemented CLI command; Reference uses this same table.
type Command struct {
	Name      string
	Arguments string
	Summary   string
}

var commands = []Command{
	{"evidence-feedback", "RUN REQUEST_JSON [--schedule SCHEDULE_ID]", "Reprice advisory evidence resources from receipt-matched typed tokens without mutating run state."},
	{"evidence-acquire", "RUN REQUEST_JSON", "Record a finite JEV decision and acquire only selected bounded explorer source context; never dispatch a model."},
	{"calibrate-models", "CALIBRATION_JSON", "Compare bounded matched task-policy outcomes; report conservative model selection without dispatch."},
	{"version", "", "Report the build version, commit and build date."},
	{"agent-interrupt", "RUN SCHEDULE_ID TURN_ID ACTOR NONCE", "Request interruption of one exact scheduled turn; delivery does not prove runtime teardown or resolve UNKNOWN effects."},
	{"agent-spawn", "RUN SCHEDULE_ID REQUEST_JSON", "Queue a read-only explorer child using controller-derived invocation and authority."},
	{"agent-followup", "RUN SCHEDULE_ID MESSAGE_JSON", "Queue an exact explorer follow-up through the existing per-agent FIFO scheduler."},
	{"agent-list", "RUN [AFTER_PATH LIMIT]", "List bounded agent topology and record current lifecycle observations."},
	{"agent-send", "RUN MESSAGE_JSON", "Deliver a bounded message to exact registered agents without waking a runtime."},
	{"agent-messages", "RUN RECIPIENT AFTER_SEQUENCE LIMIT", "Read a bounded page of messages explicitly addressed to one agent."},
	{"agent-wait", "RUN AGENT AFTER_SEQUENCE LIMIT TIMEOUT_MS", "Wait for bounded message or lifecycle activity without launching a runtime."},
	{"schedule-task", "RUN TASK_ID OPERATION [INPUT]", "Derive one exact planner/explorer/writer/reviewer task from current controller state without dispatching."},
	{"schedule-tick", "SCHEDULE_ID [WORKERS]", "Attempt one scheduling decision per worker (default 1, maximum 64) through existing controller and capacity gates."},
	{"schedule-run", "SCHEDULE_ID [WORKERS]", "Keep bounded scheduler workers available for new tasks until interrupted; queue emptiness is not completion."},
	{"schedule-recover", "SCHEDULE_ID CLAIM_ID", "Reconcile an existing exact scheduler claim without creating a replacement invocation."},
	{"schedule-create", "DEFINITION_JSON", "Bind an exact task DAG to existing runs in the selected repository without dispatching."},
	{"schedule-inspect", "SCHEDULE_ID", "Replay a repository-local task schedule without dispatching or releasing capacity."},
	{"pool-status", "", "Inspect the configured shared pool and exact active attempts without acquiring or releasing capacity."},
	{"pause", "RUN ACTOR NONCE", "Request a durable stop of new dispatch after admitted work settles."},
	{"cancel", "RUN ACTOR NONCE", "Request cancellation without discarding unresolved effects or restarting work."},
	{"settle-lifecycle", "RUN ACTOR EVIDENCE workloads-stopped", "Attest quiescence for a requested pause or cancellation while retaining UNKNOWN work."},
	{"prepare-commit-recovery", "RUN EVIDENCE workloads-stopped", "Prepare separately authorized completion of an exact recognized partial commit state."},
	{"recover-commit", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Execute one new commit recovery attempt and bind its receipt to verified final state."},
	{"prepare-commit-lease", "RUN EVIDENCE workloads-stopped", "Prepare recovery of a pending commit's abandoned lease using explicit quiescence evidence."},
	{"recover-commit-lease", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Adopt an exactly authorized abandoned lease, observe commit state and journal lease release without retrying commit writes."},
	{"prepare-commit", "RUN METADATA_JSON", "Prepare a local commit with explicit author, committer, Unix timestamps and LF-terminated message."},
	{"prepare-draft-lease", "RUN EVIDENCE workloads-stopped", "Prepare separately authorized recovery of an abandoned draft lease."},
	{"recover-draft-lease", "RUN PREVIEW_JSON INTENT_ID ACTOR TOKEN_ENV", "Recover an abandoned draft lease and observe without repeating creation."},
	{"prepare-draft", "RUN REPOSITORY BASE_REF TEXT_JSON TOKEN_ENV", "Prepare exact draft text and base commit using the named token environment variable."},
	{"draft", "RUN PREVIEW_JSON INTENT_ID ACTOR TOKEN_ENV", "Create one explicitly approved draft and reconcile hosted state."},
	{"reconcile-draft", "RUN TOKEN_ENV", "Observe an unresolved draft without repeating creation."},
	{"reconcile-push", "RUN [TOKEN_ENV]", "Observe pending push state with an optional explicitly selected credential."},
	{"prepare-push", "RUN DESTINATION TARGET_REF [TOKEN_ENV]", "Observe an exact remote branch and prepare committed-candidate publication."},
	{"prepare-push-lease", "RUN EVIDENCE workloads-stopped", "Prepare separately authorized recovery of a push lease."},
	{"recover-push-lease", "RUN PREVIEW_JSON INTENT_ID ACTOR [TOKEN_ENV]", "Adopt a quiescent push lease, reconcile remote state and release it."},
	{"push", "RUN PREVIEW_JSON INTENT_ID ACTOR [TOKEN_ENV]", "Perform one exactly authorized push and record remote readback."},
	{"commit", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Execute one exactly authorized local commit and record observed object/ref/index outcome."},
	{"usage", "RUN [--schedule SCHEDULE_ID]", "Inspect controller admissions with journaled Codex and OpenCode invocation usage from replay-validated receipts; pending OpenCode work stays UNKNOWN and money stays unknown. Pass --schedule SCHEDULE_ID to bind composite explorer turns to one exact schedule."},
	{"runtime-usage", "JOURNAL", "Report journal-bound context bytes, tool calls and observed provider usage without exposing content."},
	{"prepare-review", "RUN", "Inspect the explicit reviewer invocation for the verified candidate."},
	{"prepare-explorer", "RUN QUESTION", "Inspect a read-only exploration invocation for the current candidate."},
	{"explore", "RUN QUESTION", "Execute or resume the configured explorer and record advisory context."},
	{"review", "RUN", "Execute or resume the configured read-only reviewer and admit its bound verdict."},
	{"prepare-writer", "RUN", "Inspect the exact configured writer invocation for the current admitted candidate."},
	{"writer-files", "RUN", "Export the already recorded writer proposal as an exact file approval preview without preparing a new intent."},
	{"write", "RUN", "Execute or resume the configured writer and record a file proposal without applying changes."},
	{"ri close-producer", "RUN INTENT_ID ACTOR EVIDENCE workloads-stopped", "Close interrupted producer uncertainty with explicit quiescence evidence; retain UNKNOWN outcome."},
	{"ri prepare-producer", "RUN CHECK_JSON OUTPUT", "Freeze a semantic indexer invocation in the admitted isolated workspace."},
	{"ri produce", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Execute the exactly authorized producer and journal source/process evidence."},
	{"ri bind-import", "RUN PLAN_JSON", "Attach confirmed producer provenance to an import proposal without executing it."},
	{"ri runtime-binding", "RUN", "Revalidate the confirmed published RI snapshot and pinned executable for runtime use."},
	{"ri changed", "EXE EXE_SHA256 BEFORE_SNAPSHOT BEFORE_ID BEFORE_COMMIT AFTER_SNAPSHOT AFTER_ID AFTER_COMMIT [LIMIT [CURSOR]]", "Compare exact snapshot input declarations for two full commits; supply LIMIT for bounded pages."},
	{"ri related", "EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID NODE RELATION DIRECTION PRODUCER LIMIT [CURSOR]", "Page explicit graph relationships without relevance ranking or implicit relation expansion."},
	{"ri locate", "EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID FILE OFFSET PRODUCER LIMIT [CURSOR]", "Locate overlapping source observations at an exact byte offset without semantic ranking."},
	{"ri search", "EXE EXE_SHA256 REF_JSON [--fixed|--regex] [--case-insensitive] [--limit N] [--after CURSOR_JSON] [--overlay-run RUN] PATTERN", "Search verified committed source bytes using an admitted lexical disk index."},
	{"ri modules", "", "Emit a bounded partial inventory of committed go.mod, go.work and vendor metadata for this configured repository; dirty files are ignored and no Go command is run."},
	{"ri facts", "EXE EXE_SHA256 PATH [CACHE_DIR]", "Extract bounded partial Go syntax facts from the exact committed file; optional cache is a local derived optimization."},
	{"ri graph", "EXE EXE_SHA256 SPEC_JSON [CACHE_DIR]", "Build a partial Go graph from an explicit bounded corpus of committed files and package/generator metadata; an optional source-bound module_inventory derives package ownership; no generators are executed."},
	{"ri query", "EXE EXE_SHA256 SPEC_JSON QUERY_JSON [CACHE_DIR]", "Return bounded partial graph-backed Go symbols, imports, calls, tests, generators, module ownership, paths or impact; calls remain UNRESOLVED and semantic references/implementations are unsupported."},
	{"ri rank", "EXE EXE_SHA256 SPEC_JSON QUERY_JSON [CACHE_DIR]", "Rank committed Go files by exact objective terms and observed package degree; return bounded advisory component and provenance evidence without changing planner policy."},
	{"ri semantic", "EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID QUERY_JSON [CURSOR]", "Page producer-bound direct references or explicit IMPLEMENTS edges from an admitted semantic snapshot; preserve directional coverage and absence evidence."},
	{"ri candidate-query", "RUN EXE EXE_SHA256 BASE_SPEC_JSON QUERY_JSON [CACHE_DIR]", "Query a confirmed run candidate using a committed module-backed base graph and bounded candidate overlay; output is candidate-bound and PARTIAL."},
	{"ri context", "EXE EXE_SHA256 SPEC_JSON OBJECTIVE [CACHE_DIR]", "Compile a bounded task context from the same explicit committed Go corpus and partial graph; sensitive paths are rejected before reading."},
	{"ri topology", "EXE EXE_SHA256 SPEC_JSON CHANGED_PATHS_JSON MAX_GROUP_FILES [CACHE_DIR]", "Query advisory Go package topology, observed impact, explicit generator coupling and bounded review groups from a committed partial graph."},
	{"ri prepare-lexical", "RUN EXE EXE_SHA256 STAGE_ROOT BATCH_BYTES BATCH_FILES", "Observe committed files and preview an exact lexical indexing intent."},
	{"ri lexical", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Execute one authorized and journaled lexical indexing attempt."},
	{"ri lexical-ref", "RUN", "Reverify confirmed lexical staging and emit its search reference."},
	{"ri prepare-overlay", "RUN STAGE_ROOT", "Prepare an exact lexical overlay from the admitted candidate."},
	{"ri overlay", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Materialize one authorized and journaled lexical overlay."},
	{"ri overlay-ref", "RUN", "Reverify and export the confirmed overlay for the current candidate."},
	{"ri definition|references", "EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID SYMBOL PRODUCER LIMIT [CURSOR]", "Page direct semantic occurrences; relationship expansion and absence inference are not performed."},
	{"ri path", "EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID FROM TO RELATION DIRECTION PRODUCER MAX_DEPTH MAX_EDGES", "Find a bounded observed graph path; exhaustion does not prove absence."},
	{"ri", "status|coverage|deps|rdeps EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID ...", "Query a committed-source snapshot. Coverage adds NODE RELATION DIRECTION; deps/rdeps add NODE PRODUCER LIMIT [CURSOR] for direct dependency edges."},
	{"init", "[--codex EXE --model MODEL [--effort EFFORT] [--writer-model MODEL] [--writer-effort EFFORT] [--reviewer-model MODEL] [--reviewer-effort EFFORT] [--auth-source PATH] [--state-root PATH] [--validate-writer-edits]]", "Create a fake configuration or a complete Codex role configuration without overwriting an existing file; validate-writer-edits opts into same-turn anchored edit validation."},
	{"doctor", "", "Validate configuration and committed Git identity; report configured roles, verification readiness and memory observation without runtime dispatch."},
	{"diff", "[RUN]", "Show the current isolated candidate diff, including non-ignored untracked files with coverage metadata, without applying or dispatching work."},
	{"observe-format", "RUN MANIFEST_JSON CACHE_DIR", "Record a bounded local Go formatting artifact for one confirmed candidate; this never satisfies verification, review, repair or READY."},
	{"plan", "OBJECTIVE or --file PATH", "Create a plan from exact objective text or a bounded UTF-8 file using the explicitly configured runtime and access profile."},
	{"status", "", "List validated local run IDs, workflow/lifecycle states and plan IDs without input or evidence bodies."},
	{"inspect", "[RUN] [--export-jsonl]", "Replay one run and show its bound inputs and state, or export its validated canonical event history."},
	{"diagnose", "RUN [--anchor [--previous REPORT_JSON] | --closure | --spectrum SPECTRUM_JSON]", "Project repair findings and admitted scope; optionally validate current bytes, relocate prior code hints, inspect closure receipts or rank supplied per-test Go coverage, without dispatch or retry authority."},
	{"repair-context", "RUN SPECTRUM_JSON [--task TASK_ID]", "Admit bounded untrusted coverage localization before freezing a ready repair writer context; no dispatch, retry or closure authority."},
	{"evidence-value", "RUN REQUEST_JSON or RUN --template", "Evaluate a bounded snapshot-bound finite evidence model and known resource costs; recommend or stop without dispatch, retry or acceptance authority."},
	{"checkpoint", "RUN", "Emit a payload-free checkpoint with exact source/candidate/graph identity, completed tasks, gate status, uncertainty counts and a bounded acceptance conclusion."},
	{"resume", "[RUN] [ACTOR NONCE] or --autonomous [RUN]", "Resume planning, explicitly reopen a settled pause with ACTOR and NONCE, or continue one bounded autonomous run without resending uncertain work."},
	{"approve", "RUN PLAN ACTOR", "Approve one exact plan with an explicit human actor."},
	{"run", "RUN or --autonomous [--prepare-only | --inspect-plan] [--agent-context=true|false] [--dynamic-explorers] [--working-context] [--repair-intelligence] [--context-selector rrf-coverage-v1] [--max-repairs N] [--max-parallel N] [--parallel-writers | --isolated-writers --isolation-policy PATH | --isolated-writer-waves --isolation-policy PATH | --isolated-writer-staged --isolation-policy PATH [--cohort-selector lexicographic-v1|coupling-aware-v1|observed-coupling-v1]] [--evidence-policy PATH] [--planner-context source-bounded-v1|go-source-context-v1|go-source-context-v2|go-contract-context-v1|go-contract-context-v2|go-contract-context-v3] [--planner-context-ri-executable PATH --planner-context-ri-executable-sha256 SHA256] [--planner-context-parse-cache] [--planner-ppr] [--prompt-recipe cache-prefix-v1] OBJECTIVE or --file PATH", "Create or validate an approved run's isolated writer worktree, or create and advance a bounded autonomous coding run; pinned Go planner contexts require an explicit absolute parser path and lowercase SHA-256; planner-context-parse-cache is a versioned local RI optimization for supported Go contexts; planner-ppr opts go-source-context-v2 planning into bounded Personalized PageRank hints over the admitted graph (requires pinned RI); contract-context-v3 adds bounded source-bound generator ownership evidence without executing generators or widening write scope; parallel-writers opts into up to two independent initial implementation tasks; isolated-writers requires a versioned resource policy and creates a resource-bounded initial cohort in separate worktrees; isolated-writer-waves requires the same policy and runs up to eight initial implementations in serial resource-bounded waves with one parent aggregate; isolated-writer-staged requires the same policy and runs dependent hub-to-leaf staged cohorts each forked from its exact parent candidate with one aggregate per stage; cohort-selector lexicographic-v1 opts staged runs into the exact finite lexicographic wave optimum over the same hard gates, and coupling-aware-v1 opts staged runs into the typed C1-C4 optimum with the C4 hard gate under plan-graph-v9 (absent preserves the frozen greedy derivation; rejected for every other writer mode); isolated modes and parallel-writers are exclusive; evidence-policy opts a new serial graph run into one automatic finite source acquisition before the initial writer and is frozen at creation; prompt-recipe opts into cache-prefix-v1 request ordering; repair-intelligence opts new runs into bounded structured fixer evidence from recorded task context; context-selector opts bounded task context into experimental rrf-coverage-v1 ranking (empty preserves legacy selection); agent-context binds committed scope-aware instructions and role-selected skills (enabled for new runs); dynamic-explorers opts into dynamic Codex explorer execution; working-context enables that executor plus experimental bounded editable notes (both disabled by default); prepare-only returns after graph and workspace confirmation; inspect-plan omits the objective and reports effective capabilities and resource estimates without creating a run or dispatching."},
	{"reconcile", "RUN", "Observe unknown local commit, RI import/publication, workspace or file effects without retrying writes."},
	{"ri prepare-import", "RUN PLAN_JSON", "Validate an import plan and return its exact effect approval target."},
	{"ri import", "RUN PLAN_JSON INTENT_ID ACTOR", "Execute an exactly authorized, journaled local SCIP import."},
	{"ri prepare-publish", "RUN DIRECTORY", "Return the exact local artifact publication approval target."},
	{"ri publish", "RUN DIRECTORY INTENT_ID ACTOR", "Publish a confirmed snapshot into the local content-addressed store."},
	{"ri prepare-publication-recovery", "RUN", "Return a fresh approval target for pending local publication recovery."},
	{"ri recover-publication", "RUN INTENT_ID ACTOR", "Execute separately authorized pending publication recovery."},
	{"verify", "RUN", "Execute all frozen required checks and journal candidate-bound observations."},
	{"close-verification", "RUN PLAN_ID ACTOR EVIDENCE workloads-stopped", "Close an interrupted attempt using explicit operator quiescence evidence; retain unknown outcomes."},
	{"prepare-files", "RUN CHANGES_JSON", "Preview exact file changes against the current candidate without writing."},
	{"apply-files", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Approve and apply one exact file proposal, recording observed outcome."},
	{"prepare-recovery", "RUN", "Preview recognized partial writes as a fresh recovery approval target."},
	{"recover-files", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Explicitly approve cleanup and completion of recognized partial writes."},
}

// Reference returns generated Markdown from the actual command catalogue.
func Reference() string {
	var b strings.Builder
	b.WriteString("# CLI reference\n\nGenerated from `internal/cli`; do not edit by hand.\n\nUse `fabric [--root PATH] COMMAND`; `harness` remains a compatibility executable.\nOutput is canonical JSON except help, aggregated run-snapshot JSON, and `inspect --export-jsonl`.\n\n| Command | Arguments | Behavior |\n| --- | --- | --- |\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", markdownTableCell(c.Name), markdownTableCell(c.Arguments), markdownTableCell(c.Summary))
	}
	b.WriteString("\n`fabric help` shows commands; `fabric reference` regenerates this file.\nErrors exit 1; success exits 0. Workspaces and exact approved file proposals are\nimplemented, with journaled verification and Codex planning. GitHub effects follow.\n`reconcile` observes UNKNOWN workspace/file state without retrying writes.\n")
	b.WriteString("Commands that return a run snapshot encode the complete replay-validated state as JSON; this aggregate is not an identity payload and may exceed the canonical single-value bound. Other command output remains canonical JSON. `inspect RUN --export-jsonl` emits the validated event history as per-event canonical JSONL.\n")
	return b.String()
}

func markdownTableCell(text string) string { return strings.ReplaceAll(text, "|", `\|`) }

func output(w io.Writer, v any) error {
	if snapshot, ok := v.(control.Snapshot); ok {
		return outputSnapshot(w, snapshot)
	}
	b, err := canonical.Bytes(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// outputSnapshot serializes the complete replay-validated snapshot without
// applying the canonical single-value bound used for identity and transport
// payloads. Its fields remain the control.Snapshot JSON shape.
func outputSnapshot(w io.Writer, snapshot control.Snapshot) error {
	return json.NewEncoder(w).Encode(snapshot)
}

func configFilePath(root string) string {
	fabricPath := filepath.Join(root, "fabric.toml")
	if _, err := os.Lstat(fabricPath); err == nil {
		return fabricPath
	}
	return filepath.Join(root, "harness.toml")
}

func configuration(root string) (config.Config, error) {
	f, err := os.Open(configFilePath(root))
	if err != nil {
		return config.Config{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return config.Config{}, err
	}
	return config.Parse(b)
}

func runPath(root, id string) (string, error) {
	cfg, err := configuration(root)
	if err != nil {
		if os.IsNotExist(err) {
			if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
				return "", errors.New("run ID must be 64 lowercase hex characters")
			}
			return filepath.Join(root, ".harness", "runs", id+".jsonl"), nil
		}
		return "", err
	}
	if cfg.ControllerStateRoot == "" {
		if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
			return "", errors.New("run ID must be 64 lowercase hex characters")
		}
		return filepath.Join(root, ".harness", "runs", id+".jsonl"), nil
	}
	identity, err := repository.Discover(context.Background(), root, cfg.Repository)
	if err != nil {
		return "", err
	}
	paths, err := controllerstate.Resolve(cfg.ControllerStateRoot, identity)
	if err != nil {
		return "", err
	}
	if err := controllerstate.Validate(paths, identity); err != nil {
		return "", err
	}
	return paths.Run(id)
}

func initializeRunPath(root, id string, cfg config.Config, identity repository.Identity) (string, error) {
	paths, err := controllerstate.Resolve(cfg.ControllerStateRoot, identity)
	if err != nil {
		return "", err
	}
	if err := controllerstate.Initialize(paths, identity); err != nil {
		return "", err
	}
	return paths.Run(id)
}

// usageScheduleArgument parses RUN [--schedule SCHEDULE_ID] for the usage
// command. The schedule binding is exact; no scheduler is inferred.
func usageScheduleArgument(args []string) (runID, scheduleID string, err error) {
	switch len(args) {
	case 1:
		return args[0], "", nil
	case 3:
		if args[1] != "--schedule" {
			return "", "", errors.New("usage requires RUN [--schedule SCHEDULE_ID]")
		}
		if args[2] == "" {
			return "", "", errors.New("usage requires RUN [--schedule SCHEDULE_ID]")
		}
		return args[0], args[2], nil
	default:
		return "", "", errors.New("usage requires RUN [--schedule SCHEDULE_ID]")
	}
}

// requireScheduleReferencesRun requires the selected schedule to bind the
// selected run by exact run ID and controller path, after the existing
// repository/schedule validation. A same-repository schedule for another run
// must not silently scope usage.
func requireScheduleReferencesRun(schedPath, runID, controllerPath string) error {
	snapshot, err := taskscheduler.Inspect(schedPath)
	if err != nil {
		return err
	}
	want := filepath.Clean(controllerPath)
	if snapshot.Definition != nil {
		for _, task := range snapshot.Definition.Tasks {
			if task.RunID == runID && filepath.Clean(task.ControllerPath) == want {
				return nil
			}
		}
	}
	for _, dynamic := range snapshot.Dynamic {
		if dynamic.Task.RunID == runID && filepath.Clean(dynamic.Task.ControllerPath) == want {
			return nil
		}
	}
	return errors.New("selected schedule does not bind the selected run")
}

// Execute runs one CLI command using cwd as the default root. It does not exit
// the process. Only explicit approve arguments record human approval.
func Execute(ctx context.Context, args []string, cwd string, out io.Writer) (resultErr error) {
	ctx, finishTelemetry := startCommandTelemetry(ctx, args)
	defer func() { finishTelemetry(resultErr) }()
	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", cwd, "repository root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	args = fs.Args()
	if len(args) == 0 || args[0] == "help" {
		_, err := io.WriteString(out, Reference())
		return err
	}
	if args[0] == "reference" {
		if len(args) != 1 {
			return errors.New("reference takes no arguments")
		}
		_, err := io.WriteString(out, Reference())
		return err
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return errors.New("version takes no arguments")
		}
		info, err := buildinfo.Current()
		if err != nil {
			return err
		}
		return output(out, info)
	}
	var err error
	*root, err = filepath.Abs(*root)
	if err != nil {
		return err
	}
	command := args[0]
	args = args[1:]
	switch command {
	case "evidence-feedback":
		return evidenceFeedbackCommand(*root, args, out)
	case "evidence-acquire":
		return evidenceAcquireCommand(ctx, *root, args, out)
	case "agent-interrupt":
		return agentInterruptCommand(ctx, *root, args, out)
	case "agent-spawn", "agent-followup":
		return agentScheduleCommand(ctx, *root, command, args, out)
	case "agent-list", "agent-send", "agent-messages", "agent-wait":
		return agentCommand(ctx, *root, command, args, out)
	case "schedule-task", "schedule-create", "schedule-inspect", "schedule-tick", "schedule-run", "schedule-recover":
		return scheduleCommand(ctx, *root, command, args, out)
	case "pool-status":
		if len(args) != 0 {
			return errors.New("pool-status takes no arguments")
		}
		cfg, err := configuration(*root)
		if err != nil {
			return err
		}
		return poolStatus(ctx, cfg.TaskPool, out)
	case "pause", "cancel", "settle-lifecycle":
		return lifecycleCommand(ctx, *root, command, args, out)
	case "prepare-commit-recovery", "recover-commit":
		return commitRecoveryCommand(ctx, *root, command, args, out)
	case "prepare-commit-lease", "recover-commit-lease":
		return commitLeaseCommand(ctx, *root, command, args, out)
	case "prepare-commit", "commit":
		return commitCommand(ctx, *root, command, args, out)
	case "prepare-push", "push", "reconcile-push":
		return pushCommand(ctx, *root, command, args, out)
	case "prepare-draft", "draft", "reconcile-draft":
		return draftCommand(ctx, *root, command, args, out)
	case "prepare-draft-lease", "recover-draft-lease":
		return draftLeaseCommand(ctx, *root, command, args, out)
	case "prepare-push-lease", "recover-push-lease":
		return pushLeaseCommand(ctx, *root, command, args, out)
	case "ri":
		return riCommand(ctx, *root, args, out)
	case "checkpoint":
		return checkpointCommand(ctx, *root, args, out)
	case "runtime-usage":
		if len(args) != 1 {
			return errors.New("runtime-usage requires one journal path")
		}
		path, err := riAbsolutePath(*root, args[0])
		if err != nil {
			return err
		}
		report, err := codexruntime.MeasureContext(path)
		if err != nil {
			return err
		}
		return output(out, report)
	case "prepare-recovery", "recover-files":
		return recoveryCommand(ctx, *root, command, args, out)
	case "prepare-files", "apply-files":
		return fileCommand(ctx, *root, command, args, out)
	case "writer-files":
		return writerFilesCommand(*root, args, out)
	case "init":
		return initCommand(ctx, *root, args, out)
	case "calibrate-models":
		return modelCalibrationCommand(args, out)
	case "diff":
		return diffCommand(ctx, *root, args, out)
	case "observe-format":
		return formatObservationCommand(ctx, *root, args, out)
	case "doctor":
		if len(args) != 0 {
			return errors.New("doctor takes no arguments")
		}
		cfg, err := configuration(*root)
		if err != nil {
			return err
		}
		identity, err := repository.Discover(ctx, *root, cfg.Repository)
		if err != nil {
			return err
		}
		if _, err := controllerstate.Resolve(cfg.ControllerStateRoot, identity); err != nil {
			return err
		}
		return writeDoctor(ctx, out, identity, cfg.HostPolicy, cfg)
	case "plan":
		objective, err := planObjective(ctx, *root, args)
		if err != nil {
			return err
		}
		cfg, err := configuration(*root)
		if err != nil {
			return err
		}
		identity, err := repository.Discover(ctx, *root, cfg.Repository)
		if err != nil {
			return err
		}
		if filepath.Clean(identity.Root) != filepath.Clean(*root) {
			return errors.New("root must be repository top level")
		}
		nonce := make([]byte, 16)
		if _, err = rand.Read(nonce); err != nil {
			return err
		}
		c := control.Creation{Version: 1, Nonce: hex.EncodeToString(nonce), Repository: identity, Objective: objective, Config: cfg}
		c, err = bindCurrentHost(ctx, c)
		if err != nil {
			return err
		}
		id, err := canonical.Hash("harness.run.v1", c)
		if err != nil {
			return err
		}
		p, err := initializeRunPath(*root, id, cfg, identity)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return err
		}
		if err = control.Append(p, "run.created", c); err != nil {
			return err
		}
		s, err := resume(ctx, p)
		if err != nil {
			return err
		}
		return output(out, s)
	case "run":
		return runCommand(ctx, *root, args, out)
	case "diagnose":
		return diagnoseCommand(ctx, *root, args, out)
	case "evidence-value":
		return evidenceValueCommand(*root, args, out)
	case "repair-context":
		return repairContextCommand(ctx, *root, args, out)
	case "prepare-explorer", "explore", "usage", "prepare-review", "review", "prepare-writer", "write", "inspect", "resume", "approve", "reconcile", "verify", "close-verification":
		if command == "resume" && len(args) >= 1 && args[0] == "--autonomous" {
			return autonomousResumeCommand(ctx, *root, args[1:], out)
		}
		if command == "resume" && len(args) == 3 {
			return lifecycleCommand(ctx, *root, command, args, out)
		}
		if command == "inspect" {
			id, export, err := inspectRunArgument(*root, args)
			if err != nil {
				return err
			}
			if export {
				return inspectExport(ctx, *root, id, out)
			}
			args = []string{id}
		} else if command == "resume" && len(args) == 0 {
			id, err := latestRunID(*root)
			if err != nil {
				return err
			}
			args = []string{id}
		}
		var usageScheduler string
		if command == "usage" {
			runID, scheduleID, err := usageScheduleArgument(args)
			if err != nil {
				return err
			}
			args = []string{runID}
			usageScheduler = scheduleID
		}
		want := 1
		if command == "prepare-explorer" || command == "explore" {
			want = 2
		}
		if command == "approve" {
			want = 3
		}
		if command == "close-verification" {
			want = 5
		}
		if len(args) != want {
			return fmt.Errorf("%s requires %d arguments", command, want)
		}
		p, err := runPath(*root, args[0])
		if err != nil {
			return err
		}
		bound, err := control.Inspect(p)
		if err != nil {
			return err
		}
		if bound.RunID != args[0] || filepath.Clean(bound.Creation.Repository.Root) != filepath.Clean(*root) {
			return errors.New("journal/run repository binding mismatch")
		}
		if command == "prepare-writer" {
			i, err := control.PrepareWriterInvocation(p)
			if err != nil {
				return err
			}
			return output(out, i)
		}
		if command == "prepare-explorer" {
			i, err := control.PrepareExplorerInvocation(p, args[1])
			if err != nil {
				return err
			}
			return output(out, i)
		}
		if command == "explore" {
			record, err := control.RunExplorer(ctx, p, args[1])
			if err != nil {
				return err
			}
			return output(out, record)
		}
		if command == "usage" {
			if usageScheduler != "" {
				schedPath, err := schedulePath(*root, usageScheduler)
				if err != nil {
					return err
				}
				if err := validateAgentSchedule(*root, args[0], usageScheduler, p, schedPath); err != nil {
					return err
				}
				if err := requireScheduleReferencesRun(schedPath, args[0], p); err != nil {
					return err
				}
				report, err := control.MeasureRunUsageWithScheduler(p, schedPath)
				if err != nil {
					return err
				}
				return output(out, report)
			}
			report, err := control.MeasureRunUsage(p)
			if err != nil {
				return err
			}
			return output(out, report)
		}
		if command == "prepare-review" {
			i, err := control.PrepareReviewInvocation(p)
			if err != nil {
				return err
			}
			return output(out, i)
		}
		if command == "review" {
			record, err := control.RunReview(ctx, p)
			if err != nil {
				return err
			}
			return output(out, record)
		}
		if command == "write" {
			record, err := control.RunWriter(ctx, p)
			if err != nil {
				return err
			}
			return output(out, record)
		}
		if command == "approve" {
			if err = control.Append(p, "plan.approved", control.Approval{PlanID: args[1], Actor: args[2]}); err != nil {
				return err
			}
		}
		var s control.Snapshot
		if command == "run" {
			s, err = control.StartWorkspace(ctx, p)
		} else if command == "verify" {
			s, err = control.Verify(ctx, p)
		} else if command == "close-verification" {
			if args[4] != "workloads-stopped" {
				return errors.New("explicit workloads-stopped attestation required")
			}
			s, err = control.CloseVerification(ctx, p, args[1], args[2], args[3], true)
		} else if command == "reconcile" {
			if bound.Push != nil && bound.Push.Outcome == "UNKNOWN" {
				s, err = control.ReconcilePush(ctx, p)
			} else if bound.Commit != nil && bound.Commit.Outcome == "UNKNOWN" {
				s, err = control.ReconcileCommit(ctx, p)
			} else if bound.RIPublish != nil && bound.RIPublish.Outcome == "UNKNOWN" {
				s, err = control.ReconcileRIPublish(ctx, p)
			} else if bound.RIImport != nil && bound.RIImport.Outcome == "UNKNOWN" {
				s, err = control.ReconcileRIImport(ctx, p)
			} else if bound.RILexical != nil && bound.RILexical.Outcome == "UNKNOWN" {
				s, err = control.ReconcileRILexical(ctx, p)
			} else if bound.RILexicalOverlay != nil && bound.RILexicalOverlay.Outcome == "UNKNOWN" {
				s, err = control.ReconcileLexicalOverlay(ctx, p)
			} else if bound.FileOutcome == "UNKNOWN" {
				s, err = control.ReconcileFiles(ctx, p)
			} else {
				s, err = control.ReconcileWorkspace(ctx, p)
			}
		} else if command == "resume" {
			s, err = resume(ctx, p)
		} else {
			s, err = snapshotForCommand(command, bound, p)
		}
		if err != nil {
			return err
		}
		if s.RunID != args[0] {
			return errors.New("journal/run filename mismatch")
		}
		return output(out, s)
	case "status":
		if len(args) != 0 {
			return errors.New("status takes no arguments")
		}
		cfg, err := configuration(*root)
		if err != nil {
			return err
		}
		identity, err := repository.Discover(ctx, *root, cfg.Repository)
		if err != nil {
			return err
		}
		paths, err := controllerstate.Resolve(cfg.ControllerStateRoot, identity)
		if err != nil {
			return err
		}
		if paths.External {
			if err := controllerstate.Validate(paths, identity); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					if _, statErr := os.Stat(paths.Root); os.IsNotExist(statErr) {
						return output(out, []runStatus{})
					}
				}
				return err
			}
		}
		entries, err := filepath.Glob(filepath.Join(paths.Runs, "*.jsonl"))
		if err != nil {
			return err
		}
		runs := []runStatus{}
		for _, p := range entries {
			if runJournalSidecar(p) {
				continue
			}
			s, err := control.Inspect(p)
			if err != nil {
				return err
			}
			if filepath.Base(p) != s.RunID+".jsonl" {
				return errors.New("journal/run filename mismatch")
			}
			if filepath.Clean(s.Creation.Repository.Root) != *root {
				return errors.New("status run repository identity differs")
			}
			runs = append(runs, summarizeRun(s))
		}
		return output(out, runs)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

// snapshotForCommand reuses the already validated snapshot for a read-only
// inspect. Other commands reach this fallback only after any command-specific
// mutation or reconciliation and must load their resulting state again.
func snapshotForCommand(command string, bound control.Snapshot, path string) (control.Snapshot, error) {
	if command == "inspect" {
		return bound, nil
	}
	return control.Inspect(path)
}

func resume(ctx context.Context, p string) (control.Snapshot, error) {
	return control.ResumePlanning(ctx, p)
}
