package opencode

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness.local/engorch/internal/journal"
)

func TestReadToolSessionReturnsExactCompletedRecordAndHead(t *testing.T) {
	binding := toolSessionFixture()
	path := filepath.Join(t.TempDir(), "tool-session.jsonl")
	if err := appendToolSessionCreation(path, "opencode.tool-session-intent", binding); err != nil {
		t.Fatal(err)
	}
	if err := observeToolSessionCreation(path, binding, "ses_fixture"); err != nil {
		t.Fatal(err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ReadToolSession(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	if !equalToolSessionBinding(record.Binding, binding) || record.SessionID != "ses_fixture" || record.JournalHead != events[len(events)-1].Hash {
		t.Fatal("tool session record differs from exact journal", record)
	}
	if len(record.Binding.ToolNames) == 0 || &record.Binding.ToolNames[0] == &binding.ToolNames[0] {
		t.Fatal("tool session record aliases expected tool names")
	}
	record.Binding.ToolNames[0] = "mutated"
	again, err := ReadToolSession(path, binding)
	if err != nil || again.Binding.ToolNames[0] != "ri_search" || again.JournalHead != record.JournalHead {
		t.Fatal("returned record mutated durable replay", again, err)
	}
}

func TestReadToolSessionRejectsUnresolvedMissingAndMismatchedIntent(t *testing.T) {
	binding := toolSessionFixture()
	path := filepath.Join(t.TempDir(), "tool-session.jsonl")
	if err := appendToolSessionCreation(path, "opencode.tool-session-intent", binding); err != nil {
		t.Fatal(err)
	}
	if record, err := ReadToolSession(path, binding); err == nil || !strings.Contains(err.Error(), "unresolved") || !reflect.DeepEqual(record, ToolSessionRecord{}) {
		t.Fatal("intent-only journal was not explicitly unresolved", record, err)
	}
	changed := binding
	changed.ToolNames = append([]string(nil), binding.ToolNames...)
	changed.CatalogSHA256 = strings.Repeat("c", 64)
	if record, err := ReadToolSession(path, changed); err == nil || !strings.Contains(err.Error(), "mismatch") || !reflect.DeepEqual(record, ToolSessionRecord{}) {
		t.Fatal("mismatched expected binding was admitted", record, err)
	}
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	if record, err := ReadToolSession(missing, binding); err == nil || !strings.Contains(err.Error(), "missing") || !reflect.DeepEqual(record, ToolSessionRecord{}) {
		t.Fatal("missing intent was admitted", record, err)
	}
}

func TestReadToolSessionRejectsInvalidExpectedAndCorruptJournal(t *testing.T) {
	binding := toolSessionFixture()
	invalid := binding
	invalid.ToolNames = []string{"source_read", "ri_search"}
	if record, err := ReadToolSession(filepath.Join(t.TempDir(), "unused.jsonl"), invalid); err == nil || !reflect.DeepEqual(record, ToolSessionRecord{}) {
		t.Fatal("invalid expected binding was admitted", record, err)
	}
	path := filepath.Join(t.TempDir(), "tool-session.jsonl")
	if err := os.WriteFile(path, []byte("not a journal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if record, err := ReadToolSession(path, binding); err == nil || !reflect.DeepEqual(record, ToolSessionRecord{}) {
		t.Fatal("corrupt journal produced a session record", record, err)
	}
}
