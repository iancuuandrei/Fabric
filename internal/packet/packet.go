package packet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/codegraph"
	"github.com/iancuuandrei/fabric/internal/gitexec"
	"github.com/iancuuandrei/fabric/internal/integrations"
)

const (
	defaultMaxPatchBytes = 25 << 20 // 25 MiB cap for patch extraction
)

var (
	validBranchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	repoApplyLock sync.Map // map[cleanRepoRoot]*sync.Mutex
)

// ReviewPacket is a structured, evidence-backed review bundle designed to
// eliminate human PR review fatigue on AI-generated changes.
type ReviewPacket struct {
	RunID            string                          `json:"run_id"`
	Status           string                          `json:"status"`
	Objective        string                          `json:"objective"`
	Model            string                          `json:"model"`
	Summary          string                          `json:"summary"`
	ChangedFiles     []string                        `json:"changed_files"`
	BlastRadius      []string                        `json:"blast_radius,omitempty"`
	DiffLinesAdded   int                             `json:"diff_lines_added"`
	DiffLinesRemoved int                             `json:"diff_lines_removed"`
	Verification     []agentloop.VerificationReceipt `json:"verification,omitempty"`
	MutationScore    *float64                        `json:"mutation_score,omitempty"`
	ReviewVerdict    string                          `json:"review_verdict,omitempty"`
	ReviewNotes      string                          `json:"review_notes,omitempty"`
	SkillsUsed       []agentloop.SkillActivation     `json:"skills_used,omitempty"`
	HygieneNotes     []string                        `json:"hygiene_notes,omitempty"`
	TokensTotal      int64                           `json:"tokens_total"`
	CachedTokens     int64                           `json:"cached_tokens"`
	MaskedBytesSaved int64                           `json:"masked_bytes_saved"`
	EstimatedUSD     float64                         `json:"estimated_usd"`
	DurationMS       int64                           `json:"duration_ms"`
}

// FromEpisode constructs a ReviewPacket from a completed agentloop Episode.
func FromEpisode(ep agentloop.Episode) ReviewPacket {
	pkt := ReviewPacket{
		RunID:            ep.RunID,
		Status:           ep.Status,
		Objective:        ep.Objective,
		Model:            ep.Model,
		Summary:          ep.Summary,
		ChangedFiles:     ep.ChangedFiles,
		DiffLinesAdded:   ep.DiffLinesAdded,
		DiffLinesRemoved: ep.DiffLinesRemoved,
		Verification:     ep.Verification,
		ReviewVerdict:    ep.ReviewVerdict,
		ReviewNotes:      ep.ReviewNotes,
		SkillsUsed:       ep.SkillsUsed,
		HygieneNotes:     ep.HygieneNotes,
		TokensTotal:      ep.Usage.TotalTokens(),
		CachedTokens:     ep.Usage.CachedTokens,
		MaskedBytesSaved: ep.MaskedBytesSaved,
		EstimatedUSD:     ep.CostEstimate.EstimatedUSD,
		DurationMS:       ep.DurationMS,
	}
	if ep.Mutation != nil && ep.Mutation.TotalMutants > 0 {
		s := ep.Mutation.Score
		pkt.MutationScore = &s
	}
	if ep.WorkDir != "" && len(ep.ChangedFiles) > 0 {
		if g, err := codegraph.Build(ep.WorkDir); err == nil {
			seen := map[string]bool{}
			for _, f := range ep.ChangedFiles {
				imp := g.Impact(f, 2)
				for _, caller := range imp.Callers {
					if caller.Path != "" && !seen[caller.Path] {
						seen[caller.Path] = true
						pkt.BlastRadius = append(pkt.BlastRadius, caller.Path)
						if len(pkt.BlastRadius) >= 10 {
							break
						}
					}
				}
				if len(pkt.BlastRadius) >= 10 {
					break
				}
			}
		}
	}
	return pkt
}

// Markdown renders the ReviewPacket as concise, scannable GitHub-flavored Markdown
// suitable for PR descriptions or terminal review.
func (p ReviewPacket) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Fabric Review Packet — `%s` (%s)\n\n", shortID(p.RunID), p.Status)
	fmt.Fprintf(&b, "**Objective:** %s\n\n", strings.TrimSpace(p.Objective))
	if p.Summary != "" {
		fmt.Fprintf(&b, "**Summary:** %s\n\n", strings.TrimSpace(p.Summary))
	}
	fmt.Fprintf(&b, "### Diff & Blast Radius\n- **Files changed (%d):** %s\n- **Churn:** `+%d / -%d` lines\n",
		len(p.ChangedFiles), strings.Join(p.ChangedFiles, ", "), p.DiffLinesAdded, p.DiffLinesRemoved)
	if len(p.BlastRadius) > 0 {
		fmt.Fprintf(&b, "- **Upstream callers (CodeGraph):** %s\n", strings.Join(p.BlastRadius, ", "))
	}
	b.WriteByte('\n')

	if len(p.Verification) > 0 {
		b.WriteString("### Verification Receipts\n| Check | Status | Exit | Duration |\n| --- | --- | ---: | ---: |\n")
		for _, v := range p.Verification {
			st := "PASS"
			if !v.Passed {
				st = "FAIL"
			}
			fmt.Fprintf(&b, "| `%s` | **%s** | %d | %dms |\n", v.Name, st, v.ExitCode, v.DurationMS)
		}
		b.WriteString("\n")
	}

	if p.MutationScore != nil {
		fmt.Fprintf(&b, "### Mutation Audit\n- **Diff-scoped kill score:** `%.1f%%`\n\n", *p.MutationScore*100)
	}
	if p.ReviewVerdict != "" {
		fmt.Fprintf(&b, "### Independent Review\n- **Verdict:** `%s`\n- **Notes:** %s\n\n", p.ReviewVerdict, strings.TrimSpace(p.ReviewNotes))
	}
	if len(p.SkillsUsed) > 0 || len(p.HygieneNotes) > 0 {
		b.WriteString("### Workflow Skills & Hygiene\n")
		if len(p.SkillsUsed) > 0 {
			items := make([]string, 0, len(p.SkillsUsed))
			for _, s := range p.SkillsUsed {
				items = append(items, fmt.Sprintf("`%s` (%s/%s)", s.Skill, s.Stage, s.Reason))
			}
			fmt.Fprintf(&b, "- **Activated skills:** %s\n", strings.Join(items, ", "))
		}
		for _, hn := range p.HygieneNotes {
			fmt.Fprintf(&b, "- **Hygiene check:** %s\n", hn)
		}
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "### Economics & Efficiency\n- **Model:** `%s`\n- **Tokens:** %d total (%d cached) · **Observation masking saved:** %d bytes\n- **Estimated Cost:** `$%.4f` · **Duration:** `%.1fs`\n",
		p.Model, p.TokensTotal, p.CachedTokens, p.MaskedBytesSaved, p.EstimatedUSD, float64(p.DurationMS)/1000.0)
	return b.String()
}

// ValidateBranchName rejects empty, flag-like, or invalid Git branch names.
func ValidateBranchName(name string) error {
	if !validBranchRe.MatchString(name) || name == "HEAD" ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") ||
		strings.Contains(name, "/.") {
		return fmt.Errorf("invalid branch name %q", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || strings.HasSuffix(seg, ".lock") {
			return fmt.Errorf("invalid branch name %q", name)
		}
	}
	return nil
}

// WorktreePatch computes a complete unified Git patch for worktreeDir against
// baseRef (default "HEAD") including tracked edits, staged changes, deletions,
// and newly created untracked non-ignored files, using an ephemeral temporary
// GIT_INDEX_FILE so the worktree's real Git index is never mutated.
func WorktreePatch(ctx context.Context, worktreeDir, baseRef string, binary bool, maxBytes int64) ([]byte, error) {
	if baseRef == "" {
		baseRef = "HEAD"
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxPatchBytes
	}
	var extra []string
	if binary {
		extra = append(extra, "--binary")
	}
	diffOut, err := gitexec.DiffWorktreeWithTempIndex(ctx, worktreeDir, baseRef, extra...)
	if err != nil {
		return nil, err
	}
	if int64(len(diffOut)) > maxBytes {
		return nil, fmt.Errorf("patch exceeds maximum size bound (%d > %d bytes)", len(diffOut), maxBytes)
	}
	return diffOut, nil
}

// ApplyDryRun verifies that worktreeDir's patch applies cleanly onto repoRoot
// without modifying any files or branches, returning `git apply --stat` output.
func ApplyDryRun(ctx context.Context, repoRoot, worktreeDir, newBranch string) (string, error) {
	if newBranch != "" {
		if err := ValidateBranchName(newBranch); err != nil {
			return "", err
		}
		if branchExists(ctx, repoRoot, newBranch) {
			return "", fmt.Errorf("branch %q already exists", newBranch)
		}
	}
	if err := ensureRepoClean(ctx, repoRoot); err != nil {
		return "", err
	}
	diffOut, err := WorktreePatch(ctx, worktreeDir, "HEAD", true, defaultMaxPatchBytes)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(diffOut)) == 0 {
		return "", errors.New("worktree has no diff to apply")
	}
	if err := rejectHazardousPatch(diffOut); err != nil {
		return "", err
	}
	check := gitexec.CommandContext(ctx, "-C", repoRoot, "apply", "--check")
	check.Stdin = bytes.NewReader(diffOut)
	if out, err := check.CombinedOutput(); err != nil {
		return "", fmt.Errorf("patch does not apply cleanly: %w: %s", err, strings.TrimSpace(string(out)))
	}
	stat := gitexec.CommandContext(ctx, "-C", repoRoot, "apply", "--stat")
	stat.Stdin = bytes.NewReader(diffOut)
	statOut, _ := stat.CombinedOutput()
	return strings.TrimSpace(string(statOut)), nil
}

// ApplyWorktreePatch safely validates and applies the complete diff (including
// new untracked files) from worktreeDir onto repoRoot. It verifies clean state,
// validates newBranch, dry-runs `git apply --check` BEFORE switching branches,
// and rolls back branch creation if `git apply` fails.
func ApplyWorktreePatch(ctx context.Context, repoRoot, worktreeDir, newBranch string) error {
	mu := repoMutexFor(repoRoot)
	mu.Lock()
	defer mu.Unlock()

	if newBranch != "" {
		if err := ValidateBranchName(newBranch); err != nil {
			return err
		}
		if branchExists(ctx, repoRoot, newBranch) {
			return fmt.Errorf("branch %q already exists", newBranch)
		}
	}
	if err := ensureRepoClean(ctx, repoRoot); err != nil {
		return err
	}
	diffOut, err := WorktreePatch(ctx, worktreeDir, "HEAD", true, defaultMaxPatchBytes)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(diffOut)) == 0 {
		return errors.New("worktree has no diff to apply")
	}
	if err := rejectHazardousPatch(diffOut); err != nil {
		return err
	}

	// Verify patch applies cleanly BEFORE touching branches.
	check := gitexec.CommandContext(ctx, "-C", repoRoot, "apply", "--check")
	check.Stdin = bytes.NewReader(diffOut)
	if out, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("patch does not apply cleanly: %w: %s", err, strings.TrimSpace(string(out)))
	}

	var origRef string
	if newBranch != "" {
		if refBytes, err := gitexec.CommandContext(ctx, "-C", repoRoot, "symbolic-ref", "--short", "-q", "HEAD").Output(); err == nil {
			origRef = strings.TrimSpace(string(refBytes))
		} else if shaBytes, err := gitexec.CommandContext(ctx, "-C", repoRoot, "rev-parse", "HEAD").Output(); err == nil {
			origRef = strings.TrimSpace(string(shaBytes))
		}
		if out, err := gitexec.CommandContext(ctx, "-C", repoRoot, "checkout", "-b", newBranch, "--").CombinedOutput(); err != nil {
			return fmt.Errorf("create branch %s: %w: %s", newBranch, err, strings.TrimSpace(string(out)))
		}
	}

	apply := gitexec.CommandContext(ctx, "-C", repoRoot, "apply")
	apply.Stdin = bytes.NewReader(diffOut)
	if out, err := apply.CombinedOutput(); err != nil {
		if newBranch != "" && origRef != "" {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer rollbackCancel()
			_ = gitexec.CommandContext(rollbackCtx, "-C", repoRoot, "checkout", "--force", origRef).Run()
			_ = gitexec.CommandContext(rollbackCtx, "-C", repoRoot, "branch", "-D", "--", newBranch).Run()
		}
		return fmt.Errorf("git apply failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ensureRepoClean(ctx context.Context, repoRoot string) error {
	statusOut, err := gitexec.CommandContext(ctx, "-C", repoRoot, "status", "--porcelain").CombinedOutput()
	if err != nil {
		return fmt.Errorf("check repo status: %w", err)
	}
	for _, rawLine := range strings.Split(string(statusOut), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if len(line) < 4 {
			continue
		}
		// Ignore Fabric's own untracked runtime metadata dirs (.harness/ and .fabric/)
		// so `fabric agent` followed by `fabric apply` succeeds cleanly.
		if strings.HasPrefix(line, "?? ") {
			p := filepath.ToSlash(strings.TrimSpace(line[3:]))
			if p == ".harness/" || strings.HasPrefix(p, ".harness/") || p == ".fabric/" || strings.HasPrefix(p, ".fabric/") {
				continue
			}
		}
		return errors.New("repository has uncommitted changes; commit or stash before fabric apply")
	}
	return nil
}

func branchExists(ctx context.Context, repoRoot, branch string) bool {
	return gitexec.CommandContext(ctx, "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

func rejectHazardousPatch(patch []byte) error {
	if bytes.Contains(patch, []byte("new file mode 120000")) {
		return errors.New("patch refused: symlink creation (mode 120000) is blocked by Fabric safety policy")
	}
	if bytes.Contains(patch, []byte("+++ b/.git/")) || bytes.Contains(patch, []byte("--- a/.git/")) {
		return errors.New("patch refused: .git/ metadata modification is blocked")
	}
	return nil
}

func repoMutexFor(repoRoot string) *sync.Mutex {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		abs = repoRoot
	}
	v, _ := repoApplyLock.LoadOrStore(filepath.Clean(abs), &sync.Mutex{})
	return v.(*sync.Mutex)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// NotificationFromEpisode converts a completed agentloop Episode and optional
// external ticket/PR links into a normalized integrations.Notification payload.
func NotificationFromEpisode(ep agentloop.Episode, ticketKey, ticketURL, prURL string) integrations.Notification {
	summary := strings.TrimSpace(ep.Summary)
	if summary == "" {
		summary = strings.TrimSpace(ep.Reason)
	}
	mut := ""
	if ep.Mutation != nil && ep.Mutation.TotalMutants > 0 {
		mut = fmt.Sprintf("%d/%d (%.0f%%)", ep.Mutation.Killed, ep.Mutation.TotalMutants, ep.Mutation.Score*100)
	}
	return integrations.Notification{
		Event:        integrations.EventNameForStatus(ep.Status),
		Status:       ep.Status,
		RunID:        ep.RunID,
		Objective:    ep.Objective,
		Summary:      summary,
		TicketKey:    ticketKey,
		TicketURL:    ticketURL,
		PRURL:        prURL,
		ChangedFiles: ep.ChangedFiles,
		DiffAdded:    ep.DiffLinesAdded,
		DiffRemoved:  ep.DiffLinesRemoved,
		CostUSD:      ep.CostEstimate.EstimatedUSD,
		MutationKill: mut,
	}
}
