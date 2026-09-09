package control

import (
	"strings"
	"testing"
)

func TestAgentToolCallerIdentityBindsAdmissionAndClaim(t *testing.T) {
	f := newAgentToolFixture(t)
	id, err := agentToolCallerIdentity(f.controllerPath, f.schedulerPath, f.binding)
	if err != nil || len(id) != 64 {
		t.Fatalf("caller identity: %q %v", id, err)
	}
	recovered := f.binding
	recovered.Recovering = !recovered.Recovering
	again, err := agentToolCallerIdentity(f.controllerPath, f.schedulerPath, recovered)
	if err != nil || again != id {
		t.Fatalf("recovery changed immutable identity: %q %v", again, err)
	}
	for _, field := range []string{"admission", "invocation", "turn", "node"} {
		t.Run(field, func(t *testing.T) {
			changed := f.binding
			turn := *changed.AgentTurn
			changed.AgentTurn = &turn
			switch field {
			case "admission":
				changed.AdmissionID = strings.Repeat("0", 64)
			case "invocation":
				changed.InvocationID = strings.Repeat("0", 64)
			case "turn":
				changed.AgentTurn.TurnID = strings.Repeat("0", 64)
			case "node":
				changed.Node.AgentID = "/root/foreign"
			}
			if got, err := agentToolCallerIdentity(f.controllerPath, f.schedulerPath, changed); err == nil || got != "" {
				t.Fatalf("substituted caller accepted: %q %v", got, err)
			}
		})
	}
}
