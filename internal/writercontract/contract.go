// Package writercontract defines the finite mutation-proposal output contract.
package writercontract

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const MaxChanges = 64

// ContractChangesJSONV1 is the R59 writer/fixer wire contract: the provider
// constructs one flat string (changes_json) carrying the JSON array, and the
// deterministic controller reconstructs the semantic proposal. The semantic
// contract stays WriterProposal{CandidateID, Changes}; only the wire
// representation differs from utf8-v2. There is deliberately no dual
// representation: v1 outputs are invalid under v2 and vice versa.
const ContractChangesJSONV1 = "changes-json-v1"

// ChangesJSONMaxLength bounds the outer string payload. The structured-output
// value bound (256 KiB) still governs the complete terminal value.
const ChangesJSONMaxLength = 500000

// UTF8Schema leaves encoding to the controller, not the language model.
func UTF8Schema() json.RawMessage {
	return json.RawMessage(strings.ReplaceAll(string(Schema()), "content_base64", "content_utf8"))
}

var ErrEmptyChangeset = errors.New("WRITER_PROPOSAL_INVALID: EMPTY_CHANGESET")
var ErrTooManyChanges = errors.New("WRITER_PROPOSAL_INVALID: TOO_MANY_CHANGES")

func ValidateCount(n int) error {
	if n == 0 {
		return fmt.Errorf("%w: one to 64 changes required", ErrEmptyChangeset)
	}
	if n < 0 || n > MaxChanges {
		return fmt.Errorf("%w: one to 64 changes required", ErrTooManyChanges)
	}
	return nil
}

// Schema returns fresh bytes so callers cannot mutate a shared contract.
// Filesystem authority, candidate identity and byte validation remain independent.
func Schema() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["candidate_id","changes"],"properties":{"candidate_id":{"type":"string","pattern":"^[0-9a-f]{64}$"},"changes":{"type":"array","minItems":1,"maxItems":%d,"items":{"type":"object","additionalProperties":false,"required":["path","before_hash","content_base64","executable"],"properties":{"path":{"type":"string","minLength":1},"before_hash":{"type":["string","null"],"pattern":"^[0-9a-f]{64}$"},"content_base64":{"type":["string","null"]},"executable":{"type":"boolean"}}}}}}`, MaxChanges))
}

// ChangesJSONSchema returns the R59 v2 outer wire schema: candidate_id stays a
// directly verifiable binding and the complete change array travels as one
// JSON-encoded string. Semantic strictness (array shape, item schema, count,
// duplicates, unknown fields) is enforced deterministically after decoding,
// not by asking the model for canonical bytes.
func ChangesJSONSchema() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["candidate_id","changes_json"],"properties":{"candidate_id":{"type":"string","pattern":"^[0-9a-f]{64}$"},"changes_json":{"type":"string","minLength":2,"maxLength":%d}}}`, ChangesJSONMaxLength))
}
