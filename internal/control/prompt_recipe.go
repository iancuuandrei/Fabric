package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

const promptRecipeCachePrefixV1 = "cache-prefix-v1"

// promptRecipeBytes first applies the ordinary canonical-v1 value checks, then
// for the opt-in recipe emits the same object members in a stable-prefix order.
// It deliberately leaves the empty recipe's bytes untouched for replay.
func promptRecipeBytes(policy *ExecutionPolicy, value any) ([]byte, error) {
	raw, err := canonical.Bytes(value)
	if err != nil || policy == nil || policy.PromptRecipe == "" {
		return raw, err
	}
	if policy.PromptRecipe != promptRecipeCachePrefixV1 {
		return nil, errors.New("unsupported execution prompt recipe")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("prompt recipe requires a canonical JSON object")
	}
	if _, ok := fields["instruction"]; !ok {
		return nil, errors.New("prompt recipe requires an instruction field")
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if key != "instruction" && key != "output_schema" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	ordered := make([]string, 0, len(fields))
	ordered = append(ordered, "instruction")
	ordered = append(ordered, keys...)
	if _, ok := fields["output_schema"]; ok {
		ordered = append(ordered, "output_schema")
	}
	var out bytes.Buffer
	out.WriteByte('{')
	for index, key := range ordered {
		if index > 0 {
			out.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(fields[key])
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func promptRecipeInstruction(policy *ExecutionPolicy, role, contract, instruction string) string {
	if policy == nil || policy.PromptRecipe != promptRecipeCachePrefixV1 {
		return instruction
	}
	switch role {
	case "writer", "fixer":
		if contract == writercontract.ContractAnchoredEditsV2 || contract == writercontract.ContractAnchoredEditsV3 {
			instruction += " For every existing-file change, copy candidate_id, path, and before_hash verbatim from the successful candidate_validate_anchored_edits result, and use the exact same edits from the corresponding validation call in the final proposal. Do not recompose or guess these values. If validation does not succeed or the exact values are unavailable, inspect the candidate and validate again."
		}
	case "reviewer":
		instruction += " Preserve existing test expectations and contracts unless the objective explicitly authorizes a behavior change and the evidence justifies it. Changing tests merely to satisfy a broken implementation is not verification."
	}
	return instruction
}
