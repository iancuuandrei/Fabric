package contextbroker

import (
	"bytes"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/sourcetools"
)

func replay(events []journal.Event) (State, error) {
	state := State{Requests: []Request{}, Responses: []Response{}}
	callIDs := map[string]bool{}
	requestIDs := map[string]bool{}
	for _, event := range events {
		if state.Closed {
			return State{}, errors.New("context broker event after terminal close")
		}
		switch event.Kind {
		case boundEvent:
			if state.Binding != nil {
				return State{}, errors.New("context broker already bound")
			}
			var binding Binding
			if err := canonical.Decode(event.Payload, &binding); err != nil {
				return State{}, err
			}
			if _, err := binding.ID(); err != nil {
				return State{}, err
			}
			copy, err := cloneBinding(binding)
			if err != nil {
				return State{}, err
			}
			state.Binding = &copy
		case requestEvent:
			if state.Binding == nil || state.Pending != nil || state.Calls >= state.Binding.Limits.MaxCalls {
				return State{}, errors.New("context request transition rejected")
			}
			var request Request
			if err := canonical.Decode(event.Payload, &request); err != nil {
				return State{}, err
			}
			bindingID, _ := state.Binding.ID()
			requestID, err := request.ID()
			if err != nil || request.RequestID != requestID || request.BindingID != bindingID || request.InvocationID != state.Binding.InvocationID || len(request.Arguments) > state.Binding.Limits.MaxRequestBytes || !catalogContains(catalogFor(state.Binding.Candidate != nil), request.Tool) {
				return State{}, errors.New("context request differs from binding")
			}
			if callIDs[request.CallID] || requestIDs[request.RequestID] {
				return State{}, errors.New("duplicate context request")
			}
			callIDs[request.CallID] = true
			requestIDs[request.RequestID] = true
			state.Requests = append(state.Requests, cloneRequest(request))
			pending := cloneRequest(request)
			state.Pending = &pending
			state.Calls++
		case responseEvent:
			if state.Binding == nil || state.Pending == nil {
				return State{}, errors.New("context response without pending request")
			}
			var response Response
			if err := canonical.Decode(event.Payload, &response); err != nil {
				return State{}, err
			}
			normal, err := canonical.Normalize(response.Content)
			if err != nil || len(normal) < 2 || normal[0] != '{' || !bytes.Equal(normal, response.Content) || len(normal) > state.Binding.Limits.MaxResponseBytes {
				return State{}, errors.New("invalid context response content")
			}
			pending := state.Pending
			if response.Version != 1 || response.BindingID != pending.BindingID || response.InvocationID != pending.InvocationID || response.RequestID != pending.RequestID || response.CallID != pending.CallID {
				return State{}, errors.New("context response identity mismatch")
			}
			if state.ResponseBytes > state.Binding.Limits.MaxTotalResponseBytes-len(normal) {
				return State{}, errors.New("context response budget exhausted")
			}
			state.ResponseBytes += len(normal)
			state.Responses = append(state.Responses, cloneResponse(response))
			state.Pending = nil
		case closedEvent:
			if state.Binding == nil || state.Pending != nil {
				return State{}, errors.New("context close transition rejected")
			}
			var closed closure
			if err := canonical.Decode(event.Payload, &closed); err != nil {
				return State{}, err
			}
			bindingID, _ := state.Binding.ID()
			if closed.Version != 1 || closed.BindingID != bindingID || closed.InvocationID != state.Binding.InvocationID {
				return State{}, errors.New("context close identity mismatch")
			}
			state.Closed = true
		default:
			return State{}, errors.New("unknown context broker event")
		}
	}
	return state, nil
}

// Inspect validates the complete broker journal without executing any request.
func Inspect(path string) (State, error) {
	events, err := journal.Read(path)
	if err != nil {
		return State{}, err
	}
	return replay(events)
}

func catalogContains(catalog []sourcetools.Definition, name string) bool {
	for _, definition := range catalog {
		if definition.Name == name {
			return true
		}
	}
	return false
}
