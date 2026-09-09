"""Measure a built harness help process; emit development evidence, not a gate."""

import argparse
import ctypes
import datetime
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import statistics
import subprocess
import sys
import time


def hardware(root):
    if os.name != "nt":
        return {"cpu_model": platform.processor(), "physical_memory_bytes": None}
    command = ("$cpu = Get-CimInstance Win32_Processor; "
               "$system = Get-CimInstance Win32_ComputerSystem; "
               "@{cpu_model=@($cpu | ForEach-Object {$_.Name}); "
               "physical_memory_bytes=[long]$system.TotalPhysicalMemory} | ConvertTo-Json -Compress")
    return json.loads(run(["powershell.exe", "-NoProfile", "-Command", command], root).stdout)


def sample_command(argv, root, timeout=30, input_data=None):
    """Return output, process duration and Windows lifetime peak working set."""
    start = time.perf_counter_ns()
    with subprocess.Popen(argv, cwd=root,
                          stdin=subprocess.PIPE if input_data is not None else None,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE) as process:
        try:
            stdout, stderr = process.communicate(input=input_data, timeout=timeout)
        except subprocess.TimeoutExpired:
            process.kill()
            process.communicate()
            raise
        elapsed = time.perf_counter_ns() - start
        if process.returncode != 0:
            raise ValueError("measurement process failed")
        peak = None
        if os.name == "nt":
            class Counters(ctypes.Structure):
                _fields_ = [("cb", ctypes.c_ulong), ("faults", ctypes.c_ulong),
                            ("peak_working_set", ctypes.c_size_t),
                            ("working_set", ctypes.c_size_t),
                            ("peak_paged", ctypes.c_size_t), ("paged", ctypes.c_size_t),
                            ("peak_nonpaged", ctypes.c_size_t), ("nonpaged", ctypes.c_size_t),
                            ("pagefile", ctypes.c_size_t), ("peak_pagefile", ctypes.c_size_t)]
            counters = Counters()
            counters.cb = ctypes.sizeof(counters)
            query = ctypes.WinDLL("psapi", use_last_error=True).GetProcessMemoryInfo
            query.argtypes = [ctypes.c_void_p, ctypes.POINTER(Counters), ctypes.c_ulong]
            query.restype = ctypes.c_int
            if not query(int(process._handle), ctypes.byref(counters), counters.cb):
                raise ctypes.WinError(ctypes.get_last_error())
            peak = counters.peak_working_set
    return stdout, stderr, elapsed, peak


def run(args, root):
    return subprocess.run(args, cwd=root, check=True, capture_output=True, timeout=30)


def source_identity(root):
    paths = run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], root).stdout
    digest = hashlib.sha256(b"engorch.development-source.v1\n")
    count = 0
    for raw in sorted(set(paths.split(b"\0")) - {b""}):
        path = root / os.fsdecode(raw)
        if path.is_symlink() or not path.is_file():
            raise ValueError("source inventory requires ordinary files")
        digest.update(len(raw).to_bytes(8, "big"))
        digest.update(raw)
        digest.update(hashlib.sha256(path.read_bytes()).digest())
        count += 1
    head = subprocess.run(["git", "rev-parse", "--verify", "HEAD"], cwd=root,
                          capture_output=True, timeout=30)
    return {"sha256": digest.hexdigest(), "file_count": count,
            "head": head.stdout.decode().strip() if head.returncode == 0 else None}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path, nargs="?")
    parser.add_argument("--go", default="go", help="Go executable used to inspect binary build metadata")
    parser.add_argument("--samples", type=int, default=30)
    parser.add_argument("--warmup", type=int, default=3)
    parser.add_argument("--conditions", help="Explicit host workload/measurement conditions")
    parser.add_argument("--source-identity-only", action="store_true",
                        help="print nonignored development source and Python identity without measuring")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    if args.source_identity_only:
        if args.binary is not None or args.conditions is not None:
            parser.error("source identity mode does not accept a binary or measurement conditions")
        executable = Path(sys.executable).resolve(strict=True)
        print(json.dumps({
            "source": source_identity(root),
            "python": {
                "executable": str(executable),
                "sha256": hashlib.sha256(executable.read_bytes()).hexdigest(),
                "version": platform.python_version(),
            },
        }, sort_keys=True))
        return
    if args.binary is None or args.conditions is None:
        parser.error("binary and --conditions are required for measurement")
    if not 5 <= args.samples <= 1000 or not 1 <= args.warmup <= 100:
        parser.error("samples must be 5..1000 and warmup 1..100")
    binary = args.binary.resolve(strict=True)
    before = source_identity(root)
    binary_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    build_metadata = run([args.go, "version", "-m", str(binary)], root).stdout.decode()
    host_hardware = hardware(root)
    started = datetime.datetime.now(datetime.timezone.utc).isoformat()
    observations, output_hash = [], None
    memory_samples = []
    for index in range(args.samples + args.warmup):
        stdout, stderr, elapsed, peak = sample_command([str(binary), "help"], root)
        if stderr or not stdout or len(stdout) > 1 << 20:
            raise ValueError("help response failed the benchmark output contract")
        current = hashlib.sha256(stdout).hexdigest()
        if output_hash is not None and current != output_hash:
            raise ValueError("help output changed during sampling")
        output_hash = current
        if index >= args.warmup:
            observations.append(elapsed)
            memory_samples.append(peak)
    if before != source_identity(root) or binary_hash != hashlib.sha256(binary.read_bytes()).hexdigest():
        raise ValueError("source or binary changed during sampling")
    report = {
        "version": 1, "scope": "development_help_process_startup",
        "source": before, "binary_sha256": binary_hash,
        "binary_bytes": binary.stat().st_size, "output_sha256": output_hash,
        "binary_build_metadata": build_metadata,
        "started_utc": started, "conditions": args.conditions, "hardware": host_hardware,
        "os": platform.platform(), "machine": platform.machine(),
        "processor": platform.processor(), "logical_cpus": os.cpu_count(),
        "measurement_runtime": platform.python_version(),
        "cache_state": "warm after explicit warmup; OS cache not flushed",
        "warmup": args.warmup, "sample_count": len(observations),
        "samples_ns": observations, "median_ns": statistics.median(observations),
        "p95_ns": sorted(observations)[math.ceil(len(observations) * .95) - 1],
        "peak_memory_bytes": max(memory_samples) if all(v is not None for v in memory_samples) else None,
        "peak_memory_samples_bytes": memory_samples,
        "memory_metric": "Windows per-process peak working set; null on other platforms",
        "limitations": ["includes subprocess launch and output capture",
                        "no model invocation; not a workflow latency measurement",
                        "no guarantee of idle host or cold storage",
                        "source inventory is development identity, not a commit receipt"],
    }
    print(json.dumps(report, sort_keys=True, indent=2))


if __name__ == "__main__":
    main()
