package codexruntime

import (
	"os"
	"testing"

	"harness.local/engorch/internal/journal"
)

func TestRuntimeStateAndHeadShareValidatedRead(t *testing.T) {
	a := sourceAdapter(t)
	events, err := journal.Read(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	s, head, err := InspectWithHead(a.JournalPath)
	if err != nil || head != events[len(events)-1].Hash || s.Source == nil || *s.Source != *a.Source {
		t.Fatal("runtime state/head mismatch", err)
	}
	if err := os.WriteFile(a.JournalPath, []byte("torn"), 0600); err != nil {
		t.Fatal(err)
	}
	s, head, err = InspectWithHead(a.JournalPath)
	if err == nil || head != "" || s.Intent != nil {
		t.Fatal("invalid journal supplied authority")
	}
}
