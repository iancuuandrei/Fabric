package ri

import "testing"

func TestLexicalFilterIdentityAndValidation(t *testing.T) {
	plain, err := (LexicalQuery{Pattern: "needle", Fixed: true}).ID()
	if err != nil {
		t.Fatal(err)
	}
	empty, err := (LexicalQuery{Pattern: "needle", Fixed: true, Path: "", Type: ""}).ID()
	if err != nil || empty != plain {
		t.Fatal("empty filters changed legacy query identity", err)
	}
	filtered, err := (LexicalQuery{Pattern: "needle", Fixed: true, Path: "src", Type: "rs"}).ID()
	if err != nil || filtered == plain {
		t.Fatal("filters missing from query identity", err)
	}
	for _, query := range []LexicalQuery{
		{Pattern: "x", Path: "/src"},
		{Pattern: "x", Path: "src/../other"},
		{Pattern: "x", Path: "src\\other"},
		{Pattern: "x", Type: ".rs"},
		{Pattern: "x", Type: "r/s"},
		{Pattern: "x", Type: "-rs"},
	} {
		if _, err := query.ID(); err == nil {
			t.Fatalf("invalid filter admitted: %+v", query)
		}
	}
}
