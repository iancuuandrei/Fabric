package codexhost

import "testing"

func TestQualificationProviderJSONRejectsTrailingValues(t *testing.T) {
	for _, raw := range []string{`{} {}`, `[] true`, `{"model":"x"} garbage`} {
		var v any
		if err := decodeProvider([]byte(raw), &v); err == nil {
			t.Fatal("ambiguous provider bytes accepted", raw)
		}
	}
	var v any
	if err := decodeProvider([]byte("{\"model\":\"x\"}\n"), &v); err != nil {
		t.Fatal(err)
	}
}

func TestQualificationInventoryRejectsNestedCollaboration(t *testing.T) {
	cases := []string{
		`[{"type":"namespace","name":"collaboration","tools":[]}]`,
		`[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"spawn_agent"}]}]`,
		`[{"type":"function","name":"send_message"}]`,
	}
	for _, raw := range cases {
		var tools []any
		if err := decodeProvider([]byte(raw), &tools); err != nil {
			t.Fatal(err)
		}
		if _, err := inventoryNames(tools, ""); err == nil {
			t.Fatal("forbidden inventory accepted", raw)
		}
	}
}
