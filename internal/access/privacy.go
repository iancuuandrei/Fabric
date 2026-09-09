package access

import (
	"errors"

	"harness.local/engorch/internal/safepath"
)

// PrivacyPolicy records the operator-selected terms for one access product.
// It is not evidence that a provider complies with those terms. Repository class
// allowances remain explicit and are never inferred from the provider name.
type PrivacyPolicy struct {
	Version       int    `json:"version" toml:"version"`
	Training      string `json:"training" toml:"training"`
	Retention     string `json:"retention" toml:"retention"`
	RetentionDays int    `json:"retention_days" toml:"retention_days"`
	TermsSHA256   string `json:"terms_sha256" toml:"terms_sha256"`
}

// Validate distinguishes unknown terms from explicit no-training or zero
// retention declarations. TermsSHA256 optionally binds separately retained
// operator evidence without placing its text or a credential-bearing URL here.
func (p PrivacyPolicy) Validate() error {
	if p.Version != 1 || p.TermsSHA256 != "" && safepath.RequireDigest(p.TermsSHA256) != nil {
		return errors.New("invalid access privacy identity")
	}
	switch p.Training {
	case "unknown", "allowed", "excluded":
	default:
		return errors.New("explicit training policy required")
	}
	switch p.Retention {
	case "unknown", "zero", "provider-defined":
		if p.RetentionDays != 0 {
			return errors.New("retention days require bounded retention")
		}
	case "bounded":
		if p.RetentionDays < 1 || p.RetentionDays > 36500 {
			return errors.New("bounded retention requires positive days")
		}
	default:
		return errors.New("explicit retention policy required")
	}
	return nil
}
