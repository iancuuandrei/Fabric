package opencode

import (
	"context"
	"errors"

	"harness.local/engorch/internal/canonical"
)

// ReadTextTurn validates a dedicated session's single text-only turn. Tool-loop
// sessions require a different admission path and are rejected. The caller must
// establish owned-host quiescence before treating this snapshot as terminal.
// No activity-map observation can replace that lifecycle precondition.
func (c *Client) ReadTextTurn(ctx context.Context, b Binding, prompt string) (Assistant, string, error) {
	if err := validatePrompt(b, prompt); err != nil {
		return Assistant{}, "", err
	}
	raw, err := c.read(ctx, "/session/"+b.SessionID+"/message")
	if err != nil {
		return Assistant{}, "", err
	}
	return decodeTextTurn(raw, b, prompt)
}

func decodeTextTurn(raw []byte, b Binding, prompt string) (Assistant, string, error) {
	messages, err := decodeTranscript(raw, b.SessionID)
	if err != nil {
		return Assistant{}, "", err
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[0].ID != b.ParentID || messages[1].Role != "assistant" {
		return Assistant{}, "", errors.New("text turn history incomplete or contains extra messages")
	}
	user, err := canonical.Bytes(map[string]any{"info": messages[0].Info, "parts": messages[0].Parts})
	if err != nil {
		return Assistant{}, "", err
	}
	if err := decodePrompt(user, b, prompt); err != nil {
		return Assistant{}, "", err
	}
	assistant, err := DecodeAssistant(messages[1].Info, b)
	if err != nil {
		return Assistant{}, "", err
	}
	text, err := DecodeTextParts(messages[1].Parts, assistant)
	if err != nil {
		return Assistant{}, "", err
	}
	return assistant, text, nil
}
