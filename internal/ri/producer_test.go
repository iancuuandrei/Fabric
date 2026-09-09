package ri

import (
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/repository"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProducerEffectBindsCommandAndOutput(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	plan, err := PrepareProducer(identity, root, config.Check{Name: "index", Argv: []string{executable}, TimeoutSeconds: 10}, filepath.Join(root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	intent, err := plan.Intent(strings.Repeat("c", 64), strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	approval := effects.Authorization{IntentID: id, Actor: "operator"}
	for _, mutate := range []func(*ProducerPlan){
		func(p *ProducerPlan) { p.OutputPath = filepath.Join(root, "other.scip") },
		func(p *ProducerPlan) { p.Invocation.Check.TimeoutSeconds = 20 },
		func(p *ProducerPlan) { p.Invocation.Check.Argv = append([]string{executable}, "changed") },
	} {
		altered := plan
		mutate(&altered)
		changed, err := altered.Intent(intent.RunID, intent.PlanID)
		if err != nil {
			t.Fatal(err)
		}
		if approval.Validate(changed) == nil {
			t.Fatal("producer substitution retained authorization")
		}
	}
	if outcome, err := effects.Outcome(intent, nil); err != nil || outcome != "UNKNOWN" {
		t.Fatal(outcome, err)
	}
}
