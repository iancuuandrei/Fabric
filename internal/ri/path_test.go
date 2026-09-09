package ri

import "testing"

func TestPathEvidenceRejectsDisconnectedAndForeignEdges(t *testing.T) {
	ref := SnapshotRef{ID: "snapshot"}
	query := PathQuery{From: "a", To: "c", Relation: "DEPENDS_ON", Direction: "OUTGOING", Producer: "p", MaxDepth: 4, MaxEdges: 20}
	good := PathReport{SnapshotID: ref.ID, Query: query, Evidence: PathEvidence{Found: true, ExaminedEdges: 2, Edges: []Edge{
		{ID: "1", From: "a", To: "b", Relation: query.Relation, Producer: "p", Quality: "DECLARED"},
		{ID: "2", From: "b", To: "c", Relation: query.Relation, Producer: "p", Quality: "OBSERVED"},
	}}}
	if err := validatePath(good, ref, query); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"source", "destination", "producer", "relation", "count", "absence", "snapshot"} {
		changed := good
		changed.Evidence.Edges = append([]Edge(nil), good.Evidence.Edges...)
		switch mutation {
		case "source":
			changed.Evidence.Edges[1].From = "x"
		case "destination":
			changed.Evidence.Edges[1].To = "x"
		case "producer":
			changed.Evidence.Edges[1].Producer = "q"
		case "relation":
			changed.Evidence.Edges[1].Relation = "CALLS"
		case "count":
			changed.Evidence.ExaminedEdges = 1
		case "absence":
			changed.Evidence.AbsenceProven = true
		case "snapshot":
			changed.SnapshotID = "other"
		}
		if validatePath(changed, ref, query) == nil {
			t.Fatal("admitted", mutation)
		}
	}
	query.From, query.To, query.Direction = "c", "a", "INCOMING"
	good.Query = query
	good.Evidence.Edges[0], good.Evidence.Edges[1] = good.Evidence.Edges[1], good.Evidence.Edges[0]
	if err := validatePath(good, ref, query); err != nil {
		t.Fatal(err)
	}
}
