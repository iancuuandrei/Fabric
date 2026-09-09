package ri

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
)

// ChangePage is a finite comparison bound to two ordered snapshot IDs.
type ChangePage struct {
	BeforeID              string         `json:"before_id"`
	AfterID               string         `json:"after_id"`
	Limit                 int            `json:"limit"`
	SourceIdentityChanged bool           `json:"source_identity_changed"`
	ChangedProducers      []string       `json:"changed_producers"`
	Sources               []SourceChange `json:"sources"`
	NextAfter             *string        `json:"next_after"`
}

type changePosition struct {
	Producer string `json:"producer"`
	Path     string `json:"path"`
}
type changeCursor struct {
	Before   string         `json:"before"`
	After    string         `json:"after"`
	Limit    int            `json:"limit"`
	Position changePosition `json:"position"`
}

// ChangedPage verifies repository authority, page bounds and continuation progress.
func (c Client) ChangedPage(ctx context.Context, before, after SnapshotRef, oldIdentity, newIdentity repository.Identity, limit int, cursor *string) (ChangePage, error) {
	oldSource, err := FromRepository(oldIdentity)
	if err != nil {
		return ChangePage{}, err
	}
	newSource, err := FromRepository(newIdentity)
	if err != nil {
		return ChangePage{}, err
	}
	if oldIdentity.Name != newIdentity.Name || oldIdentity.Root != newIdentity.Root || oldIdentity.CommonDir != newIdentity.CommonDir || oldIdentity.ObjectFormat != newIdentity.ObjectFormat || before.Source != oldSource || after.Source != newSource || limit < 1 || limit > 128 {
		return ChangePage{}, errors.New("invalid change page authority/bounds")
	}
	decodeCursor := func(token string) (changePosition, error) {
		var value changeCursor
		if err := canonical.Decode([]byte(token), &value); err != nil {
			return changePosition{}, err
		}
		if value.Before != before.ID || value.After != after.ID || value.Limit != limit || value.Position.Producer == "" || value.Position.Path == "" {
			return changePosition{}, errors.New("change cursor binding mismatch")
		}
		return value.Position, nil
	}
	previous := changePosition{}
	if cursor != nil {
		previous, err = decodeCursor(*cursor)
		if err != nil {
			return ChangePage{}, err
		}
	}
	result, err := c.Call(ctx, map[string]any{"operation": "changed_page", "before_path": before.Path, "before_id": before.ID, "before_source": before.Source, "after_path": after.Path, "after_id": after.ID, "after_source": after.Source, "limit": limit, "cursor": cursor})
	if err != nil {
		return ChangePage{}, err
	}
	var page ChangePage
	if err := canonical.Decode(result, &page); err != nil {
		return ChangePage{}, err
	}
	if err := validateChangeValues(page.ChangedProducers, page.Sources); err != nil {
		return ChangePage{}, err
	}
	if page.BeforeID != before.ID || page.AfterID != after.ID || page.Limit != limit || len(page.Sources) > limit || len(page.ChangedProducers) > 128 || page.SourceIdentityChanged != (before.Source != after.Source) {
		return ChangePage{}, errors.New("change page response mismatch")
	}
	for _, change := range page.Sources {
		if change.Producer == "" || change.Path == "" || change.Producer < previous.Producer || (change.Producer == previous.Producer && change.Path <= previous.Path) || (change.Before == nil && change.After == nil) || (change.Before != nil && change.After != nil && *change.Before == *change.After) {
			return ChangePage{}, errors.New("invalid change page progress")
		}
		previous = changePosition{change.Producer, change.Path}
	}
	if page.NextAfter != nil {
		next, err := decodeCursor(*page.NextAfter)
		if err != nil {
			return ChangePage{}, err
		}
		if len(page.Sources) == 0 || next != previous {
			return ChangePage{}, errors.New("change continuation position mismatch")
		}
	}
	return page, nil
}
