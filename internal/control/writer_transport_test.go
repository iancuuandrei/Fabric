package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/writercontract"
	"strings"
	"testing"
)

func TestUTF8WriterProposalReplay(t *testing.T) {
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer", Effort: "medium", Role: "writer"}
	c.Config.WriterContract = "utf8-v2"
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	i, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(i.Input, "content_base64") || !strings.Contains(i.Input, "content_utf8") {
		t.Fatal("wrong wire contract")
	}
	id, _ := s.Candidate.ID()
	b, _ := json.Marshal(map[string]any{"candidate_id": id, "changes": []any{map[string]any{"path": "file.txt", "before_hash": digestText("base\n"), "content_utf8": "new text\n", "executable": false}}})
	r := runtime.Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, Output: string(b)}
	_, err = RecordWriterProposal(context.Background(), path, i, r)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.WriterProposal == nil || after.WriterProposal.Result.Output != string(b) {
		t.Fatal("raw model result changed in replay")
	}
}

func TestUTF8InvalidProposalDoesNotAdvance(t *testing.T) {
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer", Effort: "medium", Role: "writer"}
	c.Config.WriterContract = "utf8-v2"
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	i, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{`{"candidate_id":"x","changes":[]}`, `{"candidate_id":"x","changes":[{"path":"file.txt","content_base64":"bad"}]}`} {
		r := runtime.Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, Output: output}
		if _, err := RecordWriterProposal(context.Background(), path, i, r); err == nil {
			t.Fatal("invalid proposal accepted")
		}
		after, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if after.State != "IMPLEMENTING" || after.WriterProposal != nil || after.Verification != nil || after.Review != nil || after.Commit != nil || *after.Candidate != *s.Candidate {
			t.Fatal("invalid transport advanced workflow")
		}
	}
}

func TestUTF8WriterTransportLargeExactBytes(t *testing.T) {
	text := strings.Repeat("Română 日本語 😀\r\n\"quoted\" \\ literal = + /\n", 3000)
	raw, err := json.Marshal(map[string]any{"candidate_id": strings.Repeat("a", 64), "changes": []any{map[string]any{"path": "new.txt", "before_hash": nil, "content_utf8": text, "executable": false}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := decodeWriterProposal("utf8-v2", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(*p.Changes[0].ContentBase64)
	if err != nil || string(decoded) != text {
		t.Fatalf("byte mismatch: %v", err)
	}
	if strings.Contains(string(writercontract.UTF8Schema()), "content_base64") {
		t.Fatal("model still encodes Base64")
	}
}

func TestUTF8WriterTransportRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{
		`{"candidate_id":"x","changes":[]}`,
		`{"candidate_id":"x","changes":[{"path":"x","before_hash":null,"executable":false}]}`,
		`{"candidate_id":"x","changes":[{"path":"x","content_base64":"eA=="}]}`,
		`{"candidate_id":"x","changes":[{"path":"x","content_utf8":"\ud800"}]}`,
		`{"candidate_id":"x","changes":[{"path":"x","content_utf8":"a","content_utf8":"b"}]}`,
		`{"candidate_id":"x","changes":[{"path":"x","content_utf8":123}]}`,
		`{"candidate_id":"x","changes":[{"path":"x","content_utf8":"truncated`,
	} {
		if _, err := decodeWriterProposal("utf8-v2", raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestUTF8WriterTransportNullAndEmptyDiffer(t *testing.T) {
	for _, content := range []string{`null`, `""`} {
		raw := `{"candidate_id":"x","changes":[{"path":"x","before_hash":null,"content_utf8":` + content + `,"executable":false}]}`
		p, err := decodeWriterProposal("utf8-v2", raw)
		if err != nil {
			t.Fatal(err)
		}
		if (p.Changes[0].ContentBase64 == nil) != (content == "null") {
			t.Fatal("deletion and empty file conflated")
		}
	}
}
