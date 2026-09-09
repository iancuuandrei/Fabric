package codexruntime

import (
	"context"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/ri"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRIBindingAndUnboundToolRejection(t *testing.T) {
	a := sourceAdapter(t)
	source, err := ri.FromRepository(*a.Source)
	if err != nil {
		t.Fatal(err)
	}
	binding := RIBinding{Snapshot: ri.SnapshotRef{Path: filepath.Join(a.Directory, "snapshot"), ID: strings.Repeat("a", 64), Source: source}, Executable: filepath.Join(a.Directory, "ri.exe"), ExecutableSHA256: strings.Repeat("b", 64)}
	encoded, err := canonical.Bytes(binding)
	if err != nil {
		t.Fatal(err)
	}
	s := State{Intent: &Intent{}, Source: a.Source}
	if err := s.toolEvent("runtime.ri", encoded); err != nil {
		t.Fatal(err)
	}
	if err := s.toolEvent("runtime.ri", encoded); err == nil {
		t.Fatal("RI rebinding accepted")
	}
	foreign := binding
	foreign.Snapshot.Source.Commit = strings.Repeat("f", 40)
	if err := foreign.Validate(*a.Source); err == nil {
		t.Fatal("foreign RI source accepted")
	}
	request, err := canonical.Bytes(ToolRequest{Arguments: []byte("{}"), CallID: "ri-1", ThreadID: "thread-1", TurnID: "turn-1", Tool: "ri_status"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.HandleTool(context.Background(), request); err == nil {
		t.Fatal("unbound RI tool accepted")
	}
}
