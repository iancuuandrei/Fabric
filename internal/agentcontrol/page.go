package agentcontrol

import (
	"context"
	"errors"
	"time"

	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

// ActivityPage binds a bounded activity projection to the exact control
// journal prefix from which it was replayed.
type ActivityPage struct {
	Activities  []Activity `json:"activities"`
	JournalHead string     `json:"journal_head"`
}

// ActivitiesAfterWithHead returns a bounded per-agent activity page and the
// exact journal head observed by the same read and replay.
func (s *Service) ActivitiesAfterWithHead(agentID string, afterSequence, limit int) (ActivityPage, error) {
	if err := validateService(s); err != nil || safepath.RequireDigest(agentID) != nil || afterSequence < 0 || limit < 1 || limit > 256 {
		return ActivityPage{}, errors.Join(err, errors.New("invalid agent activity page"))
	}
	events, err := journal.Read(s.journalPath)
	if err != nil {
		return ActivityPage{}, err
	}
	state, err := Replay(events)
	if err != nil {
		return ActivityPage{}, err
	}
	if state.TreeID != s.treeID || len(events) == 0 || safepath.RequireDigest(events[len(events)-1].Hash) != nil {
		return ActivityPage{}, errors.New("agent control tree identity changed")
	}
	page := ActivityPage{Activities: make([]Activity, 0, limit), JournalHead: events[len(events)-1].Hash}
	for _, activity := range state.Activities {
		if activity.AgentID == agentID && activity.Sequence > afterSequence {
			page.Activities = append(page.Activities, activity)
			if len(page.Activities) == limit {
				break
			}
		}
	}
	return page, nil
}

// WaitWithHead returns once a later durable message or lifecycle observation
// exists. A deadline returns the last empty page and its observed journal head
// together with context.DeadlineExceeded; explicit cancellation returns only
// the cancellation error.
func (s *Service) WaitWithHead(ctx context.Context, agentID string, afterSequence, limit int) (ActivityPage, error) {
	if ctx == nil {
		return ActivityPage{}, errors.New("agent activity wait context required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return ActivityPage{}, errors.New("agent activity wait requires deadline")
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var last ActivityPage
	for {
		if err := s.reconcileTree(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && last.JournalHead != "" {
				return last, context.DeadlineExceeded
			}
			return ActivityPage{}, err
		}
		page, err := s.ActivitiesAfterWithHead(agentID, afterSequence, limit)
		if err != nil {
			return page, err
		}
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && last.JournalHead != "" {
				return last, err
			}
			return ActivityPage{}, err
		}
		if len(page.Activities) != 0 {
			return page, nil
		}
		last = page
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return last, context.DeadlineExceeded
			}
			return ActivityPage{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
