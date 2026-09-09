package opencode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestProviderItemIDValidationAndExactJSONRoundTrip(t *testing.T) {
	valid := []ProviderItemID{
		m1bProviderItemID,
		"rs:segment/path%25",
		"响应/推理:%",
		"contains spaces",
	}
	for _, want := range valid {
		t.Run(string(want), func(t *testing.T) {
			if !validProviderItemID(want) {
				t.Fatal("valid opaque provider item ID rejected", want)
			}
			raw, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			var got ProviderItemID
			if err := json.Unmarshal(raw, &got); err != nil || got != want || !reflect.DeepEqual([]byte(got), []byte(want)) {
				t.Fatal("provider item ID bytes changed during JSON round trip", got, want, err)
			}
		})
	}
}

func TestProviderItemIDRejectsInvalidValues(t *testing.T) {
	invalid := map[string]ProviderItemID{
		"empty":           "",
		"too large":       ProviderItemID(strings.Repeat("a", providerItemIDMaximumBytes+1)),
		"NUL":             "rs_\x00item",
		"ASCII control":   "rs_\nitem",
		"Unicode control": ProviderItemID("rs_" + string(rune(0x0085)) + "item"),
		"bad UTF-8":       ProviderItemID(string([]byte{'r', 's', '_', 0xff})),
	}
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			if validProviderItemID(value) {
				t.Fatal("invalid provider item ID admitted", []byte(value))
			}
		})
	}
}

func TestProviderItemIDJSONRejectsRawAndEscapedControlsAndBadUTF8(t *testing.T) {
	invalidJSON := map[string][]byte{
		"escaped NUL":     []byte(`"rs_\u0000item"`),
		"escaped control": []byte(`"rs_\u0085item"`),
		"lone surrogate":  []byte(`"rs_\ud800item"`),
		"bad UTF-8":       {'"', 'r', 's', '_', 0xff, '"'},
	}
	for name, raw := range invalidJSON {
		t.Run(name, func(t *testing.T) {
			var got ProviderItemID
			if err := json.Unmarshal(raw, &got); err == nil {
				t.Fatal("invalid JSON provider item ID admitted", []byte(got))
			}
		})
	}
}

func TestProviderItemIDJSONAcceptsValidSurrogatePairExactly(t *testing.T) {
	var got ProviderItemID
	if err := json.Unmarshal([]byte(`"rs_\ud83d\ude80/item"`), &got); err != nil || got != ProviderItemID("rs_🚀/item") {
		t.Fatal("valid surrogate pair provider item ID rejected or changed", got, err)
	}
}

func TestProviderItemIDIsNamedAndLocatorRemainsStrict(t *testing.T) {
	providerType := reflect.TypeOf(ProviderItemID(""))
	stringType := reflect.TypeOf("")
	if providerType.Name() != "ProviderItemID" || providerType == stringType || providerType.AssignableTo(stringType) {
		t.Fatal("ProviderItemID is not a distinct named string type", providerType)
	}
	for _, opaque := range []string{string(m1bProviderItemID), "rs:item", "rs/item", "rs%item", "响应"} {
		if locator(opaque) {
			t.Fatal("opaque provider identity relaxed locator admission", opaque)
		}
	}
	if !locator("session_valid-1") {
		t.Fatal("valid OpenCode locator was rejected")
	}
}
