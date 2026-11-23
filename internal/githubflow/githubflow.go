package githubflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iancuuandrei/fabric/internal/gitexec"
)

// Issue describes a fetched GitHub issue used to seed a Fabric engineering run.
type Issue struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	URL    string   `json:"url"`
	Labels []string `json:"labels,omitempty"`
}

// PRInfo describes a fetched GitHub pull request and its unified diff.
type PRInfo struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	URL     string `json:"url"`
	BaseRef string `json:"baseRefName"`
	HeadRef string `json:"headRefName"`
	Diff    string `json:"diff,omitempty"`
}

// ValidateRef rejects empty or flag-like issue/PR references (`-` prefix or whitespace).
func ValidateRef(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	trimmed = strings.TrimPrefix(trimmed, "#")
	if trimmed == "" || strings.HasPrefix(trimmed, "-") || strings.ContainsAny(trimmed, " \t\r\n") {
		return "", fmt.Errorf("invalid GitHub reference %q", ref)
	}
	return trimmed, nil
}

func resolveGH() (string, error) {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return "", errors.New("GitHub CLI (`gh`) not found on PATH; install `gh` and run `gh auth login`")
	}
	if abs, absErr := filepath.Abs(ghPath); absErr == nil {
		return abs, nil
	}
	return ghPath, nil
}

// FetchIssue retrieves a GitHub issue via `gh issue view` using explicit argv.
func FetchIssue(ctx context.Context, repoRoot, ref string) (Issue, error) {
	cleanRef, err := ValidateRef(ref)
	if err != nil {
		return Issue{}, err
	}
	ghBin, err := resolveGH()
	if err != nil {
		return Issue{}, err
	}
	cmd := exec.CommandContext(ctx, ghBin, "issue", "view", cleanRef, "--json", "number,title,body,url,labels")
	if repoRoot != "" {
		cmd.Dir = repoRoot
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Issue{}, fmt.Errorf("gh issue view failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var raw struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"url"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return Issue{}, fmt.Errorf("decode gh issue output: %w", err)
	}
	labels := make([]string, 0, len(raw.Labels))
	for _, l := range raw.Labels {
		if l.Name != "" {
			labels = append(labels, l.Name)
		}
	}
	return Issue{
		Number: raw.Number,
		Title:  raw.Title,
		Body:   raw.Body,
		URL:    raw.URL,
		Labels: labels,
	}, nil
}

// BuildIssueObjective formats a concise engineering objective from an Issue.
func BuildIssueObjective(iss Issue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resolve GitHub Issue #%d: %s", iss.Number, strings.TrimSpace(iss.Title))
	if len(iss.Labels) > 0 {
		fmt.Fprintf(&b, "\nLabels: %s", strings.Join(iss.Labels, ", "))
	}
	if body := strings.TrimSpace(iss.Body); body != "" {
		if len(body) > 4096 {
			body = body[:4096] + "\n... [truncated]"
		}
		fmt.Fprintf(&b, "\n\nIssue Description:\n%s", body)
	}
	return b.String()
}

// FetchPR retrieves PR metadata and unified diff via `gh pr view` + `gh pr diff`.
func FetchPR(ctx context.Context, repoRoot, ref string) (PRInfo, error) {
	cleanRef, err := ValidateRef(ref)
	if err != nil {
		return PRInfo{}, err
	}
	ghBin, err := resolveGH()
	if err != nil {
		return PRInfo{}, err
	}
	metaCmd := exec.CommandContext(ctx, ghBin, "pr", "view", cleanRef, "--json", "number,title,body,url,baseRefName,headRefName")
	if repoRoot != "" {
		metaCmd.Dir = repoRoot
	}
	metaOut, err := metaCmd.CombinedOutput()
	if err != nil {
		return PRInfo{}, fmt.Errorf("gh pr view failed: %w: %s", err, strings.TrimSpace(string(metaOut)))
	}
	var pr PRInfo
	if err := json.Unmarshal(metaOut, &pr); err != nil {
		return PRInfo{}, fmt.Errorf("decode gh pr output: %w", err)
	}
	diffCmd := exec.CommandContext(ctx, ghBin, "pr", "diff", cleanRef)
	if repoRoot != "" {
		diffCmd.Dir = repoRoot
	}
	diffOut, err := diffCmd.CombinedOutput()
	if err != nil {
		return PRInfo{}, fmt.Errorf("gh pr diff failed: %w: %s", err, strings.TrimSpace(string(diffOut)))
	}
	pr.Diff = string(diffOut)
	return pr, nil
}

// OpenPR pushes branch to origin (without force) and opens a GitHub pull
// request with title and Markdown body, returning the created PR URL.
func OpenPR(ctx context.Context, repoRoot, branch, title, body string, draft bool) (string, error) {
	if _, err := ValidateRef(branch); err != nil {
		return "", err
	}
	if strings.TrimSpace(title) == "" {
		return "", errors.New("PR title is required")
	}
	ghBin, err := resolveGH()
	if err != nil {
		return "", err
	}
	pushCmd := gitexec.CommandContext(ctx, "-C", repoRoot, "push", "-u", "origin", branch)
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git push failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	args := []string{"pr", "create", "--head", branch, "--title", title, "--body", body}
	if draft {
		args = append(args, "--draft")
	}
	prCmd := exec.CommandContext(ctx, ghBin, args...)
	prCmd.Dir = repoRoot
	out, err := prCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CommentOnIssue posts a comment on a GitHub issue via `gh issue comment`.
func CommentOnIssue(ctx context.Context, repoRoot, ref, body string) error {
	cleanRef, err := ValidateRef(ref)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return errors.New("comment body cannot be empty")
	}
	ghBin, err := resolveGH()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, ghBin, "issue", "comment", cleanRef, "--body", body)
	if repoRoot != "" {
		cmd.Dir = repoRoot
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh issue comment failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TransitionIssue closes, reopens, or adds a workflow label to a GitHub issue via `gh`.
func TransitionIssue(ctx context.Context, repoRoot, ref, targetState string) error {
	cleanRef, err := ValidateRef(ref)
	if err != nil {
		return err
	}
	state := strings.TrimSpace(targetState)
	if state == "" {
		return errors.New("target state cannot be empty")
	}
	ghBin, err := resolveGH()
	if err != nil {
		return err
	}
	var args []string
	switch strings.ToLower(state) {
	case "closed", "close", "done":
		args = []string{"issue", "close", cleanRef}
	case "open", "reopen":
		args = []string{"issue", "reopen", cleanRef}
	default:
		if strings.HasPrefix(state, "-") || strings.ContainsAny(state, "\r\n\x00") {
			return fmt.Errorf("invalid GitHub label state %q", targetState)
		}
		args = []string{"issue", "edit", cleanRef, "--add-label", state}
	}
	cmd := exec.CommandContext(ctx, ghBin, args...)
	if repoRoot != "" {
		cmd.Dir = repoRoot
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh issue transition failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
