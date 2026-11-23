---
name: performance-and-profiling
description: Measure before optimizing hot paths, memory allocations, I/O latency, or graph/indexing passes to achieve verified speedups without premature complexity. Use when optimizing throughput, memory footprint, or startup/query latency.
metadata:
  fabric.roles: "explorer,writer,fixer,reviewer"
---

# Measure-First Performance & Profiling Protocol

Speculative micro-optimizations add complexity and bugs while rarely moving real bottlenecks. Measure first, change one variable, and verify.

## 1. Establish Baseline Before Editing
1. Define the target workload, dataset size, and latency/allocation budget before changing production code.
2. Capture repeatable measurements first (`go test -bench=. -benchmem`, `-cpuprofile`, `-memprofile`, or timed CLI runs) so before/after deltas are comparable on identical hardware.

## 2. High-Leverage Bottleneck Checks
- Eliminate accidental quadratic loops (`O(n^2)` scans inside per-file/per-line iteration), redundant filesystem walks, repeated subprocess spawns, and unindexed lookups before tuning inner expressions.
- Preallocate slices when upper bounds are known, reuse compiled regexes at package init (`regexp.MustCompile`), and avoid `fmt.Sprintf` or full-file string copies inside tight loops.
- Bound response/file/buffer sizes (`io.LimitReader`) and apply cache-stable prompt prefixes (epoch masking, sorted map keys) to reduce token and memory churn.

## 3. Prove Gain & Zero Regression
- Re-run the exact baseline benchmark after your change and report both speedup and `B/op` / `allocs/op` delta.
- Pair fast-path optimizations with differential tests comparing the optimized path against the simple reference oracle on edge cases.
- Revert any optimization whose measured gain is noise (< 5%) relative to its readability or maintenance cost.
