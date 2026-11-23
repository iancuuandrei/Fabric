#!/usr/bin/env sh
set -eu

usage() { echo "usage: install-fabric.sh RELEASE_DIRECTORY ABSOLUTE_INSTALL_DIRECTORY" >&2; exit 2; }
[ "$#" -eq 2 ] || usage
release_dir=$1
install_dir=$2
case "$install_dir" in /*) ;; *) echo 'Install directory must be absolute' >&2; exit 2 ;; esac
[ -d "$release_dir" ] || { echo "Release directory does not exist: $release_dir" >&2; exit 1; }
case "$(uname -s)/$(uname -m)" in Linux/x86_64|Linux/amd64) platform=linux/amd64 ;; *) echo 'Only Linux amd64 installations are supported' >&2; exit 1 ;; esac

python3 - "$release_dir" "$install_dir" "$platform" <<'PY'
import hashlib, json, os, pathlib, re, sys, tempfile, zipfile

release_dir = pathlib.Path(sys.argv[1]).resolve(strict=True)
raw_install = pathlib.Path(sys.argv[2])
platform = sys.argv[3]
if not raw_install.is_absolute():
    raise SystemExit("Install directory must be absolute")
# Canonicalize without following symlinks so ".." cannot escape the root check.
abs_install = pathlib.Path(os.path.abspath(str(raw_install)))
if abs_install.name in ("", ".", ".."):
    raise SystemExit("refusing filesystem root or unsafe install directory")
if abs_install == pathlib.Path(abs_install.anchor):
    raise SystemExit("refusing filesystem root install directory")
# Target must be absent (lexists catches dangling symlinks); never merge.
if os.path.lexists(str(abs_install)):
    raise SystemExit(f"Install directory must not already exist: {abs_install}")
if raw_install != abs_install and os.path.lexists(str(raw_install)):
    raise SystemExit(f"Install directory must not already exist: {raw_install}")
parent_raw = abs_install.parent
try:
    canonical_parent = parent_raw.resolve(strict=True)
except FileNotFoundError:
    raise SystemExit(f"Install directory parent must exist: {parent_raw}")
if not canonical_parent.is_dir() or canonical_parent.is_symlink():
    raise SystemExit(f"Install directory parent must be an ordinary directory: {canonical_parent}")
# Reject symlink/reparse anywhere in the parent chain and ensure the
# canonical parent matches the literal parent (no symlink traversal).
cursor = parent_raw
while True:
    if os.path.islink(str(cursor)):
        raise SystemExit(f"refusing symlink parent: {cursor}")
    anchor = pathlib.Path(cursor.anchor)
    if cursor == anchor:
        break
    cursor = cursor.parent
if canonical_parent != parent_raw:
    raise SystemExit(f"refusing symlink parent: {parent_raw}")
canonical_install = canonical_parent / abs_install.name
if canonical_install.parent != canonical_parent or canonical_install.name in ("", ".", ".."):
    raise SystemExit("refusing unsafe installer target path")
release = json.loads((release_dir / "release.json").read_text(encoding="utf-8"))
if release.get("schema_version") != 1 or release.get("product") != "Fabric" or release.get("release_qualified") is not False:
    raise SystemExit("invalid release manifest")
version = str(release.get("version", ""))
if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:[-.][0-9A-Za-z.-]+)?", version):
    raise SystemExit("invalid release identity")
if not re.fullmatch(r"[0-9a-f]{40}", str(release.get("commit", ""))):
    raise SystemExit("invalid release commit")
artifacts = release.get("artifacts", [])
if not isinstance(artifacts, list) or not 1 <= len(artifacts) <= 2:
    raise SystemExit("invalid release artifact set")
expected_sums = {}
artifact_platforms = set()
for record in artifacts:
    record_platform = record.get("platform", "")
    record_name = record.get("file", "")
    if (record_platform not in {"windows/amd64", "linux/amd64"} or
        record_platform in artifact_platforms or
        record_name != f"fabric_{version[1:]}_{record_platform.replace('/', '_')}.zip" or
        record_name in expected_sums or
        not re.fullmatch(r"[0-9a-f]{64}", str(record.get("sha256", ""))) or
        type(record.get("bytes")) is not int or record["bytes"] <= 0):
        raise SystemExit("invalid release artifact record")
    artifact_platforms.add(record_platform)
    expected_sums[record_name] = record["sha256"]
declared = release.get("platforms")
if declared is not None:
    normalized = [str(item).replace("-amd64", "/amd64") for item in declared]
    if len(normalized) != len(set(normalized)) or set(normalized) != artifact_platforms:
        raise SystemExit("declared platforms differ from release artifacts")
matches = [a for a in release.get("artifacts", []) if a.get("platform") == platform]
if len(matches) != 1:
    raise SystemExit(f"release has no unique artifact for {platform}")
artifact = matches[0]
name = artifact.get("file", "")
version_suffix = version[1:]
expected_file = f"fabric_{version_suffix}_{platform.replace('/', '_')}.zip"
if name != expected_file:
    raise SystemExit(f"artifact version mismatch: {name}")
if pathlib.PurePosixPath(name).name != name or not name.endswith(".zip"):
    raise SystemExit("invalid artifact filename")
sums = {}
for line in (release_dir / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
    if not re.fullmatch(r"[0-9a-f]{64}  [A-Za-z0-9._-]+\.zip", line):
        raise SystemExit(f"invalid SHA256SUMS line: {line}")
    digest, filename = line.split("  ", 1)
    if filename in sums:
        raise SystemExit("duplicate SHA256SUMS entry")
    sums[filename] = digest
if sums != expected_sums:
    raise SystemExit("SHA256SUMS differ from release artifact set")
archive = release_dir / name
digest = hashlib.sha256(archive.read_bytes()).hexdigest()
if sums.get(name) != digest or artifact.get("sha256") != digest or archive.stat().st_size != artifact.get("bytes"):
    raise SystemExit("archive checksum or size mismatch")

with tempfile.TemporaryDirectory(prefix=".fabric-install-", dir=str(canonical_parent)) as temp:
    stage = pathlib.Path(temp)
    # Sibling staging must stay in the canonical parent.
    if stage.parent != canonical_parent:
        raise SystemExit("refusing unsafe installer staging path")
    with zipfile.ZipFile(archive) as zf:
        names = zf.namelist()
        if len(names) != 3 or len(set(names)) != 3 or set(names) - {"manifest.json", "fabric", "fabric-ri"}:
            raise SystemExit("archive contains unexpected entries")
        manifest = json.loads(zf.read("manifest.json"))
        if (manifest.get("schema_version") != 1 or manifest.get("product") != "Fabric" or
            manifest.get("version") != release.get("version") or manifest.get("commit") != release.get("commit") or
            manifest.get("source_date_epoch") != release.get("source_date_epoch") or manifest.get("release_qualified") is not False or
            manifest.get("platform", {}).get("os") + "/" + manifest.get("platform", {}).get("arch") != platform):
            raise SystemExit("archive identity mismatch")
        components = manifest.get("components", [])
        if manifest.get("platform", {}).get("rust_target") != "x86_64-unknown-linux-gnu":
            raise SystemExit("archive Rust target mismatch")
        expected = {"fabric": "fabric", "fabric-ri": "fabric-ri"}
        if len(components) != 2:
            raise SystemExit("archive component set is invalid")
        seen_names = set()
        seen_paths = set()
        for component in components:
            cname = component.get("name", "")
            path = component.get("path", "")
            if cname not in expected:
                raise SystemExit(f"invalid component name: {cname}")
            if path != expected[cname]:
                raise SystemExit(f"component path mismatch for {cname}: {path}")
            if cname in seen_names:
                raise SystemExit(f"duplicate component name: {cname}")
            seen_names.add(cname)
            if path in seen_paths:
                raise SystemExit(f"duplicate component path: {path}")
            seen_paths.add(path)
            if not re.fullmatch(r"[0-9a-f]{64}", str(component.get("sha256", ""))):
                raise SystemExit(f"invalid component hash: {path}")
            if hashlib.sha256(zf.read(path)).hexdigest() != component.get("sha256"):
                raise SystemExit(f"component checksum mismatch: {path}")
            (stage / path).write_bytes(zf.read(path))
            os.chmod(stage / path, 0o755)
    staged = sorted(p.name for p in stage.iterdir())
    if staged != ["fabric-ri", "fabric"]:
        raise SystemExit("staged payload is incomplete")
    # Atomically rename the whole staging directory; never move files singly.
    os.rename(str(stage), str(canonical_install))
print(f"PASS installed Fabric {release['version']} to {canonical_install}")
PY
