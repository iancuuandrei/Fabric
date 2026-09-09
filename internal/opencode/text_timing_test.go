package opencode

import (
	"fmt"
	"testing"
)

func TestTextTimingRequiresBothObservedEndpoints(t *testing.T) {
	a := Assistant{ID: "msg_fixture", Binding: Binding{SessionID: "ses_fixture"}}
	for _, timing := range []string{`{"start":0,"end":0}`, `{}`, `null`, `{"start":0}`, `{"end":0}`, `{"start":null,"end":0}`, `{"start":0,"end":null}`, `{"start":0,"end":9007199254740992}`} {
		raw := fmt.Sprintf(`[{"id":"prt_fixture","sessionID":"ses_fixture","messageID":"msg_fixture","type":"text","text":"output","time":%s}]`, timing)
		_, err := DecodeTextParts([]byte(raw), a)
		if (err == nil) != (timing == `{"start":0,"end":0}`) {
			t.Fatalf("timing %s: %v", timing, err)
		}
	}
}
