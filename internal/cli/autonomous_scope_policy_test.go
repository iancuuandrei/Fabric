package cli

import (
	"testing"

	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestNewAutonomousScopePolicyMatchesSelectedWriterTopology(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		parallel, isolated, expected int
	}{
		{"serial", 0, 0, 1},
		{"parallel", 1, 0, 2},
		{"isolated", 0, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creation := control.Creation{Execution: &control.ExecutionPolicy{ParallelImplementationVersion: tc.parallel, IsolatedImplementationVersion: tc.isolated}}
			creation.Config.Writer = &runtime.Profile{Role: "writer"}
			creation.Config.Explorer = &runtime.Profile{Role: "explorer"}
			configureAutonomousScopeReplan(&creation)
			if creation.Execution.ScopeReplanVersion != tc.expected || creation.Execution.ScopeReplanDesignVersion != tc.expected || creation.Execution.MaxScopeReplans != 2 {
				t.Fatal("new run selected a scope policy incompatible with its effective topology")
			}
		})
	}
}

func TestNewAutonomousScopePolicyNeedsBothConfiguredRoles(t *testing.T) {
	for _, missing := range []string{"writer", "explorer", "execution"} {
		t.Run(missing, func(t *testing.T) {
			creation := control.Creation{Execution: &control.ExecutionPolicy{}}
			creation.Config.Writer = &runtime.Profile{Role: "writer"}
			creation.Config.Explorer = &runtime.Profile{Role: "explorer"}
			switch missing {
			case "writer":
				creation.Config.Writer = nil
			case "explorer":
				creation.Config.Explorer = nil
			case "execution":
				creation.Execution = nil
			}
			configureAutonomousScopeReplan(&creation)
			if creation.Execution != nil && (creation.Execution.ScopeReplanVersion != 0 || creation.Execution.MaxScopeReplans != 0) {
				t.Fatal("missing design/writer role silently enabled ownership replan")
			}
		})
	}
}
