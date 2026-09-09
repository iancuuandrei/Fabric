package ri

import (
	"context"
	"errors"

	"harness.local/engorch/internal/canonical"
)

// NeighborQuery selects one exact adjacency scope and finite page size.
type NeighborQuery struct {
	Node      string `json:"node"`
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
	Producer  string `json:"producer"`
	Limit     int    `json:"limit"`
}

// Edge is one producer-bound graph relationship.
type Edge struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	Producer string `json:"producer"`
	Quality  string `json:"quality"`
}

// NeighborEvidence combines observed edges with exact-scope coverage.
type NeighborEvidence struct {
	Edges         []Edge `json:"edges"`
	Completeness  string `json:"completeness"`
	AbsenceProven bool   `json:"absence_proven"`
}

// NeighborPage retains artifact/query identities and an optional continuation.
type NeighborPage struct {
	Evidence   NeighborEvidence `json:"evidence"`
	SnapshotID string           `json:"snapshot_id"`
	QueryID    string           `json:"query_id"`
	NextAfter  *string          `json:"next_after"`
}

// Neighbors verifies each returned edge's scope and the page identity.
func (c Client) Neighbors(ctx context.Context, ref SnapshotRef, query NeighborQuery, after *string) (NeighborPage, error) {
	if query.Limit < 1 || query.Limit > 128 || (query.Direction != "OUTGOING" && query.Direction != "INCOMING") {
		return NeighborPage{}, errors.New("invalid neighbor query bounds/direction")
	}
	queryID, err := canonical.Hash("harness.ri.query.v1", query)
	if err != nil {
		return NeighborPage{}, err
	}
	startAfter, err := cursorAfter(after, ref.ID, queryID)
	if err != nil {
		return NeighborPage{}, err
	}
	result, err := c.Call(ctx, map[string]any{"operation": "neighbors", "path": ref.Path, "snapshot_id": ref.ID, "source": ref.Source, "query": query, "after": after})
	if err != nil {
		return NeighborPage{}, err
	}
	var page NeighborPage
	if err := canonical.Decode(result, &page); err != nil {
		return NeighborPage{}, err
	}
	if page.SnapshotID != ref.ID || page.QueryID != queryID || len(page.Evidence.Edges) > query.Limit {
		return NeighborPage{}, errors.New("neighbor response identity/bounds mismatch")
	}
	switch page.Evidence.Completeness {
	case "UNKNOWN", "PARTIAL", "COMPLETE":
	default:
		return NeighborPage{}, errors.New("invalid neighbor completeness")
	}
	if page.Evidence.AbsenceProven && (len(page.Evidence.Edges) != 0 || page.Evidence.Completeness != "COMPLETE") {
		return NeighborPage{}, errors.New("invalid neighbor absence claim")
	}
	previous := startAfter
	for _, edge := range page.Evidence.Edges {
		if edge.ID <= previous || len(edge.ID) > 256 || edge.Relation != query.Relation || edge.Producer != query.Producer || (query.Direction == "OUTGOING" && edge.From != query.Node) || (query.Direction == "INCOMING" && edge.To != query.Node) {
			return NeighborPage{}, errors.New("neighbor edge scope/order mismatch")
		}
		switch edge.Quality {
		case "DECLARED", "OBSERVED", "INFERRED":
		default:
			return NeighborPage{}, errors.New("unknown neighbor quality")
		}
		previous = edge.ID
	}
	if page.NextAfter != nil {
		var cursor struct {
			Snapshot string `json:"snapshot"`
			Query    string `json:"query"`
			After    string `json:"after"`
		}
		if err := canonical.Decode([]byte(*page.NextAfter), &cursor); err != nil {
			return NeighborPage{}, err
		}
		if len(page.Evidence.Edges) == 0 || cursor.Snapshot != ref.ID || cursor.Query != queryID || cursor.After != previous {
			return NeighborPage{}, errors.New("neighbor continuation mismatch")
		}
	}
	return page, nil
}
