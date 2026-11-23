package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
)

func TestPlannerPPRFlagIsContextVersioned(t *testing.T) {
	for _, test := range []struct {
		mode    string
		version int
		wantErr bool
	}{
		{mode: "", version: 0},
		{mode: autonomousPlannerContextGoSourceV2, version: 0},
		{mode: autonomousPlannerContextGoSourceV2, version: 1},
		{mode: autonomousPlannerContextGoSourceV1, version: 0},
		{mode: autonomousPlannerContextGoSourceV1, version: 1, wantErr: true},
		{mode: autonomousPlannerContextGoContractV1, version: 1, wantErr: true},
		{mode: autonomousPlannerContextGoContractV2, version: 1, wantErr: true},
		{mode: autonomousPlannerContextGoContractV3, version: 1, wantErr: true},
		{mode: autonomousPlannerContextSourceBoundedV1, version: 1, wantErr: true},
		{mode: autonomousPlannerContextGoSourceV2, version: 2, wantErr: true},
	} {
		err := validatePlannerPPRVersion(test.mode, test.version)
		if (err != nil) != test.wantErr {
			t.Fatalf("unexpected planner-ppr validation for mode=%q version=%d: %v", test.mode, test.version, err)
		}
	}
}

func TestAutonomousPlannerPPRRejectsWrongModePreRun(t *testing.T) {
	root := autonomousCLIFixture(t)
	for _, args := range [][]string{
		{"run", "--autonomous", "--planner-ppr", "objective"},
		{"run", "--autonomous", "--planner-context", autonomousPlannerContextGoSourceV1, "--planner-ppr", "objective"},
		{"run", "--autonomous", "--planner-context", autonomousPlannerContextGoContractV1, "--planner-ppr", "objective"},
		{"run", "--autonomous", "--planner-context", autonomousPlannerContextSourceBoundedV1, "--planner-ppr", "objective"},
	} {
		var out bytes.Buffer
		if err := Execute(context.Background(), args, root, &out); err == nil {
			t.Errorf("accepted invalid planner-ppr invocation %q", strings.Join(args, " "))
		}
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); err != nil || len(entries) != 0 {
		t.Fatalf("invalid planner-ppr options created a run: %v, %v", entries, err)
	}
}

// TestAutonomousPlannerPPRBindsOptIn opts v2 planning into the bounded PPR
// treatment with an explicit parser pin and proves the immutable policy
// carries version 1 without starting any provider.
func TestAutonomousPlannerPPRBindsOptIn(t *testing.T) {
	parser, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(parser)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	parserHash := hex.EncodeToString(sum[:])
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err = Execute(context.Background(), []string{
		"run", "--autonomous", "--prepare-only", "--max-parallel", "1",
		"--planner-context", autonomousPlannerContextGoSourceV2,
		"--planner-ppr",
		"--planner-context-ri-executable", parser,
		"--planner-context-ri-executable-sha256", parserHash,
		"A bounded PPR fixture objective",
	}, root, &out)
	if err == nil {
		t.Fatal("fixture should stop before external role dispatch")
	}
	var failure autonomousFailure
	if decodeErr := json.Unmarshal(out.Bytes(), &failure); decodeErr != nil || failure.RunID == "" {
		t.Fatalf("PPR admission failure did not identify the prepared run: %s (%v)", out.String(), decodeErr)
	}
	s, err := control.Inspect(filepath.Join(root, ".harness", "runs", failure.RunID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	policy := s.Creation.Execution
	if policy == nil || policy.PlannerContext != autonomousPlannerContextGoSourceV2 || policy.PlannerPPRVersion != 1 ||
		policy.PlannerContextRIExecutable != parser || policy.PlannerContextRIExecutableSHA256 != parserHash {
		t.Fatalf("planner PPR opt-in was not bound unchanged in run creation: %#v", policy)
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("bound PPR policy does not validate: %v", err)
	}
}

// TestAutonomousPlannerPPRAbsencePreservesLegacy proves an unflagged v2 run
// keeps PlannerPPRVersion zero and the historical policy encoding.
func TestAutonomousPlannerPPRAbsencePreservesLegacy(t *testing.T) {
	parser, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(parser)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	parserHash := hex.EncodeToString(sum[:])
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err = Execute(context.Background(), []string{
		"run", "--autonomous", "--prepare-only", "--max-parallel", "1",
		"--planner-context", autonomousPlannerContextGoSourceV2,
		"--planner-context-ri-executable", parser,
		"--planner-context-ri-executable-sha256", parserHash,
		"A bounded legacy fixture objective",
	}, root, &out)
	if err == nil {
		t.Fatal("fixture should stop before external role dispatch")
	}
	var failure autonomousFailure
	if decodeErr := json.Unmarshal(out.Bytes(), &failure); decodeErr != nil || failure.RunID == "" {
		t.Fatalf("legacy failure did not identify the locally prepared run: %s (%v)", out.String(), decodeErr)
	}
	s, err := control.Inspect(filepath.Join(root, ".harness", "runs", failure.RunID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	policy := s.Creation.Execution
	if policy == nil || policy.PlannerPPRVersion != 0 {
		t.Fatalf("absence did not preserve legacy PPR policy: %#v", policy)
	}
	policyBytes, err := canonical.Bytes(policy)
	if err != nil || strings.Contains(string(policyBytes), "planner_ppr_version") {
		t.Fatalf("disabled PPR changed the legacy policy encoding: %s (%v)", policyBytes, err)
	}
}

func TestInspectPlanReportsPlannerPPR(t *testing.T) {
	parser, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(parser)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	parserHash := hex.EncodeToString(sum[:])
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--inspect-plan",
		"--planner-context", autonomousPlannerContextGoSourceV2, "--planner-ppr",
		"--planner-context-ri-executable", parser,
		"--planner-context-ri-executable-sha256", parserHash}, root, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"planner_ppr":1`) {
		t.Fatalf("inspect-plan omits the PPR opt-in: %s", out.String())
	}
	var off bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--inspect-plan"}, root, &off); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(off.String(), `"planner_ppr":0`) {
		t.Fatalf("inspect-plan omits the legacy PPR default: %s", off.String())
	}
}
