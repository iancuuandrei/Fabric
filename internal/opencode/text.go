package opencode

import (
	"encoding/json"
	"errors"
	"strings"
)

// DecodeTextParts projects visible text in wire order from a final assistant's
// parts. It never interprets reasoning as output. Tool/file/compaction parts are
// unsupported here and rejected rather than silently treated as completed work.
// The caller must independently establish whole-turn completeness.
func DecodeTextParts(raw []byte, assistant Assistant) (string, error) {
	text, _, err := DecodeTextPartsWithRuntimeMetadata(raw, assistant, nil)
	return text, err
}

// DecodeTextPartsWithRuntimeMetadata keeps visible semantic text separate from
// explicitly admitted snapshot metadata. A nil expectation preserves legacy
// behavior and rejects patch parts.
func DecodeTextPartsWithRuntimeMetadata(raw []byte, assistant Assistant, expected *RuntimeMetadataExpectation) (string, []PatchSnapshotReceipt, error) {
	if assistant.ID == "" || assistant.Binding.SessionID == "" {
		return "", nil, errors.New("assistant binding required")
	}
	// Validate the whole array through the bounded object decoder before parsing.
	if len(raw) > (1<<20)-10 {
		return "", nil, errors.New("parts exceed bound")
	}
	wrapped := append([]byte(`{"parts":`), raw...)
	wrapped = append(wrapped, '}')
	m, err := wireObject(wrapped)
	if err != nil {
		return "", nil, err
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(m["parts"], &parts); err != nil || parts == nil || len(parts) > 4096 {
		return "", nil, errors.New("invalid OpenCode parts")
	}
	metadata, err := decodeRuntimeMetadataParts(raw, assistant, expected)
	if err != nil {
		return "", nil, err
	}
	seen := map[string]bool{}
	var text strings.Builder
	for _, rawPart := range parts {
		part, err := wireObject(rawPart)
		if err != nil {
			return "", nil, err
		}
		var id, session, message, kind string
		if field(part, "id", &id) != nil || field(part, "sessionID", &session) != nil || field(part, "messageID", &message) != nil || field(part, "type", &kind) != nil {
			return "", nil, errors.New("part identity missing")
		}
		if strings.TrimSpace(id) == "" || len(id) > 256 || seen[id] || session != assistant.Binding.SessionID || message != assistant.ID {
			return "", nil, errors.New("part identity mismatch or duplication")
		}
		seen[id] = true
		switch kind {
		case "reasoning", "step-start", "step-finish":
			continue
		case "patch":
			if expected == nil {
				return "", nil, errors.New("unsupported final message part")
			}
			continue
		case "text":
			for _, key := range []string{"synthetic", "ignored"} {
				if _, ok := part[key]; ok {
					var excluded bool
					if field(part, key, &excluded) != nil || excluded {
						return "", nil, errors.New("excluded text cannot become final output")
					}
				}
			}
			if _, ok := part["time"]; ok {
				var timing struct {
					Start *int64 `json:"start"`
					End   *int64 `json:"end"`
				}
				if field(part, "time", &timing) != nil || timing.Start == nil || timing.End == nil || *timing.Start < 0 || *timing.End < *timing.Start || *timing.End > 9007199254740991 {
					return "", nil, errors.New("unfinished text part")
				}
			}
			var value string
			if field(part, "text", &value) != nil || len(value) > (256<<10)-text.Len() {
				return "", nil, errors.New("invalid or oversized text")
			}
			text.WriteString(value)
		default:
			return "", nil, errors.New("unsupported final message part")
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", nil, errors.New("no visible final text")
	}
	return text.String(), metadata, nil
}
