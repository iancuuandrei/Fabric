package access

import (
	"errors"
	"slices"

	"harness.local/engorch/internal/canonical"
)

// Class is an operator-selected data classification, never inferred from text.
type Class string

const (
	// Public permits data distribution under the selected public access policy.
	Public Class = "PUBLIC"
	// Private requires an explicit private-data allowance.
	Private Class = "PRIVATE"
	// Confidential requires an explicit confidential-data allowance.
	Confidential Class = "CONFIDENTIAL"
)

// Profile identifies an access policy without containing credential values.
// CredentialRef names a controller-resolved credential; AuthMode identifies a
// runtime-host authentication mechanism. Neither grants worker credential access.
type Profile struct {
	Version                  int            `json:"version" toml:"version"`
	Name                     string         `json:"name" toml:"name"`
	Kind                     string         `json:"kind" toml:"kind"`
	Runtime                  string         `json:"runtime" toml:"runtime"`
	Provider                 string         `json:"provider" toml:"provider"`
	CredentialRef            string         `json:"credential_ref" toml:"credential_ref"`
	AuthMode                 string         `json:"auth_mode" toml:"auth_mode"`
	RepositoryClasses        []Class        `json:"repository_classes" toml:"repository_classes"`
	Privacy                  *PrivacyPolicy `json:"privacy,omitempty" toml:"privacy"`
	MaxConcurrentInvocations int            `json:"max_concurrent_invocations,omitempty" toml:"max_concurrent_invocations"`
}

func identifier(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// Validate rejects incomplete policies, unknown billing kinds and duplicate or
// unknown classes. There is no implicit privacy allowance or authentication mode.
func (p Profile) Validate() error {
	if (p.Version != 1 && p.Version != 2) || !identifier(p.Name) || !identifier(p.Runtime) || !identifier(p.Provider) {
		return errors.New("invalid access profile identity")
	}
	if p.Version == 1 && p.Privacy != nil || p.Version == 2 && (p.Privacy == nil || p.Privacy.Validate() != nil) {
		return errors.New("access profile version requires explicit privacy semantics")
	}
	if p.MaxConcurrentInvocations < 0 || p.MaxConcurrentInvocations > 1024 || p.Version == 1 && p.MaxConcurrentInvocations != 0 {
		return errors.New("invalid profile concurrency override")
	}
	switch p.Kind {
	case "api":
		if !identifier(p.CredentialRef) || p.AuthMode != "" {
			return errors.New("API access requires only a credential reference")
		}
	case "subscription":
		if !((identifier(p.AuthMode) && p.CredentialRef == "") || (identifier(p.CredentialRef) && p.AuthMode == "")) {
			return errors.New("subscription access requires exactly one authentication mode or credential reference")
		}
	default:
		return errors.New("unsupported access kind")
	}
	if len(p.RepositoryClasses) == 0 || len(p.RepositoryClasses) > 3 {
		return errors.New("explicit repository classes required")
	}
	seen := map[Class]bool{}
	for _, class := range p.RepositoryClasses {
		if class != Public && class != Private && class != Confidential || seen[class] {
			return errors.New("invalid repository class allowance")
		}
		seen[class] = true
	}
	return nil
}

// ID hashes the validated policy with classes sorted as a set. It does not hash
// credentials or mutate the caller's slice. Changing access semantics changes ID.
func (p Profile) ID() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	p.RepositoryClasses = slices.Clone(p.RepositoryClasses)
	slices.Sort(p.RepositoryClasses)
	if p.Version == 2 {
		return canonical.Hash("harness.access-profile.v2", p)
	}
	return canonical.Hash("harness.access-profile.v1", p)
}

// Allows checks exact runtime/provider binding and an explicit class allowance.
// Budget, role and concurrency admission must additionally occur in the gate.
func (p Profile) Allows(runtime, provider string, class Class) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if runtime != p.Runtime || provider != p.Provider || !slices.Contains(p.RepositoryClasses, class) {
		return errors.New("access profile denies route or repository class")
	}
	return nil
}
