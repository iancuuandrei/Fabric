package opencode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

const m1bProviderItemID = ProviderItemID("rs_6aa03c8e2d7ed1b42826448b:rs_01a081ec8bcf77328c39da85cda512ba")

func TestDecodeToolTurnRetainsExactOpenAIResponsesItemIDs(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	toolPart(transcript, 0)["metadata"] = map[string]any{"openai": map[string]any{"itemId": "fc_provider_item_1"}}
	intermediateParts := parts(transcript[1])
	intermediateText := mergePart(basePart("part_intermediate_text", "msg_tools", "text"), map[string]any{"text": "preface", "time": map[string]any{"start": 13, "end": 14}})
	transcript[1]["parts"] = append(intermediateParts[:2], append([]map[string]any{intermediateText}, intermediateParts[2:]...)...)
	finalParts := parts(transcript[2])
	finalParts[1]["metadata"] = map[string]any{"openai": map[string]any{"itemId": "msg_provider_item_2"}}

	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Calls) != 1 || observation.Calls[0].ProviderItemID != "fc_provider_item_1" || observation.Calls[0].ProviderCallID != "provider-call-1" || string(observation.Calls[0].ProviderItemID) == observation.Calls[0].ProviderCallID {
		t.Fatal("Responses item and call identities were not retained distinctly", observation.Calls)
	}
	if len(observation.Generations) != 2 || len(observation.Generations[1].TextProviderItemIDs) != 1 || observation.Generations[1].TextProviderItemIDs[0] != "msg_provider_item_2" {
		t.Fatal("final Responses text item identity was not retained", observation.Generations)
	}
	if observation.Generations[0].TextSHA256 != toolTurnDigest([]byte("preface")) || observation.Generations[1].TextSHA256 != toolTurnDigest([]byte("complete")) {
		t.Fatal("per-generation Responses text hashes mismatch", observation.Generations)
	}
	encoded, err := canonical.Bytes(observation)
	if err != nil || !strings.Contains(string(encoded), `"provider_item_id":"fc_provider_item_1"`) || !strings.Contains(string(encoded), `"text_provider_item_ids":["msg_provider_item_2"]`) || !strings.Contains(string(encoded), `"text_sha256":"`+toolTurnDigest([]byte("preface"))+`"`) {
		t.Fatal("typed Responses item identities are absent from the observation hash input", string(encoded), err)
	}
}

func TestDecodeToolTurnRetainsKnownTextPhaseOnlyOnTextParts(t *testing.T) {
	tests := []struct {
		name  string
		phase string
		want  string
	}{
		{name: "commentary", phase: `"commentary"`, want: `"commentary"`},
		{name: "final answer", phase: `"final_answer"`, want: `"final_answer"`},
		{name: "explicit null", phase: `null`, want: `null`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding, state, transcript := toolTurnFixture(t, 1)
			toolPart(transcript, 0)["metadata"] = map[string]any{"openai": map[string]any{"itemId": "fc_provider_item_1"}}
			finalParts := parts(transcript[2])
			finalParts[1]["metadata"] = map[string]any{"openai": map[string]any{"itemId": "msg_provider_item_2", "phase": json.RawMessage(test.phase)}}
			observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
			if err != nil {
				t.Fatal(err)
			}
			items := observation.Generations[1].TextProviderItems
			if len(items) != 1 || items[0].ProviderItemID != "msg_provider_item_2" || string(items[0].Phase) != test.want {
				t.Fatalf("text phase was not retained in typed observation: %#v", items)
			}
			if observation.Text != "complete" || observation.Generations[1].TextSHA256 != toolTurnDigest([]byte("complete")) {
				t.Fatal("text projection changed while retaining phase", observation)
			}
		})
	}
}

func TestDecodeToolTurnCapturedM2iTextPhaseShape(t *testing.T) {
	// Sanitized structural replay of the private M2i metadata: the captured
	// text parts had exactly this OpenAI key shape. Provider bodies and opaque
	// reasoning material remain outside the repository.
	binding, state, transcript := toolTurnFixture(t, 1)
	toolPart(transcript, 0)["metadata"] = map[string]any{"openai": map[string]any{"itemId": "fc_provider_item_1"}}
	intermediateParts := parts(transcript[1])
	intermediateText := mergePart(basePart("part_intermediate_text", "msg_tools", "text"), map[string]any{
		"text": "Visible commentary", "time": map[string]any{"start": 13, "end": 14},
		"metadata": map[string]any{"openai": map[string]any{"itemId": "rs_captured_text_item", "phase": "commentary"}},
	})
	transcript[1]["parts"] = append(intermediateParts[:2], append([]map[string]any{intermediateText}, intermediateParts[2:]...)...)
	finalParts := parts(transcript[2])
	finalParts[1]["metadata"] = map[string]any{"openai": map[string]any{"itemId": "msg_provider_item_2"}}
	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Generations[0].TextProviderItems) != 1 || observation.Generations[0].TextProviderItems[0].ProviderItemID != "rs_captured_text_item" || string(observation.Generations[0].TextProviderItems[0].Phase) != `"commentary"` {
		t.Fatal("captured M2i text metadata shape was not retained", observation.Generations[0])
	}
	if observation.Generations[0].TextSHA256 != toolTurnDigest([]byte("Visible commentary")) {
		t.Fatal("captured commentary changed visible text projection", observation.Generations[0])
	}
}

func TestDecodeToolTurnAlwaysRetainsGenerationTextDigests(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical.Bytes(observation)
	if err != nil || strings.Contains(string(encoded), "provider_item_id") || strings.Contains(string(encoded), "text_provider_item_ids") || strings.Contains(string(encoded), "text_provider_items") || !strings.Contains(string(encoded), `"text_sha256":"`+toolTurnDigest(nil)+`"`) || !strings.Contains(string(encoded), `"text_sha256":"`+toolTurnDigest([]byte("complete"))+`"`) {
		t.Fatal("generation text digests were not retained independently of provider metadata", string(encoded), err)
	}
}

func TestDecodeToolTurnHashesEmptyResponsesGenerationText(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	toolPart(transcript, 0)["metadata"] = map[string]any{"openai": map[string]any{"itemId": "fc_provider_item_1"}}
	finalParts := parts(transcript[2])
	finalParts[1]["metadata"] = map[string]any{"openai": map[string]any{"itemId": "msg_provider_item_2"}}
	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Generations) != 2 || observation.Generations[0].TextSHA256 != toolTurnDigest(nil) || observation.Generations[1].TextSHA256 != toolTurnDigest([]byte("complete")) {
		t.Fatal("empty or final Responses text digest mismatch", observation.Generations)
	}
}

func TestSealObservationHashAllowsLegacyProjectionOnlyWithoutProvider(t *testing.T) {
	observation := ToolTurnObservation{Generations: []ToolGenerationObservation{{Calls: []ToolCallObservation{}, TextSHA256: toolTurnDigest([]byte("intermediate"))}}}
	legacy, err := canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", legacyToolTurnObservationV1(observation))
	if err != nil {
		t.Fatal(err)
	}
	got, err := sealObservationHash(observation, true, legacy)
	if err != nil || got != legacy {
		t.Fatal("legacy non-provider observation projection was not replayed", got, err)
	}
	got, err = sealObservationHash(observation, false, legacy)
	if err != nil || got == legacy {
		t.Fatal("provider-backed observation downgraded to legacy projection", got, err)
	}
}

func TestDecodeToolTurnRejectsOtherProviderMetadata(t *testing.T) {
	tests := map[string]any{
		"provider executed": map[string]any{"providerExecuted": true},
		"extra provider":    map[string]any{"openai": map[string]any{"itemId": "fc_item"}, "other": map[string]any{}},
		"extra OpenAI key":  map[string]any{"openai": map[string]any{"itemId": "fc_item", "other": true}},
		"phase on tool":     map[string]any{"openai": map[string]any{"itemId": "fc_item", "phase": "commentary"}},
		"missing item ID":   map[string]any{"openai": map[string]any{}},
		"invalid item ID":   map[string]any{"openai": map[string]any{"itemId": "fc\nitem"}},
		"non-object OpenAI": map[string]any{"openai": "fc_item"},
	}
	for name, metadata := range tests {
		t.Run(name, func(t *testing.T) {
			binding, state, transcript := toolTurnFixture(t, 1)
			toolPart(transcript, 0)["metadata"] = metadata
			if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
				t.Fatal("unbound provider metadata admitted")
			}
		})
	}
}

func TestDecodeToolTurnRejectsInvalidTextPhaseValues(t *testing.T) {
	for name, phase := range map[string]any{
		"unknown string": "analysis",
		"number":         1,
		"boolean":        true,
		"object":         map[string]any{},
		"array":          []any{},
	} {
		t.Run(name, func(t *testing.T) {
			binding, state, transcript := toolTurnFixture(t, 1)
			toolPart(transcript, 0)["metadata"] = map[string]any{"openai": map[string]any{"itemId": "fc_provider_item_1"}}
			finalParts := parts(transcript[2])
			finalParts[1]["metadata"] = map[string]any{"openai": map[string]any{"itemId": "msg_provider_item_2", "phase": phase}}
			if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
				t.Fatal("invalid text phase admitted")
			}
		})
	}
}

func TestDecodeToolTurnRetainsExactM1bReasoningItemID(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	finalParts := parts(transcript[2])
	reasoning := mergePart(basePart("part_reasoning", "msg_final", "reasoning"), map[string]any{
		"text": "private", "time": map[string]any{"start": 31, "end": 32},
		"metadata": map[string]any{"openai": map[string]any{
			"itemId": string(m1bProviderItemID), "reasoningEncryptedContent": "synthetic-encrypted-replay",
		}},
	})
	transcript[2]["parts"] = append(finalParts[:1], append([]map[string]any{reasoning}, finalParts[1:]...)...)

	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	got := observation.Generations[1].Reasoning
	if len(got) != 1 || got[0].ProviderItemID != m1bProviderItemID {
		t.Fatal("exact M1b reasoning item ID was not retained", got)
	}
	if locator(string(m1bProviderItemID)) {
		t.Fatal("opaque provider item ID was admitted as an OpenCode locator")
	}
	encoded, err := canonical.Bytes(observation)
	if err != nil || !strings.Contains(string(encoded), `"provider_item_id":"`+string(m1bProviderItemID)+`"`) {
		t.Fatal("exact M1b reasoning item ID is absent from canonical observation", string(encoded), err)
	}
	var roundTrip ToolTurnObservation
	if err := canonical.Decode(encoded, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip, observation) {
		t.Fatal("M1b provider item ID did not survive canonical round trip", roundTrip, err)
	}
}

func TestDecodeToolTurnRejectsResponsesMetadataOnReasoningPart(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	finalParts := parts(transcript[2])
	reasoning := mergePart(basePart("part_reasoning", "msg_final", "reasoning"), map[string]any{
		"text": "private", "time": map[string]any{"start": 31, "end": 32},
		"metadata": map[string]any{"openai": map[string]any{"itemId": "rs_provider_item"}},
	})
	transcript[2]["parts"] = append(finalParts[:1], append([]map[string]any{reasoning}, finalParts[1:]...)...)
	if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
		t.Fatal("Responses item metadata on reasoning part admitted")
	}
}

func TestDecodeToolTurnRejectsPhaseOnReasoningPart(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	finalParts := parts(transcript[2])
	reasoning := mergePart(basePart("part_reasoning", "msg_final", "reasoning"), map[string]any{
		"text": "private", "time": map[string]any{"start": 31, "end": 32},
		"metadata": map[string]any{"openai": map[string]any{"itemId": "rs_provider_item", "reasoningEncryptedContent": "synthetic-encrypted-replay", "phase": "commentary"}},
	})
	transcript[2]["parts"] = append(finalParts[:1], append([]map[string]any{reasoning}, finalParts[1:]...)...)
	if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
		t.Fatal("phase on reasoning part admitted")
	}
}

func reasoningFixturePart(id, messageID, text string, start, end int64, metadata map[string]any) map[string]any {
	part := mergePart(basePart(id, messageID, "reasoning"), map[string]any{
		"text": text, "time": map[string]any{"start": start, "end": end},
	})
	if metadata != nil {
		part["metadata"] = metadata
	}
	return part
}

func TestDecodeToolTurnPairsMultiSummaryWithSameGenerationCarrier(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	toolPart(transcript, 0)["metadata"] = map[string]any{"openai": map[string]any{"itemId": "fc_provider_item_1"}}
	finalParts := parts(transcript[2])
	item := "rs_synthetic_summary_item"
	cipher := "synthetic-encrypted-carrier"
	summaries := []map[string]any{
		reasoningFixturePart("part_reasoning_summary_1", "msg_final", "first summary ", 31, 32, map[string]any{"openai": map[string]any{"itemId": item}}),
		reasoningFixturePart("part_reasoning_summary_2", "msg_final", "second summary ", 32, 33, map[string]any{"openai": map[string]any{"itemId": item}}),
		reasoningFixturePart("part_reasoning_carrier", "msg_final", "final summary", 33, 34, map[string]any{"openai": map[string]any{"itemId": item, "reasoningEncryptedContent": cipher}}),
	}
	transcript[2]["parts"] = append(finalParts[:1], append(summaries, finalParts[1:]...)...)

	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	got := observation.Generations[1].Reasoning
	if len(got) != 1 || got[0].ProviderItemID != ProviderItemID(item) || got[0].EncryptedContent != cipher || got[0].PartID != "part_reasoning_carrier" {
		t.Fatal("multi-summary was not paired with its same-generation carrier", got)
	}
	if want := toolTurnDigest([]byte("first summary second summary final summary")); got[0].TextSHA256 != want {
		t.Fatal("aggregated reasoning text digest mismatch", got[0].TextSHA256)
	}
	if observation.Text != "complete" || observation.Generations[1].TextSHA256 != toolTurnDigest([]byte("complete")) {
		t.Fatal("final text projection changed while pairing reasoning", observation.Text)
	}
	if len(observation.Calls) != 1 || observation.Calls[0].ProviderItemID != "fc_provider_item_1" || len(observation.Calls[0].ContentSHA256) != 64 || len(observation.Calls[0].ArgumentsSHA256) != 64 {
		t.Fatal("provider tool digest did not survive reasoning pairing", observation.Calls)
	}
	if len(observation.TranscriptSHA256) != 64 {
		t.Fatal("raw transcript digest missing after reasoning pairing", observation)
	}
	encoded, err := canonical.Bytes(observation)
	if err != nil || !strings.Contains(string(encoded), `"encrypted_content":"`+cipher+`"`) || !strings.Contains(string(encoded), `"provider_item_id":"`+item+`"`) {
		t.Fatal("encrypted carrier identity absent from canonical observation", string(encoded), err)
	}
	var roundTrip ToolTurnObservation
	if err := canonical.Decode(encoded, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip, observation) {
		t.Fatal("paired reasoning observation did not survive canonical round trip", roundTrip, err)
	}
}

func TestDecodeToolTurnSingleCarrierShapeUnchanged(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	finalParts := parts(transcript[2])
	reasoning := reasoningFixturePart("part_reasoning", "msg_final", "private", 31, 32, map[string]any{
		"openai": map[string]any{"itemId": "rs_provider_item", "reasoningEncryptedContent": "synthetic-encrypted-replay"},
	})
	transcript[2]["parts"] = append(finalParts[:1], append([]map[string]any{reasoning}, finalParts[1:]...)...)
	observation, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state)
	if err != nil {
		t.Fatal(err)
	}
	got := observation.Generations[1].Reasoning
	if len(got) != 1 || got[0].PartID != "part_reasoning" || got[0].ProviderItemID != "rs_provider_item" || got[0].EncryptedContent != "synthetic-encrypted-replay" || got[0].TextSHA256 != toolTurnDigest([]byte("private")) {
		t.Fatal("single-carrier reasoning projection changed", got)
	}
}

func TestDecodeToolTurnRejectsUnpairedOrAmbiguousReasoning(t *testing.T) {
	carrier := func(id, item, cipher string, start, end int64) map[string]any {
		return reasoningFixturePart(id, "msg_final", "carrier text", start, end, map[string]any{
			"openai": map[string]any{"itemId": item, "reasoningEncryptedContent": cipher},
		})
	}
	fragment := func(id, item string) map[string]any {
		return reasoningFixturePart(id, "msg_final", "summary text", 31, 32, map[string]any{
			"openai": map[string]any{"itemId": item},
		})
	}
	splice := func(finalParts []map[string]any, middle []map[string]any) []map[string]any {
		return append(finalParts[:1], append(middle, finalParts[1:]...)...)
	}
	tests := map[string]func(finalParts []map[string]any) []map[string]any{
		"mismatched provider item": func(finalParts []map[string]any) []map[string]any {
			return splice(finalParts, []map[string]any{
				fragment("part_reasoning_frag", "rs_item_a"),
				carrier("part_reasoning_carrier", "rs_item_b", "synthetic-encrypted-a", 32, 33),
			})
		},
		"duplicate carriers": func(finalParts []map[string]any) []map[string]any {
			return splice(finalParts, []map[string]any{
				carrier("part_reasoning_carrier_1", "rs_item", "synthetic-encrypted-a", 31, 32),
				carrier("part_reasoning_carrier_2", "rs_item", "synthetic-encrypted-a", 32, 33),
			})
		},
		"contradictory carriers": func(finalParts []map[string]any) []map[string]any {
			return splice(finalParts, []map[string]any{
				carrier("part_reasoning_carrier_1", "rs_item", "synthetic-encrypted-a", 31, 32),
				carrier("part_reasoning_carrier_2", "rs_item", "synthetic-encrypted-b", 32, 33),
			})
		},
		"duplicate part identity": func(finalParts []map[string]any) []map[string]any {
			return splice(finalParts, []map[string]any{
				fragment("part_reasoning_dup", "rs_item_a"),
				fragment("part_reasoning_dup", "rs_item_b"),
			})
		},
	}
	for name, spliceParts := range tests {
		t.Run(name, func(t *testing.T) {
			binding, state, transcript := toolTurnFixture(t, 1)
			transcript[2]["parts"] = spliceParts(parts(transcript[2]))
			if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
				t.Fatal("unpaired or ambiguous reasoning admitted")
			}
		})
	}
}

func TestDecodeToolTurnRejectsCrossGenerationReasoningPair(t *testing.T) {
	binding, state, transcript := toolTurnFixture(t, 1)
	intermediateParts := parts(transcript[1])
	fragment := reasoningFixturePart("part_reasoning_frag", "msg_tools", "summary text", 13, 14, map[string]any{
		"openai": map[string]any{"itemId": "rs_shared_item"},
	})
	transcript[1]["parts"] = append(intermediateParts[:2], append([]map[string]any{fragment}, intermediateParts[2:]...)...)
	finalParts := parts(transcript[2])
	carrier := reasoningFixturePart("part_reasoning_carrier", "msg_final", "carrier text", 31, 32, map[string]any{
		"openai": map[string]any{"itemId": "rs_shared_item", "reasoningEncryptedContent": "synthetic-encrypted-a"},
	})
	transcript[2]["parts"] = append(finalParts[:1], append([]map[string]any{carrier}, finalParts[1:]...)...)
	if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
		t.Fatal("cross-generation reasoning pair admitted")
	}
}

func TestDecodeToolTurnRejectsInvalidReasoningMetadataTimingAndBounds(t *testing.T) {
	tests := map[string]func() map[string]any{
		"extra metadata domain": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 31, 32, map[string]any{
				"openai": map[string]any{"itemId": "rs_item"}, "other": map[string]any{},
			})
		},
		"extra OpenAI key": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 31, 32, map[string]any{
				"openai": map[string]any{"itemId": "rs_item", "reasoningEncryptedContent": "synthetic-encrypted-a", "other": true},
			})
		},
		"missing item ID": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 31, 32, map[string]any{
				"openai": map[string]any{"reasoningEncryptedContent": "synthetic-encrypted-a"},
			})
		},
		"invalid item ID": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 31, 32, map[string]any{
				"openai": map[string]any{"itemId": "rs\nitem"},
			})
		},
		"non-object OpenAI metadata": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 31, 32, map[string]any{
				"openai": "rs_item",
			})
		},
		"empty encrypted content": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 31, 32, map[string]any{
				"openai": map[string]any{"itemId": "rs_item", "reasoningEncryptedContent": ""},
			})
		},
		"non-printable encrypted content": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 31, 32, map[string]any{
				"openai": map[string]any{"itemId": "rs_item", "reasoningEncryptedContent": "synthetic cipher"},
			})
		},
		"oversize encrypted content": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 31, 32, map[string]any{
				"openai": map[string]any{"itemId": "rs_item", "reasoningEncryptedContent": strings.Repeat("s", (256<<10)+1)},
			})
		},
		"oversize reasoning text": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", strings.Repeat("s", (256<<10)+1), 31, 32, map[string]any{
				"openai": map[string]any{"itemId": "rs_item", "reasoningEncryptedContent": "synthetic-encrypted-a"},
			})
		},
		"unfinished part time": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 32, 31, map[string]any{
				"openai": map[string]any{"itemId": "rs_item", "reasoningEncryptedContent": "synthetic-encrypted-a"},
			})
		},
		"time outside assistant": func() map[string]any {
			return reasoningFixturePart("part_reasoning", "msg_final", "private", 41, 42, map[string]any{
				"openai": map[string]any{"itemId": "rs_item", "reasoningEncryptedContent": "synthetic-encrypted-a"},
			})
		},
		"invalid part identity": func() map[string]any {
			return reasoningFixturePart("part reasoning", "msg_final", "private", 31, 32, map[string]any{
				"openai": map[string]any{"itemId": "rs_item", "reasoningEncryptedContent": "synthetic-encrypted-a"},
			})
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			binding, state, transcript := toolTurnFixture(t, 1)
			finalParts := parts(transcript[2])
			transcript[2]["parts"] = append(finalParts[:1], append([]map[string]any{build()}, finalParts[1:]...)...)
			if _, err := decodeToolTurn(marshalToolTranscript(t, transcript), binding, "use source_read", state); err == nil {
				t.Fatal("invalid reasoning part admitted")
			}
		})
	}
}
