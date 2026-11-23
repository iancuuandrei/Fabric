package runtimeutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

type testIntent struct {
	ID string `json:"id"`
}

type testReceipt struct {
	ID     string         `json:"id"`
	Hash   string         `json:"hash"`
	Result runtime.Result `json:"result"`
}

func TestUnwrapCodeFenceAndTruncate(t *testing.T) {
	got := UnwrapCodeFence("  ```json\n{\"ok\":true}\n```  ")
	if got != `{"ok":true}` {
		t.Fatalf("unexpected UnwrapCodeFence result: %q", got)
	}
	if Truncate("hello", 3) != "hel..." {
		t.Fatalf("unexpected Truncate result: %q", Truncate("hello", 3))
	}
	if Truncate("hi", 5) != "hi" {
		t.Fatalf("unexpected short Truncate result: %q", Truncate("hi", 5))
	}
}

func TestVerifyExecutableHash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyExecutableHash(p, h, "demo"); err != nil {
		t.Fatalf("expected hash match, got %v", err)
	}
	if err := VerifyExecutableHash(p, "0000000000000000000000000000000000000000000000000000000000000000", "demo"); err == nil {
		t.Fatal("expected hash mismatch")
	}
}

func TestJournalLifecyclePrepareAndReplay(t *testing.T) {
	inv, err := runtime.NewInvocation(runtime.Profile{
		Runtime:  "direct-api",
		Provider: "p",
		Model:    "m",
		Effort:   "low",
		Role:     "writer",
	}, "{}")
	if err != nil {
		t.Fatalf("NewInvocation failed: %v", err)
	}
	jp := filepath.Join(t.TempDir(), "run.journal.jsonl")
	validate := func(ev []journal.Event) error {
		return ValidateTwoEventJournal(ev, "test", "t.intent", "t.result",
			func(i testIntent) string { return i.ID },
			func(r testReceipt) string { return r.ID },
		)
	}
	want := testIntent{ID: inv.ID}
	cached, _, err := PrepareInvocation[testIntent, testReceipt](jp, "test", "t.intent", "t.result", want, inv, validate, func(r testReceipt) runtime.Result { return r.Result })
	if err != nil || cached != nil {
		t.Fatalf("expected clean first prepare, got cached=%v err=%v", cached, err)
	}
	if _, _, err := PrepareInvocation[testIntent, testReceipt](jp, "test", "t.intent", "t.result", want, inv, validate, func(r testReceipt) runtime.Result { return r.Result }); err == nil {
		t.Fatal("expected unresolved invocation error before result")
	}
	res := BuildResult(inv, `{"ok":true}`, runtime.Usage{})
	if _, _, err := RecordResult(jp, "t.result", "test.result.v1", inv, res, validate, func(h string) testReceipt {
		return testReceipt{ID: inv.ID, Hash: h, Result: res}
	}); err != nil {
		t.Fatalf("RecordResult failed: %v", err)
	}
	cached, head, err := PrepareInvocation[testIntent, testReceipt](jp, "test", "t.intent", "t.result", want, inv, validate, func(r testReceipt) runtime.Result { return r.Result })
	if err != nil || cached == nil || head == "" || cached.Output != `{"ok":true}` {
		t.Fatalf("expected replayed cached result, got cached=%v head=%q err=%v", cached, head, err)
	}
}
