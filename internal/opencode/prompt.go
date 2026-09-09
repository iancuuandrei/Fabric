package opencode

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
)

// PromptOnce submits a text-only turn after the caller has durably recorded its
// intent and admitted the owned session, model access and budget. A nil error
// means HTTP acceptance only, never completion. Failure is unresolved and must
// not trigger automatic resubmission. ParentID is the caller-selected user
// message locator; it correlates recovery but is not an idempotency guarantee.
func (c *Client) PromptOnce(ctx context.Context, b Binding, text string) error {
	return c.promptOnce(ctx, b, text, nil)
}

// PromptOnceWithStructuredOutput submits one opt-in native json_schema prompt.
// The schema is part of the caller's durable dispatch identity; this method
// only sends the already validated format and never retries the POST.
func (c *Client) PromptOnceWithStructuredOutput(ctx context.Context, b Binding, text string, expectation StructuredOutputExpectation) error {
	return c.promptOnce(ctx, b, text, &expectation)
}

func (c *Client) promptOnce(ctx context.Context, b Binding, text string, structured *StructuredOutputExpectation) error {
	if err := validatePrompt(b, text); err != nil {
		return err
	}
	if structured != nil {
		if err := structured.Validate(); err != nil {
			return err
		}
	}
	body, err := promptBody(b, text, structured)
	if err != nil {
		return err
	}
	_, err = c.requestStatus(ctx, http.MethodPost, "/session/"+b.SessionID+"/prompt_async", body, http.StatusNoContent)
	return err
}

func promptBody(b Binding, text string, structured *StructuredOutputExpectation) ([]byte, error) {
	payload := map[string]any{
		"messageID": b.ParentID, "agent": b.Agent, "variant": b.Variant,
		"model": map[string]string{"providerID": b.Provider, "modelID": b.Model},
		"parts": []map[string]string{{"type": "text", "text": text}},
	}
	if structured != nil {
		format, err := structured.PromptFormat()
		if err != nil {
			return nil, err
		}
		payload["format"] = format
	}
	return canonical.Bytes(payload)
}

func validatePrompt(b Binding, text string) error {
	if !locator(b.SessionID) || !locator(b.ParentID) {
		return errors.New("invalid prompt locator")
	}
	for _, value := range []string{b.Provider, b.Model, b.Agent, b.Directory, b.Root} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > 4096 {
			return errors.New("invalid prompt binding")
		}
	}
	if !utf8.ValidString(b.Variant) || len(b.Variant) > 4096 || !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > 256<<10 {
		return errors.New("invalid prompt content")
	}
	return nil
}
