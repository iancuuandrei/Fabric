package opencode

import (
	"strings"
	"testing"
)

func TestHostConfigurationRejectsExpandedAuthority(t *testing.T) {
	valid := `{"share":"disabled","plugin":[],"permission":{"*":"deny"},"unknown":{"decimal":0.25}}`
	receipt, err := decodeHostConfiguration([]byte(valid))
	if err != nil || len(receipt.SHA256) != 64 {
		t.Fatal(err)
	}
	changed, err := decodeHostConfiguration([]byte(strings.Replace(valid, "0.25", "0.5", 1)))
	if err != nil || changed.SHA256 == receipt.SHA256 {
		t.Fatal("wire identity lost", err)
	}
	for _, pair := range [][2]string{
		{`"disabled"`, `"auto"`}, {`"plugin":[]`, `"plugin":null`},
		{`"plugin":[]`, `"plugin":["external"]`},
		{`"*":"deny"`, `"*":"deny","bash":"allow"`},
		{`"*":"deny"`, `"*":"allow"`},
		{`"share":"disabled"`, `"share":"disabled","share":"auto"`},
	} {
		if got, err := decodeHostConfiguration([]byte(strings.Replace(valid, pair[0], pair[1], 1))); err == nil || got != (HostConfiguration{}) {
			t.Fatal("unsafe config returned receipt")
		}
	}
}
