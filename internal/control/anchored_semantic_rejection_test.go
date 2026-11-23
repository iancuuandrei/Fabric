package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

func TestAnchoredContentCorrectionCapturesExactPreimagesAndReplaysWithoutFilesystem(t *testing.T) {
	path, _, candidateID, beforeHash := anchoredWriterFixture(t)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Creation.Execution = &ExecutionPolicy{GraphVersion: 1, SemanticCorrectionVersion: 1}
	s.Graph = scopeReplanFixture(t).Graph
	s.Graph.PlanID = s.PlanID
	for index := range s.Graph.Graph.Tasks {
		s.Graph.Graph.Tasks[index].ScopePaths = []string{"file.txt"}
		if s.Graph.Graph.Tasks[index].Kind == engineeringplan.Implementation {
			s.Graph.Graph.Tasks[index].WritePaths = []string{"file.txt"}
		}
	}
	s.Graph.Digest, err = engineeringplan.Digest(s.Graph.Graph)
	if err != nil {
		t.Fatal(err)
	}
	s.Creation.Config.Writer.Runtime = "codex-app-server"
	s.Creation.Config.Writer.Provider = "openai"
	invocation, err := writerInvocation(s)
	if err != nil {
		t.Fatal(err)
	}
	proposal := writercontract.AnchoredProposal{CandidateID: candidateID, Changes: []writercontract.AnchoredChange{{
		Path: "file.txt", BeforeHash: &beforeHash,
		Edits: []writercontract.AnchoredEdit{{Before: "missing-anchor", After: "hello"}},
	}}}
	result := anchoredWriterResultForProposal(t, invocation, proposal)
	model := invocation.Profile.Model
	result.ObservedModel = &model
	hash, err := canonical.Hash("harness.writer-result.v1", result)
	if err != nil {
		t.Fatal(err)
	}
	s.WriterHost = &WriterHostState{Intent: WriterHostIntent{Invocation: invocation}, RuntimeReceipt: &WriterRuntimeReceipt{InvocationID: invocation.ID, ResultHash: hash, JournalHead: strings.Repeat("a", 64)}}
	proof, err := captureAnchoredSemanticRejection(context.Background(), path, s, "", result)
	if err != nil {
		t.Fatal(err)
	}
	if proof == nil || len(proof.Preimages) != 1 || proof.Preimages[0].ContentUTF8 != "base\n" {
		t.Fatal("anchor correction lost exact source evidence")
	}
	correction, err := expectedRoleSemanticCorrection(s, invocation, result, proof)
	if err != nil {
		t.Fatal(err)
	}
	if correction.Invocation.ID == invocation.ID || correction.AnchoredRejection == nil || !strings.Contains(correction.Rejection, "exactly once") {
		t.Fatal("anchor correction lost new identity or exact validation diagnostic")
	}
	payload, err := canonical.Bytes(correction)
	if err != nil {
		t.Fatal(err)
	}
	// Pure controller replay consumes only the captured evidence, no workspace
	// reads or provider calls. The live capture above remains a separate check.
	if err := replayRoleSemanticCorrection(&s, journal.Event{Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(s.RoleCorrections) != 1 || s.FileIntent != nil {
		t.Fatal("anchor correction mutated the candidate or lost its receipt")
	}
}

func TestValidAnchoredProposalDoesNotConsumeSemanticCorrection(t *testing.T) {
	path, _, candidateID, beforeHash := anchoredWriterFixture(t)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := writerInvocation(s)
	if err != nil {
		t.Fatal(err)
	}
	proposal := writercontract.AnchoredProposal{CandidateID: candidateID, Changes: []writercontract.AnchoredChange{{Path: "file.txt", BeforeHash: &beforeHash, Edits: []writercontract.AnchoredEdit{{Before: "base", After: "updated"}}}}}
	result := anchoredWriterResultForProposal(t, invocation, proposal)
	proof, err := captureAnchoredSemanticRejection(context.Background(), path, s, "", result)
	if err != nil || proof != nil {
		t.Fatalf("valid output was classified as a semantic rejection: proof=%v err=%v", proof, err)
	}
}

func TestAnchoredContentFailureCannotMaskLaterIdentityOrUnusedPreimage(t *testing.T) {
	path, _, candidateID, beforeHash := anchoredWriterFixture(t)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := worktree.Capture(context.Background(), *s.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	proposal := writercontract.AnchoredProposal{CandidateID: candidateID, Changes: []writercontract.AnchoredChange{{Path: "file.txt", BeforeHash: &beforeHash, Edits: []writercontract.AnchoredEdit{{Before: "missing", After: "new"}}}}}
	preimages := []WriterEditPreimage{{Path: "file.txt", ContentUTF8: "base\n"}}
	if _, err := composeAnchoredProposal(proposal, manifest, preimages); !semanticOutputFailureOnly(err) {
		t.Fatal("content-only anchor rejection was not typed", err)
	}
	badHash := strings.Repeat("f", 64)
	proposal.Changes = append(proposal.Changes, writercontract.AnchoredChange{Path: "z.txt", BeforeHash: &badHash, Edits: []writercontract.AnchoredEdit{{Before: "x", After: "y"}}})
	if _, err := composeAnchoredProposal(proposal, manifest, preimages); errors.Is(err, ErrSemanticOutputInvalid) || err == nil {
		t.Fatal("early content failure hid a later candidate identity failure", err)
	}
	proposal.Changes = proposal.Changes[:1]
	preimages = append(preimages, WriterEditPreimage{Path: "unused.txt", ContentUTF8: "unused"})
	if _, err := composeAnchoredProposal(proposal, manifest, preimages); errors.Is(err, ErrSemanticOutputInvalid) || err == nil {
		t.Fatal("content failure hid an unrelated source preimage", err)
	}
	proof := &AnchoredSemanticRejection{Version: 1, Candidate: *s.Candidate, Manifest: manifest, Preimages: preimages[:1]}
	proof.Preimages[0].ContentUTF8 = "substituted\n"
	if _, err := validateAnchoredSemanticRejection(s, "", proposal, proof); !errors.Is(err, ErrAutonomousUnsafe) {
		t.Fatal("substituted preimage granted an anchor correction", err)
	}
}

func TestAnchoredContentCaptureLeasesExactIsolatedChild(t *testing.T) {
	c := isolatedWriterCreation(t)
	path, s := isolatedWriterGraphFixture(t, c)
	authorizeIsolatedGraphCohortForTest(t, path, s)
	s, _ = isolateAndAdmitWriterTaskForTest(t, path, "one")
	invocation, err := writerInvocationForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := isolatedWriterBindingForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	childID, err := binding.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := worktree.Capture(context.Background(), binding.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := ""
	for _, file := range manifest {
		if file.Path == "file.txt" {
			beforeHash = file.Hash
		}
	}
	proposal := writercontract.AnchoredProposal{CandidateID: childID, Changes: []writercontract.AnchoredChange{{Path: "file.txt", BeforeHash: &beforeHash, Edits: []writercontract.AnchoredEdit{{Before: "missing-anchor", After: "new"}}}}}
	result := anchoredWriterResultForProposal(t, invocation, proposal)
	lease, err := worktree.Acquire(binding.Workspace.Request)
	if err != nil {
		t.Fatal(err)
	}
	proof, captureErr := captureAnchoredSemanticRejection(context.Background(), path, s, "one", result)
	closeErr := lease.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if proof != nil || !errors.Is(captureErr, worktree.ErrLeaseContention) {
		t.Fatal("isolated capture read child bytes outside the child lease", captureErr)
	}
	proof, err = captureAnchoredSemanticRejection(context.Background(), path, s, "one", result)
	if err != nil || proof == nil || proof.Candidate != binding.Candidate || proof.Candidate == *s.Candidate {
		t.Fatal("isolated capture lost its exact child candidate", err)
	}
}
