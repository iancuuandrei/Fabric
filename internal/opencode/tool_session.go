package opencode

import (
	"context"
	"errors"
	"slices"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

// ToolSessionBinding binds session identity to one exact role tool allowlist and
// controller catalog. It contains no endpoint credential or invocation bearer.
type ToolSessionBinding struct {
	Session       SessionBinding `json:"session"`
	ToolNames     []string       `json:"tool_names"`
	CatalogSHA256 string         `json:"catalog_sha256"`
	// StructuredOutput is the optional exact native terminal contract. The
	// runtime populates it only for its validated writer/fixer path.
	StructuredOutput *StructuredOutputExpectation `json:"structured_output,omitempty"`
}

// Validate rejects tool bindings which would normalize to a different durable
// identity. Tool spelling and limits deliberately reuse ToolsConfigurationSpec.
func (b ToolSessionBinding) Validate() error {
	_, err := b.permissionIDs()
	if err != nil {
		return err
	}
	if b.StructuredOutput != nil {
		if err := b.StructuredOutput.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (b ToolSessionBinding) permissionIDs() ([]string, error) {
	if err := b.Session.Validate(); err != nil {
		return nil, err
	}
	if safepath.RequireDigest(b.CatalogSHA256) != nil {
		return nil, errors.New("invalid tool catalog identity")
	}
	ids, err := ToolPermissionIDs(b.ToolNames)
	expectedIDs := make([]string, len(b.ToolNames))
	for n, name := range b.ToolNames {
		expectedIDs[n] = ToolsMCPServerName + "_" + name
	}
	if err != nil || !slices.Equal(expectedIDs, ids) {
		return nil, errors.New("tool session names must be safe, sorted and unique")
	}
	return ids, nil
}

func (b ToolSessionBinding) permissions() ([]sessionPermissionRule, error) {
	ids, err := b.permissionIDs()
	if err != nil {
		return nil, err
	}
	if b.StructuredOutput != nil {
		if err := b.StructuredOutput.Validate(); err != nil {
			return nil, err
		}
	}
	rules := make([]sessionPermissionRule, 1, len(ids)+2)
	rules[0] = denySessionPermissions[0]
	for _, id := range ids {
		rules = append(rules, sessionPermissionRule{
			Permission: id,
			Pattern:    "*",
			Action:     "allow",
		})
	}
	if b.StructuredOutput != nil {
		rules = append(rules, sessionPermissionRule{
			Permission: StructuredOutputToolName,
			Pattern:    "*",
			Action:     "allow",
		})
	}
	return rules, nil
}

func snapshotToolSessionBinding(binding ToolSessionBinding) ToolSessionBinding {
	binding.ToolNames = append([]string(nil), binding.ToolNames...)
	if binding.StructuredOutput != nil {
		expectation := *binding.StructuredOutput
		expectation.Schema = append([]byte(nil), binding.StructuredOutput.Schema...)
		binding.StructuredOutput = &expectation
	}
	return binding
}

func validateToolSessionMetadata(raw []byte, intentID, catalogSHA256 string) error {
	session, err := wireObject(raw)
	if err != nil {
		return err
	}
	metadata, err := wireObject(session["metadata"])
	if err != nil || !toolsExactKeys(metadata, "engorch_intent_id", "engorch_catalog_sha256") {
		return errors.New("tool session metadata mismatch")
	}
	var actualIntent, actualCatalog string
	if field(metadata, "engorch_intent_id", &actualIntent) != nil || actualIntent != intentID ||
		field(metadata, "engorch_catalog_sha256", &actualCatalog) != nil || actualCatalog != catalogSHA256 {
		return errors.New("tool session metadata mismatch")
	}
	return nil
}

// CreateToolSessionOnce performs exactly one POST for an already journaled tool
// session intent, followed by strict independent readback. It never retries.
func (c *Client) CreateToolSessionOnce(ctx context.Context, b ToolSessionBinding) (string, error) {
	b = snapshotToolSessionBinding(b)
	rules, err := b.permissions()
	if err != nil {
		return "", err
	}
	return c.createSessionOnce(ctx, b.Session, rules, b.CatalogSHA256)
}

// ReconcileToolSession performs GET-only bounded marker lookup and strict
// readback of the exact tool permissions and catalog metadata.
func (c *Client) ReconcileToolSession(ctx context.Context, b ToolSessionBinding) (string, error) {
	b = snapshotToolSessionBinding(b)
	rules, err := b.permissions()
	if err != nil {
		return "", err
	}
	return c.reconcileSession(ctx, b.Session, rules, b.CatalogSHA256)
}

type toolSessionCreationState struct {
	Binding *ToolSessionBinding
	ID      string
}

func equalToolSessionBinding(a, b ToolSessionBinding) bool {
	if a.Session != b.Session || a.CatalogSHA256 != b.CatalogSHA256 || !slices.Equal(a.ToolNames, b.ToolNames) {
		return false
	}
	if a.StructuredOutput == nil || b.StructuredOutput == nil {
		return a.StructuredOutput == nil && b.StructuredOutput == nil
	}
	return equalCanonical(a.StructuredOutput, b.StructuredOutput)
}

func replayToolSessionCreation(events []journal.Event) (toolSessionCreationState, error) {
	state := toolSessionCreationState{}
	for _, event := range events {
		switch event.Kind {
		case "opencode.tool-session-intent":
			if state.Binding != nil {
				return toolSessionCreationState{}, errors.New("tool session creation already attempted")
			}
			var binding ToolSessionBinding
			if err := canonical.Decode(event.Payload, &binding); err != nil {
				return toolSessionCreationState{}, err
			}
			if err := binding.Validate(); err != nil {
				return toolSessionCreationState{}, err
			}
			state.Binding = &binding
		case "opencode.tool-session-observed":
			var observation struct {
				Binding ToolSessionBinding `json:"binding"`
				ID      string             `json:"id"`
			}
			if err := canonical.Decode(event.Payload, &observation); err != nil {
				return toolSessionCreationState{}, err
			}
			if state.Binding == nil || state.ID != "" || !equalToolSessionBinding(observation.Binding, *state.Binding) || !locator(observation.ID) {
				return toolSessionCreationState{}, errors.New("invalid tool session observation")
			}
			state.ID = observation.ID
		default:
			return toolSessionCreationState{}, errors.New("unknown tool session creation event")
		}
	}
	return state, nil
}

func appendToolSessionCreation(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error {
		_, err := replayToolSessionCreation(events)
		return err
	})
	return err
}

func observeToolSessionCreation(path string, binding ToolSessionBinding, id string) error {
	return appendToolSessionCreation(path, "opencode.tool-session-observed", struct {
		Binding ToolSessionBinding `json:"binding"`
		ID      string             `json:"id"`
	}{binding, id})
}

// CreateToolSession persists the exact binding before its single POST. Any
// failure remains unresolved and may only be inspected through recovery.
func (c *Client) CreateToolSession(ctx context.Context, path string, binding ToolSessionBinding) (string, error) {
	binding = snapshotToolSessionBinding(binding)
	if err := binding.Validate(); err != nil {
		return "", err
	}
	if err := appendToolSessionCreation(path, "opencode.tool-session-intent", binding); err != nil {
		return "", err
	}
	id, err := c.CreateToolSessionOnce(ctx, binding)
	if err != nil {
		return "", err
	}
	if err := observeToolSessionCreation(path, binding, id); err != nil {
		return "", err
	}
	return id, nil
}

// RecoverToolSession never creates. It returns a journaled locator or performs
// one GET-only reconciliation and records the exact recovered observation.
func (c *Client) RecoverToolSession(ctx context.Context, path string, binding ToolSessionBinding) (string, error) {
	binding = snapshotToolSessionBinding(binding)
	events, err := journal.Read(path)
	if err != nil {
		return "", err
	}
	state, err := replayToolSessionCreation(events)
	if err != nil {
		return "", err
	}
	if state.Binding == nil || !equalToolSessionBinding(*state.Binding, binding) {
		return "", errors.New("tool session creation intent mismatch")
	}
	if state.ID != "" {
		return state.ID, nil
	}
	id, err := c.ReconcileToolSession(ctx, binding)
	if err != nil {
		return "", err
	}
	if err := observeToolSessionCreation(path, binding, id); err != nil {
		return "", err
	}
	return id, nil
}
