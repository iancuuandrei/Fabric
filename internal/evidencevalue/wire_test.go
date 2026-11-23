package evidencevalue

import (
	"github.com/iancuuandrei/fabric/internal/canonical"
	"reflect"
	"testing"
)

func TestNumericCanonicalRoundTrip(t *testing.T) {
	q := fixture()
	w := EncodeRequest(q)
	raw, err := canonical.Bytes(w)
	if err != nil {
		t.Fatal(err)
	}
	var read WireRequest
	if err = canonical.Decode(raw, &read); err != nil {
		t.Fatal(err)
	}
	decoded, err := read.Decode()
	if err != nil || !reflect.DeepEqual(q, decoded) {
		t.Fatal(decoded, err)
	}
	r, err := Evaluate(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = canonical.Bytes(r.Wire()); err != nil {
		t.Fatal("report violated canonical v1", err)
	}
	for _, n := range []Numeric{"NaN", "Inf", "-1", "1e999", ""} {
		w = EncodeRequest(q)
		w.Resources[0].Price = n
		if _, err := w.Decode(); err == nil {
			t.Fatal("accepted unsafe numeric", n)
		}
	}
}
