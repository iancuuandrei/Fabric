"""Re-measure persistent queries on retained synthetic benchmark artifacts."""
import argparse
import hashlib
import json
from pathlib import Path
import struct

from benchmark_lexical import canonical, run, verify
from benchmark_startup import source_identity


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("corpus", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--overlay", action="store_true", help="Four replacements, two additions and two tombstones")
    parser.add_argument("--fallback", action="store_true", help="Case-insensitive query requiring conservative full scan")
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    corpus = args.corpus.resolve(strict=True)
    if args.output.exists():
        raise ValueError("fresh output receipt required")
    baseline_bytes = (corpus / "result.json").read_bytes()
    baseline = json.loads(baseline_bytes)
    inventory = source_identity(Path(__file__).resolve().parent.parent)
    binary_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    shards = []
    for n in range((baseline["files"] + 999) // 1000):
        directory = f"shard-{n:06d}"
        hashes = [hashlib.sha256((corpus / "index" / directory / name).read_bytes()).hexdigest()
                  for name in ("lookup.bin", "index.bin", "files.bin")]
        shards.append(dict(directory=directory, files=min(1000, baseline["files"] - n * 1000), hashes=hashes))
    build = dict(manifest_id=baseline["manifest_id"], shards=shards)
    digest = hashlib.sha256(b"harness.ri.lexical-disk-build.v1\n" + baseline["manifest_id"].encode() + b"\n")
    for shard in shards:
        digest.update(canonical(shard) + b"\n")
    source = dict(repository_id="a" * 64, object_format="sha1", commit="b" * 40, tree="c" * 40)
    request = dict(operation="lexical_search", manifest_path=str(corpus / "manifest.jsonl"),
                   manifest_id=baseline["manifest_id"], source=source, source_root=str(corpus / "sources"),
                   index_path=str(corpus / "index"), build=build, build_id=digest.hexdigest(),
                   pattern="NeedleUniqueLexical", fixed=True, case_insensitive=args.fallback, limit=1000, after=None)
    # Fixture prefix is 'record 000000 ' (14 bytes).
    expected = {(f"src/{n:06d}.txt", 14, 14 + len(request["pattern"]))
                for n in range(0, baseline["files"], 1000)}
    overlay_id = None
    if args.overlay:
        overlay, overlay_id = stage_overlay(args.output.resolve().with_suffix(".overlay"), source, baseline["manifest_id"])
        request["overlay"] = overlay
        removed = {f"src/{n:06d}.txt" for n in range(0, 6000, 1000)}
        expected = {hit for hit in expected if hit[0] not in removed}
        expected.update((f"src/new-{n}.txt", 0, len(request["pattern"])) for n in range(2))
    frames = bytearray()
    for seq in range(1, 6):
        body = canonical(dict(id=seq, request=request))
        frames.extend(struct.pack(">I", len(body)) + body)
    output, elapsed, peak = run([str(binary), "--stdio-stream"], bytes(frames))
    for seq in range(1, 6):
        size = struct.unpack(">I", output[:4])[0]
        if not 0 < size <= 1 << 20:
            raise ValueError("invalid frame length")
        response = json.loads(output[4:4+size])
        if response["id"] != seq:
            raise ValueError("correlation mismatch")
        verify(response, expected)
        if response["result"]["manifest_id"] != (overlay_id or baseline["manifest_id"]):
            raise ValueError("result scope mismatch")
        if args.fallback and not response["result"]["full_scan"]:
            raise ValueError("required full-scan fallback not reported")
        output = output[4+size:]
    if output:
        raise ValueError("trailing response")
    if binary_hash != hashlib.sha256(binary.read_bytes()).hexdigest() or inventory != source_identity(Path(__file__).resolve().parent.parent):
        raise ValueError("source/binary drift")
    report = dict(version=1, binary_sha256=binary_hash, source=inventory,
                  baseline_receipt_sha256=hashlib.sha256(baseline_bytes).hexdigest(),
                  manifest_id=baseline["manifest_id"], reconstructed_build_id=request["build_id"],
                  files=baseline["files"], persistent_five_requests_ns=elapsed,
                  overlay_id=overlay_id, overlay_changed_paths=8 if args.overlay else 0,
                  full_scan_required=args.fallback,
                  expected_matches=len(expected),
                  persistent_peak_working_set_bytes=peak, correctness="PASS",
                  limitations=["Current shard hashes reconstructed; baseline runner did not save build receipt",
                               "Synthetic fixed-query corpus; OS cache uncontrolled; no new build or rg timing",
                               "Windows lifetime peak working set, not a portable memory bound"])
    with args.output.open("x") as receipt:
        json.dump(report, receipt, indent=2)
        receipt.write("\n")
    print(json.dumps(report))


def stage_overlay(root, source, base_id):
    """Write only the six changed/new source files; never alter base artifacts."""
    root.mkdir(exist_ok=False)
    sources = root / "sources"
    sources.mkdir()
    changed = {f"src/{n:06d}.txt": b"replacement without marker\n" for n in range(0, 4000, 1000)}
    changed.update({f"src/new-{n}.txt": b"NeedleUniqueLexical\n" for n in range(2)})
    deleted = ["src/004000.txt", "src/005000.txt"]
    digest = hashlib.sha256(b"harness.ri.lexical-manifest.v1\0" + canonical(source) + b"\n")
    with (root / "manifest.jsonl").open("xb") as manifest:
        manifest.write(canonical(dict(kind="source", value=source)) + b"\n")
        for path, data in sorted(changed.items()):
            raw_hash = hashlib.sha256(data).hexdigest()
            blob = hashlib.sha1(f"blob {len(data)}\0".encode() + data).hexdigest()
            record = dict(path=path, blob=blob, sha256=raw_hash, bytes=len(data))
            digest.update(canonical(record) + b"\n")
            manifest.write(canonical(dict(kind="file", value=record)) + b"\n")
            (sources / raw_hash).write_bytes(data)
    changed_id = digest.hexdigest()
    candidate = hashlib.sha256(b"engorch.synthetic-eight-path-overlay.v1").hexdigest()
    identity = dict(base=base_id, candidate=candidate, changed=changed_id, deleted=deleted)
    overlay_id = hashlib.sha256(b"harness.ri.lexical-overlay.v1\n" + canonical(identity)).hexdigest()
    return dict(candidate=candidate, manifest_path=str(root / "manifest.jsonl"),
                manifest_id=changed_id, source_root=str(sources), deleted=deleted), overlay_id


if __name__ == "__main__":
    main()
