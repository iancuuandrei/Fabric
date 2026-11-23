package control

import (
	"errors"
	"fmt"

	"github.com/iancuuandrei/fabric/internal/safepath"
)

// autonomousProgress observes already validated history. A journal head is
// progress evidence, never permission to dispatch or change a candidate.
type autonomousProgress struct {
	head        string
	sequence    int
	windowStart string
	transitions int
}

func autonomousTransitionBudget(s Snapshot) int {
	tasks := 1 + s.Creation.Config.MaxExplorationRecords()
	repairs := 0
	replans := 0
	corrections := 0
	if s.Graph != nil {
		tasks = len(s.Graph.Graph.Tasks)
	}
	if s.Creation.Execution != nil {
		repairs = s.Creation.Execution.MaxRepairs
		replans = s.Creation.Execution.MaxScopeReplans
		if s.Creation.Execution.SemanticCorrectionVersion == 1 {
			// Two separately admitted corrections per task role, plus planner
			// and final reviewer. This is an observation window, not authority.
			corrections = 2 * (tasks + 2)
		}
	}
	// Allow role admission/completion, candidate application and acceptance
	// transitions for every admitted task and up to four nodes per repair.
	return 16 + 12*(tasks+4*repairs+4*replans+corrections)
}

func autonomousProgressMarker(s Snapshot) (string, error) {
	candidate := ""
	if s.Candidate != nil {
		var err error
		candidate, err = s.Candidate.ID()
		if err != nil {
			return "", errors.Join(ErrAutonomousUnsafe, err)
		}
	}
	graph := ""
	evidence := 0
	if s.Graph != nil {
		graph, evidence = s.Graph.Digest, len(s.Graph.Evidence)
	}
	return fmt.Sprintf("%s/%s/%s/%s/%d/%d/%d", s.State, s.PlanID, candidate, graph, evidence, len(s.Explorations), s.RepairAttempts), nil
}

func (p *autonomousProgress) observe(s Snapshot) error {
	if s.ControllerSequence < 1 || safepath.RequireDigest(s.ControllerHead) != nil {
		return errors.Join(ErrAutonomousUnsafe, errors.New("progress requires validated controller history"))
	}
	if p.head != "" {
		if s.ControllerSequence == p.sequence && s.ControllerHead == p.head {
			return ErrAutonomousNoProgress
		}
		if s.ControllerSequence <= p.sequence || s.ControllerHead == p.head {
			return errors.Join(ErrAutonomousUnsafe, errors.New("controller history moved backwards or changed identity"))
		}
	}
	marker, err := autonomousProgressMarker(s)
	if err != nil {
		return err
	}
	if p.head == "" {
		p.windowStart = marker
	}
	p.head, p.sequence = s.ControllerHead, s.ControllerSequence
	p.transitions++
	if p.transitions >= autonomousTransitionBudget(s) {
		if marker == p.windowStart {
			return errors.Join(ErrAutonomousNoProgress, errors.New("admitted transition window produced no engineering progress"))
		}
		// Valid engineering progress renews the window. There is no global
		// 64/256-step cutoff; existing task/effect/repair budgets still govern.
		p.transitions, p.windowStart = 0, marker
	}
	return nil
}
