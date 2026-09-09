package gitpush

import (
	"bytes"
	"errors"
	"strings"
)

// RefObservation preserves an exact remote branch advertisement. Missing is not
// proof that a previously attempted push never occurred or permission to retry.
type RefObservation struct {
	TargetRef string  `json:"target_ref"`
	Commit    *string `json:"commit"`
}

// ParseAdvertisement validates successful ls-remote --refs output for one exact
// requested branch. The caller must separately establish command success and
// destination identity; partial output from a failed command is inadmissible.
func ParseAdvertisement(plan Plan, output []byte) (RefObservation, error) {
	if _, err := plan.ID(); err != nil {
		return RefObservation{}, err
	}
	if len(output) == 0 {
		return RefObservation{TargetRef: plan.TargetRef}, nil
	}
	if len(output) > 2048 || output[len(output)-1] != '\n' || bytes.Count(output, []byte{'\n'}) != 1 {
		return RefObservation{}, errors.New("remote advertisement is oversized, partial or ambiguous")
	}
	line := string(output[:len(output)-1])
	commit, ref, ok := strings.Cut(line, "\t")
	if !ok || ref != plan.TargetRef || len(commit) != len(plan.Candidate.Head) || strings.Trim(commit, "0123456789abcdef") != "" || strings.Trim(commit, "0") == "" {
		return RefObservation{}, errors.New("remote advertisement ref or object identity mismatch")
	}
	return RefObservation{TargetRef: ref, Commit: &commit}, nil
}
