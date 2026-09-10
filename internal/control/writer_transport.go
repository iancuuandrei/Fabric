package control

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/writercontract"
)

// utf8WireChange is one validated UTF-8 wire item shared by the utf8-v2 and
// changes-json-v1 decoders. The deterministic UTF-8 to Base64 conversion
// happens once here, after all schema checks.
type utf8WireChange struct {
	Path       string          `json:"path"`
	BeforeHash *string         `json:"before_hash"`
	Content    json.RawMessage `json:"content_utf8"`
	Executable bool            `json:"executable"`
}

// appendUTF8Change converts one validated wire item into its semantic change.
// A JSON null content means deletion; any other value must be a JSON string
// whose UTF-8 bytes are Base64-encoded deterministically.
func appendUTF8Change(proposal *WriterProposal, c utf8WireChange) error {
	if len(c.Content) == 0 {
		return errors.New("writer content_utf8 is required; null means deletion")
	}
	var encoded *string
	if string(c.Content) != "null" {
		var text string
		if err := json.Unmarshal(c.Content, &text); err != nil {
			return err
		}
		value := base64.StdEncoding.EncodeToString([]byte(text))
		encoded = &value
	}
	proposal.Changes = append(proposal.Changes, fileeffects.Change{Path: c.Path, BeforeHash: c.BeforeHash, ContentBase64: encoded, Executable: c.Executable})
	return nil
}

// decodeWriterProposal preserves the observed model output. Only the approval
// target uses Base64, generated deterministically from validated JSON text.
// Contracts are disjoint: utf8-v2 accepts only candidate_id+changes array,
// changes-json-v1 accepts only candidate_id+changes_json string. Neither
// decoder falls back to the other representation.
func decodeWriterProposal(contract, output string) (WriterProposal, error) {
	var proposal WriterProposal
	if contract == writercontract.ContractChangesJSONV1 {
		return decodeChangesJSONProposal(output)
	}
	if contract != "utf8-v2" {
		err := canonical.Decode([]byte(output), &proposal)
		return proposal, err
	}
	var wire struct {
		CandidateID string           `json:"candidate_id"`
		Changes     []utf8WireChange `json:"changes"`
	}
	if err := canonical.Decode([]byte(output), &wire); err != nil {
		return proposal, err
	}
	// Required nullable fields must be present; omission is not an assertion of
	// absence. Enforce the wire schema independently of provider enforcement.
	var fields struct {
		Changes []map[string]json.RawMessage `json:"changes"`
	}
	if err := json.Unmarshal([]byte(output), &fields); err != nil {
		return proposal, err
	}
	for _, change := range fields.Changes {
		for _, key := range []string{"path", "before_hash", "content_utf8", "executable"} {
			if _, ok := change[key]; !ok {
				return proposal, errors.New("writer required field missing: " + key)
			}
		}
		if string(change["executable"]) != "true" && string(change["executable"]) != "false" {
			return proposal, errors.New("writer executable must be boolean")
		}
	}
	if err := writercontract.ValidateCount(len(wire.Changes)); err != nil {
		return proposal, err
	}
	proposal.CandidateID = wire.CandidateID
	for _, c := range wire.Changes {
		if err := appendUTF8Change(&proposal, c); err != nil {
			return WriterProposal{}, err
		}
	}
	return proposal, nil
}

// decodeChangesJSONProposal implements WriterContract v2 (changes-json-v1).
// The outer value carries exactly candidate_id plus one changes_json string.
// That string must decode to exactly []Change with utf8 content items; the
// top-level inner value must be an array, never an object, string, null, or
// envelope containing candidate_id. Strictness is semantic (valid JSON,
// strict types, no duplicate keys, no unknown fields, exact item schema)
// without demanding canonical bytes from the model: whitespace, escaping and
// field order inside changes_json are free. The controller produces the
// canonical representation afterwards. Raw evidence is preserved by the
// caller; this normalization is the explicit v2 contract, not rewriting.
func decodeChangesJSONProposal(output string) (WriterProposal, error) {
	var proposal WriterProposal
	var outer struct {
		CandidateID string `json:"candidate_id"`
		ChangesJSON string `json:"changes_json"`
	}
	if err := canonical.Decode([]byte(output), &outer); err != nil {
		return proposal, err
	}
	inner := outer.ChangesJSON
	// Inner top-level must be exactly an array. Reject empty, null, objects,
	// strings and envelopes before typed decoding so the failure is explicit.
	trimmed := inner
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return proposal, errors.New("writer changes_json must be a JSON array string")
	}
	var items []utf8WireChange
	if err := canonical.Decode([]byte(inner), &items); err != nil {
		return proposal, err
	}
	// Required nullable fields must be present; omission is not an assertion
	// of absence. Enforce the inner wire schema independently of provider
	// enforcement, mirroring the utf8-v2 required-field check.
	var fields []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inner), &fields); err != nil {
		return proposal, err
	}
	for _, change := range fields {
		for _, key := range []string{"path", "before_hash", "content_utf8", "executable"} {
			if _, ok := change[key]; !ok {
				return proposal, errors.New("writer required field missing: " + key)
			}
		}
		if string(change["executable"]) != "true" && string(change["executable"]) != "false" {
			return proposal, errors.New("writer executable must be boolean")
		}
	}
	if err := writercontract.ValidateCount(len(items)); err != nil {
		return proposal, err
	}
	proposal.CandidateID = outer.CandidateID
	for _, c := range items {
		if err := appendUTF8Change(&proposal, c); err != nil {
			return WriterProposal{}, err
		}
	}
	return proposal, nil
}
