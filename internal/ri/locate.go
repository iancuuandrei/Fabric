package ri

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"strings"
)

// LocateQuery selects all observations containing an exact file byte position.
type LocateQuery struct {
	Path     string `json:"path"`
	Offset   int    `json:"offset"`
	Producer string `json:"producer"`
	Limit    int    `json:"limit"`
}

// Locate verifies position membership without requiring a resolved symbol.
func (c Client) Locate(ctx context.Context, ref SnapshotRef, query LocateQuery, after *string) (OccurrencePage, error) {
	if query.Offset < 0 || query.Offset > 64<<20 || query.Limit < 1 || query.Limit > 128 {
		return OccurrencePage{}, errors.New("invalid locate bounds")
	}
	id, err := canonical.Hash("harness.ri.locate-query.v1", query)
	if err != nil {
		return OccurrencePage{}, err
	}
	startAfter, err := cursorAfter(after, ref.ID, id)
	if err != nil {
		return OccurrencePage{}, err
	}
	result, err := c.Call(ctx, map[string]any{"operation": "locate", "path": ref.Path, "snapshot_id": ref.ID, "source": ref.Source, "query": query, "after": after})
	if err != nil {
		return OccurrencePage{}, err
	}
	var page OccurrencePage
	if err := canonical.Decode(result, &page); err != nil {
		return OccurrencePage{}, err
	}
	if page.SnapshotID != ref.ID || page.QueryID != id || page.AbsenceProven || len(page.Occurrences) > query.Limit {
		return OccurrencePage{}, errors.New("locate identity/bounds mismatch")
	}
	previous := startAfter
	for _, record := range page.Occurrences {
		if record.ID <= previous || len(record.ID) > 256 || record.Path != query.Path || record.Producer != query.Producer || record.Span.Start < 0 || record.Span.End < record.Span.Start || record.Span.End > 64<<20 || len(record.Spelling) != record.Span.End-record.Span.Start || record.Span.Start > query.Offset || (query.Offset >= record.Span.End && !(record.Span.Start == record.Span.End && record.Span.Start == query.Offset)) {
			return OccurrencePage{}, errors.New("locate record scope/range mismatch")
		}
		if len(record.SourceSHA256) != 64 || strings.Trim(record.SourceSHA256, "0123456789abcdef") != "" || (record.Roles != nil && (*record.Roles < 0 || *record.Roles > 127)) {
			return OccurrencePage{}, errors.New("invalid locate hash/roles")
		}
		switch record.Quality {
		case "DECLARED", "OBSERVED", "INFERRED":
		default:
			return OccurrencePage{}, errors.New("invalid locate quality")
		}
		previous = record.ID
	}
	if page.NextAfter != nil {
		var cursor struct {
			Snapshot string `json:"snapshot"`
			Query    string `json:"query"`
			After    string `json:"after"`
		}
		if err := canonical.Decode([]byte(*page.NextAfter), &cursor); err != nil {
			return OccurrencePage{}, err
		}
		if len(page.Occurrences) == 0 || cursor.Snapshot != ref.ID || cursor.Query != id || cursor.After != previous {
			return OccurrencePage{}, errors.New("locate cursor mismatch")
		}
	}
	return page, nil
}
