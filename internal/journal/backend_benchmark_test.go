package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkBackends compares the public durability path, including handle
// opening and semantic validation. Each append starts from the same history;
// setup is excluded, so an increasing log does not bias later iterations.
func BenchmarkBackends(b *testing.B) {
	for _, count := range []int{16, 128} {
		for _, backend := range []string{"jsonl", "sqlite"} {
			b.Run(fmt.Sprintf("%s/events=%d", backend, count), func(b *testing.B) {
				seed := filepath.Join(b.TempDir(), "seed.jsonl")
				if err := os.WriteFile(seed, nil, 0600); err != nil {
					b.Fatal(err)
				}
				payload := struct {
					Text string `json:"text"`
				}{strings.Repeat("fixture ", 128)}
				for range count {
					if _, err := Append(seed, "fixture", payload, func(events []Event) error { return nil }); err != nil {
						b.Fatal(err)
					}
				}
				data, err := ExportJSONL(seed)
				if err != nil {
					b.Fatal(err)
				}
				prepare := func(path string) {
					b.Helper()
					if backend == "jsonl" {
						if err := os.WriteFile(path, data, 0600); err != nil {
							b.Fatal(err)
						}
					} else {
						store, err := OpenSQLite(path)
						if err != nil {
							b.Fatal(err)
						}
						if err := store.ImportJSONL(data); err != nil {
							b.Fatal(err)
						}
						if err := store.Close(); err != nil {
							b.Fatal(err)
						}
					}
				}
				b.Run("Replay", func(b *testing.B) {
					path := filepath.Join(b.TempDir(), "history")
					prepare(path)
					b.ReportAllocs()
					b.SetBytes(int64(len(data)))
					b.ResetTimer()
					for range b.N {
						events, err := Read(path)
						if err != nil || len(events) != count {
							b.Fatal(len(events), err)
						}
					}
				})
				b.Run("Append", func(b *testing.B) {
					root := b.TempDir()
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						b.StopTimer()
						path := filepath.Join(root, fmt.Sprintf("history-%d", i))
						prepare(path)
						b.StartTimer()
						_, err := Append(path, "fixture", payload, func(events []Event) error {
							if len(events) != count+1 {
								return fmt.Errorf("unexpected history length %d", len(events))
							}
							return nil
						})
						if err != nil {
							b.Fatal(err)
						}
					}
				})
			})
		}
	}
}
