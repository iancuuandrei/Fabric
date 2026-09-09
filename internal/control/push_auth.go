package control

import (
	"context"
	"errors"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitpush"
)

func pushCredential(destination string, credentials []*gitpush.Credential) (*gitpush.Credential, error) {
	if len(credentials) == 0 {
		return nil, nil
	}
	if len(credentials) != 1 {
		return nil, errors.New("exactly one explicit push credential supported")
	}
	if err := credentials[0].ValidateDestination(destination); err != nil {
		return nil, err
	}
	return credentials[0], nil
}

func readPushRemote(ctx context.Context, plan gitpush.Plan, credential *gitpush.Credential) (gitpush.RefObservation, error) {
	if credential == nil {
		return gitpush.ObserveRemote(ctx, plan)
	}
	return gitpush.ObserveRemoteAuthenticated(ctx, plan, credential)
}

func runPush(ctx context.Context, plan gitpush.Plan, intent effects.Intent, auth effects.Authorization, credential *gitpush.Credential) (gitpush.RefObservation, error) {
	if credential == nil {
		return gitpush.Execute(ctx, plan, intent, auth)
	}
	return gitpush.ExecuteAuthenticated(ctx, plan, intent, auth, credential)
}
