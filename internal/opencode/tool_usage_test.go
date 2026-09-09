package opencode

import "testing"

func TestToolUsageIncludesCacheAndReasoning(t *testing.T) {
	usage := ToolTurnTokens{Input: 10, Output: 20, Reasoning: 7, CacheRead: 30, CacheWrite: 5}
	input, output, err := usage.InclusiveTokens()
	if err != nil || input != 45 || output != 27 {
		t.Fatal("budget projection dropped token components", input, output, err)
	}
	for _, invalid := range []ToolTurnTokens{
		{Input: -1}, {CacheRead: -1}, {CacheWrite: -1}, {Output: -1}, {Reasoning: -1},
		{Input: toolTurnMaximumExactInteger, CacheRead: 1},
		{CacheRead: toolTurnMaximumExactInteger, CacheWrite: 1},
		{Output: toolTurnMaximumExactInteger, Reasoning: 1},
	} {
		if input, output, err := invalid.InclusiveTokens(); err == nil || input != 0 || output != 0 {
			t.Fatal("invalid inclusive usage admitted")
		}
	}
}
