// Package writercontract defines the finite mutation-proposal output contract.
package writercontract

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const MaxChanges = 64

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
