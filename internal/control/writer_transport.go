package control

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/writercontract"
)

// decodeWriterProposal preserves the observed model output. Only the approval
// target uses Base64, generated deterministically from validated JSON text.
func decodeWriterProposal(contract, output string) (WriterProposal, error) {
	var proposal WriterProposal
	if contract != "utf8-v2" {
		err := canonical.Decode([]byte(output), &proposal)
		return proposal, err
	}
	var wire struct {
		CandidateID string `json:"candidate_id"`
		Changes     []struct {
			Path       string          `json:"path"`
			BeforeHash *string         `json:"before_hash"`
			Content    json.RawMessage `json:"content_utf8"`
			Executable bool            `json:"executable"`
		} `json:"changes"`
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
		if len(c.Content) == 0 {
			return WriterProposal{}, errors.New("writer content_utf8 is required; null means deletion")
		}
		var encoded *string
		if string(c.Content) != "null" {
			var text string
			if err := json.Unmarshal(c.Content, &text); err != nil {
				return WriterProposal{}, err
			}
			value := base64.StdEncoding.EncodeToString([]byte(text))
			encoded = &value
		}
		proposal.Changes = append(proposal.Changes, fileeffects.Change{Path: c.Path, BeforeHash: c.BeforeHash, ContentBase64: encoded, Executable: c.Executable})
	}
	return proposal, nil
}
