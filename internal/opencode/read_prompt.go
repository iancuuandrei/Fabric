package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"harness.local/engorch/internal/canonical"
)

// ReadPrompt establishes that the exact submitted user message is present.
// It does not establish inference completion or authorize resubmission when
// absent. Session/workspace ownership must be checked separately by the caller.
func (c *Client) ReadPrompt(ctx context.Context, b Binding, text string) error {
	if err := validatePrompt(b, text); err != nil {
		return err
	}
	raw, err := c.read(ctx, "/session/"+b.SessionID+"/message/"+b.ParentID)
	if err != nil {
		return err
	}
	return decodePrompt(raw, b, text)
}

// ReadPromptWithStructuredOutput verifies the exact native format persisted on
// an opt-in user message. It never posts or infers completion.
func (c *Client) ReadPromptWithStructuredOutput(ctx context.Context, b Binding, text string, expectation StructuredOutputExpectation) error {
	if err := validatePrompt(b, text); err != nil {
		return err
	}
	if err := expectation.Validate(); err != nil {
		return err
	}
	raw, err := c.read(ctx, "/session/"+b.SessionID+"/message/"+b.ParentID)
	if err != nil {
		return err
	}
	return decodePromptWithStructuredOutput(raw, b, text, &expectation)
}

func decodePrompt(raw []byte, b Binding, text string) error {
	return decodePromptWithStructuredOutput(raw, b, text, nil)
}

func decodePromptWithStructuredOutput(raw []byte, b Binding, text string, expectation *StructuredOutputExpectation) error {
	if err := validatePrompt(b, text); err != nil {
		return err
	}
	envelope, err := wireObject(raw)
	if err != nil {
		return err
	}
	info, err := wireObject(envelope["info"])
	if err != nil {
		return err
	}
	for key, want := range map[string]string{"id": b.ParentID, "sessionID": b.SessionID, "role": "user", "agent": b.Agent} {
		var value string
		if field(info, key, &value) != nil || value != want {
			return errors.New("prompt identity mismatch")
		}
	}
	for _, key := range []string{"system", "tools"} {
		if _, ok := info[key]; ok {
			return errors.New("unexpected prompt context")
		}
	}
	if expectation == nil {
		if _, ok := info["format"]; ok {
			return errors.New("unexpected prompt context")
		}
	} else {
		if err := expectation.Validate(); err != nil {
			return err
		}
		want, err := expectation.PromptFormat()
		if err != nil {
			return err
		}
		got, ok := info["format"]
		if !ok {
			return errors.New("structured output prompt format missing")
		}
		gotCanonical, gotErr := canonical.Normalize(got)
		wantCanonical, wantErr := canonical.Normalize(want)
		if gotErr != nil || wantErr != nil || !bytes.Equal(gotCanonical, wantCanonical) {
			return errors.New("structured output prompt format mismatch")
		}
	}
	// The pinned runtime adds an empty diff summary after an ordinary turn.
	// Admit only that inert projection, not generated summary text or file diffs.
	if raw, ok := info["summary"]; ok {
		summary, err := wireObject(raw)
		var diffs []json.RawMessage
		if err != nil || len(summary) != 1 || field(summary, "diffs", &diffs) != nil || diffs == nil || len(diffs) != 0 {
			return errors.New("unexpected prompt summary")
		}
	}
	var model struct {
		Provider string `json:"providerID"`
		Model    string `json:"modelID"`
		Variant  string `json:"variant,omitempty"`
	}
	if field(info, "model", &model) != nil || model.Provider != b.Provider || model.Model != b.Model || model.Variant != b.Variant {
		return errors.New("prompt model mismatch")
	}
	var parts []json.RawMessage
	if field(envelope, "parts", &parts) != nil || len(parts) != 1 {
		return errors.New("prompt parts mismatch")
	}
	part, err := wireObject(parts[0])
	if err != nil {
		return err
	}
	var kind string
	if field(part, "type", &kind) != nil || kind != "text" {
		return errors.New("prompt is not text")
	}
	actual, err := DecodeTextParts(envelope["parts"], Assistant{ID: b.ParentID, Binding: b})
	if err != nil {
		return err
	}
	if actual != text {
		return errors.New("prompt text mismatch")
	}
	return nil
}
