package control

import (
	"errors"
	"harness.local/engorch/internal/ri"
)

// BindRIProducerImport attaches confirmed producer provenance to an import plan.
// Source inputs and semantic tool/version/policy remain explicit caller choices.
// It returns a new proposal, never authorizes or executes an import.
func BindRIProducerImport(path string, plan ri.ImportPlan) (ri.ImportPlan, error) {
	s, err := Inspect(path)
	if err != nil {
		return ri.ImportPlan{}, err
	}
	if s.RIProducer == nil || s.RIProducer.Outcome != "CONFIRMED" {
		return ri.ImportPlan{}, errors.New("confirmed producer required")
	}
	producer := s.RIProducer
	if plan.Repository != producer.Intent.Plan.Repository {
		return ri.ImportPlan{}, errors.New("producer/import repository mismatch")
	}
	id, err := producer.Intent.Intent.ID()
	if err != nil {
		return ri.ImportPlan{}, err
	}
	invocationID, err := producer.Intent.Plan.Invocation.ID()
	if err != nil {
		return ri.ImportPlan{}, err
	}
	artifact := producer.Observation.Result.Artifact
	plan.ProducerIntentID = id
	plan.Request.IndexPath = artifact.Path
	// Clone nested slices so preparing a proposal cannot mutate the caller's plan.
	plan.Request.Manifest.Producers = append([]ri.Producer(nil), plan.Request.Manifest.Producers...)
	found := false
	for n := range plan.Request.Manifest.Producers {
		p := &plan.Request.Manifest.Producers[n]
		if p.ID != plan.Request.Producer {
			continue
		}
		if found {
			return ri.ImportPlan{}, errors.New("duplicate import producer")
		}
		found = true
		p.ArtifactSHA256 = producer.Intent.Plan.Invocation.Executable.Hash
		inputs := make([]ri.Input, 0, len(p.Inputs)+2)
		for _, input := range p.Inputs {
			if input.Name != "scip:index" && input.Name != "producer:invocation" {
				inputs = append(inputs, input)
			}
		}
		p.Inputs = append(inputs, ri.Input{Name: "scip:index", SHA256: artifact.SHA256}, ri.Input{Name: "producer:invocation", SHA256: invocationID})
	}
	if !found {
		return ri.ImportPlan{}, errors.New("import producer absent")
	}
	if _, err := plan.ID(); err != nil {
		return ri.ImportPlan{}, err
	}
	return plan, validateProducerImport(s, plan)
}

func validateProducerImport(s Snapshot, plan ri.ImportPlan) error {
	if plan.ProducerIntentID == "" {
		return nil
	}
	if s.RIProducer == nil || s.RIProducer.Outcome != "CONFIRMED" {
		return errors.New("import producer is not confirmed")
	}
	producer := s.RIProducer
	id, err := producer.Intent.Intent.ID()
	if err != nil {
		return err
	}
	artifact := producer.Observation.Result.Artifact
	invocationID, err := producer.Intent.Plan.Invocation.ID()
	if err != nil {
		return err
	}
	if plan.ProducerIntentID != id || plan.Repository != producer.Intent.Plan.Repository || plan.Request.IndexPath != artifact.Path {
		return errors.New("producer/import provenance mismatch")
	}
	found := false
	for _, p := range plan.Request.Manifest.Producers {
		if p.ID != plan.Request.Producer {
			continue
		}
		if found || p.ArtifactSHA256 != producer.Intent.Plan.Invocation.Executable.Hash {
			return errors.New("producer executable binding mismatch")
		}
		found = true
		index, invocation := 0, 0
		for _, input := range p.Inputs {
			if input.Name == "scip:index" {
				index++
				if input.SHA256 != artifact.SHA256 {
					return errors.New("producer index hash mismatch")
				}
			}
			if input.Name == "producer:invocation" {
				invocation++
				if input.SHA256 != invocationID {
					return errors.New("producer invocation hash mismatch")
				}
			}
		}
		if index != 1 || invocation != 1 {
			return errors.New("missing or duplicated producer provenance")
		}
	}
	if !found {
		return errors.New("import producer absent")
	}
	return nil
}
