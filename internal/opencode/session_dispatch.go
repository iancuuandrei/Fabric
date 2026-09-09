package opencode

import (
	"context"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
)

type sessionCreationState struct {
	Binding *SessionBinding
	ID      string
}

func replaySessionCreation(events []journal.Event) (sessionCreationState, error) {
	s := sessionCreationState{}
	for _, e := range events {
		switch e.Kind {
		case "opencode.session-intent":
			if s.Binding != nil {
				return sessionCreationState{}, errors.New("session creation already attempted")
			}
			var b SessionBinding
			if err := canonical.Decode(e.Payload, &b); err != nil {
				return sessionCreationState{}, err
			}
			if err := b.Validate(); err != nil {
				return sessionCreationState{}, err
			}
			s.Binding = &b
		case "opencode.session-observed":
			var observation struct {
				Binding SessionBinding `json:"binding"`
				ID      string         `json:"id"`
			}
			if err := canonical.Decode(e.Payload, &observation); err != nil {
				return sessionCreationState{}, err
			}
			if s.Binding == nil || s.ID != "" || observation.Binding != *s.Binding || !locator(observation.ID) {
				return sessionCreationState{}, errors.New("invalid session observation")
			}
			s.ID = observation.ID
		default:
			return sessionCreationState{}, errors.New("unknown session creation event")
		}
	}
	return s, nil
}

func appendSessionCreation(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error { _, err := replaySessionCreation(events); return err })
	return err
}

func observeSessionCreation(path string, b SessionBinding, id string) error {
	return appendSessionCreation(path, "opencode.session-observed", struct {
		Binding SessionBinding `json:"binding"`
		ID      string         `json:"id"`
	}{b, id})
}

// CreateSession persists intent before creation on the caller's admitted owned
// host. Every uncertain result requires recovery, even a crash before the POST.
// The caller supplies a private, dedicated journal path.
func (c *Client) CreateSession(ctx context.Context, path string, b SessionBinding) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	if err := appendSessionCreation(path, "opencode.session-intent", b); err != nil {
		return "", err
	}
	id, err := c.CreateSessionOnce(ctx, b)
	if err != nil {
		return "", err
	}
	if err := observeSessionCreation(path, b, id); err != nil {
		return "", err
	}
	return id, nil
}

// RecoverSession only reads the host when the journal lacks an observation.
// Returning a cached locator does not re-establish live host/session admission.
func (c *Client) RecoverSession(ctx context.Context, path string, b SessionBinding) (string, error) {
	events, err := journal.Read(path)
	if err != nil {
		return "", err
	}
	s, err := replaySessionCreation(events)
	if err != nil {
		return "", err
	}
	if s.Binding == nil || *s.Binding != b {
		return "", errors.New("session creation intent mismatch")
	}
	if s.ID != "" {
		return s.ID, nil
	}
	id, err := c.ReconcileSession(ctx, b)
	if err != nil {
		return "", err
	}
	if err := observeSessionCreation(path, b, id); err != nil {
		return "", err
	}
	return id, nil
}
