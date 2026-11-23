package codegraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildImpactAndReport(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.24\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, "pkg", "core"), 0o700)
	_ = os.MkdirAll(filepath.Join(dir, "cmd", "app"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "core", "calc.go"), []byte("package core\n\nfunc ComputeTotal(x int) int { return x * 2 }\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "cmd", "app", "main.go"), []byte("package main\n\nimport \"example.com/demo/pkg/core\"\n\nfunc main() { _ = core.ComputeTotal(5) }\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "ARCHITECTURE.md"), []byte("# Arch\nCore lives in `pkg/core/calc.go`.\n"), 0o600)

	g, err := Build(dir)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if g.FilesCount != 3 {
		t.Fatalf("expected 3 indexed files, got %d", g.FilesCount)
	}
	if len(g.Edges) < 3 {
		t.Fatalf("expected DEFINES + IMPORTS + CALLS + DOCUMENTS edges, got %d", len(g.Edges))
	}

	imp := g.Impact("ComputeTotal", 2)
	if len(imp.MatchedIDs) == 0 || len(imp.Callers) == 0 {
		t.Fatalf("expected ComputeTotal callers in impact report: %+v", imp)
	}

	fileImp := g.Impact("pkg/core/calc.go", 2)
	if len(fileImp.Docs) == 0 {
		t.Fatalf("expected ARCHITECTURE.md linked as doc for calc.go: %+v", fileImp)
	}

	md := g.ReportMarkdown()
	if !strings.Contains(md, "Top Hub Nodes") || !strings.Contains(md, "calc.go") {
		t.Fatalf("unexpected report markdown:\n%s", md)
	}
	if _, _, err := g.Save(dir); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
}

func TestJavadocSpecExtractionFunctionGraphTestsAndPersonalizedRank(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/specdemo\n\ngo 1.24\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, "pkg", "auth"), 0o700)
	_ = os.MkdirAll(filepath.Join(dir, "web"), 0o700)
	_ = os.MkdirAll(filepath.Join(dir, "py"), 0o700)

	goSrc := `package auth

import (
	"errors"
	"net/http"
)

// TokenConfig holds HMAC signing settings.
type TokenConfig struct {
	SecretEnv string
}

// VerifyRequest validates incoming bearer tokens against cfg.
// @param req (*http.Request) incoming HTTP request
// @returns bool whether authentication succeeded
// @throws ErrUnauthorized when token signature is invalid
// @see TokenConfig
func VerifyRequest(req *http.Request, cfg TokenConfig) (bool, error) {
	if cfg.SecretEnv == "" {
		return false, errors.New("missing secret")
	}
	return req != nil, nil
}

// HandleAuth delegates verification for HTTP handlers.
func HandleAuth(req *http.Request, cfg TokenConfig) (bool, error) {
	return VerifyRequest(req, cfg)
}
`
	goTestSrc := `package auth

import "testing"

func TestVerifyRequestWorks(t *testing.T) {
	_, _ = VerifyRequest(nil, TokenConfig{SecretEnv: "K"})
}
`
	tsSrc := `/**
 * Formats an authorization banner for UI display.
 * @param {string} userName caller login
 * @returns {string} rendered label
 * @throws {Error} when userName is empty
 */
export function formatAuthBanner(userName: string): string {
  if (!userName) { throw new Error("empty"); }
  return "user:" + userName;
}
`
	pySrc := `def parse_webhook_payload(raw_bytes):
    """Parse raw webhook payload safely.

    Args:
        raw_bytes (bytes): incoming HTTP body

    Returns:
        dict: parsed event data

    Raises:
        ValueError: when JSON is malformed
    """
    return {"raw": raw_bytes}
`
	_ = os.WriteFile(filepath.Join(dir, "pkg", "auth", "verify.go"), []byte(goSrc), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "auth", "verify_test.go"), []byte(goTestSrc), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "web", "banner.ts"), []byte(tsSrc), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "py", "hook.py"), []byte(pySrc), 0o600)

	g, err := Build(dir)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	var verifyNode *Node
	for i := range g.Nodes {
		if g.Nodes[i].Name == "VerifyRequest" {
			verifyNode = &g.Nodes[i]
			break
		}
	}
	if verifyNode == nil || verifyNode.Spec == nil {
		t.Fatalf("expected VerifyRequest with SymbolSpec")
	}
	if !strings.Contains(verifyNode.Spec.Signature, "func VerifyRequest(") ||
		!strings.Contains(verifyNode.Spec.Summary, "VerifyRequest validates incoming bearer tokens") ||
		len(verifyNode.Spec.Params) != 1 || verifyNode.Spec.Params[0].Name != "req" ||
		verifyNode.Spec.Returns == "" || len(verifyNode.Spec.Errors) == 0 {
		t.Fatalf("unexpected VerifyRequest SymbolSpec: %+v", verifyNode.Spec)
	}
	hasErrEffect := false
	for _, ef := range verifyNode.Spec.Effects {
		if ef == "returns_error" {
			hasErrEffect = true
		}
	}
	if !hasErrEffect {
		t.Fatalf("expected returns_error effect on VerifyRequest, got %v", verifyNode.Spec.Effects)
	}

	// Verify CALLS, USES_TYPE, TESTS, REFERENCES edges.
	rels := map[string]bool{}
	for _, e := range g.Edges {
		if e.To == verifyNode.ID || e.From == verifyNode.ID {
			rels[e.Relation] = true
		}
	}
	for _, want := range []string{"DEFINES", "CALLS", "USES_TYPE", "TESTS"} {
		if !rels[want] {
			t.Fatalf("expected relation %s linked to VerifyRequest, got %+v", want, rels)
		}
	}

	cards := g.SpecCardsForTarget("VerifyRequest", 3)
	if len(cards) == 0 || !strings.Contains(cards[0], "@called_by:") || !strings.Contains(cards[0], "@tested_by:") || !strings.Contains(cards[0], "@uses_types:") {
		t.Fatalf("expected Javadoc SpecCard with graph links, got:\n%v", cards)
	}

	// PersonalizedRank biased toward VerifyRequest should rank HandleAuth above unrelated TS/Py nodes.
	ranks := g.PersonalizedRank(map[string]float64{verifyNode.ID: 1.0})
	var handleID, pyID string
	for _, n := range g.Nodes {
		if n.Name == "HandleAuth" {
			handleID = n.ID
		}
		if n.Name == "parse_webhook_payload" {
			pyID = n.ID
			if n.Spec == nil || len(n.Spec.Params) != 1 || n.Spec.Returns == "" || len(n.Spec.Errors) != 1 {
				t.Fatalf("expected Python docstring spec extracted, got %+v", n.Spec)
			}
		}
		if n.Name == "formatAuthBanner" {
			if n.Spec == nil || len(n.Spec.Params) != 1 || n.Spec.Returns == "" || len(n.Spec.Errors) != 1 {
				t.Fatalf("expected JSDoc spec extracted, got %+v", n.Spec)
			}
		}
	}
	if ranks[handleID] <= ranks[pyID] {
		t.Fatalf("expected PPR seeded at VerifyRequest to rank caller HandleAuth (%f) > unrelated parse_webhook_payload (%f)", ranks[handleID], ranks[pyID])
	}
}
