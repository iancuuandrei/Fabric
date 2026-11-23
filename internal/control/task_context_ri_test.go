package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/gitlocal"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

// fixtureTaskContextLexicalSearcher supplies explicitly synthetic typed
// responses to exercise controller binding and bounds. Production uses
// ri.Client, which invokes and validates the pinned Rust reader.
type fixtureTaskContextLexicalSearcher struct {
	calls     int
	candidate string
	overlayID string
	manifest  ri.LexicalManifest
	queryIDs  []string
	paths     []string
	err       error
}

// TestTaskContextLexicalSearchThroughPinnedRust runs the same bounded evidence
// path used by task-context admission against an explicitly selected Rust RI
// executable. Unlike the typed fixture tests above, this proves the real local
// subprocess protocol and overlay search; it does not qualify hosted models.
func TestTaskContextLexicalSearchThroughPinnedRust(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("set ENGORCH_RI_BINARY to the validated Rust RI executable")
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	client := ri.Client{Executable: executable, ExecutableHash: hex.EncodeToString(digest[:])}

	root := t.TempDir()
	source := ri.Source{RepositoryID: strings.Repeat("a", 64), ObjectFormat: "sha1", Commit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40)}
	lexFile := func(path, body string) ri.LexicalFile {
		t.Helper()
		sha := sha256.Sum256([]byte(body))
		blob, err := gitlocal.ObjectID("sha1", "blob", []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return ri.LexicalFile{Path: path, Blob: blob, SHA256: hex.EncodeToString(sha[:]), Bytes: int64(len(body))}
	}
	base := ri.LexicalManifest{Version: 1, Source: source, Files: []ri.LexicalFile{lexFile("src/bytes.go", "func Old() {}\n")}}
	baseStage, err := ri.StageLexicalOverlay(context.Background(), ri.LexicalManifest{Version: 1, Source: source, Files: []ri.LexicalFile{}}, strings.Repeat("d", 64), base, []string{}, map[string][]byte{"src/bytes.go": []byte("func Old() {}\n")}, filepath.Join(root, "base-stage"))
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, "base-index")
	build, err := client.BuildLexical(context.Background(), base, baseStage.ManifestPath, baseStage.SourceRoot, index, 1<<20, 10)
	if err != nil {
		t.Fatal(err)
	}
	baseRef := ri.LexicalRef{ManifestPath: baseStage.ManifestPath, SourceRoot: baseStage.SourceRoot, IndexPath: index, Manifest: base, Build: build}
	changedBody := "func ParseBytes(parse_bytes string) {}\n"
	changed := ri.LexicalManifest{Version: 1, Source: source, Files: []ri.LexicalFile{lexFile("src/bytes.go", changedBody)}}
	overlay, err := ri.StageLexicalOverlay(context.Background(), base, strings.Repeat("e", 64), changed, []string{}, map[string][]byte{"src/bytes.go": []byte(changedBody)}, filepath.Join(root, "candidate-overlay"))
	if err != nil {
		t.Fatal(err)
	}
	overlayID, err := overlay.ID(base)
	if err != nil {
		t.Fatal(err)
	}
	binding := codexruntime.LexicalBinding{Base: baseRef, Overlay: &overlay, Executable: executable, ExecutableSHA256: client.ExecutableHash}
	searches, paths, err := searchTaskContextLexical(context.Background(), binding, "Change ParseBytes and parse_bytes", client)
	if err != nil {
		t.Fatal(err)
	}
	if len(searches) != 2 || len(paths) != 1 || paths[0] != "src/bytes.go" {
		t.Fatalf("unexpected actual Rust task-context evidence: searches=%+v paths=%v", searches, paths)
	}
	for _, search := range searches {
		if err := validateTaskContextLexicalSearch(binding, search, true); err != nil {
			t.Fatalf("actual Rust search result failed task-context admission: %v", err)
		}
		if search.Result.ManifestID != overlayID || len(search.Result.Matches) != 1 {
			t.Fatalf("actual Rust result did not retain exact overlay scope: %+v", search.Result)
		}
	}
}

func (f *fixtureTaskContextLexicalSearcher) SearchLexicalOverlay(_ context.Context, base ri.LexicalRef, overlay ri.LexicalOverlayRef, query ri.LexicalQuery, limit int, after *ri.LexicalCursor) (ri.LexicalResult, error) {
	f.calls++
	if after != nil || limit != taskContextLexicalPageSize || overlay.Candidate != f.candidate {
		return ri.LexicalResult{}, errors.New("fixture received an unexpected lexical binding or page")
	}
	if f.err != nil {
		return ri.LexicalResult{}, f.err
	}
	queryID, err := query.ID()
	if err != nil {
		return ri.LexicalResult{}, err
	}
	f.queryIDs = append(f.queryIDs, queryID)
	f.paths = append(f.paths, query.Path)
	return ri.LexicalResult{
		QueryID:        queryID,
		ManifestID:     f.overlayID,
		FullScan:       true,
		CandidateFiles: len(f.manifest.Files),
		SearchedFiles:  len(f.manifest.Files),
		Matches: []ri.LexicalHit{{
			Path:  f.manifest.Files[0].Path,
			Blob:  f.manifest.Files[0].Blob,
			Range: [2]int64{1, 5},
		}},
	}, nil
}

func TestTaskContextLexicalProcessFailureDegradesAndReplaysBoundedSource(t *testing.T) {
	s, binding, original, _ := taskContextLexicalFixture(t)
	searcher := &fixtureTaskContextLexicalSearcher{
		candidate: binding.Overlay.Candidate, overlayID: original.LexicalOverlayID,
		err: fmt.Errorf("local process exited: %w", ri.ErrProcessUnavailable),
	}
	searches, paths, err := searchTaskContextLexical(context.Background(), binding, "Change ParseBytes and parse_bytes", searcher)
	if err == nil || !ri.IsProcessUnavailableOnly(err) || searches != nil || paths != nil {
		t.Fatalf("expected bounded optional RI failure without fabricated results: searches=%v paths=%v err=%v", searches, paths, err)
	}

	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		t.Fatal(err)
	}
	query := "Change ParseBytes and parse_bytes"
	record := TaskContextRecord{
		Version: taskContextRIUnavailableVersion, Role: "writer", SourceID: sourceID,
		CandidateID: original.CandidateID, Query: query, QueryHash: taskContextQueryHash(query), QueryLen: len(query),
		LexicalBuildID: original.LexicalBuildID, LexicalOverlayID: original.LexicalOverlayID,
		LexicalUnavailableReason: taskContextLexicalUnavailableReason,
		Unavailable:              "no_eligible_files",
		Manifest: taskcontext.Manifest{
			Version: 1, Scope: taskcontext.Scope{SourceID: sourceID, CandidateID: original.CandidateID},
			InputHash: strings.Repeat("3", 64), Selected: []taskcontext.SelectedFile{}, Omissions: []taskcontext.Omission{},
		},
	}
	if err := validateTaskContextLexicalEvidence(s, record); err != nil {
		t.Fatalf("candidate-bound fallback record rejected: %v", err)
	}
	raw, err := canonical.Bytes(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayTaskContext(&s, journal.Event{Payload: raw}); err != nil || len(s.TaskContexts) != 1 {
		t.Fatalf("fallback evidence did not replay: contexts=%d err=%v", len(s.TaskContexts), err)
	}

	stale := record
	stale.LexicalOverlayID = strings.Repeat("9", 64)
	if err := validateTaskContextLexicalEvidence(s, stale); err == nil {
		t.Fatal("fallback admitted a stale candidate overlay")
	}
	forged := record
	forged.LexicalUnavailableReason = "transport failed: provider output omitted"
	if err := validateTaskContextLexicalEvidence(s, forged); err == nil {
		t.Fatal("fallback admitted arbitrary diagnostic text")
	}
}

func taskContextLexicalFixture(t *testing.T) (Snapshot, codexruntime.LexicalBinding, TaskContextRecord, *fixtureTaskContextLexicalSearcher) {
	t.Helper()
	path, s := boundedTaskFixture(t, map[string]string{"src/bytes.go": "ParseBytes source\n"})
	_ = path
	source, err := ri.FromRepository(s.Creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	baseFile := ri.LexicalFile{Path: "src/bytes.go", Blob: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64), Bytes: 18}
	base := ri.LexicalManifest{Version: 1, Source: source, Files: []ri.LexicalFile{baseFile}}
	baseID, err := base.ID()
	if err != nil {
		t.Fatal(err)
	}
	changedFile := ri.LexicalFile{Path: baseFile.Path, Blob: strings.Repeat("c", 40), SHA256: strings.Repeat("d", 64), Bytes: 18}
	changed := ri.LexicalManifest{Version: 1, Source: source, Files: []ri.LexicalFile{changedFile}}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	overlay := ri.LexicalOverlayRef{Candidate: candidateID, ManifestPath: filepath.Join(root, "manifest.jsonl"), SourceRoot: filepath.Join(root, "sources"), Manifest: changed, Deleted: []string{}}
	overlayID, err := overlay.ID(base)
	if err != nil {
		t.Fatal(err)
	}
	build := ri.LexicalBuild{ManifestID: baseID, Shards: []ri.LexicalShard{{Directory: "shard-000000", Files: 1, Hashes: [3]string{strings.Repeat("e", 64), strings.Repeat("f", 64), strings.Repeat("1", 64)}}}}
	plan := ri.LexicalPlan{Version: 1, Repository: s.Creation.Repository, ManifestID: baseID, Files: 1, Executable: filepath.Join(root, "ri.exe"), ExecutableSHA256: strings.Repeat("2", 64), StageRoot: filepath.Join(root, "base"), BatchBytes: 1 << 20, BatchFiles: 10}
	s.RILexical = &RILexicalState{Intent: RILexicalIntent{Plan: plan}, Observation: &RILexicalObservation{Build: &build}, Outcome: "CONFIRMED"}
	overlayPlan := LexicalOverlayPlan{CandidateID: candidateID, BaseID: baseID, ChangedID: mustManifestID(t, changed), OverlayID: overlayID, StageRoot: filepath.Join(root, "candidate")}
	s.RILexicalOverlay = &RILexicalOverlayState{Intent: RILexicalOverlayIntent{Prepared: PreparedLexicalOverlay{Plan: overlayPlan}}, Outcome: "CONFIRMED"}

	binding := codexruntime.LexicalBinding{Base: ri.LexicalRef{ManifestPath: filepath.Join(root, "base", "manifest.jsonl"), SourceRoot: filepath.Join(root, "base", "sources"), IndexPath: filepath.Join(root, "base", "index"), Manifest: base, Build: build}, Overlay: &overlay, Executable: filepath.Join(root, "ri.exe"), ExecutableSHA256: strings.Repeat("2", 64)}
	queries := taskContextLexicalQueries("Change ParseBytes and parse_bytes overflow handling")
	if len(queries) == 0 {
		t.Fatal("fixture task did not yield concrete lexical terms")
	}
	searcher := &fixtureTaskContextLexicalSearcher{candidate: candidateID, overlayID: overlayID, manifest: ri.LexicalManifest{Version: 1, Source: source, Files: []ri.LexicalFile{changedFile}}}
	searches, _, err := searchTaskContextLexical(context.Background(), binding, "Change ParseBytes and parse_bytes overflow handling", searcher)
	if err != nil {
		t.Fatal(err)
	}
	if len(searches) == 0 {
		t.Fatal("fixture search produced no evidence")
	}
	buildID, err := build.ID()
	if err != nil {
		t.Fatal(err)
	}
	record := TaskContextRecord{
		Version:          taskContextVersion,
		CandidateID:      candidateID,
		LexicalBuildID:   buildID,
		LexicalOverlayID: overlayID,
		LexicalSearches:  searches,
		Manifest:         taskcontext.Manifest{Version: 1, InputHash: strings.Repeat("3", 64), Selected: []taskcontext.SelectedFile{}, Omissions: []taskcontext.Omission{}},
	}
	record.LexicalSearchesID, err = taskContextLexicalSearchesID(searches)
	if err != nil {
		t.Fatal(err)
	}
	return s, binding, record, searcher
}

func mustManifestID(t *testing.T, manifest ri.LexicalManifest) string {
	t.Helper()
	id, err := manifest.ID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTaskContextLexicalSearchUsesBoundOverlayAndPersistsExactResult(t *testing.T) {
	s, binding, record, searcher := taskContextLexicalFixture(t)
	lex, err := roleLexical(s)
	if err != nil || lex == nil || lex.Scope != "candidate" || lex.OverlayID != record.LexicalOverlayID {
		t.Fatal("fixture did not bind candidate lexical overlay", err)
	}
	if searcher.calls != len(record.LexicalSearches) || searcher.calls > taskContextLexicalMaxQueries {
		t.Fatalf("unexpected bounded search calls: %d", searcher.calls)
	}
	for _, search := range record.LexicalSearches {
		if err := validateTaskContextLexicalSearch(binding, search, true); err != nil {
			t.Fatal("typed fixture result failed exact binding validation", err)
		}
	}
	if err := validateTaskContextLexicalEvidence(s, record); err != nil {
		t.Fatal("exact candidate overlay evidence rejected", err)
	}
	raw, err := canonical.Bytes(record)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip TaskContextRecord
	if err := canonical.Decode(raw, &roundTrip); err != nil || roundTrip.LexicalSearchesID != record.LexicalSearchesID || len(roundTrip.LexicalSearches) != len(record.LexicalSearches) || roundTrip.LexicalSearches[0].Result.Matches[0] != record.LexicalSearches[0].Result.Matches[0] {
		t.Fatal("persisted task context lost exact lexical result", err)
	}
}

func TestTaskContextLexicalEvidenceRejectsStaleBindingAndOversizedPage(t *testing.T) {
	s, binding, record, _ := taskContextLexicalFixture(t)
	stale := record
	stale.LexicalOverlayID = strings.Repeat("9", 64)
	stale.LexicalSearchesID, _ = taskContextLexicalSearchesID(stale.LexicalSearches)
	if err := validateTaskContextLexicalEvidence(s, stale); err == nil {
		t.Fatal("stale overlay binding accepted")
	}

	corrupt := record.LexicalSearches[0]
	corrupt.Result.ManifestID = strings.Repeat("8", 64)
	if err := validateTaskContextLexicalSearch(binding, corrupt, true); err == nil {
		t.Fatal("result from a different overlay accepted")
	}

	oversized := record
	oversized.LexicalSearches = append([]TaskContextLexicalSearch(nil), record.LexicalSearches...)
	for len(oversized.LexicalSearches[0].Result.Matches) <= taskContextLexicalPageSize {
		oversized.LexicalSearches[0].Result.Matches = append(oversized.LexicalSearches[0].Result.Matches, oversized.LexicalSearches[0].Result.Matches[0])
	}
	oversized.LexicalSearchesID, _ = taskContextLexicalSearchesID(oversized.LexicalSearches)
	if err := validateTaskContextLexicalEvidence(s, oversized); err == nil {
		t.Fatal("oversized lexical page accepted")
	}
}
