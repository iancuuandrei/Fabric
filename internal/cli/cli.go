package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/controllerstate"
	"harness.local/engorch/internal/repository"
)

// Command describes an implemented CLI command; Reference uses this same table.
type Command struct {
	Name      string
	Arguments string
	Summary   string
}

var commands = []Command{
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
	{"usage", "RUN", "Inspect controller admissions and journaled Codex context usage, checking admitted receipt heads."},
	{"runtime-usage", "JOURNAL", "Report journal-bound context bytes, tool calls and observed provider usage without exposing content."},
	{"prepare-review", "RUN", "Inspect the explicit reviewer invocation for the verified candidate."},
	{"prepare-explorer", "RUN QUESTION", "Inspect a read-only exploration invocation for the current candidate."},
	{"explore", "RUN QUESTION", "Execute or resume the configured explorer and record advisory context."},
	{"review", "RUN", "Execute or resume the configured read-only reviewer and admit its bound verdict."},
	{"prepare-writer", "RUN", "Inspect the exact configured writer invocation for the current admitted candidate."},
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
	{"ri prepare-lexical", "RUN EXE EXE_SHA256 STAGE_ROOT BATCH_BYTES BATCH_FILES", "Observe committed files and preview an exact lexical indexing intent."},
	{"ri lexical", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Execute one authorized and journaled lexical indexing attempt."},
	{"ri lexical-ref", "RUN", "Reverify confirmed lexical staging and emit its search reference."},
	{"ri prepare-overlay", "RUN STAGE_ROOT", "Prepare an exact lexical overlay from the admitted candidate."},
	{"ri overlay", "RUN PREVIEW_JSON INTENT_ID ACTOR", "Materialize one authorized and journaled lexical overlay."},
	{"ri overlay-ref", "RUN", "Reverify and export the confirmed overlay for the current candidate."},
	{"ri definition|references", "EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID SYMBOL PRODUCER LIMIT [CURSOR]", "Page direct semantic occurrences; relationship expansion and absence inference are not performed."},
	{"ri path", "EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID FROM TO RELATION DIRECTION PRODUCER MAX_DEPTH MAX_EDGES", "Find a bounded observed graph path; exhaustion does not prove absence."},
	{"ri", "status|coverage|deps|rdeps EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID ...", "Query a committed-source snapshot. Coverage adds NODE RELATION DIRECTION; deps/rdeps add NODE PRODUCER LIMIT [CURSOR] for direct dependency edges."},
	{"init", "", "Write a new fake-runtime configuration without overwriting an existing file."},
	{"doctor", "", "Validate configuration and committed Git identity; dispatch no runtime."},
	{"plan", "OBJECTIVE or --file PATH", "Create a plan from exact objective text or a bounded UTF-8 file using the explicitly configured runtime and access profile."},
	{"status", "", "List validated local run IDs, workflow/lifecycle states and plan IDs without input or evidence bodies."},
	{"inspect", "RUN [--export-jsonl]", "Replay one run and show its bound inputs and state, or export its validated canonical event history."},
	{"resume", "RUN [ACTOR NONCE]", "Resume planning, or explicitly reopen a settled pause with ACTOR and NONCE; never resend uncertain work."},
	{"approve", "RUN PLAN ACTOR", "Approve one exact plan with an explicit human actor."},
	{"run", "RUN", "Create or validate the approved run's isolated writer worktree."},
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
	b.WriteString("# CLI reference\n\nGenerated from `internal/cli`; do not edit by hand.\n\nUse `harness [--root PATH] COMMAND`. Output is canonical JSON except help and `inspect --export-jsonl`.\n\n| Command | Arguments | Behavior |\n| --- | --- | --- |\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", c.Name, c.Arguments, c.Summary)
	}
	b.WriteString("\n`harness help` shows commands; `harness reference` regenerates this file.\nErrors exit 1; success exits 0. Workspaces and exact approved file proposals are\nimplemented, with journaled verification and Codex planning. GitHub effects follow.\n`reconcile` observes UNKNOWN workspace/file state without retrying writes.\n")
	return b.String()
}

func output(w io.Writer, v any) error {
	b, err := canonical.Bytes(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func configuration(root string) (config.Config, error) {
	f, err := os.Open(filepath.Join(root, "harness.toml"))
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
	var err error
	*root, err = filepath.Abs(*root)
	if err != nil {
		return err
	}
	command := args[0]
	args = args[1:]
	switch command {
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
	case "init":
		if len(args) != 0 {
			return errors.New("init takes no arguments")
		}
		f, err := os.OpenFile(filepath.Join(*root, "harness.toml"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString(config.Example)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		if err = errors.Join(writeErr, f.Close()); err != nil {
			return err
		}
		return output(out, map[string]string{"status": "CREATED", "configuration": "harness.toml", "runtime": "fake"})
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
		return writeDoctorWithConfig(ctx, out, identity, cfg.HostPolicy, &cfg)
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
	case "prepare-explorer", "explore", "usage", "prepare-review", "review", "prepare-writer", "write", "inspect", "resume", "approve", "run", "reconcile", "verify", "close-verification":
		if command == "resume" && len(args) == 3 {
			return lifecycleCommand(ctx, *root, command, args, out)
		}
		if command == "inspect" && len(args) == 2 && args[1] == "--export-jsonl" {
			return inspectExport(ctx, *root, args[0], out)
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
			s, err = control.Inspect(p)
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

func resume(ctx context.Context, p string) (control.Snapshot, error) {
	return control.ResumePlanning(ctx, p)
}
