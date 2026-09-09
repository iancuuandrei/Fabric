package opencode

import (
	"strings"
	"testing"
)

func TestWireDecimalsPreserveLexemes(t *testing.T) {
	raw := []byte(`{"cost":0.0123,"tokens":12,"text":"value 1.5 and \"2\"","nested":{"ratio":1e-3},"unicode":"\ud83d\ude00"}`)
	m, err := wireObject(raw)
	if err != nil || string(m["cost"]) != "0.0123" || string(m["tokens"]) != "12" {
		t.Fatal("wire value lost", err)
	}
}

func TestWireRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{
		`{"cost":0.1,"cost":0.2}`, `{"cost":0.1,"\u0063ost":0.2}`,
		`{"nested":{"id":1,"id":2}}`, `{"x":"\ud800"}`, `{"x":1e9999}`,
		`{"x":NaN}`, `{"x":01}`, `{"x":1} {}`, `null`, `[]`,
		`{"x":"` + string([]byte{0xff}) + `"}`, strings.Repeat(" ", 1<<20) + `{}`,
		strings.Repeat(`{"x":`, 70) + `0` + strings.Repeat(`}`, 70),
	} {
		if _, err := wireObject([]byte(raw)); err == nil {
			t.Fatal("invalid wire admitted")
		}
	}
}
