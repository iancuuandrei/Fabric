package codexhost

import "testing"

func TestLaunchRequestsHardAgentDisableSeparatelyFromFeatures(t *testing.T) {
	args := launchArguments()
	for _, required := range []string{"agents.enabled=false", "features.multi_agent=false", "features.multi_agent_v2=false", "features.code_mode=false"} {
		count := 0
		for i, arg := range args {
			if arg == required && i > 0 && args[i-1] == "-c" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("missing or duplicate control %q: %v", required, args)
		}
	}
}
