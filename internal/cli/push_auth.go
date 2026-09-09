package cli

import (
	"errors"
	"harness.local/engorch/internal/gitpush"
)

func pushCredentials(destination string, extra []string) ([]*gitpush.Credential, error) {
	if len(extra) == 0 {
		return nil, nil
	}
	if len(extra) != 1 {
		return nil, errors.New("one token environment selector required")
	}
	token, err := selectedGitHubToken(extra[0])
	if err != nil {
		return nil, err
	}
	credential, err := gitpush.NewCredential(destination, token)
	if err != nil {
		return nil, err
	}
	return []*gitpush.Credential{credential}, nil
}
