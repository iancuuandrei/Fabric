package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sort"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/anchoredit"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

// isolatedWriterBinding keeps task-owned child state separate from the parent
// integration workspace. It is a derived view of the durable isolation receipt.
type isolatedWriterBinding struct {
	TaskID          string
	IsolationID     string
	BaseCandidateID string
	Workspace       worktree.Binding
	Candidate       worktree.Candidate
	Task            writerImplementationContext
}

// IsolatedGraphWriterProposal is child-side evidence only. PreparedChild is
// never a parent file effect; graph integration must freshly prepare Changes
// against the current parent candidate before applying them.
type IsolatedGraphWriterProposal struct {
	Version       int                  `json:"version"`
	TaskID        string               `json:"task_id"`
	IsolationID   string               `json:"isolation_id"`
	Invocation    runtime.Invocation   `json:"invocation"`
	Result        runtime.Result       `json:"result"`
	Workspace     worktree.Binding     `json:"workspace"`
	Candidate     worktree.Candidate   `json:"candidate"`
	PreparedChild fileeffects.Proposal `json:"prepared_child"`
	Changes       []fileeffects.Change `json:"changes"`
	EditPreimages []WriterEditPreimage `json:"edit_preimages,omitempty"`
}

// isolatedInitialWriterForTask selects child routing only for the frozen
// initial implementation cohort. Later serial repair writers remain bound to
// the ordinary current parent candidate; an invalid initial task never falls
// back to that broader binding.
func isolatedInitialWriterForTask(s Snapshot, taskID string) (bool, error) {
	if !isolatedImplementationEnabled(s) || s.State != "IMPLEMENTING" {
		return false, nil
	}
	if taskID == "" || s.GraphIsolationPreparation == nil || !containsGraphIsolationTask(s.GraphIsolationPreparation.SelectedTaskIDs, taskID) {
		return false, errors.New("initial isolated writer requires a task in the frozen child cohort")
	}
	return true, nil
}

// prepareIsolatedGraphWriterCohort freezes (when necessary) and creates the
// selected initial task worktrees serially before any writer TaskSpecs exist.
// An UNKNOWN child creation stops the cohort; it is never retried here.
func prepareIsolatedGraphWriterCohort(ctx context.Context, path string) ([]isolatedWriterBinding, error) {
	s, err := Inspect(path)
	if err != nil {
		return nil, err
	}
	if !isolatedImplementationEnabled(s) {
		return nil, errors.New("isolated implementation not enabled")
	}
	if s.GraphIsolationPreparation == nil {
		if _, err := PrepareGraphIsolationCohort(ctx, path); err != nil {
			return nil, err
		}
		s, err = Inspect(path)
		if err != nil {
			return nil, err
		}
	}
	if s.GraphIsolationPreparation == nil || len(s.GraphIsolationPreparation.SelectedTaskIDs) == 0 {
		return nil, errors.New("frozen isolated writer cohort is empty")
	}
	ids := append([]string(nil), s.GraphIsolationPreparation.SelectedTaskIDs...)
	return freezeCohortBindings(ctx, path, ids, func(ctx context.Context, path, taskID string) error {
		_, err := CreateTaskIsolation(ctx, path, taskID)
		return err
	}, isolatedWriterBindingForTask)
}

// isolatedWriterBindingForTask accepts only a confirmed child receipt matching
// the frozen graph task and the unchanged pristine parent base.
func isolatedWriterBindingForTask(s Snapshot, taskID string) (isolatedWriterBinding, error) {
	if !isolatedImplementationEnabled(s) || s.State != "IMPLEMENTING" || s.Graph == nil || s.GraphIsolationPreparation == nil || s.Candidate == nil {
		return isolatedWriterBinding{}, errors.New("isolated initial writer state required")
	}
	state, ok := s.GraphIsolations[taskID]
	if !ok || state.Outcome != "CONFIRMED" || state.Binding == nil || state.Candidate == nil {
		return isolatedWriterBinding{}, errors.New("isolated task workspace is not confirmed")
	}
	intent, task, err := expectedGraphIsolationIntent(s, taskID)
	if err != nil || !sameCanonical(intent, state.Intent) {
		return isolatedWriterBinding{}, errors.Join(errors.New("isolated task intent does not match the frozen graph"), err)
	}
	if !containsGraphIsolationTask(s.GraphIsolationPreparation.SelectedTaskIDs, taskID) {
		return isolatedWriterBinding{}, errors.New("task is outside the frozen isolated cohort")
	}
	workspaceID, err := state.Binding.ID()
	if err != nil {
		return isolatedWriterBinding{}, err
	}
	if err := state.Candidate.ValidateBinding(*state.Binding); err != nil {
		return isolatedWriterBinding{}, err
	}
	childID, err := state.Candidate.ID()
	if err != nil {
		return isolatedWriterBinding{}, err
	}
	parentID, err := s.Candidate.ID()
	if err != nil {
		return isolatedWriterBinding{}, err
	}
	parent := *s.Candidate
	child := *state.Candidate
	if intent.BaseCandidateID != parentID || s.GraphIsolationPreparation.BaseCandidateID != parentID || child.WorktreeID != workspaceID || child.Version != parent.Version || child.Head != parent.Head || child.IndexHash != parent.IndexHash || child.FilesHash != parent.FilesHash || child.FileCount != parent.FileCount {
		return isolatedWriterBinding{}, errors.New("isolated child is not bound to the frozen pristine parent base")
	}
	if child.Head != s.Creation.Repository.Commit {
		return isolatedWriterBinding{}, errors.New("isolated child source commit mismatch")
	}
	_ = childID // Candidate.ID validates and binds every child identity field.
	return isolatedWriterBinding{
		TaskID:          taskID,
		IsolationID:     intent.Request.RunID,
		BaseCandidateID: parentID,
		Workspace:       *state.Binding,
		Candidate:       *state.Candidate,
		Task: writerImplementationContext{
			ID: task.ID, Title: task.Title,
			ScopePaths:       append([]string(nil), task.ScopePaths...),
			WritePaths:       append([]string(nil), task.WritePaths...),
			ExpectedEvidence: append([]engineeringplan.Evidence(nil), task.ExpectedEvidence...),
		},
	}, nil
}

// prepareIsolatedGraphWriterProposal validates one real child-bound writer
// response and captures a child-side proposal for later parent integration.
func prepareIsolatedGraphWriterProposal(ctx context.Context, path, taskID string, invocation runtime.Invocation, result runtime.Result) (out IsolatedGraphWriterProposal, err error) {
	s, err := Inspect(path)
	if err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	var binding isolatedWriterBinding
	binding, err = isolatedWriterBindingForSnapshot(s, taskID)
	if err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	expected, err := writerInvocationForIsolatedTask(s, binding)
	resolved, resolveErr := resolveScheduledRecordedInvocation(s, expected, invocation)
	if err != nil || resolveErr != nil || resolved != invocation {
		return IsolatedGraphWriterProposal{}, errors.Join(errors.New("isolated writer invocation is stale or substituted"), err, resolveErr)
	}
	if err := runtime.ValidateResult(invocation, result, false); err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	if err := requireOpenCodeRoleReceipt(s, invocation, result); err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	lease, err := worktree.AcquireRead(binding.Workspace.Request)
	if err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	childCurrent, _, err := worktree.Capture(ctx, binding.Workspace)
	if err != nil || childCurrent != binding.Candidate {
		return IsolatedGraphWriterProposal{}, errors.Join(errors.New("isolated child candidate changed before proposal validation"), err)
	}
	childID, err := binding.Candidate.ID()
	if err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	var changes []fileeffects.Change
	var preimages []WriterEditPreimage
	if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
		anchored, decodeErr := decodeAnchoredProposal(result.Output)
		if decodeErr != nil {
			return IsolatedGraphWriterProposal{}, decodeErr
		}
		if anchored.CandidateID != childID {
			return IsolatedGraphWriterProposal{}, errors.New("isolated writer candidate substitution")
		}
		manifest, captured, readErr := readIsolatedAnchoredPreimages(ctx, binding, anchored)
		if readErr != nil {
			return IsolatedGraphWriterProposal{}, readErr
		}
		changes, err = composeAnchoredProposal(anchored, manifest, captured)
		preimages = captured
	} else {
		proposal, decodeErr := decodeWriterProposal(s.Creation.Config.WriterContract, result.Output)
		if decodeErr != nil {
			return IsolatedGraphWriterProposal{}, decodeErr
		}
		if proposal.CandidateID != childID {
			return IsolatedGraphWriterProposal{}, errors.New("isolated writer candidate substitution")
		}
		changes = proposal.Changes
	}
	if err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	if !graphWriterChangesWithinTask(binding.Task.toEngineeringTask(), changes) {
		return IsolatedGraphWriterProposal{}, &graphWriterScopeViolationError{Changes: append([]fileeffects.Change(nil), changes...)}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	childProposal, err := fileeffects.Prepare(ctx, binding.Workspace, hex.EncodeToString(nonce[:]), changes)
	if err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	if err := fileeffects.Preflight(ctx, binding.Workspace, childProposal); err != nil {
		return IsolatedGraphWriterProposal{}, err
	}
	if childProposal.Before != binding.Candidate {
		return IsolatedGraphWriterProposal{}, errors.New("isolated writer child candidate changed during proposal preparation")
	}
	childAfter, err := worktree.Fingerprint(ctx, binding.Workspace)
	if err != nil || childAfter != binding.Candidate {
		return IsolatedGraphWriterProposal{}, errors.Join(errors.New("isolated child candidate changed during proposal preparation"), err)
	}
	return IsolatedGraphWriterProposal{
		Version: 1, TaskID: taskID, IsolationID: binding.IsolationID,
		Invocation: invocation, Result: result, Workspace: binding.Workspace,
		Candidate: binding.Candidate, PreparedChild: childProposal,
		Changes:       append([]fileeffects.Change(nil), childProposal.Changes...),
		EditPreimages: preimages,
	}, nil
}

// validateIsolatedGraphWriterProposal validates persisted child-side evidence
// from journal state only; it performs no filesystem reads and authorizes no
// parent mutation.
func validateIsolatedGraphWriterProposal(s Snapshot, proposal IsolatedGraphWriterProposal) error {
	if proposal.Version != 1 || proposal.TaskID == "" {
		return errors.New("invalid isolated writer proposal version or task")
	}
	var binding isolatedWriterBinding
	binding, err := isolatedWriterBindingForSnapshot(s, proposal.TaskID)
	if err != nil {
		return err
	}
	expected, err := writerInvocationForIsolatedTask(s, binding)
	resolved, resolveErr := resolveScheduledRecordedInvocation(s, expected, proposal.Invocation)
	if err != nil || resolveErr != nil || resolved != proposal.Invocation {
		return errors.Join(errors.New("isolated writer proposal invocation mismatch"), err, resolveErr)
	}
	if proposal.IsolationID != binding.IsolationID || proposal.Workspace != binding.Workspace || proposal.Candidate != binding.Candidate {
		return errors.New("isolated writer proposal child binding mismatch")
	}
	if err := runtime.ValidateResult(proposal.Invocation, proposal.Result, false); err != nil {
		return err
	}
	if err := proposal.PreparedChild.AdmitHost(); err != nil {
		return err
	}
	if proposal.PreparedChild.Before != binding.Candidate || !sameCanonical(proposal.PreparedChild.Changes, proposal.Changes) {
		return errors.New("isolated writer child proposal preimage mismatch")
	}
	if !graphWriterChangesWithinTask(binding.Task.toEngineeringTask(), proposal.Changes) {
		return errors.New("isolated writer proposal exceeds declared task write paths")
	}
	childID, err := binding.Candidate.ID()
	if err != nil {
		return err
	}
	var expectedChanges []fileeffects.Change
	if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
		anchored, err := decodeAnchoredProposal(proposal.Result.Output)
		if err != nil {
			return err
		}
		if anchored.CandidateID != childID {
			return errors.New("isolated anchored proposal candidate mismatch")
		}
		expectedChanges, err = composeAnchoredProposal(anchored, proposal.PreparedChild.BeforeFiles, proposal.EditPreimages)
		if err != nil {
			return err
		}
	} else {
		decoded, err := decodeWriterProposal(s.Creation.Config.WriterContract, proposal.Result.Output)
		if err != nil {
			return err
		}
		if decoded.CandidateID != childID {
			return errors.New("isolated proposal candidate mismatch")
		}
		expectedChanges = decoded.Changes
	}
	if !sameCanonical(expectedChanges, proposal.Changes) {
		return errors.New("isolated proposal changes differ from validated result")
	}
	return nil
}

func (c writerImplementationContext) toEngineeringTask() engineeringplan.Task {
	return engineeringplan.Task{ID: c.ID, Kind: engineeringplan.Implementation, Title: c.Title, ScopePaths: c.ScopePaths, WritePaths: c.WritePaths, ExpectedEvidence: c.ExpectedEvidence}
}

func writerLexicalContext(s Snapshot, isolated bool) (*roleLexicalContext, error) {
	lexical, err := roleLexical(s)
	if err != nil || lexical == nil || !isolated || lexical.Scope != "candidate" {
		return lexical, err
	}
	// Candidate overlays are bound to the parent candidate ID. An isolated
	// child has a distinct candidate ID even when its bytes are pristine-equal,
	// so this mode intentionally exposes only the confirmed base index.
	baseOnly := *lexical
	baseOnly.OverlayID = ""
	baseOnly.Scope = "base_commit"
	baseOnly.Instruction = "ri_search searches exact bytes in this fixed lexical scope. Results carry blob identities and byte ranges; lexical absence is not semantic absence. Base scope does not include candidate edits."
	return &baseOnly, nil
}

func readIsolatedAnchoredPreimages(ctx context.Context, binding isolatedWriterBinding, proposal writercontract.AnchoredProposal) ([]worktree.FileState, []WriterEditPreimage, error) {
	observed, manifest, err := worktree.Capture(ctx, binding.Workspace)
	if err != nil || observed != binding.Candidate {
		return nil, nil, errors.Join(errors.New("isolated anchored candidate differs from its receipt"), err)
	}
	candidateID, err := binding.Candidate.ID()
	if err != nil {
		return nil, nil, err
	}
	byPath := make(map[string]worktree.FileState, len(manifest))
	for _, state := range manifest {
		byPath[state.Path] = state
	}
	preimages := make([]WriterEditPreimage, 0, len(proposal.Changes))
	preimageBytes := 0
	for _, change := range proposal.Changes {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := safepath.Writable(change.Path); err != nil {
			return nil, nil, err
		}
		state, exists := byPath[change.Path]
		if change.BeforeHash == nil {
			if exists {
				return nil, nil, errors.New("new isolated anchored file already exists")
			}
			continue
		}
		if !exists || state.Hash != *change.BeforeHash {
			return nil, nil, errors.New("isolated anchored before_hash differs from child manifest")
		}
		var content []byte
		var offset int64
		var size int64 = -1
		for {
			chunk, readErr := worktree.ReadSource(ctx, binding.Workspace, binding.Candidate, change.Path, offset, 32<<10)
			if readErr != nil {
				return nil, nil, readErr
			}
			if chunk.CandidateID != candidateID || chunk.Path != change.Path || chunk.SHA256 != state.Hash || chunk.Offset != offset || size >= 0 && chunk.Size != size {
				return nil, nil, errors.New("isolated anchored read binding mismatch")
			}
			size = chunk.Size
			if size > anchoredit.MaxSourceBytes {
				return nil, nil, errors.New("isolated anchored preimage exceeds per-file bound")
			}
			part, decodeErr := base64.StdEncoding.DecodeString(chunk.ContentBase64)
			if decodeErr != nil || chunk.ContentUTF8 != nil && string(part) != *chunk.ContentUTF8 {
				return nil, nil, errors.New("isolated anchored read has invalid content encoding")
			}
			content = append(content, part...)
			if len(content) > anchoredit.MaxSourceBytes || preimageBytes+len(content) > anchoredEditPreimagesMaxBytes {
				return nil, nil, errors.New("isolated anchored preimages exceed byte bound")
			}
			if chunk.NextOffset == nil {
				break
			}
			if *chunk.NextOffset <= offset {
				return nil, nil, errors.New("isolated anchored read did not advance")
			}
			offset = *chunk.NextOffset
		}
		if int64(len(content)) != size || !utf8.Valid(content) {
			return nil, nil, errors.New("isolated anchored preimage incomplete or not UTF-8")
		}
		preimageBytes += len(content)
		preimages = append(preimages, WriterEditPreimage{Path: change.Path, ContentUTF8: string(content)})
	}
	after, err := worktree.Fingerprint(ctx, binding.Workspace)
	if err != nil || after != binding.Candidate {
		return nil, nil, errors.Join(errors.New("isolated anchored candidate drift"), err)
	}
	sort.Slice(preimages, func(i, j int) bool { return preimages[i].Path < preimages[j].Path })
	return manifest, preimages, nil
}
