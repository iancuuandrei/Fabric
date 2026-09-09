package gitlocal

import (
	"io"
	"strings"
	"testing"
)

func TestObjectBufferBounds(t *testing.T) {
	b := &boundedBuffer{remaining: 4}
	if n, err := b.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if n, err := b.Write([]byte("de")); err == nil || n != 0 || b.String() != "abc" || b.remaining != 1 {
		t.Fatal("overflow changed buffer")
	}
	if n, err := b.Write([]byte("d")); err != nil || n != 1 || b.String() != "abcd" || b.remaining != 0 {
		t.Fatal("exact bound rejected")
	}
	if n, err := b.Write([]byte("e")); err == nil || n != 0 {
		t.Fatal("exhausted buffer accepted data")
	}
}

func TestObjectBufferCopyCannotBypassLimit(t *testing.T) {
	b := &boundedBuffer{remaining: 2}
	if _, ok := any(b).(io.ReaderFrom); ok {
		t.Fatal("unbounded ReaderFrom exposed")
	}
	if _, err := io.Copy(b, strings.NewReader("too large")); err == nil || len(b.Bytes()) > 2 {
		t.Fatal("copy bypassed output limit")
	}
}
