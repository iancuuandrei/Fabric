package contextcompiler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/memory"
)

func TestCompileContextPackRankingCacheCoChangeAndDeterminism(t *testing.T) {
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-q")

	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/ctxdemo\n\ngo 1.24\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Always preserve atomic temp-file writes."), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, "pkg", "auth"), 0o700)
	_ = os.MkdirAll(filepath.Join(dir, "pkg", "billing"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "auth", "AGENTS.md"), []byte("Use hmac.Equal for all token comparisons."), 0o600)

	authSrc := `package auth

import (
	"crypto/hmac"
	"errors"
	"sync"
)

var mu sync.Mutex

// VerifyHMAC checks incoming signature digests cleanly.
// @param secret string signing secret
// @param payload []byte raw request body
// @returns error when signature validation fails
func VerifyHMAC(secret string, payload, got []byte) error {
	mu.Lock()
	defer mu.Unlock()
	if len(secret) == 0 {
		return errors.New("empty secret")
	}
	if !hmac.Equal(payload, got) {
		return errors.New("mismatch")
	}
	return nil
}

// UntestedHelper performs critical post-auth auditing with zero direct tests.
func UntestedHelper(secret string) error {
	return VerifyHMAC(secret, nil, nil)
}
`
	authTestSrc := `package auth

import "testing"

func TestVerifyHMAC(t *testing.T) {
	_ = VerifyHMAC("k", []byte("a"), []byte("a"))
}
`
	billingSrc := `package billing

// InvoiceTotal computes invoice cents without touching auth.
func InvoiceTotal(a, b int) int { return a + b }

// Verify checks generic billing invariant without HMAC logic.
func Verify() bool { return true }
`
	_ = os.WriteFile(filepath.Join(dir, "pkg", "auth", "hmac.go"), []byte(authSrc), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "auth", "hmac_test.go"), []byte(authTestSrc), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "auth", "policy.md"), []byte("# Auth Policy\nSee `VerifyHMAC`.\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "billing", "invoice.go"), []byte(billingSrc), 0o600)

	runGit("add", ".")
	runGit("-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "init")

	// Add a co-change commit coupling pkg/auth/hmac.go with pkg/auth/policy.md.
	_ = os.WriteFile(filepath.Join(dir, "pkg", "auth", "hmac.go"), []byte(authSrc+"\n// co-change v2\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "auth", "policy.md"), []byte("# Auth Policy v2\nSee `VerifyHMAC`.\n"), 0o600)
	runGit("add", ".")
	runGit("-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "co-change auth")

	if _, err := memory.Add(dir, memory.Lesson{Scope: "pkg/auth", Rule: "Never log raw HMAC secrets."}); err != nil {
		t.Fatal(err)
	}

	opts := Options{
		Query:        "VerifyHMAC signature check error",
		BudgetTokens: 2800,
		UseCache:     true,
		Skills: []SkillHint{
			{Name: "concurrency-resource-safety", Description: "Prevent lock and concurrency bugs"},
			{Name: "ci-and-supply-chain-hygiene", Description: "Harden auth and webhook verification"},
			{Name: "code-hygiene", Description: "Enforce clean code"},
			{Name: "error-handling-and-observability", Description: "Classify errors cleanly"},
			{Name: "performance-and-profiling", Description: "Benchmark hot paths"},
			{Name: "tdd", Description: "Add discriminating regression tests"},
		},
	}
	pack1, err := Compile(context.Background(), dir, opts)
	if err != nil {
		t.Fatalf("first Compile failed: %v", err)
	}
	if pack1.CacheHit {
		t.Fatal("expected cold cache miss on first Compile")
	}
	if len(pack1.Items) == 0 || pack1.Items[0].Name != "VerifyHMAC" {
		t.Fatalf("expected VerifyHMAC ranked #1, got items=%+v", pack1.Items)
	}
	if pack1.UsedTokens > pack1.BudgetTokens+250 {
		t.Fatalf("compiled pack exceeded budget ceiling: used=%d budget=%d", pack1.UsedTokens, pack1.BudgetTokens)
	}
	if len(pack1.ScopedGuidance) < 2 {
		t.Fatalf("expected root + nested pkg/auth/AGENTS.md loaded, got %+v", pack1.ScopedGuidance)
	}
	if len(pack1.Lessons) == 0 || !strings.Contains(pack1.Lessons[0].Rule, "HMAC") {
		t.Fatalf("expected relevant memory lesson included, got %+v", pack1.Lessons)
	}
	if len(pack1.RecommendedSkills) == 0 || !containsStr(pack1.RecommendedSkills, "concurrency-resource-safety") {
		t.Fatalf("expected concurrency-resource-safety boosted by uses_locks effect, got %v", pack1.RecommendedSkills)
	}
	sawUntested := false
	for _, u := range pack1.UntestedSymbols {
		if strings.Contains(u, "UntestedHelper") {
			sawUntested = true
		}
	}
	if !sawUntested {
		t.Fatalf("expected UntestedHelper flagged in UntestedSymbols, got %v", pack1.UntestedSymbols)
	}

	// Second compile with unchanged files must hit SHA-256 cache and produce byte-identical Markdown (KV-cache stability!).
	pack2, err := Compile(context.Background(), dir, opts)
	if err != nil {
		t.Fatalf("second Compile failed: %v", err)
	}
	if !pack2.CacheHit {
		t.Fatal("expected SHA-256 cache hit on second Compile")
	}
	if pack1.Markdown() != pack2.Markdown() {
		t.Fatal("expected byte-identical deterministic Markdown output for KV-cache stability")
	}

	// Mutate a source file -> SHA-256 fingerprint changes -> cache miss, and verify
	// unrelated dirty files do not hijack explicit query symbol ranking even when IncludeDiff=true.
	_ = os.WriteFile(filepath.Join(dir, "pkg", "billing", "invoice.go"), []byte(billingSrc+"\n// changed\n"), 0o600)
	pack3, err := Compile(context.Background(), dir, opts)
	if err != nil {
		t.Fatalf("third Compile failed: %v", err)
	}
	if pack3.CacheHit {
		t.Fatal("expected cache invalidation after file mutation")
	}
	optsDiff := opts
	optsDiff.IncludeDiff = true
	pack4, err := Compile(context.Background(), dir, optsDiff)
	if err != nil {
		t.Fatalf("dirty-diff Compile failed: %v", err)
	}
	if len(pack4.Items) == 0 || pack4.Items[0].Name != "VerifyHMAC" {
		t.Fatalf("expected VerifyHMAC still ranked #1 with unrelated dirty diff, got items=%+v", pack4.Items)
	}
}
