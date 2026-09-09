"""Measure actual lexical binary against rg on a deterministic synthetic corpus."""
import argparse
import datetime
import hashlib
import json
from pathlib import Path
import platform
import shutil
import struct
import sys

from benchmark_startup import sample_command, source_identity


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


def run(command, data=None):
    stdout, stderr, elapsed, peak = sample_command(command, Path.cwd(), timeout=600, input_data=data)
    if stderr:
        raise RuntimeError(f"benchmark child stderr: {stderr[:2000]!r}")
    return stdout, elapsed, peak


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("output", type=Path, help="Fresh local artifact directory")
    parser.add_argument("--files", type=int, choices=[10000, 100000, 500000], required=True)
    parser.add_argument("--build-profile", choices=["debug", "release", "unknown"], default="unknown")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    binary_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    runner_hash = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    source_before = source_identity(Path(__file__).resolve().parent.parent)
    started_utc = datetime.datetime.now(datetime.timezone.utc).isoformat()
    rg = shutil.which("rg")
    if not rg:
        raise RuntimeError("rg required for executed comparison")
    rg_hash = hashlib.sha256(Path(rg).read_bytes()).hexdigest()
    root = args.output.resolve()
    root.mkdir(parents=False, exist_ok=False)
    sources = root / "sources"
    sources.mkdir()
    source = dict(repository_id="a" * 64, object_format="sha1", commit="b" * 40, tree="c" * 40)
    manifest_hash = hashlib.sha256(b"harness.ri.lexical-manifest.v1\0" + canonical(source) + b"\n")
    expected = set()
    digest_paths = {}
    marker = "NeedleUniqueLexical"
    with (root / "manifest.jsonl").open("xb") as manifest:
        manifest.write(canonical(dict(kind="source", value=source)) + b"\n")
        for n in range(args.files):
            text = f"record {n:06d} " + (marker if n % 1000 == 0 else "ordinary") + " " + "payload " * 120 + "\n"
            data = text.encode()
            digest = hashlib.sha256(data).hexdigest()
            path = f"src/{n:06d}.txt"
            blob = hashlib.sha1(f"blob {len(data)}\0".encode() + data).hexdigest()
            record = dict(path=path, blob=blob, sha256=digest, bytes=len(data))
            manifest_hash.update(canonical(record) + b"\n")
            manifest.write(canonical(dict(kind="file", value=record)) + b"\n")
            (sources / digest).write_bytes(data)
            digest_paths[digest] = path
            if (n + 1) % 10000 == 0:
                print(f"generated {n + 1}/{args.files}", file=sys.stderr, flush=True)
            if n % 1000 == 0:
                start = data.index(marker.encode())
                expected.add((path, start, start + len(marker)))
    manifest_id = manifest_hash.hexdigest()
    common = dict(manifest_path=str(root / "manifest.jsonl"), manifest_id=manifest_id,
                  source=source, source_root=str(sources))
    request = dict(common, operation="lexical_build", output_path=str(root / "index"),
                   batch_bytes=8 << 20, batch_files=1000)
    print("building index", file=sys.stderr, flush=True)
    output, build_ns, build_peak = run([str(binary), "--stdio"], canonical(request))
    built = json.loads(output)
    if not built["ok"]:
        raise RuntimeError(built)
    (root / "build-receipt.json").write_bytes(canonical(built["result"]) + b"\n")
    request = dict(common, operation="lexical_search", index_path=str(root / "index"),
                   **built["result"], pattern=marker, fixed=True, case_insensitive=False,
                   limit=1000, after=None)
    samples = []
    peaks = []
    for n in range(5):
        print(f"one-shot query {n + 1}/5", file=sys.stderr, flush=True)
        output, elapsed, peak = run([str(binary), "--stdio"], canonical(request))
        result = json.loads(output)
        verify(result, expected)
        samples.append(elapsed)
        peaks.append(peak)
    frames = bytearray()
    for seq in range(1, 6):
        body = canonical(dict(id=seq, request=request))
        frames.extend(struct.pack(">I", len(body)) + body)
    print("persistent five-query batch", file=sys.stderr, flush=True)
    output, stream_ns, stream_peak = run([str(binary), "--stdio-stream"], bytes(frames))
    for seq in range(1, 6):
        if len(output) < 4:
            raise RuntimeError("truncated response frame")
        size = struct.unpack(">I", output[:4])[0]
        if not 0 < size <= 1 << 20:
            raise RuntimeError("invalid response size")
        result = json.loads(output[4:4 + size])
        if result["id"] != seq:
            raise RuntimeError("response sequence mismatch")
        verify(result, expected)
        output = output[4 + size:]
    if output:
        raise RuntimeError("trailing stream bytes")
    rg_samples = []
    rg_peaks = []
    for n in range(5):
        print(f"rg query {n + 1}/5", file=sys.stderr, flush=True)
        output, elapsed, peak = run([rg, "--json", "--fixed-strings", "--text", "--no-ignore", marker, str(sources)])
        actual = set()
        for line in output.splitlines():
            event = json.loads(line)
            if event["type"] == "match":
                match = event["data"]
                path = digest_paths[Path(match["path"]["text"]).name]
                for item in match["submatches"]:
                    actual.add((path, match["absolute_offset"] + item["start"], match["absolute_offset"] + item["end"]))
        if actual != expected:
            raise RuntimeError("rg oracle mismatch")
        rg_samples.append(elapsed)
        rg_peaks.append(peak)
    if binary_hash != hashlib.sha256(binary.read_bytes()).hexdigest():
        raise RuntimeError("binary changed during measurement")
    if rg_hash != hashlib.sha256(Path(rg).read_bytes()).hexdigest() or source_before != source_identity(Path(__file__).resolve().parent.parent):
        raise RuntimeError("rg or development source changed during measurement")
    report = dict(version=1, files=args.files, manifest_id=manifest_id,
                  binary_sha256=binary_hash, rg_sha256=rg_hash, runner_sha256=runner_hash,
                  source=source_before, started_utc=started_utc, declared_build_profile=args.build_profile,
                  build_peak_working_set_bytes=build_peak, oneshot_peak_working_set_bytes=peaks,
                  persistent_peak_working_set_bytes=stream_peak, rg_peak_working_set_bytes=rg_peaks,
                  os=platform.platform(), build_ns=build_ns, oneshot_samples_ns=samples,
                  persistent_five_requests_ns=stream_ns, rg_samples_ns=rg_samples,
                  expected_matches=len(expected), correctness="PASS",
                  limitations=["Synthetic source identity; not Git-tree membership qualification",
                               "OS caches not flushed; no cold-cache claim",
                               "Single selective fixed query; no overlay/model evaluation",
                               "Process startup included; persistent batch includes one startup",
                               "Memory is Windows child-process lifetime peak working set; null elsewhere",
                               "Declared build profile is caller input, not embedded build attestation"])
    (root / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report))


def verify(response, expected):
    if not response["ok"]:
        raise RuntimeError(response)
    result = response["result"]
    actual = {(hit["path"], *hit["range"]) for hit in result["matches"]}
    if actual != expected or result["truncated"] or len(result["matches"]) != len(expected):
        raise RuntimeError("lexical result differs from exact expected hits")


if __name__ == "__main__":
    main()
