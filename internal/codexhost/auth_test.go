package codexhost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/codexrpc"
)

func TestExternalLoginSendsOnlyRequiredAccessMaterial(t *testing.T) {
	source := filepath.Join(t.TempDir(), "auth.json")
	content := []byte(`{"tokens":{"access_token":"fixture-access","account_id":"fixture-account","refresh_token":"must-not-send","id_token":"must-not-send-either"}}`)
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	h := &Host{Client: codexrpc.New(client)}
	defer h.Close()
	request := make(chan []byte, 1)
	go func() {
		defer server.Close()
		b, _ := bufio.NewReader(server).ReadBytes('\n')
		request <- b
		var m codexrpc.Message
		_ = json.Unmarshal(b, &m)
		_, _ = fmt.Fprintf(server, "{\"id\":%s,\"result\":{\"type\":\"chatgptAuthTokens\"}}\n", m.ID)
	}()
	if err := h.LoginChatGPT(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	b := <-request
	if strings.Contains(string(b), "must-not-send") || !strings.Contains(string(b), "fixture-access") {
		t.Fatal("wrong authentication material transmitted")
	}
	after, err := os.ReadFile(source)
	if err != nil || string(after) != string(content) {
		t.Fatal("source credentials changed", err)
	}
}
