package codexruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/ri"
)

func TestActualLexicalBroker(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires Rust RI binary")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	a := sourceAdapter(t)
	observed, err := ri.ObserveLexical(context.Background(), *a.Source)
	if err != nil {
		t.Fatal(err)
	}
	id, err := observed.Manifest.ID()
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(binary))
	plan := ri.LexicalPlan{Version: 1, Repository: *a.Source, ManifestID: id, Files: len(observed.Manifest.Files), Executable: executable, ExecutableSHA256: hash, StageRoot: filepath.Join(t.TempDir(), "stage"), BatchBytes: 64 << 20, BatchFiles: 100}
	ref, err := ri.StageLexical(context.Background(), plan, observed.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	a.Lexical = &LexicalBinding{Base: ref, Executable: executable, ExecutableSHA256: hash}
	events, err := journal.Read(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	a.JournalPath = filepath.Join(t.TempDir(), "lexical.jsonl")
	for _, event := range events {
		if err := appendEvent(a.JournalPath, event.Kind, event.Payload); err != nil {
			t.Fatal(err)
		}
		if event.Kind == "runtime.source" {
			record, err := a.Lexical.Record(*a.Source, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := appendEvent(a.JournalPath, "runtime.lexical-record", record); err != nil {
				t.Fatal(err)
			}
		}
	}
	session := NewToolSession(context.Background(), a)
	defer session.Close()
	var firstStream *ri.Stream
	for n, test := range []struct {
		args    string
		success bool
	}{
		{`{"pattern":"","fixed":true,"case_insensitive":false,"limit":1,"after":null}`, true},
		{`{"pattern":"","fixed":true,"case_insensitive":false,"limit":101,"after":null}`, false},
		{`{"pattern":"","fixed":true,"case_insensitive":false,"limit":1,"after":null,"source_root":"other"}`, false},
		{`{"pattern":"","fixed":true,"case_insensitive":false,"limit":1,"after":null}`, true},
	} {
		raw, err := canonical.Bytes(ToolRequest{Arguments: json.RawMessage(test.args), CallID: fmt.Sprintf("lexical-%d", n), ThreadID: "thread-1", TurnID: "turn-1", Tool: "ri_search"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.HandleTool(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			firstStream = session.stream
		}
		if firstStream == nil || session.stream != firstStream {
			t.Fatal("reader process was replaced")
		}
		s, err := Inspect(a.JournalPath)
		if err != nil {
			t.Fatal(err)
		}
		response := s.ToolResponses[len(s.ToolResponses)-1]
		if s.PendingTool != nil || response.Success != test.success {
			t.Fatal("lexical broker response mismatch", response)
		}
		if test.success {
			var result ri.LexicalResult
			if err := canonical.Decode([]byte(response.Content), &result); err != nil {
				t.Fatal(err)
			}
			if result.ManifestID != id || len(result.Matches) != 1 || result.Matches[0].Range != [2]int64{0, 0} {
				t.Fatal("lexical broker evidence mismatch", result)
			}
		}
	}
	if err := firstStream.Close(); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"pattern":"","fixed":true,"case_insensitive":false,"limit":1,"after":null}`)
	if _, err := session.readLexical(context.Background(), *a.Lexical, args); err == nil {
		t.Fatal("terminated reader was silently restarted")
	}
	if session.stream != firstStream {
		t.Fatal("terminated reader replaced")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.HandleTool(context.Background(), nil); err == nil {
		t.Fatal("closed tool session accepted a call")
	}
}
