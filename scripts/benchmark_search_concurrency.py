"""Measure real RI search concurrency on a retained, correctness-checked corpus."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import statistics
import struct
import subprocess
import time

from benchmark_lexical import canonical, verify


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("corpus", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--rounds", type=int, default=5)
    args = parser.parse_args()
    if not 3 <= args.rounds <= 30:
        parser.error("rounds must be 3..30")
    binary = args.binary.resolve(strict=True)
    corpus = args.corpus.resolve(strict=True)
    if args.output.exists():
        parser.error("output must be fresh")
    started_at = datetime.now(timezone.utc).isoformat()
    runner = Path(__file__).resolve()
    runner_hash = hashlib.sha256(runner.read_bytes()).hexdigest()
    baseline_bytes = (corpus / "result.json").read_bytes()
    baseline = json.loads(baseline_bytes)
    hashes = {str(binary): hashlib.sha256(binary.read_bytes()).hexdigest()}
    shards = []
    for n in range((baseline["files"] + 999) // 1000):
        directory = f"shard-{n:06d}"
        digests = []
        for name in ("lookup.bin", "index.bin", "files.bin"):
            path = corpus / "index" / directory / name
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            hashes[str(path)] = digest
            digests.append(digest)
        shards.append(dict(directory=directory, files=min(1000, baseline["files"]-n*1000), hashes=digests))
    digest = hashlib.sha256(b"harness.ri.lexical-disk-build.v1\n" + baseline["manifest_id"].encode() + b"\n")
    for shard in shards:
        digest.update(canonical(shard) + b"\n")
    marker = "NeedleUniqueLexical"
    expected = {(f"src/{n:06d}.txt", 14, 14+len(marker)) for n in range(0, baseline["files"], 1000)}
    request = dict(operation="lexical_search", manifest_path=str(corpus / "manifest.jsonl"),
                   manifest_id=baseline["manifest_id"], source=dict(repository_id="a"*64, object_format="sha1", commit="b"*40, tree="c"*40),
                   source_root=str(corpus / "sources"), index_path=str(corpus / "index"),
                   build=dict(manifest_id=baseline["manifest_id"], shards=shards), build_id=digest.hexdigest(),
                   pattern=marker, fixed=True, case_insensitive=False, limit=1000, after=None)

    def batch(fallback):
        local = dict(request, case_insensitive=fallback)
        frames = bytearray()
        for seq in range(1, 6):
            body = canonical(dict(id=seq, request=local))
            frames.extend(struct.pack(">I", len(body)) + body)
        started = time.perf_counter_ns()
        completed = subprocess.run([str(binary), "--stdio-stream"], input=frames, capture_output=True, timeout=180, check=True)
        elapsed = time.perf_counter_ns()-started
        output = completed.stdout
        for seq in range(1, 6):
            if len(output) < 4:
                raise ValueError("truncated frame")
            size = struct.unpack(">I", output[:4])[0]
            if not 0 < size <= 1 << 20 or len(output) < 4+size:
                raise ValueError("invalid response size")
            result = json.loads(output[4:4+size])
            if result["id"] != seq:
                raise ValueError("response correlation mismatch")
            verify(result, expected)
            if fallback and not result["result"]["full_scan"]:
                raise ValueError("fallback not reported")
            output = output[4+size:]
        if output:
            raise ValueError("trailing output")
        return elapsed

    results = []
    for fallback in (False, True):
        batch(fallback)  # Discarded warmup, no cold-cache claim.
        for workers in (1, 2, 4):
            samples = []
            for _ in range(args.rounds):
                with ThreadPoolExecutor(max_workers=workers) as pool:
                    started = time.perf_counter_ns()
                    timings = list(pool.map(batch, [fallback]*workers))
                    wall = time.perf_counter_ns()-started
                samples.append(dict(wall_ns=wall, process_batch_ns=timings, queries=workers*5))
            results.append(dict(full_scan=fallback, workers=workers, samples=samples,
                                median_queries_per_second=statistics.median(s["queries"]*1e9/s["wall_ns"] for s in samples)))
    for path, expected_hash in hashes.items():
        if hashlib.sha256(Path(path).read_bytes()).hexdigest() != expected_hash:
            raise ValueError("binary/index changed during benchmark")
    if hashlib.sha256(runner.read_bytes()).hexdigest() != runner_hash:
        raise ValueError("runner changed during benchmark")
    report = dict(version=1, correctness="PASS", files=baseline["files"], binary_sha256=hashes[str(binary)],
                  runner_sha256=runner_hash,
                  started_at_utc=started_at, completed_at_utc=datetime.now(timezone.utc).isoformat(),
                  host=dict(system=platform.system(), release=platform.release(), machine=platform.machine(),
                            logical_cpus=os.cpu_count(), python=platform.python_version()),
                  baseline_sha256=hashlib.sha256(baseline_bytes).hexdigest(), build_id=request["build_id"], results=results,
                  limitations=["Synthetic exact-match workload; no semantic retrieval quality claim",
                               "Each worker is a real RI process serving five requests; includes startup",
                               "Concurrent host development and OS caches uncontrolled",
                               "No agent model calls or model-quality/cost measurement; no tail-percentile claim"])
    with args.output.open("x", encoding="utf-8") as output:
        json.dump(report, output, indent=2)
        output.write("\n")
    print(json.dumps([dict(full_scan=r["full_scan"], workers=r["workers"], qps=r["median_queries_per_second"]) for r in results]))


if __name__ == "__main__":
    main()
