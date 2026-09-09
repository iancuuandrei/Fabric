package contextmcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"reflect"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/toolbridge"
)

// VerifyBackendReceipt proves that one context-owned MCP result is the exact
// response recorded by the expected durable context broker. It only reads and
// validates the journal; it performs no callback, repository read, or network
// operation. Later broker calls and durable closure do not invalidate an
// earlier exact receipt.
func VerifyBackendReceipt(path string, expected contextbroker.Binding, call toolbridge.Call, result toolbridge.Result) error {
	if path == "" {
		return errors.New("context broker journal path required")
	}
	if _, err := expected.ID(); err != nil {
		return errors.Join(errors.New("invalid expected context broker binding"), err)
	}
	state, err := contextbroker.Inspect(path)
	if err != nil {
		return err
	}
	if state.Binding == nil || !reflect.DeepEqual(*state.Binding, expected) {
		return errors.New("context broker binding differs from expected binding")
	}
	callID, err := contextCallID(call.RequestID)
	if err != nil {
		return err
	}
	arguments, err := canonical.Normalize(call.Arguments)
	if err != nil || len(arguments) < 2 || arguments[0] != '{' {
		return errors.New("context MCP arguments are not an object")
	}

	var request *contextbroker.Request
	for index := range state.Requests {
		candidate := &state.Requests[index]
		if candidate.CallID == callID {
			request = candidate
			break
		}
	}
	if request == nil {
		return errors.New("context MCP request is absent from broker journal")
	}
	if request.Tool != call.Tool || !bytes.Equal(request.Arguments, arguments) {
		return errors.New("context MCP request differs from broker journal")
	}

	var response *contextbroker.Response
	for index := range state.Responses {
		candidate := &state.Responses[index]
		if candidate.RequestID == request.RequestID {
			response = candidate
			break
		}
	}
	if response == nil {
		return errors.New("context MCP response is absent from broker journal")
	}
	encoded, err := canonical.Bytes(*response)
	if err != nil {
		return err
	}
	if result.Error != nil || result.IsError != !response.Success || !bytes.Equal(result.JSON, encoded) {
		return errors.New("context MCP result differs from broker response")
	}
	return nil
}

func contextCallID(raw json.RawMessage) (string, error) {
	canonicalID, err := canonicalMCPRequestID(raw)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("harness.context-mcp-call.v1\x00"))
	_, _ = hash.Write(canonicalID)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalMCPRequestID(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 256 {
		return nil, errors.New("invalid canonical MCP request identity")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid canonical MCP request identity")
	}
	var encoded []byte
	switch id := value.(type) {
	case string:
		if id == "" || !utf8.ValidString(id) || len(id) > 256 {
			return nil, errors.New("invalid canonical MCP request identity")
		}
		encoded, _ = json.Marshal(id)
	case json.Number:
		text := string(id)
		if len(text) > 64 || strings.ContainsAny(text, ".eE") {
			return nil, errors.New("invalid canonical MCP request identity")
		}
		integer, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return nil, errors.New("invalid canonical MCP request identity")
		}
		encoded = []byte(integer.String())
	default:
		return nil, errors.New("invalid canonical MCP request identity")
	}
	if !bytes.Equal(raw, encoded) {
		return nil, errors.New("invalid canonical MCP request identity")
	}
	return json.RawMessage(encoded), nil
}
