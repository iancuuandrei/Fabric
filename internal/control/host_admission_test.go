package control

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/hostenvironment"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/runtime"
)

type hostVerifierFunc func(context.Context) (hostenvironment.VerifiedSandbox, error)

func (f hostVerifierFunc) VerifyInherited(ctx context.Context) (hostenvironment.VerifiedSandbox, error) {
	return f(ctx)
}

func hostObservation(t *testing.T, verified bool) hostenvironment.Observation {
	t.Helper()
	in := hostenvironment.Input{GOOS: "windows", Environ: []string{"PATH=fixture"}}
	if verified {
		in.Verifier = hostVerifierFunc(func(context.Context) (hostenvironment.VerifiedSandbox, error) {
			return hostenvironment.VerifiedSandbox{Mode: hostenvironment.SandboxWorkspaceWrite, Source: "fixture-verifier", Inherited: true}, nil
		})
	}
	result, err := hostenvironment.Observe(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHostAdmissionBindsCreationAndReplaysHistoricalReport(t *testing.T) {
	c := creation(t)
	bound, err := BindHostAdmission(c, hostObservation(t, false), DefaultHostPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if bound.HostAdmission == nil || bound.HostAdmission.BindingHash == "" {
		t.Fatal("host admission was not bound")
	}
	path := t.TempDir() + "/run.jsonl"
	if err := Append(path, "run.created", bound); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Creation.HostAdmission == nil || snapshot.Creation.HostAdmission.BindingHash != bound.HostAdmission.BindingHash {
		t.Fatal("replay lost immutable host admission", snapshot.Creation.HostAdmission)
	}
	if err := RequireHostAdmission(snapshot, hostObservation(t, false)); err != nil {
		t.Fatal("default host policy rejected a fresh native observation", err)
	}
}

func TestHostAdmissionRejectsUnavailableRequiredVerificationBeforeCreation(t *testing.T) {
	policy := DefaultHostPolicy()
	policy.RequireVerifiedSandbox = true
	if _, err := BindHostAdmission(creation(t), hostObservation(t, false), policy); err == nil || !strings.Contains(err.Error(), "requires verified") {
		t.Fatal("unverified observation satisfied required sandbox policy", err)
	}
}

func TestResumePlanningRechecksFreshHostBeforeJournalMutation(t *testing.T) {
	policy := DefaultHostPolicy()
	policy.RequireVerifiedSandbox = true
	policy.AllowedSandboxModes = []hostenvironment.SandboxMode{hostenvironment.SandboxWorkspaceWrite}
	bound, err := BindHostAdmission(creation(t), hostObservation(t, true), policy)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/run.jsonl"
	if err := Append(path, "run.created", bound); err != nil {
		t.Fatal(err)
	}
	before, err := journal.ExportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResumePlanning(context.Background(), path); err == nil || !strings.Contains(err.Error(), "requires verified") {
		t.Fatal("planning proceeded without fresh verified host evidence", err)
	}
	after, err := journal.ExportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed host admission mutated the run journal")
	}
	if _, err := Inspect(path); err != nil {
		t.Fatal("historical verified report did not replay", err)
	}
}

func TestHostAdmissionBindingRejectsPostBindMutation(t *testing.T) {
	bound, err := BindHostAdmission(creation(t), hostObservation(t, false), DefaultHostPolicy())
	if err != nil {
		t.Fatal(err)
	}
	bound.HostAdmission.Observation.Platform = "linux"
	path := t.TempDir() + "/run.jsonl"
	if err := Append(path, "run.created", bound); err == nil || !strings.Contains(err.Error(), "binding mismatch") {
		t.Fatal("mutated host admission was accepted", err)
	}
	events, err := journal.Read(path)
	if err != nil || len(events) != 0 {
		t.Fatal("rejected creation changed journal", len(events), err)
	}
}

func TestLegacyCreationRemainsCompatible(t *testing.T) {
	c := creation(t)
	path := t.TempDir() + "/legacy.jsonl"
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Creation.HostAdmission != nil {
		t.Fatal("legacy creation gained a host admission")
	}
	if err := RequireHostAdmission(snapshot, hostenvironment.Observation{}); err != nil {
		t.Fatal("legacy run was rejected", err)
	}
}

func TestRequiredHostVerificationGuardsLaunchCoverage(t *testing.T) {
	policy := DefaultHostPolicy()
	policy.RequireVerifiedSandbox = true
	policy.AllowedSandboxModes = []hostenvironment.SandboxMode{hostenvironment.SandboxWorkspaceWrite}
	bound, err := BindHostAdmission(creation(t), hostObservation(t, true), policy)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/coverage.jsonl"
	if err := Append(path, "run.created", bound); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := journal.ExportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	authorization := effects.Authorization{}
	checks := map[string]func(context.Context) error{
		"planning": func(ctx context.Context) error { _, err := ResumePlanning(ctx, path); return err },
		"explorer": func(ctx context.Context) error { _, err := RunExplorer(ctx, path, "question"); return err },
		"reviewer": func(ctx context.Context) error { _, err := RunReview(ctx, path); return err },
		"writer":   func(ctx context.Context) error { _, err := RunWriter(ctx, path); return err },
		"direct provider": func(ctx context.Context) error {
			_, _, err := executeDirectProvider(ctx, path, snapshot.Creation.Config, snapshot.RunID, runtime.Invocation{})
			return err
		},
		"OpenCode provider": func(ctx context.Context) error {
			_, _, err := executeOpenCodeProvider(ctx, path, snapshot, runtime.Invocation{}, nil)
			return err
		},
		"verification": func(ctx context.Context) error { _, err := Verify(ctx, path); return err },
		"workspace":    func(ctx context.Context) error { _, err := StartWorkspace(ctx, path); return err },
		"file apply": func(ctx context.Context) error {
			_, err := ApplyFiles(ctx, path, PreparedFiles{}, authorization)
			return err
		},
		"file recovery": func(ctx context.Context) error {
			_, err := RecoverFiles(ctx, path, PreparedRecovery{}, authorization)
			return err
		},
		"commit": func(ctx context.Context) error {
			_, err := ExecuteCommit(ctx, path, PreparedCommit{}, authorization)
			return err
		},
		"commit recovery": func(ctx context.Context) error {
			_, err := RecoverCommit(ctx, path, PreparedCommitRecovery{}, authorization)
			return err
		},
		"commit lease": func(ctx context.Context) error {
			_, err := RecoverCommitLease(ctx, path, PreparedCommitLease{}, authorization)
			return err
		},
		"push": func(ctx context.Context) error {
			_, err := ExecutePush(ctx, path, PreparedPush{}, authorization)
			return err
		},
		"push lease": func(ctx context.Context) error {
			_, err := RecoverPushLease(ctx, path, PreparedPushLease{}, authorization)
			return err
		},
		"draft": func(ctx context.Context) error {
			_, err := ExecuteDraft(ctx, path, PreparedDraft{}, authorization, nil)
			return err
		},
		"draft lease": func(ctx context.Context) error {
			_, err := RecoverDraftLease(ctx, path, PreparedDraftLease{}, authorization, nil)
			return err
		},
		"RI producer": func(ctx context.Context) error {
			_, err := ExecuteRIProducer(ctx, path, ri.ProducerPlan{}, authorization)
			return err
		},
		"RI import": func(ctx context.Context) error {
			_, err := ExecuteRIImport(ctx, path, ri.ImportPlan{}, authorization)
			return err
		},
		"RI lexical": func(ctx context.Context) error {
			_, err := ExecuteRILexical(ctx, path, ri.LexicalPlan{}, ri.LexicalManifest{}, authorization)
			return err
		},
		"RI overlay": func(ctx context.Context) error {
			_, err := ExecuteLexicalOverlay(ctx, path, PreparedLexicalOverlay{}, authorization)
			return err
		},
		"RI publish":          func(ctx context.Context) error { _, err := ExecuteRIPublish(ctx, path, "", authorization); return err },
		"RI publish recovery": func(ctx context.Context) error { _, err := RecoverRIPublish(ctx, path, authorization); return err },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(context.Background()); err == nil || !strings.Contains(err.Error(), "requires verified") {
				t.Fatal("launch was not stopped by fresh host admission", err)
			}
		})
	}
	after, err := journal.ExportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected launch coverage mutated journal")
	}
}
