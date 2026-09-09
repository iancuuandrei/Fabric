package cli

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestDraftCredentialSelection(t *testing.T) {
	t.Setenv("ENGORCH_FIXTURE_GITHUB_TOKEN", "fixture-only-token")
	t.Setenv("GH_TOKEN", "ambient-must-not-be-used")
	client, err := draftClient("ENGORCH_FIXTURE_GITHUB_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	for _, name := range []string{"", "1TOKEN", "TOKEN=value", "TOKEN\n", "nonasciié", strings.Repeat("A", 129)} {
		if _, err := draftClient(name); err == nil {
			t.Fatal("invalid token selector admitted")
		}
	}
	t.Setenv("ENGORCH_FIXTURE_GITHUB_TOKEN", "")
	if _, err := draftClient("ENGORCH_FIXTURE_GITHUB_TOKEN"); err == nil {
		t.Fatal("empty selected credential fell back to ambient token")
	}
	t.Setenv("ENGORCH_FIXTURE_GITHUB_TOKEN", "fixture-secret\n")
	if _, err := draftClient("ENGORCH_FIXTURE_GITHUB_TOKEN"); err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("invalid credential accepted or exposed")
	}
}

func TestDraftCommandArgumentCounts(t *testing.T) {
	for _, command := range []string{"prepare-draft", "draft", "reconcile-draft"} {
		for _, args := range [][]string{nil, {"run"}, {"run", "a", "b", "c", "d", "extra"}} {
			if err := draftCommand(context.Background(), t.TempDir(), command, args, io.Discard); err == nil {
				t.Fatal("invalid argument count accepted")
			}
		}
	}
}
