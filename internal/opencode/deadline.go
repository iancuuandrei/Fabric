package opencode

import (
	"context"
	"errors"
	"time"
)

const (
	defaultConnectTimeout        = 5 * time.Second
	defaultReadbackTimeout       = 30 * time.Second
	defaultResponseHeaderTimeout = 30 * time.Second
)

// TransportPolicy separates bounded control/readback operations from a
// synchronous message POST, whose only time bound is its caller deadline.
// Zero values select the production defaults.
type TransportPolicy struct {
	ConnectTimeout        time.Duration
	ReadbackTimeout       time.Duration
	ResponseHeaderTimeout time.Duration
}

func normalizeTransportPolicy(policy TransportPolicy) (TransportPolicy, error) {
	if policy.ConnectTimeout == 0 {
		policy.ConnectTimeout = defaultConnectTimeout
	}
	if policy.ReadbackTimeout == 0 {
		policy.ReadbackTimeout = defaultReadbackTimeout
	}
	if policy.ResponseHeaderTimeout == 0 {
		policy.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	}
	if policy.ConnectTimeout < time.Millisecond || policy.ConnectTimeout > 5*time.Second || policy.ReadbackTimeout < time.Millisecond || policy.ReadbackTimeout > 5*time.Minute || policy.ResponseHeaderTimeout < time.Millisecond || policy.ResponseHeaderTimeout > 5*time.Minute {
		return TransportPolicy{}, errors.New("invalid OpenCode transport policy")
	}
	return policy, nil
}

func boundedReadbackContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, errors.New("OpenCode request requires context")
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
		return ctx, func() {}, nil
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	return bounded, cancel, nil
}
