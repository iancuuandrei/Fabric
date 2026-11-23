package control

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/repository"
)

// BenchmarkControllerHistory measures production controller inspection and
// append validation against valid SQLite histories. The append validator is
// control.Append's full Replay and path-binding check, not a length-only stub.
func BenchmarkControllerHistory(b *testing.B) {
	fixtures := []struct {
		name           string
		events         int
		objectiveBytes int
	}{
		{name: "lightweight/events=16", events: 16, objectiveBytes: 32},
		{name: "lightweight/events=64", events: 64, objectiveBytes: 32},
		{name: "objective=16KiB/events=64", events: 64, objectiveBytes: 16 << 10},
	}
	for _, fixture := range fixtures {
		b.Run("InspectOnce/"+fixture.name, func(b *testing.B) {
			path, _, _ := seedPerformanceHistory(b, fixture.events, fixture.objectiveBytes)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := Inspect(path); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("InspectTwice/"+fixture.name, func(b *testing.B) {
			path, _, _ := seedPerformanceHistory(b, fixture.events, fixture.objectiveBytes)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := Inspect(path); err != nil {
					b.Fatal(err)
				}
				if _, err := Inspect(path); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run("Append/"+fixture.name, func(b *testing.B) {
			_, seedDB, runID := seedPerformanceHistory(b, fixture.events, fixture.objectiveBytes)
			appendRoot := b.TempDir()
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				path := filepath.Join(appendRoot, fmt.Sprintf("append-%d.db", n))
				if err := os.WriteFile(path, seedDB, 0600); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				req := LifecycleRequest{Version: 1, RunID: runID, Action: "pause", Actor: "benchmark", Nonce: fmt.Sprintf("timed-%d", n)}
				if err := Append(path, "run.pause-requested", req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func seedPerformanceHistory(b *testing.B, events, objectiveBytes int) (string, []byte, string) {
	b.Helper()
	root := b.TempDir()
	cfg, err := config.Parse([]byte(config.Example))
	if err != nil {
		b.Fatal(err)
	}
	cfg.Repository = "benchmark"
	c := Creation{
		Version: 1, Nonce: "controller-history-benchmark", Repository: repository.Identity{
			Version: 1, Name: "benchmark", Root: root, CommonDir: filepath.Join(root, ".git"),
			ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40),
		},
		Objective: strings.Repeat("x", objectiveBytes), Config: cfg,
	}
	path := filepath.Join(root, "history.db")
	if err := Append(path, "run.created", c); err != nil {
		b.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		b.Fatal(err)
	}
	cycles := (events - 1) / 3
	for n := 0; n < cycles; n++ {
		request := LifecycleRequest{Version: 1, RunID: s.RunID, Action: "pause", Actor: "benchmark", Nonce: fmt.Sprintf("seed-pause-%d", n)}
		if err := Append(path, "run.pause-requested", request); err != nil {
			b.Fatal(err)
		}
		requestID, err := request.ID()
		if err != nil {
			b.Fatal(err)
		}
		history, err := journal.Read(path)
		if err != nil || len(history) == 0 {
			b.Fatal("read seeded history", err)
		}
		settlement := LifecycleSettlement{
			Version: 1, RunID: s.RunID, RequestID: requestID,
			ControllerHead: history[len(history)-1].Hash, Actor: "benchmark",
			Evidence: "benchmark fixture has no active work", WorkloadsStopped: true,
			Unresolved: []string{},
		}
		if err := Append(path, "run.paused", settlement); err != nil {
			b.Fatal(err)
		}
		pauseID, err := request.ID()
		if err != nil {
			b.Fatal(err)
		}
		resume := LifecycleResume{Version: 1, RunID: s.RunID, PauseRequestID: pauseID, Actor: "benchmark", Nonce: fmt.Sprintf("seed-resume-%d", n)}
		if err := Append(path, "run.resumed", resume); err != nil {
			b.Fatal(err)
		}
	}
	db, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	return path, db, s.RunID
}
