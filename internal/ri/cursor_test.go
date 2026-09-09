package ri

import (
	"harness.local/engorch/internal/canonical"
	"testing"
)

func TestInputCursorBindsSnapshotAndQuery(t *testing.T) {
	data, err := canonical.Bytes(map[string]any{"snapshot": "s", "query": "q", "after": "last"})
	if err != nil {
		t.Fatal(err)
	}
	token := string(data)
	if after, err := cursorAfter(&token, "s", "q"); err != nil || after != "last" {
		t.Fatal(after, err)
	}
	for _, scope := range [][2]string{{"other", "q"}, {"s", "other"}} {
		if _, err := cursorAfter(&token, scope[0], scope[1]); err == nil {
			t.Fatal("foreign cursor admitted")
		}
	}
	if after, err := cursorAfter(nil, "s", "q"); err != nil || after != "" {
		t.Fatal(after, err)
	}
	token = `{"snapshot":"s","query":"q","after":""}`
	if _, err := cursorAfter(&token, "s", "q"); err == nil {
		t.Fatal("empty cursor admitted")
	}
}
