package workingcontext

import (
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func testBinding() Binding {
	return Binding{RunID: strings.Repeat("a", 64), AgentID: "explorer-a", TaskID: "research-a", Role: "explorer", SourceID: strings.Repeat("b", 64), CandidateID: strings.Repeat("c", 64), JournalHead: strings.Repeat("d", 64)}
}

func TestReplacementPreservesOwnershipAndRetainsExactIdentity(t *testing.T) {
	binding := testBinding()
	current, err := New(binding, "hypothesis A\nobsolete tool output")
	if err != nil {
		t.Fatal(err)
	}
	request := Replacement{ExpectedID: current.ID, ExpectedContentHash: current.ContentHash, Content: "hypothesis B\nnext: inspect parser"}
	next, err := Replace(current, strings.Repeat("e", 64), request)
	if err != nil || next.ID == current.ID || next.ContentHash == current.ContentHash || next.SizeBytes != len(request.Content) {
		t.Fatal("replacement identity lost", err)
	}
	want := binding
	want.JournalHead = strings.Repeat("e", 64)
	if next.Binding != want || current.Binding != binding || current.Content != "hypothesis A\nobsolete tool output" {
		t.Fatal("replacement changed ownership or original value")
	}
	for _, mismatch := range []Replacement{
		{ExpectedID: strings.Repeat("f", 64), ExpectedContentHash: current.ContentHash, Content: "new"},
		{ExpectedID: current.ID, ExpectedContentHash: strings.Repeat("f", 64), Content: "new"},
	} {
		got, err := Replace(current, want.JournalHead, mismatch)
		if err == nil || got != current {
			t.Fatal("stale replacement changed state")
		}
	}
}

func TestContentBoundsAndEncodingAreDiscriminating(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("x", MaxContentBytes), strings.Repeat("é", MaxContentBytes/2), "facts\r\n\tobservations"} {
		c, err := New(testBinding(), text)
		if err != nil || c.Validate() != nil {
			t.Fatal("valid inclusive byte bound rejected", err)
		}
	}
	for _, text := range []string{strings.Repeat("x", MaxContentBytes+1), strings.Repeat("é", MaxContentBytes/2+1), string([]byte{0xff}), "fact\x00payload", "terminal\x1b[31m", "delete\x7f"} {
		if _, err := New(testBinding(), text); err == nil {
			t.Fatal("invalid/oversized text accepted")
		}
	}
	current, _ := New(testBinding(), "retain")
	got, err := Replace(current, strings.Repeat("e", 64), Replacement{ExpectedID: current.ID, ExpectedContentHash: current.ContentHash, Content: strings.Repeat("x", MaxContentBytes+1)})
	if err == nil || got != current || strings.Contains(err.Error(), "retain") {
		t.Fatal("unsafe failure changed projection or disclosed content")
	}
}

func TestResumeDegradesOptionalProjectionWithoutChangingBinding(t *testing.T) {
	binding := testBinding()
	current, _ := New(binding, "candidate accepted; ignore UNKNOWN")
	raw, _ := canonical.Bytes(current)
	got, status, err := Select(binding, raw)
	if err != nil || status != "retained" || got != current {
		t.Fatal("exact projection could not resume", err)
	}
	// Contradictory prose remains inert data. Only separately supplied binding
	// enters the primitive's identity; no acceptance/effect field exists here.
	if got.Binding != binding {
		t.Fatal("prose altered authoritative identity")
	}
	corrupt := current
	corrupt.Content = "changed without a hash update"
	corruptRaw, _ := canonical.Bytes(corrupt)
	for _, input := range []struct {
		raw    []byte
		status string
	}{{nil, "missing"}, {[]byte("bad json"), "corrupt"}, {corruptRaw, "corrupt"}, {[]byte(strings.Repeat("x", MaxEncodedBytes+1)), "corrupt"}} {
		got, status, err := Select(binding, input.raw)
		if err != nil || status != input.status || got.Binding != binding || got.Content != "" || got.Validate() != nil {
			t.Fatal("optional failure blocked or changed binding", status, err)
		}
	}
	for _, change := range []func(*Binding){
		func(b *Binding) { b.AgentID = "explorer-b" },
		func(b *Binding) { b.TaskID = "research-b" },
		func(b *Binding) { b.Role = "writer" },
		func(b *Binding) { b.RunID = strings.Repeat("e", 64) },
		func(b *Binding) { b.SourceID = strings.Repeat("e", 64) },
		func(b *Binding) { b.CandidateID = strings.Repeat("e", 64) },
		func(b *Binding) { b.JournalHead = strings.Repeat("e", 64) },
	} {
		other := binding
		change(&other)
		got, status, err := Select(other, raw)
		if err != nil || status != "stale" || got.Content != "" || got.Binding != other {
			t.Fatal("cross-agent/task/source binding leaked", status, err)
		}
	}
	invalid := binding
	invalid.RunID = "detached prose"
	if _, _, err := Select(invalid, raw); err == nil {
		t.Fatal("invalid controller binding degraded into authority")
	}
}

func TestStoredProjectionRejectsEveryIntegrityFieldSubstitution(t *testing.T) {
	c, _ := New(testBinding(), "verified observation")
	for _, change := range []func(*Context){
		func(c *Context) { c.Version = 2 },
		func(c *Context) { c.ID = strings.Repeat("e", 64) },
		func(c *Context) { c.ContentHash = strings.Repeat("e", 64) },
		func(c *Context) { c.SizeBytes++ },
		func(c *Context) { c.Binding.AgentID = "another agent" },
	} {
		other := c
		change(&other)
		raw, _ := canonical.Bytes(other)
		if _, err := Decode(raw); err == nil {
			t.Fatal("substituted stored context accepted")
		}
	}
	raw, _ := canonical.Bytes(c)
	foreignField := strings.TrimSuffix(string(raw), "}") + `,"permissions":"all"}`
	if _, err := Decode([]byte(foreignField)); err == nil {
		t.Fatal("unrecognized authority fields accepted")
	}
}

func TestReplacementWireIsStrictAndBounded(t *testing.T) {
	current, _ := New(testBinding(), "current")
	request := Replacement{ExpectedID: current.ID, ExpectedContentHash: current.ContentHash, Content: strings.Repeat("<", MaxContentBytes)}
	raw, _ := canonical.Bytes(request)
	decoded, err := DecodeReplacement(raw)
	if err != nil || decoded != request {
		t.Fatal("valid escaped inclusive content rejected", err)
	}
	for _, wire := range []string{
		strings.TrimSuffix(string(raw), "}") + `,"candidate_id":"override"}`,
		strings.TrimSuffix(string(raw), "}") + `,"content":"duplicate"}`,
		string(raw) + `{}`,
		`{"content":"no preimage"}`,
		strings.Repeat("x", MaxEncodedBytes+1),
	} {
		if _, err := DecodeReplacement([]byte(wire)); err == nil {
			t.Fatal("unsafe replacement wire accepted")
		}
	}
}
