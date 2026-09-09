package opencode

import (
	"strings"
	"testing"
)

func TestTranscriptRejectsPartialOrAmbiguousHistory(t *testing.T) {
	row := `{"info":{"id":"msg_user","sessionID":"ses_fixture","role":"user"},"parts":[{"id":"prt_text","messageID":"msg_user","sessionID":"ses_fixture","type":"text","text":"task"}]}`
	valid := "[" + row + "]"
	got, err := decodeTranscript([]byte(valid), "ses_fixture")
	if err != nil || len(got) != 1 || got[0].ID != "msg_user" {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"null", "[", "[" + row + "," + row + "]",
		strings.Replace(valid, `"messageID":"msg_user"`, `"messageID":"msg_other"`, 1),
		strings.Replace(valid, `"id":"msg_user"`, `"id":"msg_user","id":"msg_other"`, 1),
		strings.Replace(valid, `"sessionID":"ses_fixture"`, `"sessionID":"ses_other"`, 1),
		strings.Replace(valid, `"role":"user"`, `"role":"system"`, 1),
		strings.Repeat(" ", 1<<20),
	} {
		if got, err := decodeTranscript([]byte(raw), "ses_fixture"); err == nil || got != nil {
			t.Fatal("invalid history returned")
		}
	}
}
