package draftpr

import (
	"errors"
	"strconv"
)

// BranchObservation contains the repository, ref and commit reported by the host.
type BranchObservation struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	Commit     string `json:"commit"`
}

// Observation is a projected hosted response, not proof of HTTP provenance.
type Observation struct {
	Number int               `json:"number"`
	URL    string            `json:"url"`
	State  string            `json:"state"`
	Draft  bool              `json:"draft"`
	Title  string            `json:"title"`
	Body   string            `json:"body"`
	Head   BranchObservation `json:"head"`
	Base   BranchObservation `json:"base"`
}

// Validate rejects partial, changed or unrelated hosted state. The transport must
// separately establish authenticated endpoint provenance and complete decoding.
func (o Observation) Validate(plan Plan) error {
	request, err := plan.Request()
	if err != nil {
		return err
	}
	if o.Number <= 0 || o.URL != "https://github.com/"+plan.Repository+"/pull/"+strconv.Itoa(o.Number) || o.State != "open" || !o.Draft || o.Title != request.Title || o.Body != request.Body {
		return errors.New("draft observation identity, state or text mismatch")
	}
	if o.Head != (BranchObservation{Repository: plan.Repository, Ref: request.Head, Commit: plan.Push.Candidate.Head}) || o.Base != (BranchObservation{Repository: plan.Repository, Ref: request.Base, Commit: plan.BaseCommit}) {
		return errors.New("draft observation branch or commit mismatch")
	}
	return nil
}
