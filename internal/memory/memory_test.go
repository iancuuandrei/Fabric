package memory

import (
	"strings"
	"testing"
)

func TestAddListRelevantAndDeleteLessons(t *testing.T) {
	dir := t.TempDir()

	l1, err := Add(dir, Lesson{Scope: "go/sqlite", Rule: "Always set _busy_timeout=5000 when opening sqlite journals."})
	if err != nil {
		t.Fatalf("Add l1: %v", err)
	}
	l2, err := Add(dir, Lesson{Scope: "frontend/css", Rule: "Use design tokens rather than hardcoded hex colors."})
	if err != nil {
		t.Fatalf("Add l2: %v", err)
	}

	all, err := List(dir)
	if err != nil || len(all) != 2 {
		t.Fatalf("List unexpected: %+v err=%v", all, err)
	}

	rel := Relevant(dir, "Fix sqlite database locked error in Go store", 1)
	if len(rel) != 1 || rel[0].ID != l1.ID {
		t.Fatalf("expected sqlite lesson ranked first, got: %+v", rel)
	}

	formatted := FormatForPrompt(rel)
	if !strings.Contains(formatted, l1.Rule) {
		t.Fatalf("unexpected formatted prompt: %s", formatted)
	}

	if err := Delete(dir, l2.ID[:4]); err != nil {
		t.Fatalf("Delete by prefix failed: %v", err)
	}
	after, _ := List(dir)
	if len(after) != 1 || after[0].ID != l1.ID {
		t.Fatalf("expected only l1 remaining, got: %+v", after)
	}
}
