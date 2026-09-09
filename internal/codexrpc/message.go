package codexrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

// MaxMessage is the maximum number of bytes in one provider envelope.
const MaxMessage = 1 << 20

// Message preserves provider payloads without treating them as harness authority.
type Message struct {
	toolHandled bool
	EmittedAtMS *int64          `json:"emittedAtMs,omitempty"`
	ID          json.RawMessage `json:"id,omitempty"`
	Method      string          `json:"method,omitempty"`
	Params      json.RawMessage `json:"params,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
}

func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("provider JSON nesting limit")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate provider JSON key")
			}
			seen[s] = true
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected provider JSON delimiter")
	}
	_, err = d.Token()
	return err
}

func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(id, &s) == nil {
		return len(s) > 0 && len(s) <= 256
	}
	n, err := strconv.ParseInt(string(id), 10, 64)
	return err == nil && n >= 0 && n <= 9007199254740991
}

// Decode rejects ambiguous envelopes while retaining version-specific payloads.
// Unlike harness canonical JSON, provider payloads may contain fractional numbers.
func Decode(b []byte) (Message, error) {
	var m Message
	if len(b) == 0 || len(b) > MaxMessage || !utf8.Valid(b) {
		return m, errors.New("invalid provider message size or UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := uniqueValue(d, 0); err != nil {
		return m, err
	}
	if _, err := d.Token(); err != io.EOF {
		return m, errors.New("multiple provider JSON values")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil || fields == nil {
		return m, errors.New("provider envelope must be an object")
	}
	for k := range fields {
		switch k {
		case "id", "method", "params", "result", "error", "jsonrpc", "emittedAtMs":
		default:
			return m, errors.New("unknown provider envelope field: " + k[:min(len(k), 64)])
		}
	}
	if version, ok := fields["jsonrpc"]; ok && string(version) != `"2.0"` {
		return m, errors.New("unsupported JSON-RPC version")
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	_, hasMethod := fields["method"]
	_, hasID := fields["id"]
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	if _, ok := fields["emittedAtMs"]; ok && (m.EmittedAtMS == nil || *m.EmittedAtMS < 0 || *m.EmittedAtMS > 9007199254740991 || !hasMethod || hasID) {
		return m, errors.New("invalid provider notification timestamp")
	}
	if hasID && !validID(m.ID) {
		return m, errors.New("invalid provider request ID")
	}
	if hasMethod {
		if m.Method == "" || len(m.Method) > 256 || hasResult || hasError {
			return m, errors.New("invalid provider method envelope")
		}
		if len(m.Params) > 0 && (len(bytes.TrimSpace(m.Params)) == 0 || bytes.TrimSpace(m.Params)[0] != '{') {
			return m, errors.New("provider params must be an object")
		}
	} else if !hasID || hasResult == hasError || fields["params"] != nil {
		return m, errors.New("invalid provider response envelope")
	}
	return m, nil
}
