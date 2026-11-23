package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/candidatetools"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

func anchoredWriterFixture(t *testing.T) (string, runtime.Invocation, string, string) {
	t.Helper()
	path, invocation := writerInvocationFixtureForContract(t, writercontract.ContractAnchoredEditsV1)
	return anchoredWriterCandidate(t, path, invocation)
}

func anchoredWriterCandidate(t *testing.T, path string, invocation runtime.Invocation) (string, runtime.Invocation, string, string) {
	t.Helper()
	s, err := Inspect(path)
	if err != nil || s.Workspace == nil || s.Candidate == nil {
		t.Fatal("confirmed candidate unavailable", err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := worktree.Capture(context.Background(), *s.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range manifest {
		if file.Path == "file.txt" {
			return path, invocation, candidateID, file.Hash
		}
	}
	t.Fatal("fixture file.txt missing from candidate")
	return "", runtime.Invocation{}, "", ""
}

func anchoredWriterV2Fixture(t *testing.T) (string, runtime.Invocation, string, string) {
	return anchoredWriterValidatedFixture(t, writercontract.ContractAnchoredEditsV2)
}

func anchoredWriterV3Fixture(t *testing.T) (string, runtime.Invocation, string, string) {
	return anchoredWriterValidatedFixture(t, writercontract.ContractAnchoredEditsV3)
}

func anchoredWriterValidatedFixture(t *testing.T, contract string) (string, runtime.Invocation, string, string) {
	t.Helper()
	c := creation(t)
	c.Config.WriterContract = contract
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableHash, err := fixtureFileHash(executable)
	if err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(auth, []byte(`{"tokens":{"access_token":"fixture-token","account_id":"fixture-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c.Config.Codex = &config.Codex{Executable: executable, ExecutableHash: executableHash, StateRoot: filepath.Join(t.TempDir(), "codex-state"), AuthSource: auth}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	c.Config.Writer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "writer-fixture", Effort: "high", Role: "writer"}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	invocation, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	return anchoredWriterCandidate(t, path, invocation)
}

func anchoredWriterResult(t *testing.T, invocation runtime.Invocation, candidateID, beforeHash string) runtime.Result {
	t.Helper()
	proposal := writercontract.AnchoredProposal{
		CandidateID: candidateID,
		Changes: []writercontract.AnchoredChange{{
			Path: "file.txt", BeforeHash: &beforeHash,
			Edits:          []writercontract.AnchoredEdit{{Before: "base", After: "hello"}},
			NewContentUTF8: nil, Executable: false,
		}},
	}
	return anchoredWriterResultForProposal(t, invocation, proposal)
}

func anchoredWriterResultForProposal(t *testing.T, invocation runtime.Invocation, proposal writercontract.AnchoredProposal) runtime.Result {
	t.Helper()
	output, err := canonical.Bytes(proposal)
	if err != nil {
		t.Fatal(err)
	}
	return runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, Output: string(output)}
}

func TestAnchoredWriterComposesAndReplaysExactCandidatePreimage(t *testing.T) {
	path, invocation, candidateID, beforeHash := anchoredWriterFixture(t)
	result := anchoredWriterResult(t, invocation, candidateID, beforeHash)
	record, err := RecordWriterProposal(context.Background(), path, invocation, result)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.EditPreimages) != 1 || record.EditPreimages[0].Path != "file.txt" || record.EditPreimages[0].ContentUTF8 != "base\n" {
		t.Fatalf("unexpected replay evidence: %+v", record.EditPreimages)
	}
	if len(record.Prepared.Proposal.Changes) != 1 {
		t.Fatalf("unexpected prepared changes: %+v", record.Prepared.Proposal.Changes)
	}
	content, err := base64.StdEncoding.DecodeString(*record.Prepared.Proposal.Changes[0].ContentBase64)
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("untouched bytes were not preserved: %q %v", content, err)
	}
	inspected, err := Inspect(path)
	if err != nil || inspected.WriterProposal == nil {
		t.Fatalf("anchored proposal did not replay: state=%+v err=%v", inspected, err)
	}
	if inspected.WriterProposal.Result.Output != result.Output || inspected.WriterProposal.EditPreimages[0].ContentUTF8 != "base\n" {
		t.Fatal("replay rewrote observed output or preimage evidence")
	}
}

func TestAnchoredWriterReplayRejectsCorruptedPreimage(t *testing.T) {
	path, invocation, candidateID, beforeHash := anchoredWriterFixture(t)
	result := anchoredWriterResult(t, invocation, candidateID, beforeHash)
	prepared, preimages, err := prepareWriterFiles(context.Background(), path, invocation, result)
	if err != nil {
		t.Fatal(err)
	}
	preimages[0].ContentUTF8 = "foreign\n"
	if err := Append(path, "writer.proposed", WriterRecord{Invocation: invocation, Result: result, Prepared: prepared, EditPreimages: preimages}); err == nil {
		t.Fatal("preimage not matching candidate hash was admitted")
	}
}

func TestAnchoredWriterReadsUnicodeAcrossCandidateReadPages(t *testing.T) {
	path, _, _, _ := anchoredWriterFixture(t)
	s, err := Inspect(path)
	if err != nil || s.Workspace == nil {
		t.Fatal("workspace unavailable", err)
	}
	_, manifest, err := worktree.Capture(context.Background(), *s.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	var beforeHash string
	for _, file := range manifest {
		if file.Path == "file.txt" {
			beforeHash = file.Hash
		}
	}
	large := strings.Repeat("a", 32767) + "ă" + strings.Repeat("b", 32760) + "TAIL\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(large))
	change := fileeffects.Change{Path: "file.txt", BeforeHash: &beforeHash, ContentBase64: &encoded}
	prepared, err := PrepareFiles(context.Background(), path, []fileeffects.Change{change})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFiles(context.Background(), path, prepared, authorize(t, prepared)); err != nil {
		t.Fatal(err)
	}
	invocation, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(path)
	if err != nil || s.Candidate == nil {
		t.Fatal("updated candidate unavailable", err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err = worktree.Capture(context.Background(), *s.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range manifest {
		if file.Path == "file.txt" {
			beforeHash = file.Hash
		}
	}
	proposal := writercontract.AnchoredProposal{CandidateID: candidateID, Changes: []writercontract.AnchoredChange{{
		Path: "file.txt", BeforeHash: &beforeHash,
		Edits: []writercontract.AnchoredEdit{{Before: "TAIL", After: "DONE"}}, Executable: false,
	}}}
	result := anchoredWriterResultForProposal(t, invocation, proposal)
	record, err := RecordWriterProposal(context.Background(), path, invocation, result)
	if err != nil {
		t.Fatal("split Unicode source page rejected", err)
	}
	if len(record.EditPreimages) != 1 || record.EditPreimages[0].ContentUTF8 != large {
		t.Fatal("multi-page preimage did not preserve exact original UTF-8 bytes")
	}
}

func TestAnchoredWriterRejectsNullAsExistingFileDeletion(t *testing.T) {
	path, invocation, candidateID, beforeHash := anchoredWriterFixture(t)
	proposal := writercontract.AnchoredProposal{CandidateID: candidateID, Changes: []writercontract.AnchoredChange{{
		Path: "file.txt", BeforeHash: &beforeHash, Edits: nil, NewContentUTF8: nil,
		Executable: false,
	}}}
	result := anchoredWriterResultForProposal(t, invocation, proposal)
	if _, err := PrepareWriterFiles(context.Background(), path, invocation, result); err == nil {
		t.Fatal("null content was interpreted as deletion")
	}
	for _, output := range []string{
		`{"candidate_id":"` + candidateID + `","changes":[{"path":"file.txt","before_hash":"` + beforeHash + `","edits":[{"before":"base","after":"hello"}],"new_content_utf8":null}]}`,
		`{"candidate_id":"` + candidateID + `","changes":[{"path":"file.txt","edits":[],"new_content_utf8":"replacement","executable":false}]}`,
		`{"candidate_id":"` + candidateID + `","changes":[{"path":"new.go","before_hash":null,"edits":null,"new_content_utf8":"package new\n","executable":false}]}`,
	} {
		result.Output = output
		if _, err := PrepareWriterFiles(context.Background(), path, invocation, result); err == nil {
			t.Fatal("omitted required nullable/boolean field admitted")
		}
	}
}

func TestAnchoredWriterInstructionReplacesFullFileDirections(t *testing.T) {
	_, invocation := writerInvocationFixtureForContract(t, writercontract.ContractAnchoredEditsV1)
	var input struct {
		Instruction string          `json:"instruction"`
		Schema      json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"localized edits", "controller composes", "Null never means delete", "before_hash"} {
		if !strings.Contains(input.Instruction, text) {
			t.Fatalf("anchored instruction omitted %q", text)
		}
	}
	for _, forbidden := range []string{"complete-file replacement", "content_base64", "entire current candidate file", "Complete source files", "candidate_validate_anchored_edits", "16 KiB tool-request envelope"} {
		if strings.Contains(input.Instruction, forbidden) {
			t.Fatalf("anchored instruction retained conflicting direction %q", forbidden)
		}
	}
	if !strings.Contains(string(input.Schema), `"edits"`) || strings.Contains(string(input.Schema), `"content_base64"`) {
		t.Fatal("anchored invocation has the wrong wire schema")
	}
}

func TestAnchoredEditsV2InstructionRequiresSameTurnValidation(t *testing.T) {
	_, invocation, candidateID, _ := anchoredWriterV2Fixture(t)
	var input struct {
		Instruction string          `json:"instruction"`
		Schema      json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"candidate_validate_anchored_edits", "valid=false", "candidate_read", "within this same turn", "16 KiB tool-request envelope"} {
		if !strings.Contains(input.Instruction, required) {
			t.Fatalf("v2 instruction omitted %q", required)
		}
	}
	wantSchema, err := writercontract.AnchoredEditsSchemaForCandidate(candidateID)
	if err != nil {
		t.Fatal(err)
	}
	actualCanonical, actualErr := canonical.Normalize(input.Schema)
	wantCanonical, wantErr := canonical.Normalize(wantSchema)
	if actualErr != nil || wantErr != nil || string(actualCanonical) != string(wantCanonical) {
		t.Fatal("v2 changed the frozen anchored-edits output schema")
	}
}

func TestAnchoredEditsV3InstructionAndSchemaExcludeNoOpExistingFiles(t *testing.T) {
	_, invocation, _, _ := anchoredWriterV3Fixture(t)
	var input struct {
		Instruction string          `json:"instruction"`
		Schema      json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"candidate_validate_anchored_edits", "Include only files with a real change", "omit an unchanged file entirely"} {
		if !strings.Contains(input.Instruction, required) {
			t.Fatalf("v3 instruction omitted %q", required)
		}
	}
	if !strings.Contains(string(input.Schema), `"anyOf"`) || !strings.Contains(string(input.Schema), `"minItems":1`) {
		t.Fatal("v3 strict schema did not retain the existing-file edit minimum")
	}
}

func TestAnchoredEditsV2ControllerRejectsInvalidAnchorWhenToolIsIgnored(t *testing.T) {
	path, invocation, candidateID, beforeHash := anchoredWriterV2Fixture(t)
	result := anchoredWriterResult(t, invocation, candidateID, beforeHash)
	prepared, err := PrepareWriterFiles(context.Background(), path, invocation, result)
	if err != nil || len(prepared.Proposal.Changes) != 1 || prepared.Proposal.Changes[0].Path != "file.txt" {
		t.Fatalf("valid v2 proposal did not compose to one file effect: %+v %v", prepared, err)
	}
	content, err := base64.StdEncoding.DecodeString(*prepared.Proposal.Changes[0].ContentBase64)
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("valid v2 proposal did not preserve exact controller composition: %q %v", content, err)
	}

	result = anchoredWriterResult(t, invocation, candidateID, beforeHash)
	result.Output = strings.Replace(result.Output, `"before":"base"`, `"before":"not present"`, 1)
	if _, err := PrepareWriterFiles(context.Background(), path, invocation, result); err == nil || !strings.Contains(err.Error(), "anchored edit") {
		t.Fatalf("controller admitted invalid v2 proposal when model omitted validation: %v", err)
	}
	if countJournalKind(t, path, "files.intent") != 0 {
		t.Fatal("invalid anchored proposal created a file effect intent")
	}
}

func TestAnchoredEditsV2CorrectsInvalidAnchorWithinWriterTurnAndAppliesOneFile(t *testing.T) {
	path, _, _, _ := anchoredWriterV2Fixture(t)
	record, err := RunWriter(context.Background(), path)
	if err != nil {
		t.Fatal("local Codex app-server anchored writer failed", err)
	}
	s, err := Inspect(path)
	if err != nil || s.WriterHost == nil || s.WriterHost.RuntimeReceipt == nil || s.WriterProposal == nil {
		t.Fatal("v2 writer runtime/proposal evidence missing", err)
	}
	runtimePath := filepath.Join(s.WriterHost.Intent.Launch.Root, "writer.jsonl")
	runtimeState, err := codexruntime.Inspect(runtimePath)
	if err != nil || runtimeState.Candidate == nil || runtimeState.Candidate.AnchorValidationVersion != 1 || runtimeState.Thread == nil || runtimeState.TurnID != "turn-v2" || runtimeState.TurnStatus != "completed" || runtimeState.Result == nil {
		t.Fatal("v2 runtime did not retain opted-in same-turn evidence", err, runtimeState)
	}
	events, err := journal.Read(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	var requests []codexruntime.ToolRequest
	for _, event := range events {
		if event.Kind != "runtime.tool-request" {
			continue
		}
		var request codexruntime.ToolRequest
		if err := canonical.Decode(event.Payload, &request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request)
	}
	if len(requests) != 3 || len(runtimeState.ToolResponses) != 3 {
		t.Fatalf("expected invalid validation, source read and corrected validation in one turn: requests=%+v responses=%+v", requests, runtimeState.ToolResponses)
	}
	wantTools := []string{"candidate_validate_anchored_edits", "candidate_read", "candidate_validate_anchored_edits"}
	for i, request := range requests {
		if request.Tool != wantTools[i] || request.TurnID != runtimeState.TurnID || request.ThreadID != runtimeState.Thread.ThreadID || runtimeState.ToolResponses[i].CallID != request.CallID || !runtimeState.ToolResponses[i].Success {
			t.Fatalf("tool sequence escaped exact completed SDK turn or response binding: request=%+v response=%+v", request, runtimeState.ToolResponses[i])
		}
	}
	var invalid, corrected candidatetools.AnchoredEditValidation
	if err := canonical.Decode([]byte(runtimeState.ToolResponses[0].Content), &invalid); err != nil || invalid.Valid {
		t.Fatalf("first anchor validation did not report valid=false: %+v %v", invalid, err)
	}
	if err := canonical.Decode([]byte(runtimeState.ToolResponses[2].Content), &corrected); err != nil || !corrected.Valid {
		t.Fatalf("corrected anchor validation did not report valid=true: %+v %v", corrected, err)
	}
	if record.Invocation != s.WriterProposal.Invocation || record.Result.Output != s.WriterProposal.Result.Output || len(record.Prepared.Proposal.Changes) != 1 || record.Prepared.Proposal.Changes[0].Path != "file.txt" {
		t.Fatal("final proposal was not admitted from the same validated writer invocation")
	}
	if _, err := ApplyFiles(context.Background(), path, record.Prepared, authorize(t, record.Prepared)); err != nil {
		t.Fatal("one-file anchored proposal was not applied", err)
	}
	content, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "file.txt"))
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("exact anchored file effect missing: %q %v", content, err)
	}
	if countJournalKind(t, path, "files.intent") != 1 || countJournalKind(t, path, "files.observed") != 1 {
		t.Fatal("anchored correction did not produce exactly one observed file effect")
	}
}
