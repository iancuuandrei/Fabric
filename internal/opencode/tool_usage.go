package opencode

import "errors"

// InclusiveTokens reconstructs input including cache read/write and output
// including reasoning from the pinned runtime's disjoint accounting fields.
// This only projects runtime-reported values: OpenCode can normalize missing
// upstream usage to zero. Provider accounting admission requires separate wire
// evidence; this helper neither authenticates usage nor grants budget authority.
func (t ToolTurnTokens) InclusiveTokens() (input, output int64, err error) {
	for _, component := range []int64{t.Input, t.CacheRead, t.CacheWrite} {
		if component < 0 || component > toolTurnMaximumExactInteger-input {
			return 0, 0, errors.New("inclusive input token count out of range")
		}
		input += component
	}
	for _, component := range []int64{t.Output, t.Reasoning} {
		if component < 0 || component > toolTurnMaximumExactInteger-output {
			return 0, 0, errors.New("inclusive output token count out of range")
		}
		output += component
	}
	return input, output, nil
}
