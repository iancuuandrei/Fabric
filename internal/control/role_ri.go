package control

import "harness.local/engorch/internal/codexruntime"

type roleRIContext struct {
	Binding     codexruntime.RIBinding `json:"binding"`
	Scope       string                 `json:"scope"`
	Instruction string                 `json:"instruction"`
}

func roleRI(s Snapshot) (*roleRIContext, error) {
	if s.RIPublish == nil || s.RIPublish.Outcome != "CONFIRMED" {
		return nil, nil
	}
	binding, err := runtimeRISelection(s)
	if err != nil {
		return nil, err
	}
	return &roleRIContext{Binding: binding, Scope: "base_commit", Instruction: "RI tools describe only the bound base commit. Candidate changes are not indexed by this snapshot. Use candidate_list and candidate_read for current files; inspect coverage before interpreting missing relationships. RI observations do not establish relevance or permission."}, nil
}
