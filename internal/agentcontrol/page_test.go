package agentcontrol

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/journal"
)

func TestActivityPageRetainsExactObservedPrefix(t *testing.T) {
	service, _, root, child := controlFixture(t)
	if _, err := service.List(context.Background(), "", 10); err != nil {
		t.Fatal(err)
	}
	old, err := service.ActivitiesAfterWithHead(child.AgentID, 0, 10)
	if err != nil || len(old.Activities) != 1 || old.JournalHead == "" {
		t.Fatal("initial page", old, err)
	}
	if _, err := service.Send(context.Background(), MessageRequest{FromAgentID: root.AgentID, ToAgentID: child.AgentID, Nonce: "later-page", Body: "later durable activity"}); err != nil {
		t.Fatal(err)
	}
	current, err := service.ActivitiesAfterWithHead(child.AgentID, 0, 10)
	if err != nil || len(current.Activities) != 2 || current.JournalHead == old.JournalHead {
		t.Fatal("current page", current, err)
	}
	if len(old.Activities) != 1 || old.Activities[0].Kind != "status" {
		t.Fatal("later append mutated old page", old)
	}
	events, err := journal.Read(service.journalPath)
	if err != nil || len(events) == 0 || current.JournalHead != events[len(events)-1].Hash {
		t.Fatal("page head differs from exact journal prefix", current.JournalHead, err)
	}
}

func TestWaitWithHeadReturnsTimedOutObservedEmptyPage(t *testing.T) {
	service, _, _, child := controlFixture(t)
	if _, err := service.List(context.Background(), "", 10); err != nil {
		t.Fatal(err)
	}
	current, err := service.ActivitiesAfterWithHead(child.AgentID, 0, 10)
	if err != nil || len(current.Activities) == 0 {
		t.Fatal(current, err)
	}
	after := current.Activities[len(current.Activities)-1].Sequence
	// Allow the Windows race build to complete at least one journal read;
	// expiry before that read intentionally has no page evidence.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	page, err := service.WaitWithHead(ctx, child.AgentID, after, 10)
	if !errors.Is(err, context.DeadlineExceeded) || page.Activities == nil || len(page.Activities) != 0 || page.JournalHead == "" {
		t.Fatal("timeout page", page, err)
	}
	legacyCtx, legacyCancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer legacyCancel()
	legacy, legacyErr := service.Wait(legacyCtx, child.AgentID, after, 10)
	if !errors.Is(legacyErr, context.DeadlineExceeded) || legacy != nil {
		t.Fatal("legacy timeout behavior changed", legacy, legacyErr)
	}
}

func TestActivityPageRejectsInvalidIdentityAndCorruption(t *testing.T) {
	service, _, _, child := controlFixture(t)
	if _, err := service.ActivitiesAfterWithHead("invalid", 0, 1); err == nil {
		t.Fatal("invalid agent identity accepted")
	}
	if _, err := service.ActivitiesAfterWithHead(child.AgentID, -1, 1); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	foreign := *service
	foreign.treeID = strings.Repeat("f", 64)
	if _, err := foreign.ActivitiesAfterWithHead(child.AgentID, 0, 1); err == nil {
		t.Fatal("foreign tree binding accepted")
	}
	if _, err := journal.Append(service.journalPath, "corrupt.activity", struct {
		Value string `json:"value"`
	}{Value: "invalid"}, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ActivitiesAfterWithHead(child.AgentID, 0, 1); err == nil {
		t.Fatal("unknown journal event accepted")
	}
}

func TestWaitWithHeadCancellationReturnsNoPage(t *testing.T) {
	service, _, _, child := controlFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	page, err := service.WaitWithHead(ctx, child.AgentID, 0, 1)
	if !errors.Is(err, context.Canceled) || page.Activities != nil || page.JournalHead != "" {
		t.Fatal("explicit cancellation returned observed page", page, err)
	}
}

func TestWaitWithHeadPreExpiredDeadlineDoesNotFabricateHead(t *testing.T) {
	service, _, _, child := controlFixture(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	page, err := service.WaitWithHead(ctx, child.AgentID, 0, 1)
	if !errors.Is(err, context.DeadlineExceeded) || page.Activities != nil || page.JournalHead != "" {
		t.Fatal("pre-expired deadline fabricated observed page", page, err)
	}
}

func TestActivityPageRejectsMissingBoundJournal(t *testing.T) {
	service, treePath, _, child := controlFixture(t)
	missing := *service
	missing.journalPath = filepath.Join(filepath.Dir(treePath), "missing-agent-control.db")
	if _, err := missing.ActivitiesAfterWithHead(child.AgentID, 0, 1); err == nil {
		t.Fatal("missing bound journal accepted")
	}
}
