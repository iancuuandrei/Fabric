package ri

import (
	"context"
	"errors"
	"strings"

	"harness.local/engorch/internal/canonical"
)

// SnapshotRef binds a local artifact location to expected immutable provenance.
type SnapshotRef struct {
	Path   string `json:"path"`
	ID     string `json:"snapshot_id"`
	Source Source `json:"source"`
}

// Status reports counts admitted by the pinned Rust reader.
type Status struct {
	SnapshotID      string   `json:"snapshot_id"`
	Source          Source   `json:"source"`
	Nodes           int      `json:"nodes"`
	Edges           int      `json:"edges"`
	CoverageRecords int      `json:"coverage_records"`
	Occurrences     int      `json:"occurrences"`
	Producers       []string `json:"producers"`
}

// Inspect verifies response provenance and graph bounds after process admission.
func (c Client) Inspect(ctx context.Context, ref SnapshotRef) (Status, error) {
	result, err := c.Call(ctx, struct {
		Operation string `json:"operation"`
		SnapshotRef
	}{"status", ref})
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := canonical.Decode(result, &status); err != nil {
		return Status{}, err
	}
	if status.SnapshotID != ref.ID || status.Source != ref.Source || status.Nodes < 0 || status.Nodes > 100000 || status.Edges < 0 || status.Edges > 1000000 || status.CoverageRecords < 0 || status.CoverageRecords > 1000000 || status.Occurrences < 0 || status.Occurrences > 100000 {
		return Status{}, errors.New("RI status identity or bounds mismatch")
	}
	if len(status.Producers) == 0 || len(status.Producers) > 64 {
		return Status{}, errors.New("RI status producer registry missing or oversized")
	}
	for i, producer := range status.Producers {
		if strings.TrimSpace(producer) == "" || len(producer) > 256 || (i > 0 && producer <= status.Producers[i-1]) {
			return Status{}, errors.New("RI status producer registry invalid")
		}
	}
	return status, nil
}

// CoverageScope identifies one graph coverage dimension without merging producers.
type CoverageScope struct {
	Node      string `json:"node"`
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
}

// CoverageDeclaration preserves null (no assertion) separately from UNKNOWN.
type CoverageDeclaration struct {
	Producer     string  `json:"producer"`
	Completeness *string `json:"completeness"`
}

// CoverageReport retains exact requested scope and producer declarations.
type CoverageReport struct {
	SnapshotID   string                `json:"snapshot_id"`
	Node         string                `json:"node"`
	Relation     string                `json:"relation"`
	Direction    string                `json:"direction"`
	Declarations []CoverageDeclaration `json:"declarations"`
}

// Coverage reads declarations and validates response scope, ordering and values.
// The report itself does not establish absence of graph relationships.
func (c Client) Coverage(ctx context.Context, ref SnapshotRef, scope CoverageScope) (CoverageReport, error) {
	result, err := c.Call(ctx, struct {
		Operation string `json:"operation"`
		SnapshotRef
		CoverageScope
	}{"coverage", ref, scope})
	if err != nil {
		return CoverageReport{}, err
	}
	var report CoverageReport
	if err := canonical.Decode(result, &report); err != nil {
		return CoverageReport{}, err
	}
	if report.SnapshotID != ref.ID || report.Node != scope.Node || report.Relation != scope.Relation || report.Direction != scope.Direction || len(report.Declarations) < 1 || len(report.Declarations) > 64 {
		return CoverageReport{}, errors.New("RI coverage scope or declaration count mismatch")
	}
	previous := ""
	for _, declaration := range report.Declarations {
		if declaration.Producer <= previous || len(declaration.Producer) > 256 {
			return CoverageReport{}, errors.New("RI coverage producer ordering invalid")
		}
		previous = declaration.Producer
		if declaration.Completeness != nil {
			switch *declaration.Completeness {
			case "UNKNOWN", "PARTIAL", "COMPLETE":
			default:
				return CoverageReport{}, errors.New("unknown RI completeness value")
			}
		}
	}
	return report, nil
}
