package control

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/faultlocalization"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

// RepairFinding preserves producer truth and identity. A reported location is
// advisory until its bytes are independently validated on the current candidate.
type RepairFinding struct {
	ID               string `json:"id"`
	CandidateID      string `json:"candidate_id"`
	Source           string `json:"source"`
	Category         string `json:"category"`
	Path             string `json:"path,omitempty"`
	Line             int    `json:"line,omitempty"`
	Column           int    `json:"column,omitempty"`
	Message          string `json:"message"`
	MessageShortened bool   `json:"message_shortened,omitempty"`
	EvidenceID       string `json:"evidence_id"`
	GateID           string `json:"gate_id"`
	CandidateBinding string `json:"candidate_binding"`
	LocationStatus   string `json:"location_status"`
	ClosureOracleID  string `json:"closure_oracle_id"`
}

// RepairSpecification proposes investigation inside an already admitted ready
// task's write paths. It grants no authority, retry, or finding closure.
type RepairSpecification struct {
	CandidateID       string   `json:"candidate_id"`
	TaskID            string   `json:"task_id"`
	FindingIDs        []string `json:"finding_ids"`
	AllowedWritePaths []string `json:"allowed_write_paths"`
	SuggestedStrategy string   `json:"suggested_strategy"`
	ClosureOracleIDs  []string `json:"closure_oracle_ids"`
	AnchorIDs         []string `json:"anchor_ids,omitempty"`
}

// RepairDiagnosis is a pure journal projection, not a filesystem observation or
// a persisted policy. Unknown invocations are never converted into failures.
type RepairDiagnosis struct {
	Version                     int                       `json:"version"`
	RunID                       string                    `json:"run_id"`
	ControllerHead              string                    `json:"controller_head"`
	CandidateID                 string                    `json:"candidate_id,omitempty"`
	Findings                    []RepairFinding           `json:"findings"`
	Specifications              []RepairSpecification     `json:"specifications"`
	VerificationPending         bool                      `json:"verification_pending"`
	VerificationClosureRecorded bool                      `json:"verification_closure_recorded"`
	RepairAttempts              int                       `json:"repair_attempts"`
	MaxRepairs                  *int                      `json:"max_repairs"`
	Authority                   string                    `json:"authority"`
	AnchoringStatus             string                    `json:"anchoring_status,omitempty"`
	Anchors                     []RepairAnchor            `json:"anchors,omitempty"`
	OmittedFindings             int                       `json:"omitted_findings,omitempty"`
	ProjectionHash              string                    `json:"projection_hash,omitempty"`
	Spectrum                    *faultlocalization.Report `json:"spectrum,omitempty"`
}

// DiagnoseRepair derives bounded findings and ready-task repair specifications
// from a validated snapshot. It neither reads candidate files nor appends events,
// changes invocation identity, dispatches a provider, or settles an effect.
func DiagnoseRepair(s Snapshot) (RepairDiagnosis, error) {
	r := RepairDiagnosis{Version: 1, RunID: s.RunID, ControllerHead: s.ControllerHead,
		Findings: []RepairFinding{}, Specifications: []RepairSpecification{},
		RepairAttempts: s.RepairAttempts, Authority: "advisory_only"}
	if s.Creation.Execution != nil {
		limit := s.Creation.Execution.MaxRepairs
		r.MaxRepairs = &limit
	}
	if s.Candidate != nil {
		id, err := s.Candidate.ID()
		if err != nil {
			return r, err
		}
		r.CandidateID = id
	}
	if err := r.addNativeFindings(s.Verification); err != nil {
		return r, err
	}
	if err := r.addReviewFindings(s); err != nil {
		return r, err
	}
	err := r.addReadySpecifications(s)
	return r, err
}

func (r *RepairDiagnosis) addNativeFindings(v *VerificationState) error {
	if v == nil {
		return nil
	}
	r.VerificationPending, r.VerificationClosureRecorded = v.Pending, v.Closure != nil
	evidence, err := canonical.Hash("harness.repair-verification.v1", v)
	if err != nil {
		return err
	}
	for _, o := range v.Observations {
		if o.Result.Status == "PASS" {
			continue
		}
		f := nativeRepairFinding(o, v.PlanID, evidence)
		if err := r.addFinding(f, o.Start.Index); err != nil {
			return err
		}
	}
	return nil
}

func nativeRepairFinding(o VerificationObservation, planID, evidence string) RepairFinding {
	f := RepairFinding{CandidateID: o.Result.CandidateID, Source: "native_check", Category: "check_unavailable",
		Message: "Recorded native check status: " + o.Result.Status, EvidenceID: evidence, GateID: planID,
		ClosureOracleID: o.Result.InvocationID, LocationStatus: "unlocalized"}
	// Only observed compiler diagnostics are parsed. An unavailable check
	// or an uncertain launch cannot establish a source-level defect.
	if o.Result.Status != "FAIL" {
		return f
	}
	f.Category = "check_failure"
	locations := goDiagnosticLocations(o.Result.Stdout.Excerpt + "\n" + o.Result.Stderr.Excerpt)
	switch len(locations) {
	case 0:
	case 1:
		f.Path, f.Line, f.Column = locations[0].path, locations[0].line, locations[0].column
		f.LocationStatus = "reported_unvalidated"
	default:
		f.LocationStatus = "ambiguous"
	}
	return f
}

func (r *RepairDiagnosis) addReviewFindings(s Snapshot) error {
	review, err := writerReviewContext(s)
	if err != nil {
		return err
	}
	if review == nil || review.Decision != "changes_requested" {
		return nil
	}
	for i, concern := range review.Findings {
		f := RepairFinding{CandidateID: review.CandidateID, Source: "reviewer", Category: "review_concern",
			Path: concern.Path, Message: concern.Message, MessageShortened: review.Shortened,
			EvidenceID: review.EvidenceHash, GateID: review.InvocationID, ClosureOracleID: review.InvocationID, LocationStatus: "unlocalized"}
		if f.Path != "" {
			if err := safepath.Relative(f.Path); err != nil {
				return errors.New("invalid finding path")
			}
			f.LocationStatus = "reported_unvalidated"
		}
		if err := r.addFinding(f, i); err != nil {
			return err
		}
	}
	return nil
}

func (r *RepairDiagnosis) addReadySpecifications(s Snapshot) error {
	// Do not manufacture permissions for a failed run or consume a repair slot.
	// The existing graph readiness/ownership gate remains the only source.
	if s.Graph == nil || r.CandidateID == "" || (s.State != "IMPLEMENTING" && s.State != "REPAIRING") {
		return nil
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		return err
	}
	for _, task := range ready {
		if task.Kind != engineeringplan.Implementation || task.ParentID == "" || len(task.WritePaths) == 0 {
			continue
		}
		gate, ok := s.Graph.Evidence[task.ParentID]
		if !ok || gate.Outcome != "failed" {
			continue
		}
		spec := r.specificationForGate(task, repairFailureID(gate))
		if len(spec.FindingIDs) != 0 {
			r.Specifications = append(r.Specifications, spec)
		}
	}
	return nil
}

func (r *RepairDiagnosis) specificationForGate(task engineeringplan.Task, gateID string) RepairSpecification {
	spec := RepairSpecification{CandidateID: r.CandidateID, TaskID: task.ID,
		FindingIDs: []string{}, AllowedWritePaths: append([]string(nil), task.WritePaths...),
		SuggestedStrategy: "localization_required", ClosureOracleIDs: []string{}}
	for _, f := range r.Findings {
		if r.findingMatchesCandidate(f) && f.GateID == gateID {
			spec.FindingIDs = append(spec.FindingIDs, f.ID)
			spec.ClosureOracleIDs = append(spec.ClosureOracleIDs, f.ClosureOracleID)
		}
	}
	return spec
}

func (r *RepairDiagnosis) addFinding(f RepairFinding, ordinal int) error {
	if len(r.Findings) >= 128 {
		return errors.New("repair finding bound exceeded")
	}
	id, err := canonical.Hash("harness.repair-finding.v1", struct {
		Finding RepairFinding `json:"finding"`
		Ordinal int           `json:"ordinal"`
	}{f, ordinal})
	if err != nil {
		return err
	}
	f.ID = id
	f.CandidateBinding = "stale_candidate"
	if r.CandidateID == "" {
		f.CandidateBinding = "candidate_unavailable"
	} else if f.CandidateID == r.CandidateID {
		f.CandidateBinding = "matching_recorded_candidate"
	}
	r.Findings = append(r.Findings, f)
	return nil
}

type diagnosticLocation struct {
	path         string
	line, column int
}

var goDiagnosticPattern = regexp.MustCompile(`^(.+\.go):([0-9]+):(?:([0-9]+):)?\s+.+$`)

func goDiagnosticLocations(text string) []diagnosticLocation {
	seen := map[diagnosticLocation]bool{}
	locations := []diagnosticLocation{}
	for _, line := range strings.Split(text, "\n") {
		location, ok := goDiagnosticLocation(line)
		if ok && !seen[location] {
			locations = append(locations, location)
			seen[location] = true
		}
	}
	return locations
}

func goDiagnosticLocation(line string) (diagnosticLocation, bool) {
	match := goDiagnosticPattern.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
	if match == nil {
		return diagnosticLocation{}, false
	}
	name := strings.TrimPrefix(strings.ReplaceAll(match[1], "\\", "/"), "./")
	if safepath.Relative(name) != nil {
		return diagnosticLocation{}, false
	}
	n, err := strconv.Atoi(match[2])
	if err != nil || n <= 0 {
		return diagnosticLocation{}, false
	}
	column := 0
	if match[3] != "" {
		column, err = strconv.Atoi(match[3])
		if err != nil || column <= 0 {
			return diagnosticLocation{}, false
		}
	}
	return diagnosticLocation{name, n, column}, true
}
