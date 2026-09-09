// Package providercredential resolves explicitly selected provider credentials
// into short-lived controller-owned memory. It performs no ambient discovery.
package providercredential

import (
	"context"
	"errors"
)

const (
	maxMappings    = 128
	maxSecretBytes = 32 << 10
)

// Source transfers ownership of credential bytes for one exact policy reference.
// Implementations must not return shared mutable storage.
type Source interface {
	Take(context.Context, string) ([]byte, error)
}

// EnvironmentSource maps policy credential references to explicit environment
// names. The injected lookup is the only environment access it can perform.
type EnvironmentSource struct {
	mapping map[string]string
	lookup  func(string) (string, bool)
}

// NewEnvironmentSource freezes an explicit reference mapping. Callers that want
// process environment access must inject os.LookupEnv themselves.
func NewEnvironmentSource(mapping map[string]string, lookup func(string) (string, bool)) (*EnvironmentSource, error) {
	if len(mapping) < 1 || len(mapping) > maxMappings || lookup == nil {
		return nil, errors.New("invalid explicit credential source")
	}
	frozen := make(map[string]string, len(mapping))
	for ref, name := range mapping {
		if !credentialRef(ref) || !environmentName(name) {
			return nil, errors.New("invalid explicit credential mapping")
		}
		if _, duplicate := frozen[ref]; duplicate {
			return nil, errors.New("duplicate credential mapping")
		}
		frozen[ref] = name
	}
	return &EnvironmentSource{mapping: frozen, lookup: lookup}, nil
}

// Take resolves only the requested mapped reference and returns a fresh byte
// slice. Errors never include the reference, environment name or secret value.
// Go strings held by the injected lookup cannot be erased by this package.
func (s *EnvironmentSource) Take(ctx context.Context, ref string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("provider credential unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.lookup == nil || !credentialRef(ref) {
		return nil, errors.New("provider credential unavailable")
	}
	name, ok := s.mapping[ref]
	if !ok {
		return nil, errors.New("provider credential unavailable")
	}
	value, ok := s.lookup(name)
	if !ok {
		return nil, errors.New("provider credential unavailable")
	}
	if !validSecretString(value) {
		return nil, errors.New("provider credential unavailable")
	}
	secret := []byte(value)
	if err := ctx.Err(); err != nil {
		clear(secret)
		return nil, err
	}
	return secret, nil
}

func validSecretString(secret string) bool {
	if len(secret) < 1 || len(secret) > maxSecretBytes {
		return false
	}
	for index := 0; index < len(secret); index++ {
		if secret[index] < 33 || secret[index] > 126 {
			return false
		}
	}
	return true
}

func credentialRef(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func environmentName(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index, c := range value {
		if index == 0 {
			if !(c >= 'A' && c <= 'Z' || c == '_') {
				return false
			}
			continue
		}
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func validSecret(secret []byte) bool {
	if len(secret) < 1 || len(secret) > maxSecretBytes {
		return false
	}
	for _, value := range secret {
		if value < 33 || value > 126 {
			return false
		}
	}
	return true
}
