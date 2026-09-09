package opencode

import (
	"encoding/json"
	"errors"
	"unicode"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
)

const providerItemIDMaximumBytes = 256

// ProviderItemID is an opaque provider-domain identity. Its contents are never
// interpreted as an OpenCode locator, call identity, or filesystem path.
type ProviderItemID string

func (id *ProviderItemID) UnmarshalJSON(raw []byte) error {
	if id == nil {
		return errors.New("invalid provider item ID")
	}
	normal, err := canonical.Normalize(raw)
	if err != nil {
		return errors.New("invalid provider item ID")
	}
	var value string
	if err := json.Unmarshal(normal, &value); err != nil || !validProviderItemID(ProviderItemID(value)) {
		return errors.New("invalid provider item ID")
	}
	*id = ProviderItemID(value)
	return nil
}

func validProviderItemID(id ProviderItemID) bool {
	value := string(id)
	if value == "" || len(value) > providerItemIDMaximumBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
