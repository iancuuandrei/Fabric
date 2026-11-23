package control

import (
	"errors"
	"math/big"
	"sort"
	"strings"

	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// taskContextSelectorRRFCoverageV1 is the single experimental selector mode.
// It requires Context bounded-v1; empty preserves exact legacy behavior.
const taskContextSelectorRRFCoverageV1 = "rrf-coverage-v1"

// taskContextSelectorMode returns the immutable selector bound to this run.
func taskContextSelectorMode(s Snapshot) string {
	if s.Creation.Execution == nil {
		return ""
	}
	return s.Creation.Execution.ContextSelector
}

func validateTaskContextSelectorBinding(s Snapshot, manifest taskcontext.Manifest) error {
	want := taskContextSelectorMode(s)
	if manifest.Selector != want {
		return errors.New("task context selector substitution")
	}
	if err := taskcontext.ValidateSelector(manifest.Selector); err != nil {
		return err
	}
	return nil
}

// prioritizeTaskPathsRRF ranks already captured FileState names with equal
// unweighted RRF over independent ordinal sources: writer/changed paths,
// combined exploration/evidence paths, candidate lexical paths and objective
// path-token matches. Absent/no-signal rankers are skipped. Ties break by
// path for determinism. It performs no file reads.
func prioritizeTaskPathsRRF(states []worktree.FileState, explorationPaths, writerPaths, lexicalPaths []string, query string) []worktree.FileState {
	writer := map[string]bool{}
	for _, p := range writerPaths {
		writer[p] = true
	}
	exploration := map[string]bool{}
	for _, p := range explorationPaths {
		exploration[p] = true
	}
	lexical := map[string]bool{}
	for _, p := range lexicalPaths {
		lexical[p] = true
	}
	terms := taskQueryTerms(query)
	rankSet := func(set map[string]bool) map[string]int {
		matched := []string{}
		for _, st := range states {
			if set[st.Path] {
				matched = append(matched, st.Path)
			}
		}
		if len(matched) == 0 {
			return nil
		}
		sort.Strings(matched)
		out := make(map[string]int, len(matched))
		for i, p := range matched {
			out[p] = i + 1
		}
		return out
	}
	rWriter := rankSet(writer)
	rExploration := rankSet(exploration)
	rLexical := rankSet(lexical)
	type tokenHit struct {
		path  string
		count int
	}
	hits := []tokenHit{}
	for _, st := range states {
		lower := strings.ToLower(st.Path)
		n := 0
		for _, term := range terms {
			if strings.Contains(lower, term) {
				n++
			}
		}
		if n > 0 {
			hits = append(hits, tokenHit{st.Path, n})
		}
	}
	var rPath map[string]int
	if len(hits) > 0 {
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].count != hits[j].count {
				return hits[i].count > hits[j].count
			}
			return hits[i].path < hits[j].path
		})
		rPath = make(map[string]int, len(hits))
		for i, h := range hits {
			rPath[h.path] = i + 1
		}
	}
	rankings := []map[string]int{}
	for _, r := range []map[string]int{rWriter, rExploration, rLexical, rPath} {
		if len(r) > 0 {
			rankings = append(rankings, r)
		}
	}
	if len(rankings) == 0 {
		out := append([]worktree.FileState(nil), states...)
		sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
		return out
	}
	type scored struct {
		state worktree.FileState
		score *big.Rat
	}
	scoredList := make([]scored, 0, len(states))
	for _, st := range states {
		sum := new(big.Rat)
		for _, r := range rankings {
			if rank, ok := r[st.Path]; ok {
				term := new(big.Rat).SetFrac(big.NewInt(1), big.NewInt(int64(60+rank)))
				sum.Add(sum, term)
			}
		}
		scoredList = append(scoredList, scored{state: st, score: sum})
	}
	sort.Slice(scoredList, func(i, j int) bool {
		c := scoredList[i].score.Cmp(scoredList[j].score)
		if c != 0 {
			return c > 0
		}
		return scoredList[i].state.Path < scoredList[j].state.Path
	})
	out := make([]worktree.FileState, 0, len(scoredList))
	for _, sc := range scoredList {
		out = append(out, sc.state)
	}
	return out
}
