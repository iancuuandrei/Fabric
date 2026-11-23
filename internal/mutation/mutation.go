package mutation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/gitexec"
	"github.com/iancuuandrei/fabric/internal/sandbox"
)

// Mutant describes one single-line diff-scoped code mutation.
type Mutant struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Operator string `json:"operator"`
	Original string `json:"original"`
	Mutated  string `json:"mutated"`
	Status   string `json:"status"` // KILLED, SURVIVED, TIMEOUT
}

// Report summarizes a diff-scoped mutation audit.
type Report struct {
	Status       string   `json:"status,omitempty"` // OK, BASELINE_FAILED
	Error        string   `json:"error,omitempty"`
	TotalMutants int      `json:"total_mutants"`
	Killed       int      `json:"killed"`
	Survived     int      `json:"survived"`
	Score        float64  `json:"score"`
	Mutants      []Mutant `json:"mutants"`
}

// Config controls bounded diff-scoped mutation execution.
type Config struct {
	WorkDir           string
	TestArgv          []string
	Sandbox           sandbox.Config
	MaxMutants        int
	PerMutantLimit    time.Duration
	SkipBaselineCheck bool
}

type rule struct {
	name string
	from string
	to   string
}

var mutationRules = []rule{
	{"eq_to_neq", " == ", " != "},
	{"neq_to_eq", " != ", " == "},
	{"lte_to_lt", " <= ", " < "},
	{"gte_to_gt", " >= ", " > "},
	{"lt_to_lte", " < ", " <= "},
	{"gt_to_gte", " > ", " >= "},
	{"and_to_or", " && ", " || "},
	{"or_to_and", " || ", " && "},
	{"true_to_false", " true", " false"},
	{"false_to_true", " false", " true"},
}

// ChangedLine identifies one modified or added 1-based line in a source file.
type ChangedLine struct {
	Path string
	Line int
}

// ParseUnifiedDiffLines extracts added/modified production file paths and 1-based
// line numbers from a `git diff -U0` output, skipping test and generated files.
func ParseUnifiedDiffLines(diffText string) []ChangedLine {
	var out []ChangedLine
	curFile := ""
	for _, raw := range strings.Split(diffText, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "+++ b/") {
			curFile = strings.TrimPrefix(line, "+++ b/")
			continue
		}
		if strings.HasPrefix(line, "@@ ") && curFile != "" {
			if isNonProductionFile(curFile) {
				continue
			}
			plusIdx := strings.Index(line, "+")
			if plusIdx < 0 {
				continue
			}
			rest := line[plusIdx+1:]
			spaceIdx := strings.IndexByte(rest, ' ')
			if spaceIdx > 0 {
				rest = rest[:spaceIdx]
			}
			startStr, countStr, hasComma := strings.Cut(rest, ",")
			start, err := strconv.Atoi(startStr)
			if err != nil || start <= 0 {
				continue
			}
			count := 1
			if hasComma {
				c, err := strconv.Atoi(countStr)
				if err != nil {
					continue
				}
				count = c
			}
			for i := 0; i < count; i++ {
				out = append(out, ChangedLine{Path: curFile, Line: start + i})
			}
		}
	}
	return out
}

func isNonProductionFile(relPath string) bool {
	p := filepath.ToSlash(strings.ToLower(relPath))
	base := path.Base(p)
	if strings.Contains(p, "/testdata/") || strings.Contains(p, "/__tests__/") ||
		strings.Contains(p, "/__mocks__/") || strings.HasPrefix(p, "tests/") ||
		strings.Contains(p, "/tests/") || strings.Contains(p, "/fixtures/") {
		return true
	}
	for _, suffix := range []string{
		"_test.go", ".test.ts", ".test.tsx", ".spec.ts", ".spec.tsx",
		".test.js", ".test.jsx", ".spec.js", ".spec.jsx", "_test.py",
		".pb.go", "_gen.go", ".generated.ts", "_mock.go", ".d.ts",
	} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py")
}

// GenerateMutants builds up to maxMutants single-line mutations on changedLines.
func GenerateMutants(workDir string, changedLines []ChangedLine, maxMutants int) ([]Mutant, error) {
	if maxMutants <= 0 {
		maxMutants = 12
	}
	fileCache := map[string][]string{}
	var mutants []Mutant

	for _, cl := range changedLines {
		if len(mutants) >= maxMutants {
			break
		}
		if isNonProductionFile(cl.Path) {
			continue
		}
		lines, ok := fileCache[cl.Path]
		if !ok {
			b, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(cl.Path)))
			if err != nil {
				continue
			}
			lines = strings.Split(string(b), "\n")
			fileCache[cl.Path] = lines
		}
		if cl.Line < 1 || cl.Line > len(lines) {
			continue
		}
		orig := lines[cl.Line-1]
		trimmed := strings.TrimSpace(orig)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		for _, r := range mutationRules {
			if strings.Contains(orig, r.from) {
				mutated := strings.Replace(orig, r.from, r.to, 1)
				mutants = append(mutants, Mutant{
					ID:       fmt.Sprintf("m%02d", len(mutants)+1),
					Path:     cl.Path,
					Line:     cl.Line,
					Operator: r.name,
					Original: strings.TrimSpace(orig),
					Mutated:  strings.TrimSpace(mutated),
				})
				break
			}
		}
	}
	return mutants, nil
}

// AuditDiff computes the diff in cfg.WorkDir (including newly created untracked
// files via an ephemeral GIT_INDEX_FILE), verifies baseline tests, mutates
// changed production lines, and guarantees exact restoration of every file.
func AuditDiff(ctx context.Context, cfg Config) (Report, error) {
	if cfg.WorkDir == "" || len(cfg.TestArgv) == 0 {
		return Report{}, errors.New("mutation audit requires work_dir and test_argv")
	}
	diffText, err := readWorktreeDiffU0(ctx, cfg.WorkDir)
	if err != nil {
		return Report{}, err
	}
	changed := ParseUnifiedDiffLines(diffText)
	return AuditChangedLines(ctx, cfg, changed)
}

func readWorktreeDiffU0(ctx context.Context, workDir string) (string, error) {
	if diffBytes, err := gitexec.DiffWorktreeWithTempIndex(ctx, workDir, "HEAD", "-U0"); err == nil {
		return string(diffBytes), nil
	}
	// Fallback for repos without HEAD commit yet.
	cmd := gitexec.CommandContext(ctx, "-C", workDir, "diff", "-U0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff for mutation audit failed: %w", err)
	}
	return string(out), nil
}

// AuditChangedLines verifies baseline tests, runs mutation testing over
// changedLines, and unconditionally restores each file after its test execution.
func AuditChangedLines(ctx context.Context, cfg Config, changed []ChangedLine) (Report, error) {
	mutants, err := GenerateMutants(cfg.WorkDir, changed, cfg.MaxMutants)
	if err != nil {
		return Report{}, err
	}
	if len(mutants) == 0 {
		return Report{Status: "OK", Score: 1.0}, nil
	}

	limit := cfg.PerMutantLimit
	if !cfg.SkipBaselineCheck {
		start := time.Now()
		baseTimeout := limit
		if baseTimeout <= 0 {
			baseTimeout = 90 * time.Second
		}
		baseRes, baseErr := sandbox.Run(ctx, cfg.Sandbox, cfg.WorkDir, cfg.TestArgv, baseTimeout)
		if baseErr != nil || !baseRes.Passed {
			msg := "baseline tests failed before mutation audit"
			if baseErr != nil {
				msg += ": " + baseErr.Error()
			}
			return Report{Status: "BASELINE_FAILED", Error: msg}, nil
		}
		if limit <= 0 {
			cal := 3 * time.Since(start)
			if cal < 15*time.Second {
				cal = 15 * time.Second
			}
			if cal > 90*time.Second {
				cal = 90 * time.Second
			}
			limit = cal
		}
	}
	if limit <= 0 {
		limit = 45 * time.Second
	}

	rep := Report{Status: "OK", TotalMutants: len(mutants)}
	for _, m := range mutants {
		if ctx.Err() != nil {
			break
		}
		fullPath := filepath.Join(cfg.WorkDir, filepath.FromSlash(m.Path))
		info, statErr := os.Stat(fullPath)
		mode := os.FileMode(0o600)
		if statErr == nil {
			mode = info.Mode().Perm()
		}
		origBytes, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		lines := strings.Split(string(origBytes), "\n")
		if m.Line < 1 || m.Line > len(lines) {
			continue
		}

		// Apply single-line mutation and guarantee restoration via defer.
		status := func() string {
			defer func() { _ = os.WriteFile(fullPath, origBytes, mode) }()
			mutatedLine := lines[m.Line-1]
			for _, r := range mutationRules {
				if r.name == m.Operator {
					mutatedLine = strings.Replace(mutatedLine, r.from, r.to, 1)
					break
				}
			}
			lines[m.Line-1] = mutatedLine
			if err := os.WriteFile(fullPath, []byte(strings.Join(lines, "\n")), mode); err != nil {
				return "SURVIVED"
			}
			res, runErr := sandbox.Run(ctx, cfg.Sandbox, cfg.WorkDir, cfg.TestArgv, limit)
			if runErr != nil || !res.Passed {
				return "KILLED"
			}
			return "SURVIVED"
		}()

		m.Status = status
		if status == "KILLED" {
			rep.Killed++
		} else {
			rep.Survived++
		}
		rep.Mutants = append(rep.Mutants, m)
	}
	if rep.TotalMutants > 0 {
		rep.Score = float64(rep.Killed) / float64(rep.TotalMutants)
	}
	return rep, nil
}
