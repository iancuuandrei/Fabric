package providercredential

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providergateway"
)

// Binding is the non-secret identity attached to one credential lease. The
// endpoint ID indirectly binds its exact URL, adapter, authentication scheme,
// credential reference and any adapter-owned header name.
type Binding struct {
	Version            int    `json:"version"`
	AccessPolicyID     string `json:"access_policy_id"`
	AccessInvocationID string `json:"access_invocation_id"`
	RouteID            string `json:"route_id"`
	AccessProfileID    string `json:"access_profile_id"`
	Provider           string `json:"provider"`
	Runtime            string `json:"runtime"`
	EndpointID         string `json:"endpoint_id"`
	CredentialRef      string `json:"credential_ref"`
	AuthScheme         string `json:"auth_scheme"`
	BillingMode        string `json:"billing_mode"`
}

// ID validates and hashes the complete non-secret credential identity.
func (b Binding) ID() (string, error) {
	if b.Version != 1 || !credentialRef(b.Provider) || !credentialRef(b.Runtime) || !credentialRef(b.CredentialRef) || (b.AuthScheme != "bearer" && b.AuthScheme != "api-key-header") {
		return "", errors.New("invalid provider credential binding")
	}
	for _, digest := range []string{b.AccessPolicyID, b.AccessInvocationID, b.RouteID, b.AccessProfileID, b.EndpointID} {
		if len(digest) != 64 {
			return "", errors.New("invalid provider credential binding")
		}
		for _, value := range digest {
			if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
				return "", errors.New("invalid provider credential binding")
			}
		}
	}
	if b.BillingMode != "api" && b.BillingMode != "subscription" {
		return "", errors.New("invalid provider credential binding")
	}
	return canonical.Hash("harness.provider-credential-binding.v1", b)
}

// Lease owns short-lived credential bytes for one exact active model-access
// invocation. RequireActive is a snapshot; the controller remains responsible
// for serializing terminalization and outbound effects under invocation ownership.
type Lease struct {
	mu         sync.Mutex
	closed     bool
	accessPath string
	policy     access.Policy
	intent     access.Intent
	binding    Binding
	secret     []byte
}

// Resolve validates the exact policy, intent, selected profile and endpoint,
// checks active access before reading the explicit source, and owns the returned
// bytes. It performs no network operation and grants no worker credential access.
func Resolve(ctx context.Context, accessPath string, policy access.Policy, intent access.Intent, endpoint providergateway.EndpointContract, source Source) (*Lease, error) {
	if ctx == nil || source == nil || accessPath == "" {
		return nil, errors.New("provider credential resolution rejected")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	frozenPolicy, frozenIntent, err := freezeAdmission(policy, intent)
	if err != nil {
		return nil, errors.New("provider credential resolution rejected")
	}
	binding, err := bindingFor(frozenPolicy, frozenIntent, endpoint)
	if err != nil {
		return nil, err
	}
	if err := access.RequireActive(accessPath, frozenPolicy, frozenIntent); err != nil {
		return nil, err
	}
	secret, err := source.Take(ctx, binding.CredentialRef)
	defer clear(secret)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, errors.New("provider credential unavailable")
	}
	if !validSecret(secret) {
		return nil, errors.New("provider credential unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Lease{accessPath: accessPath, policy: frozenPolicy, intent: frozenIntent, binding: binding, secret: append([]byte(nil), secret...)}, nil
}

func bindingFor(policy access.Policy, intent access.Intent, endpoint providergateway.EndpointContract) (Binding, error) {
	policyID, err := policy.ID()
	if err != nil || intent.PolicyID != policyID {
		return Binding{}, errors.New("provider credential policy mismatch")
	}
	invocationID, err := intent.ID()
	if err != nil || invocationID != intent.Reservation.InvocationID {
		return Binding{}, errors.New("provider credential invocation mismatch")
	}
	routeID, err := intent.Route.ID()
	if err != nil {
		return Binding{}, errors.New("provider credential route mismatch")
	}
	routeMatched := false
	for _, route := range policy.Routes {
		if route == intent.Route {
			routeMatched = true
			break
		}
	}
	if !routeMatched {
		return Binding{}, errors.New("provider credential route mismatch")
	}
	var profile access.Profile
	profileMatched := false
	for _, candidate := range policy.Profiles {
		profileID, profileErr := candidate.ID()
		if profileErr == nil && profileID == intent.Route.AccessID {
			profile, profileMatched = candidate, true
			break
		}
	}
	if !profileMatched || profile.CredentialRef == "" || profile.AuthMode != "" || profile.Kind != intent.Reservation.BillingMode || profile.Allows(intent.Route.Runtime, intent.Route.Provider, policy.Class) != nil {
		return Binding{}, errors.New("provider credential profile mismatch")
	}
	if profile.Kind == "subscription" && intent.Reservation.CostMicroUSD != nil {
		return Binding{}, errors.New("provider credential billing mismatch")
	}
	endpointID, err := endpoint.ID()
	if err != nil || endpoint.Version != 2 || endpoint.Auth == nil || endpoint.Provider != intent.Route.Provider || endpoint.Auth.CredentialRef != profile.CredentialRef {
		return Binding{}, errors.New("provider credential endpoint mismatch")
	}
	binding := Binding{Version: 1, AccessPolicyID: policyID, AccessInvocationID: invocationID, RouteID: routeID, AccessProfileID: intent.Route.AccessID, Provider: intent.Route.Provider, Runtime: intent.Route.Runtime, EndpointID: endpointID, CredentialRef: profile.CredentialRef, AuthScheme: endpoint.Auth.Scheme, BillingMode: profile.Kind}
	if _, err := binding.ID(); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func freezeAdmission(policy access.Policy, intent access.Intent) (access.Policy, access.Intent, error) {
	var frozenPolicy access.Policy
	raw, err := canonical.Bytes(policy)
	if err != nil {
		return access.Policy{}, access.Intent{}, err
	}
	if err := canonical.Decode(raw, &frozenPolicy); err != nil {
		return access.Policy{}, access.Intent{}, err
	}
	var frozenIntent access.Intent
	raw, err = canonical.Bytes(intent)
	if err != nil {
		return access.Policy{}, access.Intent{}, err
	}
	if err := canonical.Decode(raw, &frozenIntent); err != nil {
		return access.Policy{}, access.Intent{}, err
	}
	return frozenPolicy, frozenIntent, nil
}

// Binding returns a value snapshot containing no credential bytes.
func (l *Lease) Binding() Binding {
	if l == nil {
		return Binding{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.binding
}

// WithSecret repeats the active-access snapshot immediately before invoking a
// controller transport callback with a temporary copy. The callback must not
// retain the bytes or reenter this Lease. Transport code owns header formatting.
func (l *Lease) WithSecret(ctx context.Context, use func(context.Context, []byte) error) error {
	if ctx == nil || use == nil || l == nil {
		return errors.New("provider credential use rejected")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || len(l.secret) == 0 {
		return errors.New("provider credential lease closed")
	}
	if err := access.RequireActive(l.accessPath, l.policy, l.intent); err != nil {
		return err
	}
	secret := append([]byte(nil), l.secret...)
	defer clear(secret)
	if err := use(ctx, secret); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return errors.New("provider credential consumer failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Close clears the lease-owned byte buffer. It is idempotent and waits for an
// in-process WithSecret callback to finish before releasing the buffer.
func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		clear(l.secret)
		l.secret = nil
		l.closed = true
	}
	return nil
}

// Format prevents debug formatting from exposing lease internals.
func (l *Lease) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, "[provider credential redacted]")
}
