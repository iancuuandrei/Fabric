package memory

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/gitexec"
)

const (
	maxRuleChars   = 500
	maxLessonsFile = 250
	maxPromptBytes = 2560
)

// Lesson captures one durable, verified repository invariant or convention.
type Lesson struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope,omitempty"`
	Rule      string    `json:"rule"`
	Evidence  string    `json:"evidence,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func lessonsPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".harness", "memory", "lessons.jsonl")
}

// Add validates, assigns an ID and commit stamp, and appends a Lesson to
// <repoRoot>/.harness/memory/lessons.jsonl.
func Add(repoRoot string, l Lesson) (Lesson, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return Lesson{}, errors.New("repository root is required")
	}
	l.Rule = strings.TrimSpace(l.Rule)
	l.Scope = strings.TrimSpace(l.Scope)
	l.Evidence = strings.TrimSpace(l.Evidence)
	if l.Rule == "" {
		return Lesson{}, errors.New("lesson rule cannot be empty")
	}
	if len(l.Rule) > maxRuleChars {
		return Lesson{}, fmt.Errorf("lesson rule exceeds %d characters", maxRuleChars)
	}
	if l.ID == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		l.ID = hex.EncodeToString(b[:])
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now().UTC()
	}
	if l.Commit == "" {
		if out, err := gitexec.CommandContext(context.Background(), "-C", repoRoot, "rev-parse", "--short", "HEAD").Output(); err == nil {
			l.Commit = strings.TrimSpace(string(out))
		}
	}

	existing, err := List(repoRoot)
	if err != nil {
		return Lesson{}, err
	}
	existing = append(existing, l)
	if len(existing) > maxLessonsFile {
		existing = existing[len(existing)-maxLessonsFile:]
	}
	if err := writeAll(repoRoot, existing); err != nil {
		return Lesson{}, err
	}
	return l, nil
}

// List returns all recorded project lessons sorted oldest-to-newest. If no
// lessons file exists yet, it returns an empty slice and nil error.
func List(repoRoot string) ([]Lesson, error) {
	p := lessonsPath(repoRoot)
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return []Lesson{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var lessons []Lesson
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 16<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var l Lesson
		if err := json.Unmarshal([]byte(line), &l); err == nil && l.Rule != "" {
			lessons = append(lessons, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return lessons, nil
}

// Delete removes one lesson matching id (exact or unambiguous prefix).
func Delete(repoRoot, id string) error {
	q := strings.TrimSpace(id)
	if q == "" {
		return errors.New("lesson id is required")
	}
	lessons, err := List(repoRoot)
	if err != nil {
		return err
	}
	var matchedIdx []int
	for i, l := range lessons {
		if l.ID == q {
			matchedIdx = []int{i}
			break
		}
		if strings.HasPrefix(strings.ToLower(l.ID), strings.ToLower(q)) {
			matchedIdx = append(matchedIdx, i)
		}
	}
	if len(matchedIdx) == 0 {
		return fmt.Errorf("lesson %q not found", id)
	}
	if len(matchedIdx) > 1 {
		return fmt.Errorf("lesson prefix %q is ambiguous (%d matches)", id, len(matchedIdx))
	}
	drop := matchedIdx[0]
	updated := append([]Lesson(nil), lessons[:drop]...)
	updated = append(updated, lessons[drop+1:]...)
	return writeAll(repoRoot, updated)
}

// Relevant selects up to limit lessons scored by scope/keyword overlap against
// objective, falling back to the most recent lessons within the prompt cap.
func Relevant(repoRoot, objective string, limit int) []Lesson {
	if limit <= 0 {
		limit = 5
	}
	lessons, err := List(repoRoot)
	if err != nil || len(lessons) == 0 {
		return nil
	}
	tokens := tokenize(objective)
	type scored struct {
		lesson Lesson
		score  int
		order  int
	}
	items := make([]scored, 0, len(lessons))
	for i, l := range lessons {
		sc := 0
		scopeLower := strings.ToLower(l.Scope)
		ruleLower := strings.ToLower(l.Rule)
		for tok := range tokens {
			if strings.Contains(scopeLower, tok) {
				sc += 3
			}
			if strings.Contains(ruleLower, tok) {
				sc++
			}
		}
		items = append(items, scored{lesson: l, score: sc, order: i})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].order > items[j].order
	})
	out := make([]Lesson, 0, min(limit, len(items)))
	usedBytes := 0
	for _, it := range items {
		if len(out) >= limit {
			break
		}
		entrySize := len(it.lesson.Rule) + len(it.lesson.Scope) + 24
		if usedBytes+entrySize > maxPromptBytes {
			break
		}
		usedBytes += entrySize
		out = append(out, it.lesson)
	}
	return out
}

// FormatForPrompt renders selected lessons concisely for prompt injection.
func FormatForPrompt(lessons []Lesson) string {
	if len(lessons) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Verified project lessons:\n")
	for _, l := range lessons {
		if l.Scope != "" {
			fmt.Fprintf(&b, "- [%s] (%s) %s\n", l.ID, l.Scope, l.Rule)
		} else {
			fmt.Fprintf(&b, "- [%s] %s\n", l.ID, l.Rule)
		}
	}
	return b.String()
}

func tokenize(s string) map[string]struct{} {
	out := map[string]struct{}{}
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-')
	})
	for _, f := range fields {
		if len(f) >= 3 {
			out[f] = struct{}{}
		}
	}
	return out
}

func writeAll(repoRoot string, lessons []Lesson) error {
	p := lessonsPath(repoRoot)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	for _, l := range lessons {
		line, err := json.Marshal(l)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(p, []byte(b.String()), 0o600)
}
