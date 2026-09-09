package opencode

import (
	"errors"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/runtime"
)

// DispatchForInvocation binds the controller's immutable routing to explicit
// OpenCode locators and agent. Effort is the exact OpenCode variant identifier;
// no implicit alias or fallback is selected. This does not admit provider access.
func DispatchForInvocation(i runtime.Invocation, session, message, agent, directory, root string) (DispatchIntent, error) {
	return DispatchForInvocationWithStructuredOutput(i, session, message, agent, directory, root, nil)
}

// DispatchForInvocationWithStructuredOutput binds an optional exact native
// OpenCode structured-output contract. A nil expectation preserves the legacy
// text-only dispatch representation.
func DispatchForInvocationWithStructuredOutput(i runtime.Invocation, session, message, agent, directory, root string, structured *StructuredOutputExpectation) (DispatchIntent, error) {
	expected, err := runtime.NewInvocation(i.Profile, i.Input)
	if err != nil {
		return DispatchIntent{}, err
	}
	if i.Version != 1 || i.ID != expected.ID || i.Profile.Runtime != "opencode-http" {
		return DispatchIntent{}, errors.New("invalid OpenCode invocation")
	}
	if structured != nil {
		copy := *structured
		copy.Schema = append([]byte(nil), structured.Schema...)
		structured = &copy
		if err := structured.Validate(); err != nil {
			return DispatchIntent{}, err
		}
	}
	intent := DispatchIntent{Binding: Binding{SessionID: session, ParentID: message, Provider: i.Profile.Provider, Model: i.Profile.Model, Agent: agent, Directory: directory, Root: root, Variant: i.Profile.Effort}, Text: i.Input, StructuredOutput: structured}
	if err := validatePrompt(intent.Binding, intent.Text); err != nil {
		return DispatchIntent{}, err
	}
	return intent, nil
}

// ValidateInvocationAccess prevents mixing an invocation with another route,
// role, input or reservation identity. This pure check does not prove that the
// reservation is durable/active, that credentials are admitted, or that provider
// token and monetary ceilings can be enforced; those remain controller gates.
func ValidateInvocationAccess(i runtime.Invocation, admission access.Intent) error {
	expected, err := runtime.NewInvocation(i.Profile, i.Input)
	if err != nil {
		return err
	}
	if i.Version != 1 || i.ID != expected.ID || i.Profile.Runtime != "opencode-http" {
		return errors.New("invalid OpenCode invocation")
	}
	input, err := access.InputID(i.Input)
	if err != nil {
		return err
	}
	r := admission.Route
	if admission.InputHash != input || r.Runtime != i.Profile.Runtime || r.Provider != i.Profile.Provider || r.Model != i.Profile.Model || r.Effort != i.Profile.Effort || r.Role != i.Profile.Role {
		return errors.New("OpenCode invocation/access mismatch")
	}
	id, err := admission.ID()
	if err != nil {
		return err
	}
	if id != admission.Reservation.InvocationID {
		return errors.New("OpenCode reservation identity mismatch")
	}
	return nil
}

// ValidateDurableInvocationAccess additionally requires the exact admission to
// be active in the selected run's fully validated ledger. This is a point-in-time
// snapshot only. The controller still owns dispatch serialization, runtime and
// credential admission, and provider limit enforcement before dispatch.
func ValidateDurableInvocationAccess(path string, policy access.Policy, i runtime.Invocation, admission access.Intent) error {
	if err := ValidateInvocationAccess(i, admission); err != nil {
		return err
	}
	return access.RequireActive(path, policy, admission)
}
