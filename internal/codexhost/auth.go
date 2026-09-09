package codexhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

// LoginChatGPT supplies an existing local access token through the provider's
// external-token protocol. It does not copy credentials into the new home, send
// refresh/ID tokens, log secrets or change the source authentication file.
// A refresh request remains unsupported and cannot silently retry generation.
func (h *Host) LoginChatGPT(ctx context.Context, source string) error {
	if h == nil || h.Client == nil || !filepath.IsAbs(source) {
		return errors.New("host and absolute authentication source required")
	}
	directory := filepath.Dir(source)
	if err := safepath.Directory(directory); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(source)
	hash, _, _, exists, err := safepath.ReadRegular(root, name, 64<<10)
	if err != nil || !exists {
		return errors.New("authentication source unavailable or unsafe")
	}
	f, err := root.Open(name)
	if err != nil {
		return errors.New("authentication source could not be opened")
	}
	b, readErr := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || len(b) > 64<<10 || digest(b) != hash {
		return errors.New("authentication source changed or exceeded bounds")
	}
	defer clear(b)
	if _, err := canonical.Normalize(b); err != nil {
		return errors.New("authentication source JSON invalid")
	}
	var credentials struct {
		Tokens struct {
			Access  string `json:"access_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(b, &credentials); err != nil {
		return errors.New("authentication source shape invalid")
	}
	if len(credentials.Tokens.Access) < 1 || len(credentials.Tokens.Access) > 32<<10 || len(credentials.Tokens.Account) < 1 || len(credentials.Tokens.Account) > 256 {
		return errors.New("ChatGPT access token and account identity required")
	}
	r, events, err := h.Client.Call(ctx, "account/login/start", map[string]any{"type": "chatgptAuthTokens", "accessToken": credentials.Tokens.Access, "chatgptAccountId": credentials.Tokens.Account})
	if err != nil {
		return err
	}
	if len(r.Error) != 0 {
		return errors.New("external-token login rejected")
	}
	var result struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil || result.Type != "chatgptAuthTokens" {
		return errors.New("unexpected authentication mode")
	}
	for _, event := range events {
		if len(event.ID) != 0 {
			return errors.New("unexpected server request during authentication")
		}
		switch event.Method {
		case "account/updated", "account/login/completed":
		default:
			return errors.New("unexpected authentication event")
		}
	}
	return nil
}
