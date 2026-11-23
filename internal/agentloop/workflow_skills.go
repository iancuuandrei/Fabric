package agentloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/iancuuandrei/fabric/internal/contextcompiler"
	"github.com/iancuuandrei/fabric/internal/gitexec"
)

const (
	maxDiscoverSkillsBytes = 2900
	maxRepairSkillsBytes   = 2200
	maxReviewRubricBytes   = 7600
)

// SkillActivation records one engineering skill activated automatically by
// Fabric's workflow stages or loaded explicitly by the agent.
type SkillActivation struct {
	Stage  string `json:"stage"` // discover, implement, repair, verify, review
	Skill  string `json:"skill"`
	Reason string `json:"reason"` // auto-stage, effect-matched, diff-matched, explicit-load
}

func skillHintsFromMetadata(skills []SkillMetadata) []contextcompiler.SkillHint {
	hints := make([]contextcompiler.SkillHint, 0, len(skills))
	for _, s := range skills {
		hints = append(hints, contextcompiler.SkillHint{
			Name:        s.Name,
			Description: s.Description,
		})
	}
	return hints
}

func recordSkillActivation(list *[]SkillActivation, stage, skill, reason string) {
	if list == nil || strings.TrimSpace(skill) == "" {
		return
	}
	for _, existing := range *list {
		if existing.Stage == stage && strings.EqualFold(existing.Skill, skill) {
			return
		}
	}
	*list = append(*list, SkillActivation{Stage: stage, Skill: skill, Reason: reason})
}

func selectDiscoverSkills(skills []SkillMetadata, recommended []string) (string, []SkillActivation) {
	if len(skills) == 0 {
		return "", nil
	}
	type pick struct {
		name   string
		reason string
	}
	var picks []pick
	seen := map[string]bool{}
	add := func(name, reason string) {
		if seen[name] || len(picks) >= 3 {
			return
		}
		if _, ok := loadSkillBody(skills, name); ok {
			seen[name] = true
			picks = append(picks, pick{name: name, reason: reason})
		}
	}

	add("brownfield-convention-matching", "auto-stage")
	for _, rec := range recommended {
		add(rec, "effect-matched")
	}
	add("modular-design-and-boundaries", "auto-stage")
	add("code-hygiene", "auto-stage")

	var b strings.Builder
	var acts []SkillActivation
	used := 0
	for _, p := range picks {
		raw, ok := loadSkillBody(skills, p.name)
		if !ok {
			continue
		}
		body := stripSkillFrontmatter(raw)
		if body == "" {
			continue
		}
		if len(body) > 950 {
			body = body[:950] + "\n... [load_skill `" + p.name + "` for full guide]"
		}
		if used+len(body) > maxDiscoverSkillsBytes && len(acts) > 0 {
			break
		}
		used += len(body)
		fmt.Fprintf(&b, "### Skill `%s` (%s)\n%s\n\n", p.name, p.reason, body)
		recordSkillActivation(&acts, "discover", p.name, p.reason)
	}
	if len(acts) == 0 {
		return "", nil
	}
	return fenceUntrustedRepoGuidance("active_workflow_skills", b.String()), acts
}

func buildRepairSkillGuidance(skills []SkillMetadata, attempt int) (string, []SkillActivation) {
	names := []string{"repair"}
	if attempt >= 2 {
		names = append(names, "systematic-debugging")
	}
	var b strings.Builder
	var acts []SkillActivation
	used := 0
	for _, nm := range names {
		raw, ok := loadSkillBody(skills, nm)
		if !ok {
			continue
		}
		body := stripSkillFrontmatter(raw)
		if body == "" {
			continue
		}
		if used+len(body) > maxRepairSkillsBytes && len(acts) > 0 {
			break
		}
		used += len(body)
		fmt.Fprintf(&b, "### Repair Workflow Skill `%s`\n%s\n\n", nm, body)
		recordSkillActivation(&acts, "repair", nm, "auto-stage")
	}
	if len(acts) == 0 {
		return "", nil
	}
	return fenceUntrustedRepoGuidance("repair_workflow_skills", b.String()), acts
}

func auditDiffHygiene(ctx context.Context, workDir, diff string) []string {
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	prodFiles, testFiles, debugPrintHit, todoHit := scanDiffStats(diff)
	var notes []string
	if debugPrintHit != "" {
		notes = append(notes, fmt.Sprintf("debug print call added in production file %s (prefer structured logger or remove)", debugPrintHit))
	}
	if todoHit != "" {
		notes = append(notes, fmt.Sprintf("unresolved TODO/FIXME marker added in %s", todoHit))
	}
	if prodFiles > 0 && testFiles == 0 {
		notes = append(notes, fmt.Sprintf("production source edited (%d file(s)) with zero test files modified; verify existing tests cover edge cases", prodFiles))
	}
	if workDir != "" {
		if out, err := gitexec.CommandContext(ctx, "-C", workDir, "diff", "--check").CombinedOutput(); err != nil && len(strings.TrimSpace(string(out))) > 0 {
			firstLine := strings.Split(strings.TrimSpace(string(out)), "\n")[0]
			notes = append(notes, "git diff --check flagged formatting/conflict issue: "+firstLine)
		}
	}
	return notes
}

func scanDiffStats(diff string) (prodFiles, testFiles int, debugPrintHit, todoHit string) {
	var curFile string
	for _, line := range strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "+++ ") {
			p := strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			if p == "/dev/null" {
				curFile = ""
				continue
			}
			curFile = p
			if isDiffTestFile(curFile) {
				testFiles++
			} else if isSourceDiffFile(curFile) {
				prodFiles++
			}
			continue
		}
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") || isDiffTestFile(curFile) || !isSourceDiffFile(curFile) {
			continue
		}
		added := line[1:]
		if debugPrintHit == "" && (strings.Contains(added, "fmt.Println(") || strings.Contains(added, "console.log(")) {
			debugPrintHit = curFile
		}
		if todoHit == "" && (strings.Contains(added, "TODO") || strings.Contains(added, "FIXME")) {
			todoHit = curFile
		}
	}
	return prodFiles, testFiles, debugPrintHit, todoHit
}

func composeReviewRubric(skills []SkillMetadata, diff string, hygieneNotes []string) (string, []SkillActivation) {
	type lens struct {
		name   string
		reason string
	}
	selected := []lens{
		{name: "code-review", reason: "auto-stage"},
		{name: "code-hygiene", reason: "auto-stage"},
	}

	lowerDiff := strings.ToLower(diff)
	if containsAny(lowerDiff, "auth", "webhook", "hmac", "token", "secret", "net/http", "exec.command", "sandbox", "safepath", ".github/workflows") {
		selected = append(selected,
			lens{name: "security-audit", reason: "diff-matched"},
			lens{name: "ci-and-supply-chain-hygiene", reason: "diff-matched"},
		)
	}
	if containsAny(lowerDiff, "sync.", "chan ", "context.", ".lock(", "newtimer", "newticker", "\tgo ") {
		selected = append(selected, lens{name: "concurrency-resource-safety", reason: "diff-matched"})
	}
	if containsAny(lowerDiff, "openapispec", `toml:"`, `json:"`, "config.go", "handlefunc(") {
		selected = append(selected, lens{name: "safe-migrations-and-contracts", reason: "diff-matched"})
	}

	var b strings.Builder
	if len(hygieneNotes) > 0 {
		b.WriteString("Deterministic pre-review hygiene signals (verify before citing):\n")
		for _, n := range hygieneNotes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		b.WriteByte('\n')
	}

	var acts []SkillActivation
	used := b.Len()
	for _, s := range selected {
		raw, ok := loadSkillBody(skills, s.name)
		if !ok {
			continue
		}
		body := stripSkillFrontmatter(raw)
		if body == "" {
			continue
		}
		if len(body) > 1350 {
			body = body[:1350] + "\n... [truncated for review rubric budget]"
		}
		if used+len(body) > maxReviewRubricBytes && len(acts) > 0 {
			break
		}
		used += len(body)
		fmt.Fprintf(&b, "## Lens `%s` (%s)\n%s\n\n", s.name, s.reason, body)
		recordSkillActivation(&acts, "review", s.name, s.reason)
	}
	return b.String(), acts
}

func stripSkillFrontmatter(raw string) string {
	norm := strings.ReplaceAll(strings.TrimSpace(raw), "\r\n", "\n")
	if strings.HasPrefix(norm, "---\n") {
		if idx := strings.Index(norm[4:], "\n---"); idx >= 0 {
			after := norm[4+idx+4:]
			return strings.TrimSpace(after)
		}
	}
	return norm
}

func isDiffTestFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, "_test.go") ||
		strings.Contains(lower, ".test.") ||
		strings.Contains(lower, ".spec.") ||
		strings.HasPrefix(lower, "test_") ||
		strings.Contains(lower, "/test_") ||
		strings.HasSuffix(lower, "test.java")
}

func isSourceDiffFile(path string) bool {
	if path == "" {
		return false
	}
	lower := strings.ToLower(path)
	for _, ext := range []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".cs"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
