package opencode

import (
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
)

// wireObject keeps exact numeric lexemes while validating the same key, Unicode
// and nesting constraints as internal JSON. Only a validation copy has numeric
// values replaced; callers receive original bytes and must validate field domains.
func wireObject(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) > canonical.MaxBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, errors.New("invalid OpenCode JSON")
	}
	validation := make([]byte, 0, len(raw))
	quoted, escaped := false, false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if quoted {
			validation = append(validation, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			validation = append(validation, c)
			continue
		}
		if c == '-' || c >= '0' && c <= '9' {
			start := i
			for i+1 < len(raw) {
				n := raw[i+1]
				if !(n >= '0' && n <= '9' || n == '.' || n == 'e' || n == 'E' || n == '+' || n == '-') {
					break
				}
				i++
			}
			if _, err := strconv.ParseFloat(string(raw[start:i+1]), 64); err != nil {
				return nil, errors.New("OpenCode numeric value out of range")
			}
			validation = append(validation, '0')
			continue
		}
		validation = append(validation, c)
	}
	if _, err := canonical.Normalize(validation); err != nil {
		return nil, errors.New("ambiguous OpenCode JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errors.New("OpenCode object required")
	}
	return object, nil
}
