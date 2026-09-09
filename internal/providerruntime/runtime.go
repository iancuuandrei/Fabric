package providerruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/worktree"
)

// Run starts or deterministically recovers one direct structured inference.
// It performs at most one production Client.Execute call. A completed gateway
// call whose exact content did not reach this journal remains unresolved.
func Run(ctx context.Context, config Config, invocation Invocation) (Result, error) {
	if ctx == nil || config.JournalPath == "" || config.AccessJournalPath == "" || config.GatewayJournalPath == "" || config.Transport == nil {
		return Result{}, ErrRejected
	}
	body, err := BuildRequest(config.Binding, config.Expectation, invocation)
	if err != nil {
		return Result{}, err
	}
	binding, err := bindingFor(config, invocation, body)
	if err != nil {
		return Result{}, err
	}
	state, err := Inspect(config.JournalPath)
	if err != nil {
		return Result{}, ErrUnresolved
	}
	if state.Binding == nil {
		gateway, inspectErr := providergateway.Inspect(config.GatewayJournalPath)
		if inspectErr != nil || gateway.Binding == nil || !reflect.DeepEqual(*gateway.Binding, config.Binding) || len(gateway.Calls) != 0 {
			return Result{}, ErrUnresolved
		}
		if err := appendEvent(config.JournalPath, boundEvent, binding); err != nil {
			return Result{}, ErrUnresolved
		}
		state, err = Inspect(config.JournalPath)
		if err != nil {
			return Result{}, ErrUnresolved
		}
	}
	if state.Binding == nil || !sameBinding(*state.Binding, binding) {
		return Result{}, ErrRejected
	}
	if state.Result != nil {
		gateway, inspectErr := providergateway.Inspect(config.GatewayJournalPath)
		if inspectErr != nil || !gatewayContainsReceipt(config.Binding, gateway, state.Result.Receipt) {
			return Result{}, ErrUnresolved
		}
		return *state.Result, nil
	}
	execute := func(_ worktree.LeaseIdentity) error {
		result, runErr := executeOnce(ctx, config, binding)
		if runErr != nil {
			return runErr
		}
		state.Result = &result
		return nil
	}
	if config.WorkspaceRequest != nil {
		if err := config.WorkspaceLease.WithOwnership(*config.WorkspaceRequest, execute); err != nil {
			if errors.Is(err, ErrUnresolved) {
				return Result{}, err
			}
			return Result{}, ErrUnresolved
		}
	} else if err := execute(worktree.LeaseIdentity{}); err != nil {
		return Result{}, err
	}
	return *state.Result, nil
}

func executeOnce(ctx context.Context, config Config, binding InvocationRecord) (Result, error) {
	gateway, err := providergateway.Inspect(config.GatewayJournalPath)
	if err != nil || gateway.Binding == nil || !reflect.DeepEqual(*gateway.Binding, config.Binding) || len(gateway.Calls) != 0 || gateway.Pending != nil {
		return Result{}, ErrUnresolved
	}
	transportResult, err := config.Transport.Execute(ctx, providertransport.Request{AccessJournalPath: config.AccessJournalPath, GatewayJournalPath: config.GatewayJournalPath, Policy: config.Policy, Intent: config.Intent, Binding: config.Binding, Lease: config.Credential, Body: binding.RequestBody, Expectation: binding.Expectation.gatewayExpectation()})
	if err != nil {
		if errors.Is(err, providertransport.ErrPending) {
			return Result{}, ErrUnresolved
		}
		return Result{}, errors.Join(ErrRejected, err)
	}
	if transportResult.Content == nil || transportResult.Receipt.Finish != "stop" || len(transportResult.Content.ToolCalls) != 0 {
		return Result{}, ErrUnresolved
	}
	parsed, digest, err := validateOutput(binding.Invocation.Output, transportResult.Content.OutputText)
	if err != nil {
		return Result{}, ErrUnresolved
	}
	result := Result{Version: 1, Text: transportResult.Content.OutputText, JSON: parsed, JSONSHA256: digest, Receipt: transportResult.Receipt}
	if err := validateResult(binding, result); err != nil {
		return Result{}, ErrUnresolved
	}
	if err := appendEvent(config.JournalPath, resultEvent, result); err != nil {
		return Result{}, ErrUnresolved
	}
	return result, nil
}

func validateOutput(contract OutputContract, text string) (string, string, error) {
	if contract.validate() != nil || len(text) == 0 || len(text) > maximumPromptBytes {
		return "", "", errors.New("invalid structured provider output")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.UseNumber()
	nodes := 0
	if err := readUniqueJSONValue(decoder, 0, &nodes); err != nil {
		return "", "", errors.New("provider output is not one unambiguous JSON value")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", "", errors.New("provider output contains trailing JSON")
	}
	digest := sha256.Sum256([]byte(text))
	return text, hex.EncodeToString(digest[:]), nil
}

func readUniqueJSONValue(decoder *json.Decoder, depth int, nodes *int) error {
	*nodes++
	if depth > 64 || *nodes > 10000 {
		return errors.New("JSON complexity bound exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return errors.New("duplicate or invalid JSON object key")
			}
			seen[key] = true
			if err := readUniqueJSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := readUniqueJSONValue(decoder, depth+1, nodes); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func gatewayContainsReceipt(binding providergateway.Binding, state providergateway.State, receipt providergateway.CallReceipt) bool {
	return state.Binding != nil && reflect.DeepEqual(*state.Binding, binding) && state.Pending == nil && state.Finished && !state.Exhausted &&
		len(state.Calls) == 1 && state.Calls[0].Receipt != nil && reflect.DeepEqual(*state.Calls[0].Receipt, receipt) && reflect.DeepEqual(state.Aggregate, receipt.Usage)
}
