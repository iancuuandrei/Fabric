package ri

import (
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/repository"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportEffectBindsRuntimeInputsAndDestination(t *testing.T) {
	root := t.TempDir()
	identity := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	source, err := FromRepository(identity)
	if err != nil {
		t.Fatal(err)
	}
	plan := ImportPlan{Version: 1, Executable: filepath.Join(root, "ri.exe"), ExecutableSHA256: strings.Repeat("c", 64), Repository: identity, OutputPath: filepath.Join(root, "stage"), Request: ImportRequest{IndexPath: filepath.Join(root, "index"), Source: source, Manifest: Manifest{Format: 1, Source: source, Producers: []Producer{}}, Producer: "p", Policy: "strict", Sources: map[string]string{}}}
	intent, err := plan.Intent(strings.Repeat("d", 64), strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	approval := effects.Authorization{IntentID: id, Actor: "operator"}
	if err := approval.Validate(intent); err != nil {
		t.Fatal(err)
	}
	if outcome, err := effects.Outcome(intent, nil); err != nil || outcome != "UNKNOWN" {
		t.Fatal(outcome, err)
	}
	for _, mutate := range []func(*ImportPlan){
		func(p *ImportPlan) { p.MaterializeSources = !p.MaterializeSources },
		func(p *ImportPlan) { p.OutputPath = filepath.Join(root, "other") },
		func(p *ImportPlan) { p.ExecutableSHA256 = strings.Repeat("f", 64) },
		func(p *ImportPlan) { p.Request.Policy = "scip_go027" },
		func(p *ImportPlan) { p.Request.IndexPath = filepath.Join(root, "other-index") },
	} {
		altered := plan
		mutate(&altered)
		other, err := altered.Intent(intent.RunID, intent.PlanID)
		if err != nil {
			t.Fatal(err)
		}
		if approval.Validate(other) == nil {
			t.Fatal("substituted import retained approval")
		}
	}
	plan.Request.Source.Commit = strings.Repeat("f", 40)
	if _, err := plan.ID(); err == nil {
		t.Fatal("foreign source accepted")
	}
}
