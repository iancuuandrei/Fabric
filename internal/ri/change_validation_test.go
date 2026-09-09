package ri

import (
	"strings"
	"testing"
)

func TestChangeValuesRejectMalformedDigestsAndDuplicateProducers(t *testing.T) {
	hash := strings.Repeat("a", 64)
	good := SourceChange{Producer: "p", Path: "a.go", After: &hash}
	if err := validateChangeValues([]string{"p"}, []SourceChange{good}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 63)} {
		changed := good
		changed.After = &value
		if validateChangeValues([]string{"p"}, []SourceChange{changed}) == nil {
			t.Fatal("invalid digest admitted", value)
		}
	}
	for _, producers := range [][]string{{"p", "p"}, {"z", "a"}, {""}} {
		if validateChangeValues(producers, nil) == nil {
			t.Fatal("invalid producer order admitted", producers)
		}
	}
}
