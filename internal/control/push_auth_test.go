package control

import (
	"testing"

	"harness.local/engorch/internal/gitpush"
)

func TestPushCredentialSelection(t *testing.T) {
	const destination = "https://github.com/fixture/project.git"
	c, err := gitpush.NewCredential(destination, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if selected, err := pushCredential(destination, nil); err != nil || selected != nil {
		t.Fatal("local mode changed")
	}
	if selected, err := pushCredential(destination, []*gitpush.Credential{c}); err != nil || selected != c {
		t.Fatal("explicit credential lost")
	}
	for _, credentials := range [][]*gitpush.Credential{{nil}, {c, c}, {new(gitpush.Credential)}} {
		if _, err := pushCredential(destination, credentials); err == nil {
			t.Fatal("invalid explicit credentials admitted")
		}
	}
	if _, err := pushCredential(destination+"/other", []*gitpush.Credential{c}); err == nil {
		t.Fatal("credential widened")
	}
}
