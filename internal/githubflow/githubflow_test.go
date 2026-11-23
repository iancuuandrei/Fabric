package githubflow

import (
	"context"
	"strings"
	"testing"
)

func TestValidateRefAndBuildIssueObjective(t *testing.T) {
	if r, err := ValidateRef("#42"); err != nil || r != "42" {
		t.Fatalf("expected #42 normalized to 42, got %q err=%v", r, err)
	}
	for _, bad := range []string{"", "   ", "--repo=evil/repo", "-f", "123 abc"} {
		if _, err := ValidateRef(bad); err == nil {
			t.Fatalf("expected error for invalid ref %q", bad)
		}
	}

	obj := BuildIssueObjective(Issue{
		Number: 17,
		Title:  "Fix timeout race",
		Labels: []string{"bug", "concurrency"},
		Body:   "Context deadline exceeded leaves child alive.",
	})
	if !strings.Contains(obj, "Issue #17") || !strings.Contains(obj, "Fix timeout race") || !strings.Contains(obj, "concurrency") {
		t.Fatalf("unexpected formatted objective:\n%s", obj)
	}

	if _, err := FetchIssue(context.Background(), "", "--flag"); err == nil {
		t.Fatal("expected flag injection ref rejection")
	}
	if err := CommentOnIssue(context.Background(), "", "--flag", "hello"); err == nil {
		t.Fatal("expected flag injection ref rejection in CommentOnIssue")
	}
	if err := TransitionIssue(context.Background(), "", "42", "--evil-label"); err == nil {
		t.Fatal("expected flag injection label rejection in TransitionIssue")
	}
	if err := TransitionIssue(context.Background(), "", "42", ""); err == nil {
		t.Fatal("expected empty targetState rejection")
	}
}
