package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/repository"
)

// BenchmarkPlannerGoContractResource measures complete v4 planner
// admission against one explicitly pinned, externally supplied committed tree.
// Timed work is AdmitPlannerGoContext only; run creation and cache resets are setup.
// cache_resident-B is the mean post-admission resident cache size, not bytes written.
// Required environment: ENGORCH_RI_BENCH_REPOSITORY,
// ENGORCH_RI_BENCH_HUMANIZE_COMMIT, ENGORCH_RI_BINARY, and
// ENGORCH_RI_BENCH_RI_SHA256. It never mutates the benchmark repository.
func BenchmarkPlannerGoContractResource(b *testing.B) {
	root, commit, executable, expectedProducer := os.Getenv("ENGORCH_RI_BENCH_REPOSITORY"), os.Getenv("ENGORCH_RI_BENCH_HUMANIZE_COMMIT"), os.Getenv("ENGORCH_RI_BINARY"), os.Getenv("ENGORCH_RI_BENCH_RI_SHA256")
	if root == "" || commit == "" || executable == "" || expectedProducer == "" {
		b.Skip("set ENGORCH_RI_BENCH_REPOSITORY, ENGORCH_RI_BENCH_HUMANIZE_COMMIT, ENGORCH_RI_BINARY, and ENGORCH_RI_BENCH_RI_SHA256")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(executable) || filepath.Clean(executable) != executable || !isLowerDigest(commit) || !isLowerSHA256(expectedProducer) {
		b.Fatal("benchmark root, commit, executable, or producer pin is invalid")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		b.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	if hex.EncodeToString(sum[:]) != expectedProducer {
		b.Fatal("RI executable differs from ENGORCH_RI_BENCH_RI_SHA256")
	}
	cfg, err := config.Parse([]byte(config.Example))
	if err != nil {
		b.Fatal(err)
	}
	identity, err := repository.Discover(context.Background(), root, cfg.Repository)
	if err != nil {
		b.Fatal(err)
	}
	if identity.Commit != commit {
		b.Fatal("repository HEAD differs from ENGORCH_RI_BENCH_HUMANIZE_COMMIT")
	}
	cfg.Repository = identity.Name
	cacheHome, journals := b.TempDir(), b.TempDir()
	sequence := 0
	if os.PathSeparator == '\\' {
		b.Setenv("LOCALAPPDATA", cacheHome)
	} else {
		b.Setenv("XDG_CACHE_HOME", cacheHome)
	}
	makeCreation := func(cacheVersion int) Creation {
		return Creation{Version: 1, Nonce: "benchmark-contract-cache-v1", Repository: identity, Objective: "Review committed contract behavior and generator ownership.", Config: cfg, Execution: &ExecutionPolicy{Mode: "autonomous-v1", PlannerContext: plannerContextGoContractV1, PlannerContextRIExecutable: executable, PlannerContextRIExecutableSHA256: expectedProducer, PlannerParseCacheVersion: cacheVersion}}
	}
	prepareRun := func(b *testing.B, cacheVersion, ordinal int) string {
		b.Helper()
		sequence++
		path := filepath.Join(journals, fmt.Sprintf("%d-%d-%d.jsonl", cacheVersion, ordinal, sequence))
		if err := Append(path, "run.created", makeCreation(cacheVersion)); err != nil {
			b.Fatal(err)
		}
		return path
	}
	admit := func(b *testing.B, path string) PlannerGoContextRecord {
		b.Helper()
		record, err := AdmitPlannerGoContext(context.Background(), path)
		if err != nil {
			b.Fatal(err)
		}
		if record.Unavailable != "" || record.Graph == nil || record.ContractContext == nil {
			b.Fatal("benchmark admission did not produce contract evidence")
		}
		return record
	}
	cacheDir, err := ensurePlannerParseCacheDir(identity, expectedProducer)
	if err != nil {
		b.Fatal(err)
	}
	var baselineRecordID, baselineGraphDigest, baselineContractDigest string
	for _, scenario := range []struct {
		name, mode   string
		cacheVersion int
	}{
		{"uncached", "uncached", 0},
		{"cold", "cold", 1},
		{"warm", "warm", 1},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			b.StopTimer()
			if scenario.mode == "warm" {
				_ = admit(b, prepareRun(b, 1, -1))
			}
			b.ReportAllocs()
			b.ResetTimer()
			var recordBytes, residentCacheBytes int64
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if scenario.mode == "cold" {
					if err := os.RemoveAll(cacheDir); err != nil {
						b.Fatal(err)
					}
				}
				path := prepareRun(b, scenario.cacheVersion, i)
				b.StartTimer()
				record := admit(b, path)
				b.StopTimer()
				encoded, err := canonical.Bytes(record)
				if err != nil {
					b.Fatal(err)
				}
				recordBytes += int64(len(encoded))
				if baselineRecordID == "" {
					baselineRecordID, baselineGraphDigest, baselineContractDigest = record.RecordID, record.Graph.Digest, record.ContractContext.Digest
				} else if record.RecordID != baselineRecordID || record.Graph.Digest != baselineGraphDigest || record.ContractContext.Digest != baselineContractDigest {
					b.Fatal("cache scenario changed durable contract evidence identity")
				}
				residentCacheBytes += plannerContractBenchmarkCacheBytes(b, cacheDir)
				b.StartTimer()
			}
			b.StopTimer()
			if b.N > 0 {
				b.ReportMetric(float64(recordBytes)/float64(b.N), "record_B/op")
				b.ReportMetric(float64(residentCacheBytes)/float64(b.N), "cache_resident-B")
			}
		})
	}
}

func isLowerDigest(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func plannerContractBenchmarkCacheBytes(b *testing.B, root string) int64 {
	b.Helper()
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		b.Fatal(err)
	}
	return total
}

func isLowerSHA256(value string) bool {
	return len(value) == 64 && strings.IndexFunc(value, func(ch rune) bool { return !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') }) == -1
}
