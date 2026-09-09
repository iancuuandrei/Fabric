package ri

import (
	"context"
	"errors"

	"harness.local/engorch/internal/canonical"
)

// PathQuery defines one producer/relation traversal and its resource limits.
type PathQuery struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
	Producer  string `json:"producer"`
	MaxDepth  int    `json:"max_depth"`
	MaxEdges  int    `json:"max_edges"`
}

// PathEvidence reports an observed path or bounded exploration outcome.
type PathEvidence struct {
	Found         bool   `json:"found"`
	Edges         []Edge `json:"edges"`
	Truncated     bool   `json:"truncated"`
	ExaminedEdges int    `json:"examined_edges"`
	AbsenceProven bool   `json:"absence_proven"`
}

// PathReport binds path evidence to the exact requested artifact and query.
type PathReport struct {
	SnapshotID string       `json:"snapshot_id"`
	Query      PathQuery    `json:"query"`
	Evidence   PathEvidence `json:"evidence"`
}

// Path checks response scope, edge continuity, endpoints and resource counts.
func (c Client) Path(ctx context.Context, ref SnapshotRef, query PathQuery) (PathReport, error) {
	if query.MaxDepth < 1 || query.MaxDepth > 128 || query.MaxEdges < 1 || query.MaxEdges > 100000 || (query.Direction != "OUTGOING" && query.Direction != "INCOMING") {
		return PathReport{}, errors.New("invalid path query bounds/direction")
	}
	result, err := c.Call(ctx, map[string]any{"operation": "path", "path": ref.Path, "snapshot_id": ref.ID, "source": ref.Source, "query": query})
	if err != nil {
		return PathReport{}, err
	}
	var report PathReport
	if err := canonical.Decode(result, &report); err != nil {
		return PathReport{}, err
	}
	if err := validatePath(report, ref, query); err != nil {
		return PathReport{}, err
	}
	return report, nil
}

func validatePath(report PathReport, ref SnapshotRef, query PathQuery) error {
	evidence := report.Evidence
	if report.SnapshotID != ref.ID || report.Query != query || evidence.AbsenceProven || evidence.ExaminedEdges < 0 || evidence.ExaminedEdges > query.MaxEdges || len(evidence.Edges) > query.MaxDepth || len(evidence.Edges) > evidence.ExaminedEdges {
		return errors.New("path response identity/bounds mismatch")
	}
	if !evidence.Found {
		if len(evidence.Edges) != 0 || query.From == query.To {
			return errors.New("invalid missing path")
		}
		return nil
	}
	current := query.From
	seen := map[string]bool{current: true}
	for _, edge := range evidence.Edges {
		from, to := edge.From, edge.To
		if query.Direction == "INCOMING" {
			from, to = to, from
		}
		if from != current || to == "" || seen[to] || edge.Producer != query.Producer || edge.Relation != query.Relation || edge.ID == "" {
			return errors.New("path edge discontinuity or scope mismatch")
		}
		switch edge.Quality {
		case "DECLARED", "OBSERVED", "INFERRED":
		default:
			return errors.New("invalid path quality")
		}
		seen[to] = true
		current = to
	}
	if current != query.To {
		return errors.New("path destination mismatch")
	}
	return nil
}
