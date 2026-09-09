package taskpool

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkCapacityDecision(b *testing.B) {
	for _, count := range []int{1, 32, 1024} {
		b.Run(fmt.Sprintf("active=%d", count), func(b *testing.B) {
			s := Snapshot{Limits: &Limits{Total: count + 1}, Active: map[string]Request{}}
			for i := 1; i <= count; i++ {
				r := request(i)
				s.Active[r.ID] = r
			}
			next := request(count + 1)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if !room(s, next) {
					b.Fatal("eligible request rejected")
				}
			}
		})
	}
}

func BenchmarkDurableAcquireSettle(b *testing.B) {
	root := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		path := filepath.Join(root, fmt.Sprintf("pool-%d", i))
		if err := Bind(path, Limits{Total: 6}); err != nil {
			b.Fatal(err)
		}
		r := request(1)
		b.StartTimer()
		if err := Acquire(path, r); err != nil {
			b.Fatal(err)
		}
		if err := Settle(path, Release{r.ID, fmt.Sprintf("%064x", 300)}); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		s, err := Inspect(path)
		if err != nil || len(s.Active) != 0 || len(s.Settled) != 1 {
			b.Fatal("invalid final accounting", s, err)
		}
		b.StartTimer()
	}
}
