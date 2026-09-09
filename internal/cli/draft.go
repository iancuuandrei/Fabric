package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/draftpr"
	"harness.local/engorch/internal/effects"
)

type draftPreview struct {
	Prepared control.PreparedDraft `json:"prepared"`
	IntentID string                `json:"intent_id"`
}

type draftText struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func draftClient(env string) (*draftpr.Client, error) {
	token, err := selectedGitHubToken(env)
	if err != nil {
		return nil, err
	}
	return draftpr.NewClient(token)
}

func selectedGitHubToken(env string) (string, error) {
	if len(env) == 0 || len(env) > 128 {
		return "", errors.New("invalid GitHub token environment variable name")
	}
	for i, c := range env {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return "", errors.New("invalid GitHub token environment variable name")
		}
	}
	token, ok := os.LookupEnv(env)
	if !ok {
		return "", errors.New("selected GitHub token environment variable is unset")
	}
	return token, nil
}

func draftCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	want := 5
	if command == "reconcile-draft" {
		want = 2
	}
	if len(args) != want {
		return errors.New("prepare-draft requires RUN REPOSITORY BASE_REF TEXT_JSON TOKEN_ENV; draft requires RUN PREVIEW_JSON INTENT_ID ACTOR TOKEN_ENV; reconcile-draft requires RUN TOKEN_ENV")
	}
	path, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	s, err := control.Inspect(path)
	if err != nil {
		return err
	}
	if s.RunID != args[0] || filepath.Clean(s.Creation.Repository.Root) != filepath.Clean(root) {
		return errors.New("journal/run repository binding mismatch")
	}
	var text draftText
	var preview draftPreview
	if command == "prepare-draft" {
		if err := readJSON(args[3], &text); err != nil {
			return err
		}
	} else if command == "draft" {
		if err := readJSON(args[1], &preview); err != nil {
			return err
		}
		if args[2] != preview.IntentID {
			return errors.New("explicit draft approval differs from preview")
		}
		if err := (effects.Authorization{IntentID: args[2], Actor: args[3]}).Validate(preview.Prepared.Intent); err != nil {
			return err
		}
	}
	client, err := draftClient(args[len(args)-1])
	if err != nil {
		return err
	}
	defer client.Close()
	switch command {
	case "prepare-draft":
		prepared, err := control.PrepareDraft(ctx, path, args[1], args[2], text.Title, text.Body, client)
		if err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		return output(out, draftPreview{Prepared: prepared, IntentID: id})
	case "draft":
		after, err := control.ExecuteDraft(ctx, path, preview.Prepared, effects.Authorization{IntentID: args[2], Actor: args[3]}, client)
		return errors.Join(err, output(out, after))
	case "reconcile-draft":
		after, err := control.ReconcileDraft(ctx, path, client)
		return errors.Join(err, output(out, after))
	default:
		return errors.New("unknown draft command")
	}
}
