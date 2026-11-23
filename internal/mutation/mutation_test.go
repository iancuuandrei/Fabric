package mutation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiffScopedMutationDetectionAndRestore(t *testing.T) {
	dir := t.TempDir()
	origSrc := "package calc\nfunc IsPositive(n int) bool {\n\treturn n > 0\n}\n"
	srcPath := filepath.Join(dir, "calc.go")
	if err := os.WriteFile(srcPath, []byte(origSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	// Diff line 3 (`return n > 0` -> `return n >= 0`).
	// Using `git --version` (always exits 0) simulates a weak test suite -> mutant SURVIVES.
	rep, err := AuditChangedLines(context.Background(), Config{
		WorkDir:    dir,
		TestArgv:   []string{"git", "--version"},
		MaxMutants: 5,
	}, []ChangedLine{{Path: "calc.go", Line: 3}})
	if err != nil {
		t.Fatalf("AuditChangedLines failed: %v", err)
	}
	if rep.TotalMutants != 1 || rep.Survived != 1 || rep.Score != 0 {
		t.Fatalf("expected 1 surviving mutant, got: %+v", rep)
	}
	// Original source MUST be restored cleanly.
	after, _ := os.ReadFile(srcPath)
	if string(after) != origSrc {
		t.Fatalf("source was not restored after mutation: %q", string(after))
	}

	// Failing baseline MUST return BASELINE_FAILED instead of faking 100% killed.
	repBaseFail, err := AuditChangedLines(context.Background(), Config{
		WorkDir:    dir,
		TestArgv:   []string{"git", "rev-parse", "--verify", "refs/missing-mutant-kill"},
		MaxMutants: 5,
	}, []ChangedLine{{Path: "calc.go", Line: 3}})
	if err != nil || repBaseFail.Status != "BASELINE_FAILED" {
		t.Fatalf("expected BASELINE_FAILED on broken baseline, got: %+v err=%v", repBaseFail, err)
	}

	// With SkipBaselineCheck=true (caller already verified baseline), failing test kills mutant.
	rep2, err := AuditChangedLines(context.Background(), Config{
		WorkDir:           dir,
		TestArgv:          []string{"git", "rev-parse", "--verify", "refs/missing-mutant-kill"},
		MaxMutants:        5,
		SkipBaselineCheck: true,
	}, []ChangedLine{{Path: "calc.go", Line: 3}})
	if err != nil || rep2.Killed != 1 || rep2.Score != 1.0 {
		t.Fatalf("expected 1 killed mutant, got: %+v err=%v", rep2, err)
	}

	// Test files must be skipped by ParseUnifiedDiffLines.
	if lines := ParseUnifiedDiffLines("+++ b/calc_test.go\n@@ -1 +1 @@\n"); len(lines) != 0 {
		t.Fatalf("expected *_test.go skipped, got: %+v", lines)
	}
}
