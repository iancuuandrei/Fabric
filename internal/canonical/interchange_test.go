package canonical

import (
	"bytes"
	"os"
	"testing"
)

func TestSharedRustCanonicalFixture(t *testing.T) {
	b, e := os.ReadFile("../../testdata/canonical-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	want, e := os.ReadFile("../../testdata/canonical-v1.sha256")
	if e != nil {
		t.Fatal(e)
	}
	normalized, e := Normalize(b)
	if e != nil || !bytes.Equal(normalized, b) {
		t.Fatal("fixture is not canonical", e)
	}
	value := map[string]any{"z": int64(9007199254740991), "a": "Ș\n\u0001<>&\u2028"}
	encoded, e := Bytes(value)
	if e != nil || !bytes.Equal(encoded, b) {
		t.Fatal("Go encoding differs from shared fixture", e)
	}
	hash, e := Hash("harness.canonical-fixture.v1", value)
	if e != nil || hash != string(want) {
		t.Fatal("Go hash differs from shared fixture", e)
	}
}
