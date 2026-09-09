package codexruntime

import (
	"bytes"
	"context"
	"harness.local/engorch/internal/canonical"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/ri"
)

func TestLexicalBindingRejectsSourceCandidateAndBuildSubstitution(t *testing.T) {
	a := sourceAdapter(t)
	source, err := ri.FromRepository(*a.Source)
	if err != nil {
		t.Fatal(err)
	}
	manifest := ri.LexicalManifest{Version: 1, Source: source, Files: []ri.LexicalFile{}}
	id, err := manifest.ID()
	if err != nil {
		t.Fatal(err)
	}
	b := LexicalBinding{Base: ri.LexicalRef{ManifestPath: filepath.Join(a.Directory, "manifest.jsonl"), SourceRoot: filepath.Join(a.Directory, "sources"), IndexPath: filepath.Join(a.Directory, "index"), Manifest: manifest, Build: ri.LexicalBuild{ManifestID: id, Shards: []ri.LexicalShard{}}}, Executable: filepath.Join(a.Directory, "ri.exe"), ExecutableSHA256: strings.Repeat("b", 64)}
	baseID, err := b.ID(*a.Source, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical.Bytes(b)
	if err != nil {
		t.Fatal(err)
	}
	state := State{Intent: &Intent{}, Source: a.Source}
	compactRecord, err := b.Record(*a.Source, "")
	if err != nil {
		t.Fatal(err)
	}
	compactBytes, err := canonical.Bytes(compactRecord)
	if err != nil {
		t.Fatal(err)
	}
	compactState := State{Intent: &Intent{}, Source: a.Source}
	if err := compactState.toolEvent("runtime.lexical-record", compactBytes); err != nil {
		t.Fatal(err)
	}
	if err := lexicalContinuation(compactState, &b); err != nil {
		t.Fatal(err)
	}
	if err := compactState.toolEvent("runtime.lexical", encoded); err == nil {
		t.Fatal("mixed lexical rebinding admitted")
	}
	if err := compactState.toolEvent("runtime.lexical-record", compactBytes); err == nil {
		t.Fatal("duplicate compact binding admitted")
	}
	changedCompact := b
	changedCompact.ExecutableSHA256 = strings.Repeat("0", 64)
	if err := lexicalContinuation(compactState, &changedCompact); err == nil {
		t.Fatal("compact continuation reader substitution admitted")
	}
	if err := state.toolEvent("runtime.lexical", encoded); err != nil {
		t.Fatal(err)
	}
	if err := state.toolEvent("runtime.lexical", encoded); err == nil {
		t.Fatal("lexical rebinding accepted")
	}
	if err := lexicalContinuation(state, &b); err != nil {
		t.Fatal(err)
	}
	if err := lexicalContinuation(state, nil); err == nil {
		t.Fatal("lexical binding removed on resume")
	}
	other := b
	other.ExecutableSHA256 = strings.Repeat("e", 64)
	if err := lexicalContinuation(state, &other); err == nil {
		t.Fatal("reader changed on resume")
	}
	candidate := strings.Repeat("c", 64)
	b.Overlay = &ri.LexicalOverlayRef{Candidate: candidate, ManifestPath: filepath.Join(a.Directory, "overlay.jsonl"), SourceRoot: filepath.Join(a.Directory, "changed"), Manifest: manifest, Deleted: []string{}}
	overlayID, err := b.ID(*a.Source, candidate)
	if err != nil || overlayID == baseID {
		t.Fatal("overlay not bound", err)
	}
	record, err := b.Record(*a.Source, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Validate(*a.Source, candidate); err != nil {
		t.Fatal("offline validation failed", err)
	}
	if _, err := record.Hydrate(context.Background(), *a.Source, candidate); err == nil {
		t.Fatal("missing manifests hydrated")
	}
	var records bytes.Buffer
	if err := manifest.WriteRecords(&records); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{b.Base.ManifestPath, b.Overlay.ManifestPath} {
		if err := os.WriteFile(path, records.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	hydrated, err := record.Hydrate(context.Background(), *a.Source, candidate)
	if err != nil {
		t.Fatal(err)
	}
	hydratedID, err := hydrated.ID(*a.Source, candidate)
	if err != nil || hydratedID != overlayID {
		t.Fatal("compact roundtrip identity changed", err)
	}
	if err := record.Validate(*a.Source, strings.Repeat("0", 64)); err == nil {
		t.Fatal("record candidate substitution admitted")
	}
	if err := b.Validate(*a.Source, ""); err == nil {
		t.Fatal("overlay admitted without candidate")
	}
	a.Lexical = &b
	if err := a.validateLexicalAdmission(); err == nil {
		t.Fatal("adapter admitted overlay without candidate")
	}
	baseOnly := b
	baseOnly.Overlay = nil
	a.Lexical = &baseOnly
	if err := a.validateLexicalAdmission(); err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(*a.Source, strings.Repeat("d", 64)); err == nil {
		t.Fatal("foreign candidate admitted")
	}
	foreign := b
	foreign.Base.Manifest.Source.Commit = strings.Repeat("f", len(source.Commit))
	if err := foreign.Validate(*a.Source, candidate); err == nil {
		t.Fatal("foreign source admitted")
	}
	foreign = b
	foreign.Base.Build.ManifestID = strings.Repeat("e", 64)
	if err := foreign.Validate(*a.Source, candidate); err == nil {
		t.Fatal("foreign build admitted")
	}
	foreign = b
	foreign.Executable = "relative.exe"
	if err := foreign.Validate(*a.Source, candidate); err == nil {
		t.Fatal("relative executable admitted")
	}
}
