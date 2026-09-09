package ri

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"strings"
)

// OccurrenceQuery selects direct semantic definitions or references.
type OccurrenceQuery struct {
	Symbol      string `json:"symbol"`
	Producer    string `json:"producer"`
	Definitions bool   `json:"definitions"`
	Limit       int    `json:"limit"`
}

// Span is a half-open byte interval in an exact source artifact.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Occurrence retains source spelling, identity and explicit semantic roles.
type Occurrence struct {
	ID           string  `json:"id"`
	Path         string  `json:"path"`
	SourceSHA256 string  `json:"source_sha256"`
	Span         Span    `json:"span"`
	Spelling     string  `json:"spelling"`
	Symbol       *string `json:"symbol"`
	Roles        *int    `json:"roles"`
	Producer     string  `json:"producer"`
	Quality      string  `json:"quality"`
}

// OccurrencePage is an exact-query page without an inferred absence claim.
type OccurrencePage struct {
	Occurrences   []Occurrence `json:"occurrences"`
	SnapshotID    string       `json:"snapshot_id"`
	QueryID       string       `json:"query_id"`
	NextAfter     *string      `json:"next_after"`
	AbsenceProven bool         `json:"absence_proven"`
}

// Occurrences validates semantic scope, role flags and byte intervals in a page.
func (c Client) Occurrences(ctx context.Context, ref SnapshotRef, query OccurrenceQuery, after *string) (OccurrencePage, error) {
	if query.Limit < 1 || query.Limit > 128 {
		return OccurrencePage{}, errors.New("invalid occurrence page limit")
	}
	id, err := canonical.Hash("harness.ri.occurrence-query.v1", query)
	if err != nil {
		return OccurrencePage{}, err
	}
	startAfter, err := cursorAfter(after, ref.ID, id)
	if err != nil {
		return OccurrencePage{}, err
	}
	result, err := c.Call(ctx, map[string]any{"operation": "occurrences", "path": ref.Path, "snapshot_id": ref.ID, "source": ref.Source, "query": query, "after": after})
	if err != nil {
		return OccurrencePage{}, err
	}
	var page OccurrencePage
	if err := canonical.Decode(result, &page); err != nil {
		return OccurrencePage{}, err
	}
	if page.SnapshotID != ref.ID || page.QueryID != id || page.AbsenceProven || len(page.Occurrences) > query.Limit {
		return OccurrencePage{}, errors.New("occurrence page identity/bounds mismatch")
	}
	previous := startAfter
	for _, record := range page.Occurrences {
		if len(record.ID) > 256 || record.Path == "" || len(record.SourceSHA256) != 64 || strings.Trim(record.SourceSHA256, "0123456789abcdef") != "" {
			return OccurrencePage{}, errors.New("invalid occurrence identity/source hash")
		}
		switch record.Quality {
		case "DECLARED", "OBSERVED", "INFERRED":
		default:
			return OccurrencePage{}, errors.New("unknown occurrence quality")
		}
		if record.ID <= previous || record.Producer != query.Producer || record.Symbol == nil || *record.Symbol != query.Symbol || record.Roles == nil || *record.Roles < 0 || *record.Roles > 127 || ((*record.Roles&1) != 0) != query.Definitions || record.Span.Start < 0 || record.Span.End < record.Span.Start || record.Span.End > 64<<20 || len(record.Spelling) != record.Span.End-record.Span.Start {
			return OccurrencePage{}, errors.New("occurrence scope/role/range mismatch")
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
			return OccurrencePage{}, errors.New("occurrence continuation mismatch")
		}
	}
	return page, nil
}
