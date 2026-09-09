package opencode

import (
	"context"
	"encoding/json"
	"errors"
)

// TranscriptMessage retains the complete wire message for role-specific
// validation. Reading a transcript does not establish host quiescence.
type TranscriptMessage struct {
	ID    string          `json:"id"`
	Role  string          `json:"role"`
	Info  json.RawMessage `json:"info"`
	Parts json.RawMessage `json:"parts"`
}

// ReadTranscript uses the unpaginated endpoint on an owned session. The HTTP
// byte bound and message bound fail closed, never returning a partial history.
// All message and part identities are checked before any result is returned.
func (c *Client) ReadTranscript(ctx context.Context, session string) ([]TranscriptMessage, error) {
	if !locator(session) {
		return nil, errors.New("invalid transcript session")
	}
	raw, err := c.read(ctx, "/session/"+session+"/message")
	if err != nil {
		return nil, err
	}
	return decodeTranscript(raw, session)
}

func decodeTranscript(raw []byte, session string) ([]TranscriptMessage, error) {
	if !locator(session) || len(raw) > (1<<20)-10 {
		return nil, errors.New("invalid transcript bound")
	}
	wrapped := append([]byte(`{"items":`), raw...)
	wrapped = append(wrapped, '}')
	object, err := wireObject(wrapped)
	if err != nil {
		return nil, err
	}
	var rows []json.RawMessage
	if json.Unmarshal(object["items"], &rows) != nil || rows == nil || len(rows) > 4096 {
		return nil, errors.New("invalid transcript array")
	}
	messages := make([]TranscriptMessage, 0, len(rows))
	seenMessages, seenParts := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		envelope, err := wireObject(row)
		if err != nil {
			return nil, err
		}
		info, err := wireObject(envelope["info"])
		if err != nil {
			return nil, err
		}
		var id, owner, role string
		if field(info, "id", &id) != nil || field(info, "sessionID", &owner) != nil || field(info, "role", &role) != nil || !locator(id) || owner != session || seenMessages[id] || (role != "user" && role != "assistant") {
			return nil, errors.New("invalid transcript message identity")
		}
		seenMessages[id] = true
		var parts []json.RawMessage
		if json.Unmarshal(envelope["parts"], &parts) != nil || parts == nil || len(parts) > 4096 {
			return nil, errors.New("invalid transcript parts")
		}
		for _, rawPart := range parts {
			part, err := wireObject(rawPart)
			if err != nil {
				return nil, err
			}
			var partID, parent, partSession string
			if field(part, "id", &partID) != nil || field(part, "messageID", &parent) != nil || field(part, "sessionID", &partSession) != nil || !locator(partID) || seenParts[partID] || parent != id || partSession != session {
				return nil, errors.New("invalid transcript part identity")
			}
			seenParts[partID] = true
		}
		messages = append(messages, TranscriptMessage{ID: id, Role: role, Info: envelope["info"], Parts: envelope["parts"]})
	}
	return messages, nil
}
