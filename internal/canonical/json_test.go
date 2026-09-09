package canonical

import (
	"fmt"
	"strings"
	"testing"
)

func TestCanonicalUnicodeAndOrdering(t *testing.T) {
	got, err := Normalize([]byte(` { "z": [true,null,2], "a": "<é>\u2028\ud83d\ude00" } `))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"a\":\"<é>\u2028😀\",\"z\":[true,null,2]}"
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
	again, err := Normalize(got)
	if err != nil || string(again) != want {
		t.Fatal("not idempotent", err)
	}
}

func TestRejectAmbiguousInputs(t *testing.T) {
	for _, s := range []string{`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `{"é":1}`, `1.0`, `1e2`, `-0`, `9007199254740992`, `"\ud800"`, `"\udc00"`, `"\ud800\u0041"`, `{} {}`, `{"x":NaN}`, `"` + string([]byte{0xff}) + `"`, strings.Repeat("[", 66) + strings.Repeat("]", 66)} {
		t.Run(fmt.Sprintf("%q", s), func(t *testing.T) {
			if _, err := Normalize([]byte(s)); err == nil {
				t.Fatal("accepted ambiguous input")
			}
		})
	}
	if _, err := Bytes(map[string]string{"x": string([]byte{0xff})}); err == nil {
		t.Fatal("silently replaced invalid Go string")
	}
	var dst struct {
		X int `json:"x"`
	}
	for _, raw := range []string{`{"X":1}`, `{}`, `{"x":1,"X":2}`, `{"x":null}`} {
		if err := Decode([]byte(raw), &dst); err == nil {
			t.Fatal("nonexact typed field admitted", raw)
		}
	}
	if _, err := Bytes(1.0); err == nil {
		t.Fatal("float value admitted")
	}
	if err := Decode([]byte(`{"x":1,"y":2}`), &dst); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestHashDomainSeparation(t *testing.T) {
	a, _ := Hash("a", map[string]int{"x": 1})
	b, _ := Hash("b", map[string]int{"x": 1})
	if a == b {
		t.Fatal("domain not bound")
	}
}

func FuzzNormalize(f *testing.F) {
	for _, s := range []string{`{"a":1}`, `"\ud83d\ude00"`, `{"x":null}`, `[]`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		n, err := Normalize(b)
		if err != nil {
			return
		}
		again, err := Normalize(n)
		if err != nil || string(n) != string(again) {
			t.Fatal("canonicalization unstable")
		}
	})
}

func ExampleNormalize() {
	b, err := Normalize([]byte(`{"z":2,"a":1}`))
	fmt.Println(string(b), err)
	// Output: {"a":1,"z":2} <nil>
}
