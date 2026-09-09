package codexruntime

import (
	"context"
	"testing"

	"harness.local/engorch/internal/ri"
)

type filterCaptureReader struct {
	query   ri.LexicalQuery
	overlay bool
}

func (r *filterCaptureReader) SearchLexical(_ context.Context, _ ri.LexicalRef, q ri.LexicalQuery, _ int, _ *ri.LexicalCursor) (ri.LexicalResult, error) {
	r.query = q
	return ri.LexicalResult{}, nil
}
func (r *filterCaptureReader) SearchLexicalOverlay(_ context.Context, _ ri.LexicalRef, _ ri.LexicalOverlayRef, q ri.LexicalQuery, _ int, _ *ri.LexicalCursor) (ri.LexicalResult, error) {
	r.query = q
	r.overlay = true
	return ri.LexicalResult{}, nil
}

func TestLexicalToolPropagatesFiltersToBaseAndOverlay(t *testing.T) {
	for _, overlay := range []bool{false, true} {
		binding := LexicalBinding{}
		if overlay {
			binding.Overlay = &ri.LexicalOverlayRef{}
		}
		reader := &filterCaptureReader{}
		_, err := lexicalReadWith(context.Background(), binding, []byte(`{"pattern":"needle","fixed":true,"case_insensitive":false,"limit":10,"after":null,"path":"src","type":"go"}`), reader)
		if err != nil || reader.query.Path != "src" || reader.query.Type != "go" || !reader.query.Fixed || reader.overlay != overlay {
			t.Fatal("filters lost", err)
		}
	}
}
