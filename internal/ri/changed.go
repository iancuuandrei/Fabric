package ri

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
)

// SourceChange compares declared source inputs, not inferred Git file changes.
type SourceChange struct {
	Producer string  `json:"producer"`
	Path     string  `json:"path"`
	Before   *string `json:"before"`
	After    *string `json:"after"`
}

// Changes separates producer/configuration changes from source input changes.
type Changes struct {
	SourceIdentityChanged bool           `json:"source_identity_changed"`
	ChangedProducers      []string       `json:"changed_producers"`
	Sources               []SourceChange `json:"sources"`
}

// ChangeReport binds comparison output to both immutable artifact IDs.
type ChangeReport struct {
	BeforeID string  `json:"before_id"`
	AfterID  string  `json:"after_id"`
	Changes  Changes `json:"changes"`
}

// Changed compares snapshots only when their controller identities share repository
// authority. Identities must originate from trusted controller observations; this
// method validates shape and bindings but does not rediscover historical checkouts.
func (c Client) Changed(ctx context.Context, before, after SnapshotRef, oldIdentity, newIdentity repository.Identity) (ChangeReport, error) {
	oldSource, err := FromRepository(oldIdentity)
	if err != nil {
		return ChangeReport{}, err
	}
	newSource, err := FromRepository(newIdentity)
	if err != nil {
		return ChangeReport{}, err
	}
	if oldIdentity.Name != newIdentity.Name || oldIdentity.Root != newIdentity.Root || oldIdentity.CommonDir != newIdentity.CommonDir || oldIdentity.ObjectFormat != newIdentity.ObjectFormat || before.Source != oldSource || after.Source != newSource {
		return ChangeReport{}, errors.New("snapshot comparison repository authority mismatch")
	}
	result, err := c.Call(ctx, map[string]any{"operation": "changed", "before_path": before.Path, "before_id": before.ID, "before_source": before.Source, "after_path": after.Path, "after_id": after.ID, "after_source": after.Source})
	if err != nil {
		return ChangeReport{}, err
	}
	var report ChangeReport
	if err := canonical.Decode(result, &report); err != nil {
		return ChangeReport{}, err
	}
	if err := validateChangeValues(report.Changes.ChangedProducers, report.Changes.Sources); err != nil {
		return ChangeReport{}, err
	}
	if report.BeforeID != before.ID || report.AfterID != after.ID || report.Changes.SourceIdentityChanged != (before.Source != after.Source) || len(report.Changes.ChangedProducers) > 128 || len(report.Changes.Sources) > 524288 {
		return ChangeReport{}, errors.New("snapshot change response binding mismatch")
	}
	previous := ""
	for _, producer := range report.Changes.ChangedProducers {
		if producer <= previous || len(producer) > 256 {
			return ChangeReport{}, errors.New("invalid changed producer order")
		}
		previous = producer
	}
	previousProducer, previousPath := "", ""
	for _, change := range report.Changes.Sources {
		if change.Producer == "" || change.Path == "" || change.Producer < previousProducer || (change.Producer == previousProducer && change.Path <= previousPath) || (change.Before == nil && change.After == nil) || (change.Before != nil && change.After != nil && *change.Before == *change.After) {
			return ChangeReport{}, errors.New("invalid source change ordering or equality")
		}
		previousProducer, previousPath = change.Producer, change.Path
	}
	return report, nil
}
