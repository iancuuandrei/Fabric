"""Wrap the Rust snapshot measurement with host and development-source identity."""

import argparse
import datetime
import hashlib
import json
from pathlib import Path
import platform
import statistics
import subprocess

from benchmark_startup import hardware, sample_command, source_identity


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--conditions", required=True)
    parser.add_argument("--request", type=Path, help="Bound request for benchmark_existing")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    binary = args.binary.resolve(strict=True)
    source = source_identity(root)
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    host = hardware(root)
    rust = subprocess.run(["rustc", "--version", "--verbose"], cwd=root,
                          capture_output=True, check=True, timeout=30).stdout.decode()
    started = datetime.datetime.now(datetime.timezone.utc).isoformat()
    command = [str(binary)]
    request_hash = None
    if args.request:
        args.request = args.request.resolve(strict=True)
        request_hash = hashlib.sha256(args.request.read_bytes()).hexdigest()
        command.append(str(args.request))
    stdout, stderr, process_ns, peak_memory = sample_command(command, root, timeout=300)
    if stderr or len(stdout) > 1 << 20:
        raise ValueError("unexpected snapshot benchmark output")
    datasets = json.loads(stdout)
    if args.request:
        datasets = [datasets]
        if request_hash != hashlib.sha256(args.request.read_bytes()).hexdigest():
            raise ValueError("benchmark request changed")
    for dataset in datasets:
        for key in ("build_samples_ns", "verified_load_samples_ns", "warm_query_batch_samples_ns"):
            if key == "build_samples_ns" and args.request:
                continue
            samples = dataset[key]
            if len(samples) != 5 or any(type(value) is not int or value <= 0 for value in samples):
                raise ValueError("invalid timing samples")
            dataset[key + "_median"] = statistics.median(samples)
    if source != source_identity(root) or digest != hashlib.sha256(binary.read_bytes()).hexdigest():
        raise ValueError("source or binary changed during measurement")
    print(json.dumps({"version": 1, "source": source, "binary_sha256": digest,
                      "binary_bytes": binary.stat().st_size, "rustc": rust,
                      "os": platform.platform(), "machine": platform.machine(),
                      "hardware": host, "conditions": args.conditions,
                      "started_utc": started, "datasets": datasets,
                      "request_sha256": request_hash,
                      "whole_process_ns": process_ns,
                      "whole_process_peak_memory_bytes": peak_memory,
                      "memory_scope": "Windows peak working set across all datasets, warmups and phases; null elsewhere",
                      "limitations": ["compiler metadata is host rustc, not embedded build attestation",
                                      "five samples do not establish tail latency",
                                      "development inventory is not an exact-commit release receipt"]},
                     sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
