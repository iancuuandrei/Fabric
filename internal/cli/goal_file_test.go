package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanObjectiveFilePreservesBytesAndRejectsInvalidInput(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "goal.md")
	want := "# Obiectiv\r\nPăstrează textul exact.\n"
	if err := os.WriteFile(path, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := planObjective(context.Background(), root, []string{"--file", "goal.md"})
	if err != nil || got != want {
		t.Fatal("goal bytes changed", err)
	}
	for _, body := range [][]byte{[]byte(" \n"), {0xff}, []byte(strings.Repeat("x", (256<<10)+1))} {
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := planObjective(context.Background(), root, []string{"--file", path}); err == nil {
			t.Fatal("invalid goal file accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := planObjective(ctx, root, []string{"--file", "missing"}); err != context.Canceled {
		t.Fatal("cancelled read admitted", err)
	}
}
