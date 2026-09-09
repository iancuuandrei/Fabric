package ri

import (
	"bytes"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
	"os"
)

// LexicalCompletion is written after source/manifest/index file synchronization.
// It is only a recovery candidate; later observation must recheck actual artifacts.
type LexicalCompletion struct {
	PlanID  string       `json:"plan_id"`
	BuildID string       `json:"build_id"`
	Build   LexicalBuild `json:"build"`
}

// ReadLexicalCompletion validates a controller-owned completion record against
// the attempted plan. It performs no write and does not itself confirm an effect.
func ReadLexicalCompletion(plan LexicalPlan) (LexicalCompletion, error) {
	id, err := plan.ID()
	if err != nil {
		return LexicalCompletion{}, err
	}
	if err := safepath.Directory(plan.StageRoot); err != nil {
		return LexicalCompletion{}, err
	}
	root, err := os.OpenRoot(plan.StageRoot)
	if err != nil {
		return LexicalCompletion{}, err
	}
	defer root.Close()
	var bytes bytes.Buffer
	_, _, _, exists, err := safepath.CopyRegular(root, "completion.json", canonical.MaxBytes, &bytes)
	if err != nil {
		return LexicalCompletion{}, err
	}
	if !exists {
		return LexicalCompletion{}, errors.New("lexical completion missing")
	}
	var record LexicalCompletion
	if err := canonical.Decode(bytes.Bytes(), &record); err != nil {
		return LexicalCompletion{}, err
	}
	buildID, err := record.Build.ID()
	if err != nil {
		return LexicalCompletion{}, err
	}
	count := 0
	for _, shard := range record.Build.Shards {
		if shard.Files > plan.BatchFiles {
			return LexicalCompletion{}, errors.New("lexical completion shard exceeds plan")
		}
		count += shard.Files
	}
	if record.PlanID != id || record.BuildID != buildID || record.Build.ManifestID != plan.ManifestID || count != plan.Files {
		return LexicalCompletion{}, errors.New("lexical completion differs from attempted plan")
	}
	return record, nil
}
