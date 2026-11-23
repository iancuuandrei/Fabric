package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

var errGraphWriterScopeViolation = errors.New("graph writer proposal exceeds declared task write paths")

type graphWriterScopeViolationError struct {
	Changes []fileeffects.Change
}

// Error reports that a proposal escaped its exact graph-task write paths.
func (e *graphWriterScopeViolationError) Error() string { return errGraphWriterScopeViolation.Error() }

// Is matches the stable scope-violation sentinel for safe error classification.
func (e *graphWriterScopeViolationError) Is(target error) bool {
	return target == errGraphWriterScopeViolation
}

type graphWriterTaskContextKey struct{}

var graphWriterActiveMu sync.Mutex
var graphWriterActiveCalls = map[string]int{}

func graphWriterActivityKey(path, taskID string) string { return path + "\x00" + taskID }

func markGraphWriterActive(path, taskID string) func() {
	key := graphWriterActivityKey(path, taskID)
	graphWriterActiveMu.Lock()
	graphWriterActiveCalls[key]++
	graphWriterActiveMu.Unlock()
	return func() {
		graphWriterActiveMu.Lock()
		if graphWriterActiveCalls[key] <= 1 {
			delete(graphWriterActiveCalls, key)
		} else {
			graphWriterActiveCalls[key]--
		}
		graphWriterActiveMu.Unlock()
	}
}

func graphWriterCurrentlyActive(path, taskID string) bool {
	graphWriterActiveMu.Lock()
	defer graphWriterActiveMu.Unlock()
	return graphWriterActiveCalls[graphWriterActivityKey(path, taskID)] > 0
}

func autonomousGraphWriterDispatchBlocked(s Snapshot, path, taskID string) error {
	check := s
	if len(s.GraphWriterHosts) > 0 {
		check.GraphWriterHosts = make(map[string]WriterHostState, len(s.GraphWriterHosts))
		for id, host := range s.GraphWriterHosts {
			if id != taskID && graphWriterCurrentlyActive(path, id) {
				continue
			}
			check.GraphWriterHosts[id] = host
		}
	}
	return autonomousDispatchBlocked(check)
}

func withGraphWriterTask(ctx context.Context, taskID string) context.Context {
	return context.WithValue(ctx, graphWriterTaskContextKey{}, taskID)
}

func graphWriterTaskFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	taskID, _ := ctx.Value(graphWriterTaskContextKey{}).(string)
	return taskID
}

func graphWriterProjectedSnapshot(s Snapshot, taskID string) Snapshot {
	return graphWriterProjectedSnapshotWithHost(s, taskID, taskID)
}

func graphWriterProjectedSnapshotWithHost(s Snapshot, taskID, hostTaskID string) Snapshot {
	if hostTaskID != "" {
		s.WriterHost = graphWriterHostState(s, hostTaskID)
	}
	return s
}

func appendWriterHostTransition(path, taskID, kind string, payload any) error {
	if taskID == "" {
		return Append(path, kind, payload)
	}
	event := GraphWriterHostEvent{TaskID: taskID}
	graphKind := "graph.writer." + strings.TrimPrefix(kind, "writer.")
	switch kind {
	case "writer.host-intent", "writer.host-ready":
		intent, ok := payload.(WriterHostIntent)
		if !ok {
			return errors.New("graph writer host intent payload mismatch")
		}
		event.Intent = &intent
	case "writer.host-observed":
		receipt, ok := payload.(codexhost.Receipt)
		if !ok {
			return errors.New("graph writer host receipt payload mismatch")
		}
		event.HostReceipt = &receipt
	case "writer.runtime-observed":
		receipt, ok := payload.(WriterRuntimeReceipt)
		if !ok {
			return errors.New("graph writer runtime receipt payload mismatch")
		}
		event.RuntimeReceipt = &receipt
	default:
		return errors.New("unsupported writer host transition")
	}
	return appendGraphWriterHostEvent(path, graphKind, taskID, event)
}

// GraphWriterHostEvent attributes one isolated Codex host transition to a
// frozen implementation task. It never carries write authorization.
type GraphWriterHostEvent struct {
	TaskID         string                `json:"task_id"`
	Intent         *WriterHostIntent     `json:"intent,omitempty"`
	HostReceipt    *codexhost.Receipt    `json:"host_receipt,omitempty"`
	RuntimeReceipt *WriterRuntimeReceipt `json:"runtime_receipt,omitempty"`
}

// GraphWriterRecord retains one task's actual runtime result and independently
// prepared proposal against the common frozen candidate.
type GraphWriterRecord struct {
	TaskID   string                       `json:"task_id"`
	Writer   WriterRecord                 `json:"writer,omitempty"`
	Dispatch *GraphWriterDispatchTiming   `json:"dispatch,omitempty"`
	Isolated *IsolatedGraphWriterProposal `json:"isolated,omitempty"`
}

// MarshalJSON preserves the historical legacy writer record shape while
// omitting the zero legacy WriterRecord for isolated evidence. Struct values
// are not omitted by encoding/json's omitempty, and serializing that zero would
// fabricate a malformed PreparedFiles proposal beside the real child record.
func (r GraphWriterRecord) MarshalJSON() ([]byte, error) {
	if r.Isolated == nil {
		type legacy GraphWriterRecord
		return json.Marshal(legacy(r))
	}
	if !reflect.DeepEqual(r.Writer, WriterRecord{}) {
		return nil, errors.New("isolated graph writer record cannot contain a legacy writer payload")
	}
	type isolatedRecord struct {
		TaskID   string                       `json:"task_id"`
		Writer   *WriterRecord                `json:"writer,omitempty"`
		Dispatch *GraphWriterDispatchTiming   `json:"dispatch,omitempty"`
		Isolated *IsolatedGraphWriterProposal `json:"isolated,omitempty"`
	}
	return json.Marshal(isolatedRecord{TaskID: r.TaskID, Dispatch: r.Dispatch, Isolated: r.Isolated})
}

// UnmarshalJSON rejects mixed authority-bearing payloads even when the legacy
// writer is all-zero. Isolated evidence has its own binding and must not be
// accompanied by a second, ambiguous legacy writer representation.
func (r *GraphWriterRecord) UnmarshalJSON(raw []byte) error {
	type plain GraphWriterRecord
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		return err
	}
	if isolated, ok := shape["isolated"]; ok && !bytes.Equal(bytes.TrimSpace(isolated), []byte("null")) {
		if _, hasWriter := shape["writer"]; hasWriter {
			return errors.New("isolated graph writer record cannot contain a legacy writer payload")
		}
	}
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*r = GraphWriterRecord(decoded)
	return nil
}

// GraphWriterDispatchTiming records the controller wrapper interval for a
// completed graph writer. It does not count provider requests or alter the
// invocation identity; absent timing preserves historical records.
type GraphWriterDispatchTiming struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at"`
}

func (t *GraphWriterDispatchTiming) validate() error {
	if t == nil {
		return nil
	}
	if !strings.HasSuffix(t.StartedAt, "Z") || !strings.HasSuffix(t.EndedAt, "Z") {
		return errors.New("graph writer dispatch timing must be UTC")
	}
	startedAt, startErr := time.Parse(time.RFC3339Nano, t.StartedAt)
	endedAt, endErr := time.Parse(time.RFC3339Nano, t.EndedAt)
	if startErr != nil || endErr != nil || endedAt.Before(startedAt) {
		return errors.New("invalid graph writer dispatch timing")
	}
	return nil
}

func observedGraphWriterDispatchTiming(startedAt, endedAt time.Time) *GraphWriterDispatchTiming {
	timing := &GraphWriterDispatchTiming{StartedAt: startedAt.UTC().Format(time.RFC3339Nano), EndedAt: endedAt.UTC().Format(time.RFC3339Nano)}
	if timing.validate() != nil {
		return nil
	}
	return timing
}

// GraphWriterMember binds one aggregate member to its exact graph task and
// actual writer invocation.
type GraphWriterMember struct {
	TaskID           string `json:"task_id"`
	InvocationID     string `json:"invocation_id"`
	IsolationID      string `json:"isolation_id,omitempty"`
	ChildCandidateID string `json:"child_candidate_id,omitempty"`
}

// GraphWriterBatchRecord is a runner-owned deterministic composition of actual
// member proposals. It contains no fabricated model Result.
type GraphWriterBatchRecord struct {
	Version                int                 `json:"version"`
	GraphDigest            string              `json:"graph_digest"`
	Revision               int                 `json:"revision"`
	CandidateID            string              `json:"candidate_id"`
	Members                []GraphWriterMember `json:"members"`
	Prepared               PreparedFiles       `json:"prepared"`
	IsolationPreparationID string              `json:"isolation_preparation_id,omitempty"`
	ScopeReplanRequestID   string              `json:"scope_replan_request_id,omitempty"`
	CohortIndex            int                 `json:"cohort_index,omitempty"`
}

type graphWriterAggregateIdentity struct {
	Version                int                 `json:"version"`
	GraphDigest            string              `json:"graph_digest"`
	Revision               int                 `json:"revision"`
	CandidateID            string              `json:"candidate_id"`
	Members                []GraphWriterMember `json:"members"`
	IsolationPreparationID string              `json:"isolation_preparation_id,omitempty"`
	CohortIndex            int                 `json:"cohort_index,omitempty"`
}

func expectedWriterHostForTask(s Snapshot, taskID string) (WriterHostIntent, error) {
	if correction, ok := scheduledCorrectionForTask(s, taskID); ok {
		invocation, err := scheduledTurnInvocation(correction.Invocation, taskscheduler.OperationWriter, correction.ScheduledTaskID)
		if err != nil {
			return WriterHostIntent{}, err
		}
		return expectedWriterHostForInvocation(s, invocation)
	}
	i, err := writerInvocationForTask(s, taskID)
	if err != nil {
		return WriterHostIntent{}, err
	}
	return expectedWriterHostForInvocation(s, i)
}

func expectedWriterHostForInvocation(s Snapshot, i runtime.Invocation) (WriterHostIntent, error) {
	c := s.Creation.Config.Codex
	if i.Profile.Runtime != "codex-app-server" || c == nil {
		return WriterHostIntent{}, errors.New("configured Codex graph writer required")
	}
	rel, err := filepath.Rel(s.Creation.Repository.Root, c.StateRoot)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return WriterHostIntent{}, errors.New("writer host state must be outside source repository")
	}
	l, err := codexhost.Expected(filepath.Join(c.StateRoot, s.RunID, "writer-"+i.ID), c.Executable, c.ExecutableHash)
	return WriterHostIntent{i, l}, err
}

func graphWriterHostState(s Snapshot, taskID string) *WriterHostState {
	state, ok := s.GraphWriterHosts[taskID]
	if !ok {
		return nil
	}
	copy := state
	return &copy
}

func appendGraphWriterHostEvent(path, kind, taskID string, event GraphWriterHostEvent) error {
	if taskID == "" || event.TaskID != taskID {
		return errors.New("graph writer host task binding required")
	}
	return Append(path, kind, event)
}

func replayGraphWriterHost(s *Snapshot, e journal.Event) error {
	var event GraphWriterHostEvent
	if err := canonical.Decode(e.Payload, &event); err != nil {
		return err
	}
	if event.TaskID == "" {
		return errors.New("graph writer host task missing")
	}
	expected, err := expectedWriterHostForTask(*s, event.TaskID)
	if err != nil {
		return err
	}
	host, exists := s.GraphWriterHosts[event.TaskID]
	switch e.Kind {
	case "graph.writer.host-intent":
		if exists || event.Intent == nil || *event.Intent != expected || event.HostReceipt != nil || event.RuntimeReceipt != nil {
			return errors.New("graph writer host intent substitution or duplication")
		}
		host = WriterHostState{Intent: expected}
	case "graph.writer.host-ready":
		if !exists || host.Ready || host.Intent != expected || event.Intent == nil || *event.Intent != expected || event.HostReceipt != nil || event.RuntimeReceipt != nil {
			return errors.New("graph writer host preparation transition rejected")
		}
		host.Ready = true
	case "graph.writer.host-observed":
		if !exists || !host.Ready || host.Intent != expected || event.HostReceipt == nil || event.Intent != nil || event.RuntimeReceipt != nil {
			return errors.New("graph writer host observation transition rejected")
		}
		if err := validateCodexHostReceipt(*event.HostReceipt, expected.Launch, s.Creation.Config.Codex); err != nil {
			return err
		}
		host.Receipt = event.HostReceipt
	case "graph.writer.runtime-observed":
		if !exists || !host.Ready || host.Receipt == nil || host.Intent != expected || host.RuntimeReceipt != nil || event.RuntimeReceipt == nil || event.Intent != nil || event.HostReceipt != nil {
			return errors.New("graph writer runtime receipt transition rejected")
		}
		receipt := event.RuntimeReceipt
		if receipt.InvocationID != expected.Invocation.ID || strings.TrimSpace(receipt.ThreadID) == "" || len(receipt.ThreadID) > 256 || strings.TrimSpace(receipt.TurnID) == "" || len(receipt.TurnID) > 256 {
			return errors.New("graph writer runtime receipt identity mismatch")
		}
		for _, digest := range []string{receipt.JournalHead, receipt.ResultHash} {
			if err := safepath.RequireDigest(digest); err != nil {
				return err
			}
		}
		host.RuntimeReceipt = receipt
	default:
		return errors.New("unsupported graph writer host event")
	}
	if s.GraphWriterHosts == nil {
		s.GraphWriterHosts = make(map[string]WriterHostState)
	}
	s.GraphWriterHosts[event.TaskID] = host
	return nil
}

func replayGraphWriterProposal(s *Snapshot, e journal.Event, seen map[string]bool) error {
	var record GraphWriterRecord
	if err := canonical.Decode(e.Payload, &record); err != nil {
		return err
	}
	if record.Isolated != nil {
		return replayIsolatedGraphWriterProposal(s, record, seen)
	}
	if isolatedImplementationEnabled(*s) && s.State != "REPAIRING" {
		return errors.New("isolated graph writer proposal requires child isolation evidence")
	}
	if record.TaskID == "" || s.Graph == nil || s.GraphWriterResults[record.TaskID].TaskID != "" {
		return errors.New("graph writer proposal task missing or duplicated")
	}
	if err := record.Dispatch.validate(); err != nil {
		return err
	}
	task, ok := s.Graph.Graph.Task(record.TaskID)
	if !ok || task.Kind != "implementation" || task.Completed {
		return errors.New("graph writer proposal task is not an incomplete implementation")
	}
	if isolatedImplementationEnabled(*s) && (s.State != "REPAIRING" || task.ParentID == "") {
		return errors.New("isolated graph writer parent proposal is limited to a scoped repair task")
	}
	base, err := writerInvocationForTask(*s, record.TaskID)
	if err != nil {
		return err
	}
	i, err := resolveScheduledRecordedInvocation(*s, base, record.Writer.Invocation)
	if err != nil || i != record.Writer.Invocation {
		return errors.New("graph writer proposal invocation mismatch")
	}
	if err := runtime.ValidateResult(i, record.Writer.Result, true); err != nil {
		return err
	}
	host, ok := s.GraphWriterHosts[roleReceiptTaskID(*s, i, record.TaskID)]
	if i.Profile.Runtime != "codex-app-server" || !ok || host.Intent.Invocation != i || host.RuntimeReceipt == nil {
		return errors.New("graph writer proposal requires exact runtime receipt")
	}
	hash, err := canonical.Hash("harness.writer-result.v1", record.Writer.Result)
	if err != nil || hash != host.RuntimeReceipt.ResultHash {
		return errors.Join(errors.New("graph writer proposal differs from runtime result"), err)
	}
	if s.Candidate == nil {
		return errors.New("graph writer candidate unavailable")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return err
	}
	var reply WriterProposal
	if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
		anchored, err := decodeAnchoredProposal(record.Writer.Result.Output)
		if err != nil || anchored.CandidateID != candidateID {
			return errors.Join(errors.New("graph writer candidate mismatch"), err)
		}
		changes, err := composeAnchoredProposal(anchored, record.Writer.Prepared.Proposal.BeforeFiles, record.Writer.EditPreimages)
		if err != nil {
			return err
		}
		reply = WriterProposal{CandidateID: candidateID, Changes: changes}
	} else {
		if len(record.Writer.EditPreimages) != 0 {
			return errors.New("unexpected graph writer edit preimages")
		}
		reply, err = decodeWriterProposal(s.Creation.Config.WriterContract, record.Writer.Result.Output)
		if err != nil || reply.CandidateID != candidateID {
			return errors.Join(errors.New("graph writer candidate mismatch"), err)
		}
	}
	prepared, err := preparedFiles(*s, record.Writer.Prepared.Proposal)
	if err != nil {
		return err
	}
	a, err := canonical.Bytes(prepared)
	if err != nil {
		return err
	}
	b, err := canonical.Bytes(record.Writer.Prepared)
	if err != nil || string(a) != string(b) {
		return errors.Join(errors.New("graph writer prepared effect substitution"), err)
	}
	if !graphWriterChangesWithinTask(task, reply.Changes) {
		return errors.New("graph writer proposal exceeds its task write paths")
	}
	a, err = canonicalWriterChanges(reply.Changes)
	if err != nil {
		return err
	}
	b, err = canonicalWriterChanges(record.Writer.Prepared.Proposal.Changes)
	if err != nil || string(a) != string(b) {
		return errors.Join(errors.New("graph writer reply differs from prepared changes"), err)
	}
	key := "graph-writer-proposal:" + i.ID
	if seen[key] {
		return errors.New("duplicate graph writer proposal invocation")
	}
	seen[key] = true
	if s.GraphWriterResults == nil {
		s.GraphWriterResults = make(map[string]GraphWriterRecord)
	}
	s.GraphWriterResults[record.TaskID] = record
	return nil
}

func replayIsolatedGraphWriterProposal(s *Snapshot, record GraphWriterRecord, seen map[string]bool) error {
	if !isolatedImplementationEnabled(*s) || s.Graph == nil || record.TaskID == "" || record.Isolated.TaskID != record.TaskID || s.GraphWriterResults[record.TaskID].TaskID != "" || !sameCanonical(record.Writer, WriterRecord{}) {
		return errors.New("isolated graph writer proposal identity rejected")
	}
	if err := record.Dispatch.validate(); err != nil {
		return err
	}
	if err := validateIsolatedGraphWriterProposal(*s, *record.Isolated); err != nil {
		return err
	}
	invocation, err := writerInvocationForTask(*s, record.TaskID)
	if err != nil {
		return err
	}
	resolved, err := resolveScheduledRecordedInvocation(*s, invocation, record.Isolated.Invocation)
	if err != nil || resolved != record.Isolated.Invocation {
		return errors.Join(errors.New("isolated graph writer proposal invocation differs from schedule"), err)
	}
	if invocation.Profile.Runtime == "opencode-http" {
		if _, taskID, taskErr := isolatedWriterInvocationForReceiptID(*s, record.Isolated.Invocation.ID); taskErr != nil || taskID != record.TaskID {
			return errors.Join(errors.New("isolated OpenCode writer proposal task binding mismatch"), taskErr)
		}
		if err := requireOpenCodeRoleReceipt(*s, record.Isolated.Invocation, record.Isolated.Result); err != nil {
			return err
		}
		receipt, ok := s.ProviderRuntime[record.Isolated.Invocation.ID]
		if !ok || receipt.InvocationID != record.Isolated.Invocation.ID || receipt.Role != record.Isolated.Invocation.Profile.Role {
			return errors.New("isolated OpenCode writer proposal requires exact provider receipt")
		}
		if host, exists := s.GraphWriterHosts[roleReceiptTaskID(*s, record.Isolated.Invocation, record.TaskID)]; exists && host.RuntimeReceipt != nil {
			return errors.New("isolated OpenCode writer proposal cannot reuse a Codex runtime receipt")
		}
		resultHash, err := canonical.Hash("harness.writer-result.v1", record.Isolated.Result)
		if err != nil || resultHash != receipt.ResultHash {
			return errors.Join(errors.New("isolated OpenCode writer result differs from provider receipt"), err)
		}
	} else {
		host, ok := s.GraphWriterHosts[roleReceiptTaskID(*s, record.Isolated.Invocation, record.TaskID)]
		if !ok || host.Intent.Invocation != invocation || host.RuntimeReceipt == nil || invocation.Profile.Runtime != "codex-app-server" {
			return errors.New("isolated graph writer proposal requires exact runtime receipt")
		}
		resultHash, err := canonical.Hash("harness.writer-result.v1", record.Isolated.Result)
		if err != nil || resultHash != host.RuntimeReceipt.ResultHash {
			return errors.Join(errors.New("isolated graph writer result differs from runtime receipt"), err)
		}
	}
	key := "graph-writer-proposal:" + invocation.ID
	if seen[key] {
		return errors.New("duplicate graph writer proposal invocation")
	}
	seen[key] = true
	if s.GraphWriterResults == nil {
		s.GraphWriterResults = make(map[string]GraphWriterRecord)
	}
	s.GraphWriterResults[record.TaskID] = record
	return nil
}

func graphWriterChangesWithinTask(task engineeringplan.Task, changes []fileeffects.Change) bool {
	if len(changes) == 0 {
		return false
	}
	for _, change := range changes {
		admitted := false
		for _, writePath := range task.WritePaths {
			if change.Path == writePath || strings.HasPrefix(change.Path, writePath+"/") {
				admitted = true
				break
			}
		}
		if !admitted {
			return false
		}
	}
	return true
}

func replayGraphWriterBatch(s *Snapshot, e journal.Event) error {
	var batch GraphWriterBatchRecord
	if err := canonical.Decode(e.Payload, &batch); err != nil {
		return err
	}
	if batch.Version == 2 {
		if stagedIsolationEnabled(*s) {
			return errors.New("isolated writer aggregate requires non-staged policy")
		}
		return replayIsolatedGraphWriterBatch(s, batch)
	}
	if batch.Version == 3 {
		return replayScopeReplannedGraphWriterBatch(s, batch)
	}
	if batch.Version == 4 {
		return replayStagedBatch(s, batch)
	}
	if batch.Version != 1 || s.Graph == nil || s.Candidate == nil || s.GraphWriterBatch != nil || batch.GraphDigest != s.Graph.Digest || batch.Revision != s.Graph.Revision {
		return errors.New("graph writer batch identity or transition rejected")
	}
	if !parallelImplementationEnabled(*s) {
		return errors.New("graph writer batch requires the immutable parallel implementation policy")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil || candidateID != batch.CandidateID {
		return errors.Join(errors.New("graph writer batch candidate changed"), err)
	}
	if len(batch.Members) < 1 || len(batch.Members) > 2 {
		return errors.New("graph writer batch must contain one or two members")
	}
	memberIDs := make([]string, 0, len(batch.Members))
	seenTasks := map[string]bool{}
	for _, member := range batch.Members {
		if seenTasks[member.TaskID] {
			return errors.New("duplicate graph writer batch member")
		}
		seenTasks[member.TaskID] = true
		record, ok := s.GraphWriterResults[member.TaskID]
		if !ok || record.Writer.Invocation.ID != member.InvocationID || record.Writer.Prepared.Proposal.Before != *s.Candidate {
			return errors.New("graph writer batch member evidence mismatch")
		}
		memberIDs = append(memberIDs, member.TaskID)
	}
	expected, err := buildGraphWriterBatch(*s, memberIDs)
	if err != nil {
		return err
	}
	a, err := canonical.Bytes(expected)
	if err != nil {
		return err
	}
	b, err := canonical.Bytes(batch)
	if err != nil || string(a) != string(b) {
		return errors.Join(errors.New("graph writer aggregate differs from deterministic composition"), err)
	}
	s.GraphWriterBatch = &batch
	return nil
}

func replayIsolatedGraphWriterBatch(s *Snapshot, batch GraphWriterBatchRecord) error {
	if batch.Version != 2 || !isolatedImplementationEnabled(*s) || stagedIsolationEnabled(*s) || s.Graph == nil || s.Candidate == nil || s.GraphWriterBatch != nil || s.GraphIsolationPreparation == nil || (s.GraphIsolationPreparation.Version != 1 && s.GraphIsolationPreparation.Version != 2) || batch.GraphDigest != s.Graph.Digest || batch.Revision != s.Graph.Revision || batch.IsolationPreparationID != s.GraphIsolationPreparation.PreparationID {
		return errors.New("isolated writer aggregate identity or transition rejected")
	}
	ids, err := resolveWriterBatchMemberIDs(*s, batch, "isolated writer aggregate parent candidate changed", "isolated writer aggregate must contain one to eight members", "invalid or duplicate isolated writer aggregate member", "isolated writer aggregate child candidate mismatch", "isolated writer aggregate member evidence mismatch")
	if err != nil {
		return err
	}
	expected, err := expectedIsolatedGraphWriterBatch(*s, ids, batch.Prepared)
	if err != nil {
		return err
	}
	a, err := canonical.Bytes(expected)
	if err != nil {
		return err
	}
	b, err := canonical.Bytes(batch)
	if err != nil || string(a) != string(b) {
		return errors.Join(errors.New("isolated writer aggregate differs from validated child changes"), err)
	}
	s.GraphWriterBatch = &batch
	return nil
}

func validateIsolatedGraphWriterCohort(s Snapshot, tasks []engineeringplan.Task) error {
	if !isolatedImplementationEnabled(s) || s.State != "IMPLEMENTING" || s.Graph == nil || s.Candidate == nil || s.FileIntent != nil || s.Creation.Execution == nil {
		return errors.New("isolated graph writer requires a pristine initial graph candidate")
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return err
	}
	readyByID := map[string]engineeringplan.Task{}
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			readyByID[task.ID] = task
		}
	}
	allImpl := map[string]bool{}
	for _, task := range s.Graph.Graph.Tasks {
		if task.Kind != engineeringplan.Implementation {
			continue
		}
		if task.Completed {
			return errors.New("isolated initial cohort does not support previously completed implementations")
		}
		allImpl[task.ID] = true
	}
	if len(allImpl) == 0 || len(readyByID) != len(allImpl) || len(tasks) != len(allImpl) {
		return errors.New("isolated initial cohort requires every implementation to be ready together")
	}
	if isolatedWavesEnabled(s) {
		return validateIsolatedGraphWriterWavesCohort(s, tasks, readyByID, allImpl)
	}
	maxParallel := s.Creation.Execution.EffectiveMaxParallel()
	if len(allImpl) > maxParallel {
		return errors.New("isolated initial cohort exceeds max parallel; serial integration is unsupported")
	}
	for _, task := range tasks {
		readyTask, ok := readyByID[task.ID]
		if !ok || !reflect.DeepEqual(task, readyTask) {
			return errors.New("isolated implementation cohort differs from exact ready set")
		}
		delete(readyByID, task.ID)
	}
	if len(readyByID) != 0 {
		return errors.New("isolated implementation cohort is incomplete")
	}
	preparation, err := expectedGraphIsolationPreparation(s)
	if err != nil {
		return err
	}
	if preparation.Version != 1 {
		return errors.New("isolated initial cohort version differs from policy")
	}
	if len(preparation.SelectedTaskIDs) != len(tasks) {
		return errors.New("isolated initial cohort does not fit declared resource capacities")
	}
	if s.GraphIsolationPreparation != nil && !sameCanonical(*s.GraphIsolationPreparation, preparation) {
		return errors.New("recorded isolated preparation differs from the complete initial cohort")
	}
	if err := requireCohortTasksInSelection(tasks, preparation.SelectedTaskIDs, "resource-bounded initial cohort differs from ready implementations"); err != nil {
		return err
	}
	return requireCohortTasksDisjoint(tasks, "isolated initial implementation tasks are not independent and disjoint")
}

func validateIsolatedGraphWriterWavesCohort(s Snapshot, tasks []engineeringplan.Task, readyByID map[string]engineeringplan.Task, allImpl map[string]bool) error {
	if len(allImpl) == 0 || len(allImpl) > 8 {
		return errors.New("isolated wave cohort requires one to eight initial implementations")
	}
	remaining := map[string]engineeringplan.Task{}
	for id, task := range readyByID {
		remaining[id] = task
	}
	for _, task := range tasks {
		readyTask, ok := remaining[task.ID]
		if !ok || !reflect.DeepEqual(task, readyTask) {
			return errors.New("isolated implementation cohort differs from exact ready set")
		}
		delete(remaining, task.ID)
	}
	if len(remaining) != 0 {
		return errors.New("isolated implementation cohort is incomplete")
	}
	preparation, err := expectedGraphIsolationPreparation(s)
	if err != nil {
		return err
	}
	if preparation.Version != 2 {
		return errors.New("isolated wave cohort version differs from policy")
	}
	if len(preparation.SelectedTaskIDs) != len(tasks) || len(preparation.Waves) == 0 || len(preparation.WaveEstimated) != len(preparation.Waves) || len(preparation.WaveBlocked) != len(preparation.Waves) {
		return errors.New("isolated wave cohort does not cover all ready implementations")
	}
	if s.GraphIsolationPreparation != nil && !sameCanonical(*s.GraphIsolationPreparation, preparation) {
		return errors.New("recorded isolated preparation differs from the complete initial cohort")
	}
	if err := requireCohortTasksInSelection(tasks, preparation.SelectedTaskIDs, "resource-bounded initial cohort differs from ready implementations"); err != nil {
		return err
	}
	if err := validateIsolatedWavesPartition(preparation, tasks); err != nil {
		return err
	}
	return requireCohortTasksDisjoint(tasks, "isolated initial implementation tasks are not independent and disjoint")
}

func validateIsolatedWavesPartition(preparation GraphIsolationPreparation, tasks []engineeringplan.Task) error {
	if preparation.Version != 2 || len(preparation.Waves) == 0 {
		return errors.New("isolated waves are unavailable")
	}
	seen := map[string]bool{}
	for waveIndex, wave := range preparation.Waves {
		if len(wave) == 0 {
			return errors.New("isolated wave is empty")
		}
		for _, id := range wave {
			if id == "" || seen[id] {
				return errors.New("isolated wave membership is duplicated or empty")
			}
			seen[id] = true
			if !containsGraphIsolationTask(preparation.SelectedTaskIDs, id) {
				return errors.New("isolated wave task is outside the frozen cohort")
			}
		}
		_ = waveIndex
	}
	if len(seen) != len(tasks) || len(seen) != len(preparation.SelectedTaskIDs) {
		return errors.New("isolated waves do not cover the frozen cohort")
	}
	for _, task := range tasks {
		if !seen[task.ID] {
			return errors.New("isolated wave cohort differs from ready implementations")
		}
	}
	return nil
}

func isolatedGraphWriterIDs(s Snapshot, tasks []engineeringplan.Task) ([]string, error) {
	if err := validateIsolatedGraphWriterCohort(s, tasks); err != nil {
		return nil, err
	}
	ids := taskIDs(tasks)
	sort.Strings(ids)
	return ids, nil
}

// buildIsolatedGraphWriterBatch prepares one fresh effect against the parent
// candidate from validated child changes. Child proposal fingerprints remain
// child-side evidence and are never transferred to the parent.
func buildIsolatedGraphWriterBatch(ctx context.Context, path string, taskIDs []string) (GraphWriterBatchRecord, error) {
	s, err := Inspect(path)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	ids, err := isolatedGraphWriterIDsForResults(s, taskIDs)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	identity, members, changes, err := isolatedGraphWriterAggregateParts(s, ids)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	nonce, err := canonical.Hash("harness.graph-writer-isolated-aggregate.v1", identity)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	latest, prepared, candidateID, err := proposeAggregateParentEffect(ctx, path, s, identity.IsolationPreparationID, "isolated-graph-writers-"+nonce, "isolated aggregate parent or frozen cohort changed", "isolated aggregate preimage differs from current parent candidate", changes)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	return GraphWriterBatchRecord{Version: 2, GraphDigest: latest.Graph.Digest, Revision: latest.Graph.Revision, CandidateID: candidateID, Members: members, Prepared: prepared, IsolationPreparationID: identity.IsolationPreparationID}, nil
}

// expectedIsolatedGraphWriterBatch reconstructs a recorded parent proposal
// using its recorded parent preimage manifest and validated child changes.
// It performs no worktree reads and grants no write authority.
func expectedIsolatedGraphWriterBatch(s Snapshot, taskIDs []string, supplied PreparedFiles) (GraphWriterBatchRecord, error) {
	if s.Graph == nil || s.Candidate == nil || s.Workspace == nil || s.GraphIsolationPreparation == nil {
		return GraphWriterBatchRecord{}, errors.New("isolated aggregate state unavailable")
	}
	ids, err := isolatedGraphWriterIDsForResults(s, taskIDs)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	identity, members, changes, err := isolatedGraphWriterAggregateParts(s, ids)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	identityHash, err := canonical.Hash("harness.graph-writer-isolated-aggregate.v1", identity)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	expected, err := validateAggregateProposalEffect(s, supplied, changes, "isolated-graph-writers-"+identityHash, "isolated aggregate parent proposal identity mismatch", "isolated aggregate after-candidate mismatch", "isolated aggregate prepared effect identity mismatch")
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	return GraphWriterBatchRecord{Version: 2, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, CandidateID: candidateID, Members: members, Prepared: expected, IsolationPreparationID: identity.IsolationPreparationID}, nil
}

func isolatedGraphWriterIDsForResults(s Snapshot, requested []string) ([]string, error) {
	if s.GraphIsolationPreparation == nil || s.Candidate == nil || len(requested) == 0 {
		return nil, errors.New("frozen isolated graph writer results required")
	}
	return sortedFrozenCohortIDs(requested, s.GraphIsolationPreparation.SelectedTaskIDs, "isolated aggregate must include the entire frozen cohort", "isolated aggregate membership differs from frozen cohort")
}

func isolatedGraphWriterAggregateParts(s Snapshot, ids []string) (graphWriterAggregateIdentity, []GraphWriterMember, []fileeffects.Change, error) {
	if s.Graph == nil || s.Candidate == nil || s.GraphIsolationPreparation == nil {
		return graphWriterAggregateIdentity{}, nil, nil, errors.New("isolated aggregate graph state unavailable")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil || candidateID != s.GraphIsolationPreparation.BaseCandidateID {
		return graphWriterAggregateIdentity{}, nil, nil, errors.Join(errors.New("isolated aggregate parent candidate changed"), err)
	}
	identity := graphWriterAggregateIdentity{Version: 2, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, CandidateID: candidateID, IsolationPreparationID: s.GraphIsolationPreparation.PreparationID}
	members := make([]GraphWriterMember, 0, len(ids))
	var changes []fileeffects.Change
	seenPaths := map[string]bool{}
	for _, id := range ids {
		task, ok := s.Graph.Graph.Task(id)
		record, found := s.GraphWriterResults[id]
		if !ok || task.Kind != engineeringplan.Implementation || !found || record.Isolated == nil || record.Isolated.TaskID != id {
			return graphWriterAggregateIdentity{}, nil, nil, fmt.Errorf("isolated graph writer task %q lacks validated child changes", id)
		}
		if err := validateIsolatedGraphWriterProposal(s, *record.Isolated); err != nil {
			return graphWriterAggregateIdentity{}, nil, nil, err
		}
		childID, err := record.Isolated.Candidate.ID()
		if err != nil {
			return graphWriterAggregateIdentity{}, nil, nil, err
		}
		if !graphWriterChangesWithinTask(task, record.Isolated.Changes) {
			return graphWriterAggregateIdentity{}, nil, nil, errors.New("isolated graph writer changes exceed task ownership")
		}
		invocation := graphWriterRecordInvocation(record)
		members = append(members, GraphWriterMember{TaskID: id, InvocationID: invocation.ID, IsolationID: record.Isolated.IsolationID, ChildCandidateID: childID})
		for _, change := range record.Isolated.Changes {
			if seenPaths[change.Path] {
				return graphWriterAggregateIdentity{}, nil, nil, errors.New("isolated graph writers propose the same parent path")
			}
			seenPaths[change.Path] = true
			changes = append(changes, change)
		}
	}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			first, ok1 := s.Graph.Graph.Task(ids[i])
			second, ok2 := s.Graph.Graph.Task(ids[j])
			if !ok1 || !ok2 || !tasksIndependentAndDisjoint(first, second) {
				return graphWriterAggregateIdentity{}, nil, nil, errors.New("isolated graph writer tasks are not independent and disjoint")
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return identity, members, changes, nil
}

func buildGraphWriterBatch(s Snapshot, taskIDs []string) (GraphWriterBatchRecord, error) {
	if s.Graph == nil || s.Candidate == nil || len(taskIDs) == 0 || len(taskIDs) > 2 {
		return GraphWriterBatchRecord{}, errors.New("invalid graph writer batch request")
	}
	ids := append([]string(nil), taskIDs...)
	sort.Strings(ids)
	ready, err := graphReadyTasks(s)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	var readyIDs []string
	for _, task := range ready {
		if task.Kind == "implementation" {
			readyIDs = append(readyIDs, task.ID)
		}
	}
	sort.Strings(readyIDs)
	if len(ids) != len(readyIDs) {
		return GraphWriterBatchRecord{}, errors.New("aggregate must include every ready implementation")
	}
	for i := range ids {
		if ids[i] != readyIDs[i] {
			return GraphWriterBatchRecord{}, errors.New("aggregate implementation cohort changed")
		}
	}
	var members []GraphWriterMember
	var changes []fileeffects.Change
	var beforeFiles []worktree.FileState
	for _, id := range ids {
		record, ok := s.GraphWriterResults[id]
		if !ok || record.Writer.Prepared.Proposal.Before != *s.Candidate {
			return GraphWriterBatchRecord{}, fmt.Errorf("graph writer task %q lacks a candidate-bound proposal", id)
		}
		if beforeFiles == nil {
			beforeFiles = append([]worktree.FileState{}, record.Writer.Prepared.Proposal.BeforeFiles...)
		} else if !sameCanonical(beforeFiles, record.Writer.Prepared.Proposal.BeforeFiles) {
			return GraphWriterBatchRecord{}, errors.New("graph writer proposal manifests differ for the frozen candidate")
		}
		members = append(members, GraphWriterMember{TaskID: id, InvocationID: record.Writer.Invocation.ID})
		changes = append(changes, record.Writer.Prepared.Proposal.Changes...)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	if len(ids) == 2 {
		first, ok1 := s.Graph.Graph.Task(ids[0])
		second, ok2 := s.Graph.Graph.Task(ids[1])
		if !ok1 || !ok2 || !tasksIndependentAndDisjoint(first, second) {
			return GraphWriterBatchRecord{}, errors.New("graph writer tasks are not independent and disjoint")
		}
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	identity := graphWriterAggregateIdentity{Version: 1, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, CandidateID: candidateID, Members: members}
	nonce, err := canonical.Hash("harness.graph-writer-aggregate.v1", identity)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	proposal := fileeffects.Proposal{Version: 1, Nonce: "graph-writers-" + nonce, Before: *s.Candidate, BeforeFiles: beforeFiles, Changes: changes}
	proposal.After, err = composeGraphWriterCandidate(proposal.Before, proposal.BeforeFiles, proposal.Changes)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	prepared, err := preparedFiles(s, proposal)
	if err != nil {
		return GraphWriterBatchRecord{}, err
	}
	return GraphWriterBatchRecord{Version: 1, GraphDigest: s.Graph.Digest, Revision: s.Graph.Revision, CandidateID: candidateID, Members: members, Prepared: prepared}, nil
}

func composeGraphWriterCandidate(before worktree.Candidate, beforeFiles []worktree.FileState, changes []fileeffects.Change) (worktree.Candidate, error) {
	states := make(map[string]worktree.FileState, len(beforeFiles)+len(changes))
	for _, state := range beforeFiles {
		states[state.Path] = state
	}
	for _, change := range changes {
		current, exists := states[change.Path]
		if change.BeforeHash == nil {
			if exists {
				return worktree.Candidate{}, errors.New("aggregate expected an absent path")
			}
		} else if !exists || current.Hash != *change.BeforeHash {
			return worktree.Candidate{}, errors.New("aggregate file precondition differs from frozen candidate")
		}
		if change.ContentBase64 == nil {
			if !exists || change.Executable {
				return worktree.Candidate{}, errors.New("aggregate contains an invalid deletion")
			}
			delete(states, change.Path)
			continue
		}
		content, err := base64.StdEncoding.Strict().DecodeString(*change.ContentBase64)
		if err != nil || base64.StdEncoding.EncodeToString(content) != *change.ContentBase64 {
			return worktree.Candidate{}, errors.Join(errors.New("aggregate content is not canonical base64"), err)
		}
		hash := sha256.Sum256(content)
		states[change.Path] = worktree.FileState{Path: change.Path, Hash: hex.EncodeToString(hash[:]), Executable: change.Executable}
	}
	files := make([]worktree.FileState, 0, len(states))
	for _, state := range states {
		files = append(files, state)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	hash, err := worktree.FilesID(files)
	if err != nil {
		return worktree.Candidate{}, err
	}
	after := before
	after.FilesHash = hash
	after.FileCount = len(files)
	return after, nil
}

func graphContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func tasksIndependentAndDisjoint(first, second engineeringplan.Task) bool {
	for _, dependency := range first.Dependencies {
		if dependency == second.ID {
			return false
		}
	}
	for _, dependency := range second.Dependencies {
		if dependency == first.ID {
			return false
		}
	}
	for _, a := range first.WritePaths {
		for _, b := range second.WritePaths {
			a, b = strings.ToLower(strings.TrimSuffix(a, "/")), strings.ToLower(strings.TrimSuffix(b, "/"))
			if a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") {
				return false
			}
		}
	}
	return len(first.WritePaths) > 0 && len(second.WritePaths) > 0
}

func recordGraphWriterBatch(path string, batch GraphWriterBatchRecord) error {
	return Append(path, "graph.writer.batch-proposed", batch)
}

func recordGraphWriterProposal(path string, taskID string, writer WriterRecord, dispatch *GraphWriterDispatchTiming) error {
	return Append(path, "graph.writer.proposed", GraphWriterRecord{TaskID: taskID, Writer: writer, Dispatch: dispatch})
}

func recordIsolatedGraphWriterProposal(path string, proposal IsolatedGraphWriterProposal, dispatch *GraphWriterDispatchTiming) error {
	return Append(path, "graph.writer.proposed", GraphWriterRecord{TaskID: proposal.TaskID, Dispatch: dispatch, Isolated: &proposal})
}

func graphWriterRecordInvocation(record GraphWriterRecord) runtime.Invocation {
	if record.Isolated != nil {
		return record.Isolated.Invocation
	}
	return record.Writer.Invocation
}

func graphWriterRecordChanges(record GraphWriterRecord) []fileeffects.Change {
	if record.Isolated != nil {
		return record.Isolated.Changes
	}
	return record.Writer.Prepared.Proposal.Changes
}

func graphWriterInvocation(s Snapshot, taskID string) (runtime.Invocation, error) {
	return writerInvocationForTask(s, taskID)
}

func graphWriterInvocationForContext(ctx context.Context, s Snapshot, taskID string) (runtime.Invocation, error) {
	if correction, ok := scheduledRoleCorrectionFromContext(ctx); ok && correction.TaskID == taskID && correction.ScheduledTaskID != "" {
		return scheduledTurnInvocation(correction.Invocation, taskscheduler.OperationWriter, correction.ScheduledTaskID)
	}
	return graphWriterInvocation(s, taskID)
}

func graphWriterTaskQuestion(s Snapshot, taskID string) (string, error) {
	if s.Graph == nil {
		return "", errors.New("graph unavailable")
	}
	task, ok := s.Graph.Graph.Task(taskID)
	if !ok || task.Kind != "implementation" {
		return "", errors.New("implementation task unavailable")
	}
	question := fmt.Sprintf("[%s] %s | scope: %s | write paths: %s | objective: %s", task.ID, task.Title, strings.Join(task.ScopePaths, ","), strings.Join(task.WritePaths, ","), s.Creation.Objective)
	if len(question) > taskContextFullQueryMax {
		return "", errors.New("writer task context exceeds bound")
	}
	return question, nil
}

func prepareGraphWriterFiles(ctx context.Context, path string, taskID string, invocation runtime.Invocation, result runtime.Result) (WriterRecord, error) {
	s, err := Inspect(path)
	if err != nil {
		return WriterRecord{}, err
	}
	expected, err := graphWriterInvocationForContext(ctx, s, taskID)
	if err != nil || expected != invocation {
		return WriterRecord{}, errors.Join(errors.New("graph writer invocation is stale or substituted"), err)
	}
	prepared, preimages, err := prepareWriterFilesForTask(ctx, path, taskID, invocation, result)
	if err != nil {
		return WriterRecord{}, err
	}
	task, _ := s.Graph.Graph.Task(taskID)
	var reply WriterProposal
	if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
		anchored, err := decodeAnchoredProposal(result.Output)
		if err != nil {
			return WriterRecord{}, err
		}
		changes, err := composeAnchoredProposal(anchored, prepared.Proposal.BeforeFiles, preimages)
		if err != nil {
			return WriterRecord{}, err
		}
		reply = WriterProposal{CandidateID: anchored.CandidateID, Changes: changes}
	} else {
		reply, err = decodeWriterProposal(s.Creation.Config.WriterContract, result.Output)
		if err != nil {
			return WriterRecord{}, err
		}
	}
	if !graphWriterChangesWithinTask(task, reply.Changes) {
		return WriterRecord{}, errGraphWriterScopeViolation
	}
	return WriterRecord{Invocation: invocation, Result: result, Prepared: prepared, EditPreimages: preimages}, nil
}

func validateGraphWriterInvocationID(id string) error { return safepath.RequireDigest(id) }

func isStaticGraphWriterCohort(s Snapshot, claim taskscheduler.Claim) bool {
	if claim.Task.Operation != taskscheduler.OperationWriter || claim.AgentTurn != nil || !graphWriterCohortEnabled(s) || s.State != "IMPLEMENTING" || s.Graph == nil || s.Candidate == nil || s.Workspace == nil || claim.Task.RunID != s.RunID || claim.Task.Input != "" {
		return false
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return false
	}
	if stagedIsolationEnabled(s) {
		var implementations []engineeringplan.Task
		for _, task := range ready {
			if task.Kind == engineeringplan.Implementation {
				implementations = append(implementations, task)
			}
		}
		if err := validateStagedCohort(s, implementations); err != nil {
			return false
		}
	} else if isolatedImplementationEnabled(s) {
		var implementations []engineeringplan.Task
		for _, task := range ready {
			if task.Kind == engineeringplan.Implementation {
				implementations = append(implementations, task)
			}
		}
		if err := validateIsolatedGraphWriterCohort(s, implementations); err != nil {
			return false
		}
	}
	for _, task := range ready {
		if task.ID != claim.Task.ID || task.Kind != engineeringplan.Implementation {
			continue
		}
		invocation, err := writerInvocationForTask(s, task.ID)
		if err != nil || invocation.ID != claim.Task.InvocationID {
			return false
		}
		return staticGraphWriterRuntimeAdmitted(s, invocation.Profile.Runtime)
	}
	return false
}

func verifyGraphWriterCohortDelta(controllerPath string, claim taskscheduler.Claim) error {
	events, err := journal.Read(controllerPath)
	if err != nil || len(events) == 0 {
		return errors.New("controller journal unavailable for writer cohort check")
	}
	idx := -1
	for i, event := range events {
		if event.Hash == claim.ControllerHead {
			idx = i
		}
	}
	if idx < 0 {
		return errors.New("writer cohort bound head not found")
	}
	if idx == len(events)-1 {
		return nil
	}
	bound, err := Replay(events[:idx+1])
	if err != nil {
		return err
	}
	current, err := Replay(events)
	if err != nil {
		return err
	}
	if bound.RunID != current.RunID || bound.PlanID != current.PlanID || bound.Graph == nil || current.Graph == nil || bound.Graph.Digest != current.Graph.Digest || bound.Graph.Revision != current.Graph.Revision || bound.Candidate == nil || current.Candidate == nil || *bound.Candidate != *current.Candidate || lifecycleStatus(bound) != lifecycleStatus(current) || bound.FileOutcome == "UNKNOWN" || current.FileOutcome == "UNKNOWN" || graphHasUnknown(bound) || graphHasUnknown(current) {
		return errors.New("writer cohort binding changed or became UNKNOWN")
	}
	ready, err := graphReadyTasks(bound)
	if err != nil {
		return err
	}
	cohort := map[string]engineeringplan.Task{}
	invocations := map[string]string{}
	for _, task := range ready {
		if task.Kind != engineeringplan.Implementation {
			continue
		}
		invocation, err := writerInvocationForTask(bound, task.ID)
		if err != nil {
			return err
		}
		if !staticGraphWriterRuntimeAdmitted(bound, invocation.Profile.Runtime) {
			return errors.New("writer cohort runtime unsupported")
		}
		cohort[task.ID] = task
		invocations[invocation.ID] = task.ID
	}
	maxCohort := 2
	if stagedIsolationEnabled(bound) && bound.State == "IMPLEMENTING" {
		var implementations []engineeringplan.Task
		for _, task := range ready {
			if task.Kind == engineeringplan.Implementation {
				implementations = append(implementations, task)
			}
		}
		if err := validateStagedCohort(bound, implementations); err != nil {
			return err
		}
		maxCohort = 8
	} else if isolatedImplementationEnabled(bound) && bound.State == "IMPLEMENTING" {
		var implementations []engineeringplan.Task
		for _, task := range ready {
			if task.Kind == engineeringplan.Implementation {
				implementations = append(implementations, task)
			}
		}
		if err := validateIsolatedGraphWriterCohort(bound, implementations); err != nil {
			return err
		}
		maxCohort = 8
	} else if isolatedImplementationEnabled(bound) {
		repairTask := cohort[claim.Task.ID]
		if bound.State != "REPAIRING" || repairTask.ID == "" || repairTask.ParentID == "" {
			return errors.New("isolated graph writer parent dispatch is limited to a scoped repair task")
		}
	}
	if len(cohort) < 1 || len(cohort) > maxCohort || cohort[claim.Task.ID].ID == "" || invocations[claim.Task.InvocationID] != claim.Task.ID {
		return errors.New("writer claim is outside frozen implementation cohort")
	}
	boundCandidateID, err := bound.Candidate.ID()
	if err != nil {
		return err
	}
	allowed := map[string]bool{"task.context-admitted": true, "graph.writer.host-intent": true, "graph.writer.host-ready": true, "graph.writer.host-observed": true, "graph.writer.runtime-observed": true, "graph.writer.proposed": true, "model.access-intent": true, "model.access-receipt": true, "model.access-semantic-pending": true}
	if isolatedImplementationEnabled(bound) && bound.State == "IMPLEMENTING" {
		allowed["graph.writer.memory-admitted"] = true
		allowed["graph.writer.memory-released"] = true
	}
	memoryActive := map[string]GraphMemoryAdmissionActive{}
	if bound.GraphMemoryAdmission != nil {
		for taskID, active := range bound.GraphMemoryAdmission.ActiveTaskIDs {
			memoryActive[taskID] = active
		}
	}
	for _, event := range events[idx+1:] {
		if !allowed[event.Kind] {
			return fmt.Errorf("writer cohort sibling event %q is not attributable", event.Kind)
		}
		switch event.Kind {
		case "graph.writer.memory-admitted":
			var record GraphMemoryAdmissionRecord
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return err
			}
			if !isolatedImplementationEnabled(bound) || record.RunID != bound.RunID || record.GraphDigest != bound.Graph.Digest || record.Revision != bound.Graph.Revision || record.CandidateID != boundCandidateID || bound.GraphIsolationPreparation == nil || record.PreparationID != bound.GraphIsolationPreparation.PreparationID || cohort[record.TaskID].ID == "" || invocations[record.InvocationID] != record.TaskID {
				return errors.New("memory admission is outside frozen writer cohort")
			}
			if !sameGraphMemoryAdmissionActiveSet(record.ActiveTaskIDs, memoryActive) {
				return errors.New("memory admission active set differs from writer cohort history")
			}
			if record.Granted {
				if _, exists := memoryActive[record.TaskID]; exists {
					return errors.New("memory admission duplicates an active writer task")
				}
				memoryActive[record.TaskID] = GraphMemoryAdmissionActive{AdmissionID: record.AdmissionID, InvocationID: record.InvocationID}
			}
		case "graph.writer.memory-released":
			var release GraphMemoryAdmissionRelease
			if err := canonical.Decode(event.Payload, &release); err != nil {
				return err
			}
			active, ok := memoryActive[release.TaskID]
			if !ok || cohort[release.TaskID].ID == "" || active.AdmissionID != release.AdmissionID {
				return errors.New("memory release is outside frozen writer admission")
			}
			delete(memoryActive, release.TaskID)
		case "task.context-admitted":
			var record TaskContextRecord
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return err
			}
			matched := false
			for taskID := range cohort {
				question, err := graphWriterTaskQuestion(bound, taskID)
				if err == nil && record.Role == "writer" && record.Query == question {
					matched = true
					break
				}
			}
			if !matched {
				return errors.New("writer cohort task context is not bound")
			}
		case "graph.writer.host-intent", "graph.writer.host-ready":
			var record GraphWriterHostEvent
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return err
			}
			if _, ok := cohort[record.TaskID]; !ok || record.Intent == nil {
				return errors.New("writer host intent outside frozen cohort")
			}
			want, err := expectedWriterHostForTask(bound, record.TaskID)
			if err != nil || want != *record.Intent {
				return errors.Join(errors.New("writer host intent differs from cohort"), err)
			}
		case "graph.writer.host-observed":
			var record GraphWriterHostEvent
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return err
			}
			if _, ok := cohort[record.TaskID]; !ok || record.HostReceipt == nil {
				return errors.New("writer host receipt outside frozen cohort")
			}
			host, ok := current.GraphWriterHosts[record.TaskID]
			wantReceipt, wantErr := canonical.Bytes(host.Receipt)
			gotReceipt, gotErr := canonical.Bytes(record.HostReceipt)
			if !ok || host.Receipt == nil || wantErr != nil || gotErr != nil || string(wantReceipt) != string(gotReceipt) {
				return errors.New("writer host receipt not replayed")
			}
		case "graph.writer.runtime-observed":
			var record GraphWriterHostEvent
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return err
			}
			if _, ok := cohort[record.TaskID]; !ok || record.RuntimeReceipt == nil {
				return errors.New("writer runtime receipt outside frozen cohort")
			}
			host, ok := current.GraphWriterHosts[record.TaskID]
			if !ok || host.RuntimeReceipt == nil || *host.RuntimeReceipt != *record.RuntimeReceipt {
				return errors.New("writer runtime receipt not replayed")
			}
		case "graph.writer.proposed":
			var record GraphWriterRecord
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return err
			}
			task, ok := cohort[record.TaskID]
			if !ok {
				return errors.New("writer proposal outside frozen cohort")
			}
			invocation, err := writerInvocationForTask(bound, task.ID)
			if err != nil || invocation != graphWriterRecordInvocation(record) {
				return errors.Join(errors.New("writer proposal invocation differs from frozen task"), err)
			}
			if isolatedImplementationEnabled(bound) && bound.State == "IMPLEMENTING" {
				if record.Isolated == nil || validateIsolatedGraphWriterProposal(current, *record.Isolated) != nil {
					return errors.New("isolated writer proposal differs from confirmed child binding")
				}
				if active, ok := memoryActive[record.TaskID]; !ok || active.InvocationID != invocation.ID {
					return errors.New("isolated writer proposal lacks its exact memory admission")
				} else {
					delete(memoryActive, record.TaskID)
				}
			} else if isolatedImplementationEnabled(bound) {
				if bound.State != "REPAIRING" || task.ParentID == "" || record.Isolated != nil || record.Writer.Prepared.Proposal.Before != *bound.Candidate {
					return errors.New("isolated graph repair proposal is not parent-bound")
				}
			} else if record.Writer.Prepared.Proposal.Before != *bound.Candidate {
				return errors.New("writer proposal candidate differs from frozen cohort")
			}
		case "model.access-intent":
			var record modelAccessIntentEvent
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return err
			}
			taskID, ok := invocations[record.RuntimeInvocationID]
			if !ok {
				return errors.New("model access intent outside frozen writer cohort")
			}
			invocation, err := writerInvocationForTask(bound, taskID)
			if err != nil || invocation.ID != record.RuntimeInvocationID {
				return errors.Join(errors.New("model access intent invocation differs from frozen task"), err)
			}
			want, err := deriveModelAccessIntent(bound, invocation, 1)
			if err != nil || !sameCanonical(record.Intent, want) {
				return errors.Join(errors.New("model access intent differs from frozen task"), err)
			}
		case "model.access-receipt":
			var record ModelAccessTerminal
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return err
			}
			if _, ok := invocations[record.RuntimeInvocationID]; !ok {
				return errors.New("model access receipt outside frozen writer cohort")
			}
			index := modelAccessIndex(current, record.RuntimeInvocationID)
			if index < 0 || current.ModelAccess[index].Terminal == nil || !sameCanonical(*current.ModelAccess[index].Terminal, record) {
				return errors.New("model access receipt differs from replayed cohort receipt")
			}
		case "model.access-semantic-pending":
			var record ModelAccessSemanticPending
			if err := canonical.Decode(event.Payload, &record); err != nil {
				return err
			}
			taskID, ok := invocations[record.RuntimeInvocationID]
			if !ok {
				return errors.New("semantic usage pending event outside frozen writer cohort")
			}
			invocation, err := writerInvocationForTask(bound, taskID)
			if err != nil || invocation.ID != record.RuntimeInvocationID {
				return errors.Join(errors.New("semantic usage pending invocation differs from frozen task"), err)
			}
			index := modelAccessIndex(current, record.RuntimeInvocationID)
			if index < 0 || current.ModelAccess[index].SemanticPending == nil || !sameCanonical(*current.ModelAccess[index].SemanticPending, record) {
				return errors.New("semantic usage pending proof differs from replayed cohort proof")
			}
			runtimePath, ok := runtimeJournalForSemanticInvocation(current, invocation)
			if !ok {
				return errors.New("semantic usage pending runtime journal is outside the cohort")
			}
			runtimeState, head, err := codexruntime.InspectWithHead(runtimePath)
			if err != nil || head != record.RuntimeJournalHead {
				return errors.Join(errors.New("semantic usage pending runtime head differs from frozen member"), err)
			}
			observed, _, err := semanticUsagePendingFromRuntime(current, runtimeState, head, invocation, "harness.writer-result.v1")
			if err != nil || !sameCanonical(observed, record) {
				return errors.Join(errors.New("semantic usage pending runtime result differs from frozen member"), err)
			}
		}
	}
	if current.GraphMemoryAdmission == nil {
		if len(memoryActive) != 0 {
			return errors.New("writer memory admission history differs from replay")
		}
	} else if !sameGraphMemoryAdmissionActiveMap(memoryActive, current.GraphMemoryAdmission.ActiveTaskIDs) {
		return errors.New("writer memory admission active set differs from replay")
	}
	return nil
}

func sameGraphMemoryAdmissionActiveSet(ids []string, active map[string]GraphMemoryAdmissionActive) bool {
	if len(ids) != len(active) {
		return false
	}
	taskIDs := make([]string, 0, len(active))
	for taskID := range active {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	for index, taskID := range taskIDs {
		if ids[index] != taskID {
			return false
		}
	}
	return true
}

func sameGraphMemoryAdmissionActiveMap(left, right map[string]GraphMemoryAdmissionActive) bool {
	if len(left) != len(right) {
		return false
	}
	for taskID, value := range left {
		if right[taskID] != value {
			return false
		}
	}
	return true
}

func buildGraphWriterBatchSpecs(controllerPath string, s Snapshot, tasks []engineeringplan.Task) ([]taskscheduler.TaskSpec, error) {
	maxCohort := 2
	if isolatedImplementationEnabled(s) {
		maxCohort = 8
		if _, err := isolatedGraphWriterIDs(s, tasks); err != nil {
			return nil, err
		}
	} else if !parallelImplementationEnabled(s) {
		return nil, errors.New("writer cohort policy is not enabled")
	}
	if len(tasks) < 1 || len(tasks) > maxCohort {
		return nil, fmt.Errorf("writer cohort must contain one to %d implementations", maxCohort)
	}
	return buildWriterTaskSpecs(s, controllerPath, tasks)
}

func graphWriterCohortEnabled(s Snapshot) bool {
	return parallelImplementationEnabled(s) || isolatedImplementationEnabled(s)
}

func runGraphWriterBatch(ctx context.Context, controllerPath string, initial Snapshot, tasks []engineeringplan.Task) error {
	if initial.Creation.Execution == nil || !parallelImplementationEnabled(initial) || initial.State != "IMPLEMENTING" {
		return errors.New("parallel graph writer batch is not enabled here")
	}
	// Freeze one exact context query per implementation before deriving the
	// invocation-bound schedule. Each task keeps its own recorded query/result.
	for _, task := range tasks {
		question, err := graphWriterTaskQuestion(initial, task.ID)
		if err != nil {
			return err
		}
		if err := maybeAdmitTaskContext(ctx, controllerPath, "writer", question); err != nil {
			return err
		}
	}
	initial, err := Inspect(controllerPath)
	if err != nil {
		return err
	}
	specs, err := buildGraphWriterBatchSpecs(controllerPath, initial, tasks)
	if err != nil {
		return err
	}
	workers := initial.Creation.Execution.EffectiveMaxParallel()
	if workers > len(specs) {
		workers = len(specs)
	}
	if workers > 2 {
		workers = 2
	}
	if workers < 1 {
		workers = 1
	}
	id, err := graphCohortID(initial.Graph.Digest, initial.Graph.Revision, specs)
	if err != nil {
		return err
	}
	schedulePath := controllerPath + ".graph-writers-" + id[:16] + ".jsonl"
	definition := taskscheduler.Definition{Version: 1, Nonce: "graph-writers-" + id, Tasks: specs}
	if _, err := taskscheduler.Bind(schedulePath, definition); err != nil {
		return err
	}
	return runScheduledGraphWriterCohort(ctx, controllerPath, schedulePath, specs, workers, false)
}

func runIsolatedGraphWriterBatch(ctx context.Context, controllerPath string, initial Snapshot, tasks []engineeringplan.Task) error {
	if !isolatedImplementationEnabled(initial) || initial.Creation.Execution == nil || initial.State != "IMPLEMENTING" {
		return errors.New("isolated graph writer batch is not enabled here")
	}
	if isolatedWavesEnabled(initial) {
		return runIsolatedGraphWriterWaves(ctx, controllerPath, initial, tasks)
	}
	if _, err := isolatedGraphWriterIDs(initial, tasks); err != nil {
		return err
	}
	// Freeze the exact complete cohort and confirm every child workspace before
	// deriving invocation IDs or scheduler claims. UNKNOWN isolation outcomes
	// stop this path and are never recreated here.
	bindings, err := prepareIsolatedGraphWriterCohort(ctx, controllerPath)
	if err != nil {
		return err
	}
	current, err := Inspect(controllerPath)
	if err != nil {
		return err
	}
	if len(bindings) != len(tasks) {
		return errors.New("confirmed isolated bindings differ from complete implementation cohort")
	}
	bindingIDs := map[string]bool{}
	for _, binding := range bindings {
		bindingIDs[binding.TaskID] = true
	}
	for _, task := range tasks {
		if !bindingIDs[task.ID] {
			return errors.New("confirmed isolated binding membership differs from frozen task cohort")
		}
		if _, err := isolatedWriterBindingForTask(current, task.ID); err != nil {
			return err
		}
	}
	if _, err := isolatedGraphWriterIDs(current, tasks); err != nil {
		return err
	}
	specs, err := buildGraphWriterBatchSpecs(controllerPath, current, tasks)
	if err != nil {
		return err
	}
	workers := current.Creation.Execution.EffectiveMaxParallel()
	if workers > len(specs) {
		workers = len(specs)
	}
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		return errors.New("isolated writer cohort has no scheduler capacity")
	}
	id, err := graphCohortID(current.Graph.Digest, current.Graph.Revision, specs)
	if err != nil {
		return err
	}
	schedulePath := controllerPath + ".isolated-graph-writers-" + id[:16] + ".jsonl"
	definition := taskscheduler.Definition{Version: 1, Nonce: "isolated-graph-writers-" + id, Tasks: specs}
	if _, err := taskscheduler.Bind(schedulePath, definition); err != nil {
		return err
	}
	return runScheduledGraphWriterCohort(ctx, controllerPath, schedulePath, specs, workers, true)
}

func buildIsolatedGraphWriterWaveSpecs(controllerPath string, s Snapshot, waveTasks []engineeringplan.Task, waveIndex int) ([]taskscheduler.TaskSpec, error) {
	if !isolatedWavesEnabled(s) || s.State != "IMPLEMENTING" || s.Graph == nil || s.Candidate == nil || s.FileIntent != nil || s.Creation.Execution == nil {
		return nil, errors.New("isolated wave writer requires a pristine initial graph candidate")
	}
	if s.GraphIsolationPreparation == nil || s.GraphIsolationPreparation.Version != 2 || len(s.GraphIsolationPreparation.Waves) == 0 {
		return nil, errors.New("frozen isolated waves are unavailable")
	}
	if waveIndex < 0 || waveIndex >= len(s.GraphIsolationPreparation.Waves) {
		return nil, errors.New("isolated wave index is outside the frozen cohort")
	}
	return buildFrozenWaveSpecs(s, controllerPath, waveTasks, waveIndex, "isolated wave membership differs from frozen cohort", "isolated wave task is stale or substituted", "isolated wave task is outside its frozen wave", "isolated wave tasks are not independent and disjoint")
}

func runIsolatedGraphWriterWaves(ctx context.Context, controllerPath string, initial Snapshot, tasks []engineeringplan.Task) error {
	if !isolatedWavesEnabled(initial) || initial.Creation.Execution == nil || initial.State != "IMPLEMENTING" {
		return errors.New("isolated wave writer batch is not enabled here")
	}
	if _, err := isolatedGraphWriterIDs(initial, tasks); err != nil {
		return err
	}
	bindings, err := prepareIsolatedGraphWriterCohort(ctx, controllerPath)
	if err != nil {
		return err
	}
	current, err := Inspect(controllerPath)
	if err != nil {
		return err
	}
	if len(bindings) != len(tasks) {
		return errors.New("confirmed isolated bindings differ from complete implementation cohort")
	}
	bindingIDs := map[string]bool{}
	for _, binding := range bindings {
		bindingIDs[binding.TaskID] = true
	}
	for _, task := range tasks {
		if !bindingIDs[task.ID] {
			return errors.New("confirmed isolated binding membership differs from frozen task cohort")
		}
		if _, err := isolatedWriterBindingForTask(current, task.ID); err != nil {
			return err
		}
	}
	if _, err := isolatedGraphWriterIDs(current, tasks); err != nil {
		return err
	}
	if current.GraphIsolationPreparation == nil || current.GraphIsolationPreparation.Version != 2 || len(current.GraphIsolationPreparation.Waves) == 0 {
		return errors.New("frozen isolated waves are unavailable")
	}
	byID := map[string]engineeringplan.Task{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	preparationID := current.GraphIsolationPreparation.PreparationID
	for waveIndex, waveIDs := range current.GraphIsolationPreparation.Waves {
		latest, err := Inspect(controllerPath)
		if err != nil {
			return err
		}
		if latest.FileIntent != nil || latest.GraphWriterBatch != nil {
			return errors.New("isolated waves require a pristine parent before aggregation")
		}
		if graphHasUnknown(latest) || latest.FileOutcome == "UNKNOWN" {
			return errors.New("isolated waves blocked on UNKNOWN; explicit reconciliation required")
		}
		for _, isolation := range latest.GraphIsolations {
			if isolation.Outcome == "UNKNOWN" {
				return errors.New("isolated waves blocked on UNKNOWN child isolation; explicit reconciliation required")
			}
		}
		waveTasks, err := waveTasksForCohort(byID, waveIDs, "isolated wave task is outside the frozen cohort")
		if err != nil {
			return err
		}
		allProposed, err := waveProposalStatus(latest, waveTasks, "isolated wave is partially proposed; UNKNOWN or tamper suspected")
		if err != nil {
			return err
		}
		if allProposed {
			continue
		}
		specs, err := buildIsolatedGraphWriterWaveSpecs(controllerPath, latest, waveTasks, waveIndex)
		if err != nil {
			return err
		}
		if err := scheduleWaveCohort(ctx, controllerPath, latest, specs, preparationID, waveIndex, ".isolated-graph-writers-wave-", "harness.isolated-wave-schedule.v1", "isolated wave has no scheduler capacity", "isolated wave settled without a complete candidate-bound proposal set"); err != nil {
			return err
		}
	}
	return nil
}

// legacyGraphWriterCohortTimeout preserves the historical writer-cohort wait
// for runtimes without a frozen per-invocation total deadline (Codex, fake,
// serial legacy). It is not an OpenCode admission bound.
const legacyGraphWriterCohortTimeout = 5 * time.Minute

// graphWriterMaxCorrectionsPerTask is the finite admitted semantic-correction
// allowance per writer task. It mirrors buildSemanticCorrectionInvocation's
// allowance of two (attempt 1..2); a third correction is rejected with
// ErrSemanticCorrectionBudget and never becomes a scheduler claim.
const graphWriterMaxCorrectionsPerTask = 2

// effectiveGraphWriterPerInvocationTimeout returns the frozen finite
// per-invocation runtime already enforced by openCodeProviderRuntimeContext:
// the 15-minute default or the validated host InvocationTimeoutSeconds
// 1..7200. It reports false for non-OpenCode cohorts, where no such frozen
// limit exists and the legacy cohort timeout is preserved byte-for-byte in
// behavior. A zero or out-of-range host value falls back to the default,
// matching the provider runtime helper.
func effectiveGraphWriterPerInvocationTimeout(s Snapshot) (time.Duration, bool) {
	if s.Creation.Config.Writer == nil || s.Creation.Config.Writer.Runtime != "opencode-http" {
		return legacyGraphWriterCohortTimeout, false
	}
	if !isolatedImplementationEnabled(s) {
		return legacyGraphWriterCohortTimeout, false
	}
	limit := openCodeProviderRuntimeTimeout
	if host := s.Creation.Config.OpenCode; host != nil && host.InvocationTimeoutSeconds > 0 && host.InvocationTimeoutSeconds <= 7200 {
		limit = time.Duration(host.InvocationTimeoutSeconds) * time.Second
	}
	return limit, true
}

// graphWriterCohortTimeout derives one cohort/wave lifetime from the frozen
// per-invocation limit and the finite number of legal invocations. With N
// tasks and at most two admitted corrections per task, the worst legal shape
// is N sequential invocations times three (one initial plus two correction
// generations). The bound is deliberately independent of the advertised
// scheduler worker count: the memory admission gate parks future claims
// whenever len(active) reaches Decision.EffectiveWorkers, and that decision
// can reduce to one worker under pressure while scheduler slots remain, so
// guaranteed parallelism of ceil(N/W) slots is not valid and legal work could
// again be cancelled under memory pressure. Resource and claim contention
// park the same way. Indefinite parking under sustained pressure may still hit
// this bounded deadline; the bound covers only the finite admitted serial
// work, with no guarantee under infinite pressure. The workers argument is
// accepted for call-site compatibility and ignored. The result is a duration
// only; an earlier caller deadline or cancellation still wins through the
// cohort loop's ctx.Done() branch, UNKNOWN remains UNKNOWN, and no retry,
// admission, role or budget semantics change. Each staged/isolated wave
// computes its own bound from its own specs; sequential waves do not share
// one deadline. The legal cohort is bounded (at most eight tasks, at most
// three invocations each, at most 7200 seconds per invocation), so the product
// cannot overflow and no artificial cap is applied.
func graphWriterCohortTimeout(s Snapshot, numTasks, workers int) time.Duration {
	perInvocation, ok := effectiveGraphWriterPerInvocationTimeout(s)
	if !ok {
		return legacyGraphWriterCohortTimeout
	}
	if numTasks < 1 {
		return perInvocation
	}
	return perInvocation * time.Duration(numTasks*(1+graphWriterMaxCorrectionsPerTask))
}

// runScheduledGraphWriterCohort keeps every initial member alive until the
// complete current schedule is settled. A known semantic rejection may then
// receive a new dynamic claim; no correction is admitted while any sibling or
// predecessor claim is pending or UNKNOWN.
func runScheduledGraphWriterCohort(ctx context.Context, controllerPath, schedulePath string, specs []taskscheduler.TaskSpec, workers int, isolated bool) error {
	// Authoritative source-bound read before any pump starts: the cohort
	// lifetime derives from the frozen controller state, and a read failure
	// fails closed with the read error returned directly. No silent legacy
	// fallback limit applies here.
	ctrl0, err := Inspect(controllerPath)
	if err != nil {
		return err
	}
	cohortTimeout := graphWriterCohortTimeout(ctrl0, len(specs), workers)
	pumpCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pumpResult := make(chan error, 1)
	go func() {
		var adapter taskscheduler.Adapter = ScheduledDispatchAdapter{JournalPath: schedulePath}
		if isolated {
			adapter = memoryGatedScheduledDispatchAdapter{ScheduledDispatchAdapter: ScheduledDispatchAdapter{JournalPath: schedulePath}, gate: newGraphMemoryAdmissionGate(controllerPath, nil)}
		}
		pumpResult <- taskscheduler.Pump(pumpCtx, schedulePath, adapter, taskscheduler.PumpOptions{Workers: workers, PollInterval: 10 * time.Millisecond})
	}()
	deadline := time.Now().Add(cohortTimeout)
	handledClaims := map[string]bool{}
	for {
		schedule, err := taskscheduler.Inspect(schedulePath)
		if err != nil {
			cancel()
			return errors.Join(err, waitGraphPump(pumpResult, 5*time.Second))
		}
		ctrl, err := Inspect(controllerPath)
		if err != nil {
			cancel()
			return errors.Join(err, waitGraphPump(pumpResult, 5*time.Second))
		}
		allTerminal, unknownTask := true, ""
		for taskID, state := range schedule.Tasks {
			switch state.Status {
			case taskscheduler.StatusSucceeded, taskscheduler.StatusFailed, taskscheduler.StatusCancelled:
			case taskscheduler.StatusUnknown:
				unknownTask = taskID
				allTerminal = false
			default:
				allTerminal = false
			}
		}
		if unknownTask != "" {
			cancel()
			return errors.Join(fmt.Errorf("writer cohort task %q has an UNKNOWN effect", unknownTask), waitGraphPump(pumpResult, 5*time.Second))
		}
		if allTerminal {
			if cohortScopeReplanningEnabled(ctrl) && hasFailedWriterClaim(schedule) {
				cohort, cohortErr := InspectSettledGraphWriterCohort(controllerPath, schedulePath)
				if cohortErr == nil && settledCohortHasScopeViolation(cohort) {
					bound, inspectErr := Inspect(controllerPath)
					if inspectErr != nil {
						cancel()
						return errors.Join(inspectErr, waitGraphPump(pumpResult, 5*time.Second))
					}
					updated, replanErr := recordGraphScopeReplanCohort(ctx, controllerPath, bound, cohort)
					if replanErr == nil && len(updated.ScopeReplanRequests) > 0 && len(updated.ScopeReplanRequests) == len(updated.ScopeReplans) {
						_, replanErr = PrepareScopeReplannedGraphWriterBatch(ctx, controllerPath)
					}
					cancel()
					return errors.Join(replanErr, waitGraphPump(pumpResult, 5*time.Second))
				}
			}
			admittedSuccessor := false
			for taskID, state := range schedule.Tasks {
				if state.Status != taskscheduler.StatusFailed || state.Claim == nil || state.Evidence == nil {
					continue
				}
				claimID, claimErr := state.Claim.ID()
				if claimErr != nil {
					cancel()
					return errors.Join(claimErr, waitGraphPump(pumpResult, 5*time.Second))
				}
				if handledClaims[claimID] {
					continue
				}
				handledClaims[claimID] = true
				admitted, correctionErr := AfterSettledScheduledClaim(ctx, controllerPath, schedulePath, taskID)
				if correctionErr != nil {
					cancel()
					return errors.Join(fmt.Errorf("scheduled writer correction for %q could not be admitted: %w", taskID, correctionErr), waitGraphPump(pumpResult, 5*time.Second))
				}
				admittedSuccessor = admittedSuccessor || admitted
			}
			if admittedSuccessor {
				// The schedule value above is a terminal pre-admission snapshot.
				// Reinspect on the next pass so a just-added correction cannot be
				// mistaken for an incomplete settled cohort.
				continue
			}
			if !hasNonterminalScheduleTasks(schedule) {
				if !graphWriterCohortResultsReady(ctrl, schedule, specs, isolated) {
					cancel()
					return errors.Join(errors.New("writer cohort settled without a complete candidate-bound proposal set"), waitGraphPump(pumpResult, 5*time.Second))
				}
				cancel()
				return waitGraphPump(pumpResult, 5*time.Second)
			}
		}
		if time.Now().After(deadline) {
			cancel()
			return errors.Join(errors.New("writer cohort timed out before settled evidence"), waitGraphPump(pumpResult, 5*time.Second))
		}
		select {
		case <-ctx.Done():
			cancel()
			return errors.Join(ctx.Err(), waitGraphPump(pumpResult, 5*time.Second))
		case <-time.After(20 * time.Millisecond):
		}
		select {
		case err := <-pumpResult:
			if err != nil {
				return err
			}
			return errors.New("writer cohort pump exited before terminal evidence")
		default:
		}
	}
}

func hasFailedWriterClaim(schedule taskscheduler.Snapshot) bool {
	for _, state := range schedule.Tasks {
		if state.Status == taskscheduler.StatusFailed {
			return true
		}
	}
	return false
}

func hasNonterminalScheduleTasks(schedule taskscheduler.Snapshot) bool {
	for _, state := range schedule.Tasks {
		switch state.Status {
		case taskscheduler.StatusSucceeded, taskscheduler.StatusFailed, taskscheduler.StatusCancelled:
		default:
			return true
		}
	}
	return false
}

func settledCohortHasScopeViolation(cohort SettledGraphWriterCohort) bool {
	for _, member := range cohort.Members {
		if member.ScopeViolation {
			return true
		}
	}
	return false
}

func graphWriterCohortResultsReady(s Snapshot, schedule taskscheduler.Snapshot, specs []taskscheduler.TaskSpec, isolated bool) bool {
	for _, spec := range specs {
		record, ok := s.GraphWriterResults[spec.ID]
		if !ok || (record.Isolated != nil) != isolated {
			return false
		}
		wantInvocation := spec.InvocationID
		var latest *RoleSemanticCorrection
		for index := range s.RoleCorrections {
			correction := &s.RoleCorrections[index]
			if correction.TaskID == spec.ID && correction.ScheduledTaskID != "" && correction.Invocation.Profile.Role == "writer" {
				latest = correction
			}
		}
		if latest != nil {
			invocation, err := scheduledTurnInvocation(latest.Invocation, taskscheduler.OperationWriter, latest.ScheduledTaskID)
			state, ok := schedule.Tasks[latest.ScheduledTaskID]
			if err != nil || !ok || state.Status != taskscheduler.StatusSucceeded {
				return false
			}
			wantInvocation = invocation.ID
		}
		if graphWriterRecordInvocation(record).ID != wantInvocation {
			return false
		}
	}
	return true
}
