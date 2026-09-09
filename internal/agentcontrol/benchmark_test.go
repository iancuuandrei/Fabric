package agentcontrol

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/agenttree"
)

// BenchmarkMailbox measures durable control-plane work, not model latency or
// runtime message consumption. Setup is excluded; journal replay is included.
func BenchmarkMailbox(b *testing.B) {
	for _, history := range []int{1, 32, 128} {
		b.Run(fmt.Sprintf("ReadPage/history=%d", history), func(b *testing.B) {
			service, request := benchmarkMailbox(b, history)
			after := history - 16
			if after < 0 {
				after = 0
			}
			want := history - after
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				messages, err := service.MessagesAfter(request.ToAgentID, after, 16)
				if err != nil || len(messages) != want {
					b.Fatalf("read page: count=%d error=%v", len(messages), err)
				}
				for _, message := range messages {
					if message.Body != request.Body {
						b.Fatal("message body changed")
					}
				}
			}
		})
	}
	b.Run("Send/fresh-journals", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			service, request := benchmarkMailbox(b, 0)
			request.Nonce = "measured-send"
			b.StartTimer()
			record, err := service.Send(context.Background(), request)
			if err != nil || record.Body != request.Body || record.Message.Wake {
				b.Fatalf("send: %v", err)
			}
		}
	})
}

// BenchmarkActivityPageWithHead includes disk reading, integrity validation and
// replay. The seeded message history is fixed during each measured run.
func BenchmarkActivityPageWithHead(b *testing.B) {
	for _, history := range []int{1, 32, 128} {
		b.Run(fmt.Sprintf("history=%d", history), func(b *testing.B) {
			service, request := benchmarkMailbox(b, history)
			after := max(0, history-16)
			baseline, err := service.ActivitiesAfterWithHead(request.ToAgentID, after, 16)
			if err != nil || len(baseline.Activities) != history-after || baseline.JournalHead == "" {
				b.Fatal("invalid activity benchmark baseline", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				page, err := service.ActivitiesAfterWithHead(request.ToAgentID, after, 16)
				if err != nil || page.JournalHead != baseline.JournalHead || len(page.Activities) != len(baseline.Activities) {
					b.Fatal("activity benchmark page changed", err)
				}
				for index, activity := range page.Activities {
					if activity != baseline.Activities[index] {
						b.Fatal("activity benchmark identity changed")
					}
				}
			}
		})
	}
}

func benchmarkMailbox(b *testing.B, history int) (*Service, MessageRequest) {
	b.Helper()
	directory := b.TempDir()
	treePath := filepath.Join(directory, "tree.db")
	treeID := strings.Repeat("a", 64)
	root, err := agenttree.Create(treePath, treeID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: strings.Repeat("b", 64), ContextSHA256: strings.Repeat("c", 64)})
	if err != nil {
		b.Fatal(err)
	}
	reservation, err := agenttree.ReserveChild(treePath, treeID, agenttree.NodeSpec{ParentAgentID: root.AgentID, Name: "explorer", Role: "explorer", Authority: agenttree.AuthorityReadOnly, InvocationID: strings.Repeat("d", 64), ContextSHA256: strings.Repeat("e", 64)})
	if err != nil {
		b.Fatal(err)
	}
	child, err := agenttree.CommitChild(treePath, reservation.ReservationID)
	if err != nil {
		b.Fatal(err)
	}
	service, err := Bind(treePath, filepath.Join(directory, "control.db"))
	if err != nil {
		b.Fatal(err)
	}
	request := MessageRequest{FromAgentID: root.AgentID, ToAgentID: child.AgentID, Body: strings.Repeat("x", 1024)}
	for index := range history {
		request.Nonce = fmt.Sprintf("seed-%d", index)
		if _, err := service.Send(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
	return service, request
}
