package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestWriterFilesExportsRecordedIdentityAndRejectsStaleCandidate(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base"}} {
		if b, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	execute := func(dst any, args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		if err := Execute(context.Background(), args, root, &out); err != nil {
			t.Fatal(err)
		}
		if dst != nil {
			if err := json.Unmarshal(out.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
		return out.Bytes()
	}
	execute(nil, "init")
	configPath := filepath.Join(root, "harness.toml")
	data, _ := os.ReadFile(configPath)
	data = append(data, []byte("\n[writer]\nruntime = \"fake\"\nprovider = \"deterministic\"\nmodel = \"fixture-v1\"\neffort = \"none\"\nrole = \"writer\"\n")...)
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	var s control.Snapshot
	execute(&s, "plan", "Add one file")
	execute(&s, "approve", s.RunID, s.PlanID, "fixture")
	execute(&s, "run", s.RunID)
	path, err := runPath(root, s.RunID)
	if err != nil {
		t.Fatal(err)
	}
	i, err := control.PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, _ := s.Candidate.ID()
	model, provider, effort := i.Profile.Model, i.Profile.Provider, i.Profile.Effort
	r := runtime.Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort,
		Output: `{"candidate_id":"` + candidateID + `","changes":[{"path":"added.txt","before_hash":null,"content_base64":"aGVsbG8K","executable":false}]}`}
	record, err := control.RecordWriterProposal(context.Background(), path, i, r)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	var preview filePreview
	first := execute(&preview, "writer-files", s.RunID)
	second := execute(nil, "writer-files", s.RunID)
	id, _ := record.Prepared.Intent.ID()
	if preview.IntentID != id || !bytes.Equal(first, second) {
		t.Fatal("export replaced the original approval identity")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only export changed journal")
	}
	previewPath := filepath.Join(t.TempDir(), "files.json")
	if err := os.WriteFile(previewPath, first, 0600); err != nil {
		t.Fatal(err)
	}
	execute(&s, "apply-files", s.RunID, previewPath, id, "fixture")
	if err := Execute(context.Background(), []string{"writer-files", s.RunID}, root, &bytes.Buffer{}); err == nil {
		t.Fatal("stale writer approval exported after candidate changed")
	}
}
