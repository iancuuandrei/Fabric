package draftpr

import (
	"encoding/json"
	"errors"
	"fmt"

	"harness.local/engorch/internal/canonical"
)

// DecodeObservation projects a complete GitHub pull response. It requires exact
// member names and nonnull required fields, while allowing unused API members.
// The entire response must satisfy canonical v1 bounds and JSON restrictions;
// decoding alone establishes neither endpoint provenance nor plan agreement.
func DecodeObservation(raw []byte) (Observation, error) {
	normal, err := canonical.Normalize(raw)
	if err != nil {
		return Observation{}, err
	}
	var o Observation
	fields := map[string]any{"number": &o.Number, "html_url": &o.URL, "state": &o.State, "draft": &o.Draft, "title": &o.Title, "body": &o.Body}
	var head, base json.RawMessage
	fields["head"], fields["base"] = &head, &base
	if err := projectObject(normal, fields); err != nil {
		return Observation{}, err
	}
	if o.Head, err = decodeBranch(head); err != nil {
		return Observation{}, err
	}
	if o.Base, err = decodeBranch(base); err != nil {
		return Observation{}, err
	}
	return o, nil
}

func projectObject(raw []byte, fields map[string]any) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return err
	}
	if members == nil {
		return errors.New("required response object is null")
	}
	for name, dst := range fields {
		value, exists := members[name]
		if !exists || string(value) == "null" {
			return fmt.Errorf("missing or null response member %s", name)
		}
		if err := json.Unmarshal(value, dst); err != nil {
			return fmt.Errorf("invalid response member %s: %w", name, err)
		}
	}
	return nil
}

func decodeBranch(raw []byte) (BranchObservation, error) {
	var b BranchObservation
	var repo json.RawMessage
	if err := projectObject(raw, map[string]any{"ref": &b.Ref, "sha": &b.Commit, "repo": &repo}); err != nil {
		return BranchObservation{}, err
	}
	if err := projectObject(repo, map[string]any{"full_name": &b.Repository}); err != nil {
		return BranchObservation{}, err
	}
	return b, nil
}
