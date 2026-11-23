package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

const (
	reviewImpactContextVersion1 = 1
	reviewImpactMaxRecordBytes  = 900 << 10
	reviewImpactMaxGroupFiles   = 8
	reviewImpactMaxHistory      = 9 // initial review plus the maximum eight repairs
	reviewImpactRIUnavailable   = "candidate_review_ri_unavailable"
)

// reviewImpactCorpusInput is the source-free deterministic input needed to
// recompute one candidate topology. Candidate graph and its module inventory
// are stored once; source bytes are never retained here.
type reviewImpactCorpusInput struct {
	Version                   int                    `json:"version"`
	Source                    ri.Source              `json:"source"`
	CandidateID               string                 `json:"candidate_id"`
	CandidateFilesHash        string                 `json:"candidate_files_hash"`
	ProducerSHA256            string                 `json:"producer_sha256"`
	BaseGraphDigest           string                 `json:"base_graph_digest"`
	BaseModuleInventoryDigest string                 `json:"base_module_inventory_digest"`
	Graph                     ri.GoEngineeringGraph  `json:"graph"`
	ChangedPaths              []string               `json:"changed_paths"`
	AdmittedPaths             []string               `json:"admitted_paths"`
	DeletedPaths              []string               `json:"deleted_paths"`
	OmittedCount              int                    `json:"omitted_count"`
	Omissions                 []taskcontext.Omission `json:"omissions"`
	OmissionsTrimmed          bool                   `json:"omissions_trimmed"`
	Coverage                  string                 `json:"coverage"`
}

func reviewImpactCorpusFrom(result ri.GoCandidateCorpus) reviewImpactCorpusInput {
	return reviewImpactCorpusInput{
		Version: result.Version, Source: result.Source, CandidateID: result.CandidateID,
		CandidateFilesHash: result.CandidateFilesHash, ProducerSHA256: result.ProducerSHA256,
		BaseGraphDigest: result.BaseGraphDigest, BaseModuleInventoryDigest: result.BaseModuleInventoryDigest,
		Graph: result.Graph, ChangedPaths: append([]string{}, result.ChangedPaths...),
		AdmittedPaths: append([]string{}, result.AdmittedPaths...), DeletedPaths: append([]string{}, result.DeletedPaths...),
		OmittedCount: result.OmittedCount, Omissions: append([]taskcontext.Omission{}, result.Omissions...),
		OmissionsTrimmed: result.OmissionsTrimmed, Coverage: result.Coverage,
	}
}

func (input reviewImpactCorpusInput) corpus() (ri.GoCandidateCorpus, error) {
	if input.Graph.ModuleInventory == nil {
		return ri.GoCandidateCorpus{}, errors.New("candidate review graph lacks its module inventory")
	}
	return ri.GoCandidateCorpus{
		Version: input.Version, Source: input.Source, CandidateID: input.CandidateID,
		CandidateFilesHash: input.CandidateFilesHash, ProducerSHA256: input.ProducerSHA256,
		BaseGraphDigest: input.BaseGraphDigest, BaseModuleInventoryDigest: input.BaseModuleInventoryDigest,
		ModuleInventory: *input.Graph.ModuleInventory, Graph: input.Graph,
		ChangedPaths: append([]string{}, input.ChangedPaths...), AdmittedPaths: append([]string{}, input.AdmittedPaths...),
		DeletedPaths: append([]string{}, input.DeletedPaths...), OmittedCount: input.OmittedCount,
		Omissions: append([]taskcontext.Omission{}, input.Omissions...), OmissionsTrimmed: input.OmissionsTrimmed,
		Coverage: input.Coverage,
	}, nil
}

// ReviewImpactContextRecord is one source-free, candidate-bound review
// observation. Corpus and Projection are jointly replayed for available data;
// oversized observations retain an explicit count/omission summary instead.
type ReviewImpactContextRecord struct {
	Version                        int                           `json:"version"`
	RunID                          string                        `json:"run_id"`
	Source                         ri.Source                     `json:"source"`
	Candidate                      worktree.Candidate            `json:"candidate"`
	CandidateID                    string                        `json:"candidate_id"`
	CandidateFilesHash             string                        `json:"candidate_files_hash"`
	PlannerGoContextRecordID       string                        `json:"planner_go_context_record_id"`
	RIExecutableSHA256             string                        `json:"ri_executable_sha256"`
	BaseGraphDigest                string                        `json:"base_graph_digest"`
	BaseModuleInventoryDigest      string                        `json:"base_module_inventory_digest"`
	CandidateGraphDigest           string                        `json:"candidate_graph_digest"`
	CandidateModuleInventoryDigest string                        `json:"candidate_module_inventory_digest"`
	CandidateModuleInventory       *ri.GoModuleInventory         `json:"candidate_module_inventory,omitempty"`
	Corpus                         *reviewImpactCorpusInput      `json:"corpus,omitempty"`
	Projection                     *ri.GoCandidateReviewTopology `json:"projection,omitempty"`
	UnavailableReason              string                        `json:"unavailable_reason,omitempty"`
	ChangedPathCount               int                           `json:"changed_path_count"`
	AdmittedPathCount              int                           `json:"admitted_path_count"`
	DeletedPathCount               int                           `json:"deleted_path_count"`
	OmittedCount                   int                           `json:"omitted_count"`
	Omissions                      []taskcontext.Omission        `json:"omissions"`
	OmissionsTrimmed               bool                          `json:"omissions_trimmed"`
	RecordID                       string                        `json:"record_id"`
}

// ReviewImpactContextPrompt is the only review-impact data sent to the model.
// It contains bounded topology or an explicit unavailable summary, never the
// candidate graph corpus itself.
type ReviewImpactContextPrompt struct {
	Version                        int                           `json:"version"`
	Coverage                       string                        `json:"coverage"`
	CandidateID                    string                        `json:"candidate_id"`
	CandidateFilesHash             string                        `json:"candidate_files_hash"`
	CandidateGraphDigest           string                        `json:"candidate_graph_digest"`
	CandidateModuleInventoryDigest string                        `json:"candidate_module_inventory_digest"`
	Projection                     *ri.GoCandidateReviewTopology `json:"projection,omitempty"`
	UnavailableReason              string                        `json:"unavailable_reason,omitempty"`
	ChangedPathCount               int                           `json:"changed_path_count"`
	AdmittedPathCount              int                           `json:"admitted_path_count"`
	DeletedPathCount               int                           `json:"deleted_path_count"`
	OmittedCount                   int                           `json:"omitted_count"`
	Omissions                      []taskcontext.Omission        `json:"omissions"`
	OmissionsTrimmed               bool                          `json:"omissions_trimmed"`
}

func reviewImpactContextEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.ReviewImpactContextVersion == reviewImpactContextVersion1
}

func reviewImpactContextForCandidate(s Snapshot) *ReviewImpactContextRecord {
	if s.Candidate == nil {
		return nil
	}
	id, err := s.Candidate.ID()
	if err != nil {
		return nil
	}
	for index := range s.ReviewImpactContexts {
		record := &s.ReviewImpactContexts[index]
		if record.CandidateID == id && record.Candidate == *s.Candidate {
			return record
		}
	}
	return nil
}

func reviewImpactPromptFor(record *ReviewImpactContextRecord) *ReviewImpactContextPrompt {
	if record == nil {
		return nil
	}
	return &ReviewImpactContextPrompt{
		Version: record.Version, Coverage: "PARTIAL", CandidateID: record.CandidateID,
		CandidateFilesHash: record.CandidateFilesHash, CandidateGraphDigest: record.CandidateGraphDigest,
		CandidateModuleInventoryDigest: record.CandidateModuleInventoryDigest, Projection: record.Projection,
		UnavailableReason: record.UnavailableReason, ChangedPathCount: record.ChangedPathCount,
		AdmittedPathCount: record.AdmittedPathCount, DeletedPathCount: record.DeletedPathCount,
		OmittedCount: record.OmittedCount, Omissions: append([]taskcontext.Omission{}, record.Omissions...),
		OmissionsTrimmed: record.OmissionsTrimmed,
	}
}

func reviewImpactContextRecordID(record ReviewImpactContextRecord) (string, error) {
	record.RecordID = ""
	return canonical.Hash("harness.control.review-impact-context.v1", record)
}

func reviewImpactBase(s Snapshot) (ri.Source, ri.GoEngineeringGraph, ri.GoModuleInventory, error) {
	var source ri.Source
	var graph ri.GoEngineeringGraph
	var inventory ri.GoModuleInventory
	if !reviewImpactContextEnabled(s) || (s.Creation.Execution.PlannerContext != plannerContextGoContractV1 && s.Creation.Execution.PlannerContext != plannerContextGoContractV2 && s.Creation.Execution.PlannerContext != plannerContextGoContractV3) || s.PlannerGoContext == nil || (s.PlannerGoContext.Version != 4 && s.PlannerGoContext.Version != 5 && s.PlannerGoContext.Version != 6) || s.PlannerGoContext.Unavailable != "" || s.PlannerGoContext.Graph == nil || s.PlannerGoContext.ContractContext == nil || s.PlannerGoContext.Graph.ModuleInventory == nil {
		return source, graph, inventory, errors.New("review impact context requires admitted go-contract-context evidence")
	}
	identity := s.Creation.Repository
	if err := identity.Validate(); err != nil {
		return source, graph, inventory, err
	}
	var err error
	source, err = ri.FromRepository(identity)
	if err != nil {
		return source, graph, inventory, err
	}
	graph = *s.PlannerGoContext.Graph
	inventory = *graph.ModuleInventory
	if err := ri.ValidateGoEngineeringGraph(graph); err != nil || graph.SourceID != source.RepositoryID || graph.CandidateID != "" || graph.ProducerSHA256 != s.Creation.Execution.PlannerContextRIExecutableSHA256 {
		return source, graph, inventory, errors.New("review impact base graph binding mismatch")
	}
	if err := ri.ValidateGoModuleInventory(inventory, identity); err != nil || inventory.Digest != graph.ModuleInventory.Digest {
		return source, graph, inventory, errors.New("review impact base module inventory mismatch")
	}
	return source, graph, inventory, nil
}

// maybeAdmitReviewImpactContext persists the exact current candidate projection
// before reviewer dispatch. An already admitted exact candidate is reused
// without reads or another parser invocation.
func maybeAdmitReviewImpactContext(ctx context.Context, path string) error {
	s, err := Inspect(path)
	if err != nil {
		return err
	}
	if !reviewImpactContextEnabled(s) {
		return nil
	}
	if reviewImpactContextForCandidate(s) != nil {
		return nil
	}
	if err := validateReviewImpactCandidateBoundary(s); err != nil {
		return err
	}
	source, baseGraph, baseInventory, err := reviewImpactBase(s)
	if err != nil {
		return err
	}
	if err := s.Candidate.ValidateBinding(*s.Workspace); err != nil {
		return err
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return err
	}
	var admissionErr error
	admissionErr = lease.WithOwnership(s.Workspace.Request, func(worktree.LeaseIdentity) error {
		before, _, err := worktree.Capture(ctx, *s.Workspace)
		if err != nil || before != *s.Candidate {
			if err != nil {
				return err
			}
			return errors.New("review impact candidate changed before collection")
		}
		policy := s.Creation.Execution
		client := ri.Client{Executable: policy.PlannerContextRIExecutable, ExecutableHash: policy.PlannerContextRIExecutableSHA256}
		cacheDir := ""
		if policy.CandidateFactsCacheVersion == 1 {
			cacheDir, err = ensureCandidateFactsCacheDir(s.Creation.Repository, policy.PlannerContextRIExecutableSHA256)
			cacheDir, err = optionalCacheDirectory(cacheDir, err)
			if err != nil {
				return err
			}
		}
		corpus, err := ri.CollectCandidateGoCorpus(ctx, s.Creation.Repository, *s.Workspace, *s.Candidate, baseGraph, baseInventory, client, cacheDir)
		if err != nil {
			if !ri.IsProcessUnavailableOnly(err) {
				return err
			}
			record, recordErr := makeReviewImpactRIUnavailableRecord(s, source, baseGraph, baseInventory)
			if recordErr != nil {
				return recordErr
			}
			if recordErr = validateReviewImpactContextRecord(s, record); recordErr != nil {
				return recordErr
			}
			// Re-capture after the optional RI failure before persisting fallback
			// evidence. The admitted baseline review must still bind this exact
			// candidate; process unavailability never relaxes candidate identity.
			current, _, captureErr := worktree.Capture(ctx, *s.Workspace)
			if captureErr != nil || current != *s.Candidate {
				if captureErr != nil {
					return captureErr
				}
				return errors.New("review impact candidate changed after RI became unavailable")
			}
			if appendErr := Append(path, "review.impact-context-admitted", record); appendErr != nil {
				return appendErr
			}
			after, _, captureErr := worktree.Capture(ctx, *s.Workspace)
			if captureErr != nil || after != *s.Candidate {
				if captureErr != nil {
					return captureErr
				}
				return errors.New("review impact candidate changed during fallback admission")
			}
			return nil
		}
		if err := ri.ValidateCandidateGoModuleInventory(corpus.ModuleInventory, s.Creation.Repository, *s.Candidate, baseInventory); err != nil {
			return err
		}
		record, err := makeReviewImpactContextRecord(s, source, baseGraph, baseInventory, corpus)
		if err != nil {
			return err
		}
		if err := validateReviewImpactContextRecord(s, record); err != nil {
			return err
		}
		current, _, err := worktree.Capture(ctx, *s.Workspace)
		if err != nil || current != *s.Candidate {
			if err != nil {
				return err
			}
			return errors.New("review impact candidate changed before admission")
		}
		if err := Append(path, "review.impact-context-admitted", record); err != nil {
			return err
		}
		after, _, err := worktree.Capture(ctx, *s.Workspace)
		if err != nil || after != *s.Candidate {
			if err != nil {
				return err
			}
			return errors.New("review impact candidate changed during admission")
		}
		return nil
	})
	return errors.Join(admissionErr, lease.Close())
}

func makeReviewImpactRIUnavailableRecord(s Snapshot, source ri.Source, baseGraph ri.GoEngineeringGraph, baseInventory ri.GoModuleInventory) (ReviewImpactContextRecord, error) {
	if s.Candidate == nil || s.PlannerGoContext == nil || s.Creation.Execution == nil {
		return ReviewImpactContextRecord{}, errors.New("review impact fallback lacks candidate or planner binding")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return ReviewImpactContextRecord{}, err
	}
	return sealReviewImpactRecord(ReviewImpactContextRecord{
		Version: reviewImpactContextVersion1, RunID: s.RunID, Source: source, Candidate: *s.Candidate,
		CandidateID: candidateID, CandidateFilesHash: s.Candidate.FilesHash,
		PlannerGoContextRecordID: s.PlannerGoContext.RecordID,
		RIExecutableSHA256:       s.Creation.Execution.PlannerContextRIExecutableSHA256,
		BaseGraphDigest:          baseGraph.Digest, BaseModuleInventoryDigest: baseInventory.Digest,
		UnavailableReason: reviewImpactRIUnavailable,
		Omissions:         []taskcontext.Omission{},
	})
}

func makeReviewImpactContextRecord(s Snapshot, source ri.Source, baseGraph ri.GoEngineeringGraph, baseInventory ri.GoModuleInventory, corpus ri.GoCandidateCorpus) (ReviewImpactContextRecord, error) {
	if s.Candidate == nil || s.PlannerGoContext == nil || s.Creation.Execution == nil {
		return ReviewImpactContextRecord{}, errors.New("review impact context lacks candidate or planner binding")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return ReviewImpactContextRecord{}, err
	}
	record := ReviewImpactContextRecord{
		Version: reviewImpactContextVersion1, RunID: s.RunID, Source: source, Candidate: *s.Candidate,
		CandidateID: candidateID, CandidateFilesHash: s.Candidate.FilesHash,
		PlannerGoContextRecordID: s.PlannerGoContext.RecordID,
		RIExecutableSHA256:       s.Creation.Execution.PlannerContextRIExecutableSHA256,
		BaseGraphDigest:          baseGraph.Digest, BaseModuleInventoryDigest: baseInventory.Digest,
		CandidateGraphDigest: corpus.Graph.Digest, CandidateModuleInventoryDigest: corpus.ModuleInventory.Digest,
		ChangedPathCount: len(corpus.ChangedPaths), AdmittedPathCount: len(corpus.AdmittedPaths),
		DeletedPathCount: len(corpus.DeletedPaths), OmittedCount: corpus.OmittedCount,
		Omissions: append([]taskcontext.Omission{}, corpus.Omissions...), OmissionsTrimmed: corpus.OmissionsTrimmed,
	}
	input := reviewImpactCorpusFrom(corpus)
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return ReviewImpactContextRecord{}, err
	}
	if len(inputBytes) > reviewImpactMaxRecordBytes-(16<<10) {
		record.UnavailableReason = "candidate_review_record_budget"
		record.CandidateModuleInventory = cloneGoModuleInventory(corpus.ModuleInventory)
		return sealReviewImpactRecord(record)
	}
	projection, err := ri.QueryGoCandidateReviewTopology(baseGraph, baseInventory, corpus, reviewImpactMaxGroupFiles)
	if err != nil {
		return ReviewImpactContextRecord{}, err
	}
	record.Corpus = &input
	record.Projection = &projection
	return sealReviewImpactRecord(record)
}

func sealReviewImpactRecord(record ReviewImpactContextRecord) (ReviewImpactContextRecord, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return ReviewImpactContextRecord{}, err
	}
	if len(encoded) > reviewImpactMaxRecordBytes {
		return ReviewImpactContextRecord{}, errors.New("review impact fallback record exceeds bounded size")
	}
	record.RecordID, err = reviewImpactContextRecordID(record)
	if err != nil {
		return ReviewImpactContextRecord{}, err
	}
	if err := boundedCanonical(record, reviewImpactMaxRecordBytes); err != nil {
		return ReviewImpactContextRecord{}, err
	}
	return record, nil
}

func cloneGoModuleInventory(inventory ri.GoModuleInventory) *ri.GoModuleInventory {
	copy := inventory
	copy.Files = append([]ri.GoManifestObservation{}, inventory.Files...)
	copy.Omissions = append([]ri.GoManifestOmission{}, inventory.Omissions...)
	return &copy
}

func replayReviewImpactContext(s *Snapshot, record ReviewImpactContextRecord) error {
	if s == nil {
		return errors.New("review impact context has no snapshot")
	}
	if s.reviewImpactContextForCandidate(record.CandidateID) != nil {
		return errors.New("duplicate review impact candidate record")
	}
	if len(s.ReviewImpactContexts) >= reviewImpactHistoryLimit(s) {
		return errors.New("review impact context history exceeds the run repair bound")
	}
	if err := validateReviewImpactContextRecord(*s, record); err != nil {
		return err
	}
	s.ReviewImpactContexts = append(s.ReviewImpactContexts, record)
	return nil
}

func reviewImpactHistoryLimit(s *Snapshot) int {
	if s == nil || s.Creation.Execution == nil {
		return 0
	}
	limit := s.Creation.Execution.MaxRepairs + 1
	if limit > reviewImpactMaxHistory {
		return reviewImpactMaxHistory
	}
	return limit
}

// validateReviewImpactCandidateBoundary permits only a settled
// changes-requested review for an older candidate to remain in the snapshot.
func validateReviewImpactCandidateBoundary(s Snapshot) error {
	if s.State != "REVIEWING" || s.Workspace == nil || s.Candidate == nil {
		return errors.New("review impact context requires an undispatched verified candidate")
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		return err
	}
	currentCandidateID, err := s.Candidate.ID()
	if err != nil {
		return err
	}
	if s.Review == nil {
		if s.ReviewHost != nil {
			return errors.New("review impact context has a host without a completed review")
		}
		return nil
	}

	prior := s.Review
	var verdict ReviewVerdict
	if err := canonical.Decode([]byte(prior.Result.Output), &verdict); err != nil {
		return errors.New("review impact context prior review verdict is malformed")
	}
	var invocationInput struct {
		CandidateID        string `json:"candidate_id"`
		VerificationPlanID string `json:"verification_plan_id"`
	}
	// The full historical review input has many other members. Its exact
	// invocation identity and original review event were already replayed; here
	// extract the two binding fields without rejecting those other members.
	if err := json.Unmarshal([]byte(prior.Invocation.Input), &invocationInput); err != nil ||
		invocationInput.CandidateID != verdict.CandidateID || invocationInput.VerificationPlanID != verdict.VerificationPlanID {
		return errors.New("review impact context prior invocation does not bind its verdict")
	}
	if safepath.RequireDigest(verdict.CandidateID) != nil || verdict.CandidateID == currentCandidateID || verdict.Decision != "changes_requested" || len(verdict.Findings) == 0 {
		return errors.New("review impact context prior review is not a settled finding for an older candidate")
	}
	if err := requireOpenCodeRoleReceipt(s, prior.Invocation, prior.Result); err != nil {
		return errors.New("review impact context prior review lacks its provider receipt")
	}
	if prior.Invocation.Profile.Runtime == "codex-app-server" {
		host := s.ReviewHost
		if host == nil || !host.Ready || host.Receipt == nil || host.RuntimeReceipt == nil || host.Intent.Invocation != prior.Invocation {
			return errors.New("review impact context prior host result is incomplete or mismatched")
		}
		runtimeReceipt := host.RuntimeReceipt
		resultHash, hashErr := canonical.Hash("harness.review-result.v1", prior.Result)
		if hashErr != nil || runtimeReceipt.InvocationID != prior.Invocation.ID || runtimeReceipt.ResultHash != resultHash {
			return errors.New("review impact context prior host receipt does not bind its result")
		}
	} else if s.ReviewHost != nil {
		return errors.New("review impact context prior host does not match its runtime")
	}
	return nil
}

func (s Snapshot) reviewImpactContextForCandidate(candidateID string) *ReviewImpactContextRecord {
	for index := range s.ReviewImpactContexts {
		if s.ReviewImpactContexts[index].CandidateID == candidateID {
			return &s.ReviewImpactContexts[index]
		}
	}
	return nil
}

func validateReviewImpactContextRecord(s Snapshot, record ReviewImpactContextRecord) error {
	if !reviewImpactContextEnabled(s) || (s.Creation.Execution.PlannerContext != plannerContextGoContractV1 && s.Creation.Execution.PlannerContext != plannerContextGoContractV2 && s.Creation.Execution.PlannerContext != plannerContextGoContractV3) || s.Candidate == nil || s.Workspace == nil || s.PlannerGoContext == nil || s.PlannerGoContext.Graph == nil || s.PlannerGoContext.Graph.ModuleInventory == nil {
		return errors.New("review impact context transition rejected")
	}
	if err := validateReviewImpactCandidateBoundary(s); err != nil {
		return errors.Join(errors.New("review impact context transition rejected"), err)
	}
	source, baseGraph, baseInventory, err := reviewImpactBase(s)
	if err != nil {
		return err
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return err
	}
	if record.Version != reviewImpactContextVersion1 || record.RunID != s.RunID || record.Source != source || record.Candidate != *s.Candidate || record.CandidateID != candidateID || record.CandidateFilesHash != s.Candidate.FilesHash || record.PlannerGoContextRecordID != s.PlannerGoContext.RecordID || record.RIExecutableSHA256 != s.Creation.Execution.PlannerContextRIExecutableSHA256 || record.BaseGraphDigest != baseGraph.Digest || record.BaseModuleInventoryDigest != baseInventory.Digest || record.ChangedPathCount < 0 || record.AdmittedPathCount < 0 || record.DeletedPathCount < 0 || record.OmittedCount < 0 || record.OmittedCount < len(record.Omissions) || len(record.Omissions) > 64 || record.OmissionsTrimmed != (record.OmittedCount > len(record.Omissions)) {
		return errors.New("review impact context binding mismatch")
	}
	if record.UnavailableReason == reviewImpactRIUnavailable {
		if record.CandidateGraphDigest != "" || record.CandidateModuleInventoryDigest != "" || record.Corpus != nil || record.Projection != nil || record.CandidateModuleInventory != nil || record.ChangedPathCount != 0 || record.AdmittedPathCount != 0 || record.DeletedPathCount != 0 || record.OmittedCount != 0 || len(record.Omissions) != 0 || record.OmissionsTrimmed {
			return errors.New("RI-unavailable review impact record contains unobserved candidate analysis")
		}
	} else if safepath.RequireDigest(record.CandidateGraphDigest) != nil || safepath.RequireDigest(record.CandidateModuleInventoryDigest) != nil {
		return errors.New("review impact context candidate graph binding missing")
	}
	if err := record.Candidate.ValidateBinding(*s.Workspace); err != nil {
		return errors.New("review impact candidate workspace binding mismatch")
	}
	for _, omission := range record.Omissions {
		if omission.Path == "[redacted]" {
			if omission.Reason != "unsafe_or_sensitive_path" {
				return errors.New("review impact omission is invalid")
			}
		} else if safepath.Relative(omission.Path) != nil || !taskcontext.EligiblePath(omission.Path) || omission.Reason == "unsafe_or_sensitive_path" || strings.TrimSpace(omission.Reason) == "" || len(omission.Reason) > 64 {
			return errors.New("review impact omission is invalid")
		}
	}
	if record.UnavailableReason == "" {
		if record.Corpus == nil || record.Projection == nil || record.CandidateModuleInventory != nil {
			return errors.New("review impact projection or corpus is missing")
		}
		corpus, err := record.Corpus.corpus()
		if err != nil {
			return err
		}
		if record.CandidateGraphDigest != corpus.Graph.Digest || record.CandidateModuleInventoryDigest != corpus.ModuleInventory.Digest || record.ChangedPathCount != len(corpus.ChangedPaths) || record.AdmittedPathCount != len(corpus.AdmittedPaths) || record.DeletedPathCount != len(corpus.DeletedPaths) || record.OmittedCount != corpus.OmittedCount || record.OmissionsTrimmed != corpus.OmissionsTrimmed || !reflect.DeepEqual(record.Omissions, corpus.Omissions) {
			return errors.New("review impact summary differs from corpus")
		}
		if err := ri.ValidateCandidateGoModuleInventory(corpus.ModuleInventory, s.Creation.Repository, *s.Candidate, baseInventory); err != nil {
			return err
		}
		if err := ri.ValidateGoCandidateReviewTopology(*record.Projection, baseGraph, baseInventory, corpus, reviewImpactMaxGroupFiles); err != nil {
			return err
		}
	} else {
		if record.UnavailableReason == reviewImpactRIUnavailable {
			if record.Corpus != nil || record.Projection != nil || record.CandidateModuleInventory != nil {
				return errors.New("RI-unavailable review impact record contains candidate projection data")
			}
		} else if record.UnavailableReason != "candidate_review_record_budget" || record.Corpus != nil || record.Projection != nil || record.CandidateModuleInventory == nil {
			return errors.New("invalid unavailable review impact record")
		} else {
			if err := ri.ValidateCandidateGoModuleInventory(*record.CandidateModuleInventory, s.Creation.Repository, *s.Candidate, baseInventory); err != nil {
				return err
			}
		}
	}
	id, err := reviewImpactContextRecordID(record)
	if err != nil || id != record.RecordID || safepath.RequireDigest(record.RecordID) != nil {
		return errors.New("review impact context record identity mismatch")
	}
	return boundedCanonical(record, reviewImpactMaxRecordBytes)
}
