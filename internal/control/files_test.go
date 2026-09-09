package control

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
)

func textPointer(s string) *string { return &s }
func content64(s string) *string   { return textPointer(base64.StdEncoding.EncodeToString([]byte(s))) }
func digestText(s string) *string {
	h := sha256.Sum256([]byte(s))
	return textPointer(hex.EncodeToString(h[:]))
}

func writer(t *testing.T) (string, Snapshot) {
	t.Helper()
	p, _ := approvedRepository(t)
	s, err := StartWorkspace(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return p, s
}
func prepareChange(t *testing.T, p string) PreparedFiles {
	t.Helper()
	prepared, err := PrepareFiles(context.Background(), p, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("base\n"), ContentBase64: content64("after\n")}, {Path: "nested/binary.dat", ContentBase64: content64("\x00\xffbinary")}})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
func authorize(t *testing.T, p PreparedFiles) effects.Authorization {
	t.Helper()
	id, err := p.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	return effects.Authorization{IntentID: id, Actor: "explicit-operator"}
}

func TestApprovedFileEffectsPreserveSourceAndRejectStaleApproval(t *testing.T) {
	p, s := writer(t)
	prepared := prepareChange(t, p)
	wrong := authorize(t, prepared)
	wrong.IntentID = "stale"
	if _, err := ApplyFiles(context.Background(), p, prepared, wrong); err == nil {
		t.Fatal("stale approval admitted")
	}
	before, err := Inspect(p)
	if err != nil || before.FileIntent != nil {
		t.Fatal("unauthorized effect recorded", err)
	}
	result, err := ApplyFiles(context.Background(), p, prepared, authorize(t, prepared))
	if err != nil {
		t.Fatal(err)
	}
	if result.FileOutcome != "CONFIRMED" || result.Candidate == nil || *result.Candidate != prepared.Proposal.After {
		t.Fatal("after-state mismatch")
	}
	raw, err := os.ReadFile(filepath.Join(s.Workspace.Request.Path, "nested", "binary.dat"))
	if err != nil || string(raw) != "\x00\xffbinary" {
		t.Fatal("binary bytes changed", err)
	}
	source, err := os.ReadFile(filepath.Join(s.Creation.Repository.Root, "file.txt"))
	if err != nil || string(source) != "base\n" {
		t.Fatal("canonical source changed", err)
	}
	if _, err := ApplyFiles(context.Background(), p, prepared, authorize(t, prepared)); err == nil {
		t.Fatal("old effect repeated")
	}
	deletion, err := PrepareFiles(context.Background(), p, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("after\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyFiles(context.Background(), p, deletion, authorize(t, deletion)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(s.Workspace.Request.Path, "file.txt")); !os.IsNotExist(err) {
		t.Fatal("deletion not observed")
	}
}

func TestLostFileConfirmationIsReconciledWithoutRetry(t *testing.T) {
	p, s := writer(t)
	prepared := prepareChange(t, p)
	if err := Append(p, "files.intent", FileIntent{prepared, authorize(t, prepared)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFiles(context.Background(), p, prepared, authorize(t, prepared)); err == nil {
		t.Fatal("pending effect retried")
	}
	if err := fileeffects.Apply(context.Background(), *s.Workspace, prepared.Proposal); err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileFiles(context.Background(), p)
	if err != nil || result.FileOutcome != "CONFIRMED" {
		t.Fatal(result.FileOutcome, err)
	}
}

func TestPartialAndAbsentFileEffectsRemainDistinct(t *testing.T) {
	p, s := writer(t)
	prepared := prepareChange(t, p)
	if err := Append(p, "files.intent", FileIntent{prepared, authorize(t, prepared)}); err != nil {
		t.Fatal(err)
	}
	before, err := ReconcileFiles(context.Background(), p)
	if err != nil || before.FileOutcome != "NOT_APPLIED" {
		t.Fatal(before.FileOutcome, err)
	}
	prepared = prepareChange(t, p)
	if err := Append(p, "files.intent", FileIntent{prepared, authorize(t, prepared)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Workspace.Request.Path, "file.txt"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	partial, err := ReconcileFiles(context.Background(), p)
	if err == nil || partial.FileOutcome != "UNKNOWN" {
		t.Fatal("partial effect classified as complete", err)
	}
	if _, err := PrepareFiles(context.Background(), p, prepared.Proposal.Changes); err == nil {
		t.Fatal("new work admitted over unknown effect")
	}
}

func TestUnexpectedFileBlocksBeforeIntent(t *testing.T) {
	p, s := writer(t)
	prepared := prepareChange(t, p)
	if err := os.WriteFile(filepath.Join(s.Workspace.Request.Path, "unexpected"), []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFiles(context.Background(), p, prepared, authorize(t, prepared)); err == nil {
		t.Fatal("stale whole candidate admitted")
	}
	after, err := Inspect(p)
	if err != nil || after.FileIntent != nil {
		t.Fatal("recorded intent for stale candidate", err)
	}
}

func TestExplicitRecoveryFinishesOnlyApprovedPartialWrites(t *testing.T) {
	p, s := writer(t)
	prepared := prepareChange(t, p)
	if err := Append(p, "files.intent", FileIntent{prepared, authorize(t, prepared)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Workspace.Request.Path, "file.txt"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The second temporary file was only partly written when the process stopped.
	id, err := prepared.Proposal.ID()
	if err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(s.Workspace.Request.Path, ".harness-tmp-"+id+"-1")
	if err := os.WriteFile(temp, []byte("\x00\xff"), 0600); err != nil {
		t.Fatal(err)
	}
	recovery, err := PrepareFileRecovery(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := recovery.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverFiles(context.Background(), p, recovery, authorize(t, prepared)); err == nil {
		t.Fatal("original approval reused for recovery")
	}
	result, err := RecoverFiles(context.Background(), p, recovery, effects.Authorization{IntentID: rid, Actor: "recovery-operator"})
	if err != nil {
		t.Fatal(err)
	}
	if result.FileOutcome != "CONFIRMED" || result.FileRecovery == nil || *result.Candidate != prepared.Proposal.After {
		t.Fatal("recovery not confirmed")
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("temporary not cleaned")
	}
	if _, err := RecoverFiles(context.Background(), p, recovery, effects.Authorization{IntentID: rid, Actor: "recovery-operator"}); err == nil {
		t.Fatal("recovery repeated")
	}
}

func TestRecoveryRejectsUnexpectedAndStaleState(t *testing.T) {
	p, s := writer(t)
	prepared := prepareChange(t, p)
	if err := Append(p, "files.intent", FileIntent{prepared, authorize(t, prepared)}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(s.Workspace.Request.Path, "file.txt")
	if err := os.WriteFile(target, []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	recovery, err := PrepareFileRecovery(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := recovery.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("unapproved edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverFiles(context.Background(), p, recovery, effects.Authorization{IntentID: rid, Actor: "operator"}); err == nil {
		t.Fatal("stale recovery applied")
	}
	if _, err := PrepareFileRecovery(context.Background(), p); err == nil {
		t.Fatal("unexpected bytes admitted for recovery")
	}
}
