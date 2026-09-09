# Build and verify a local package

`scripts/package-local.ps1` builds the Go control plane as `engorch` and the Rust
repository-intelligence process as `engorch-ri`. It produces a local directory,
not an installer, archive, signed artifact, tag, or published release. The script
never commits or modifies Git refs.

Both the output and build directories must be new absolute paths. If either is
inside the source repository, Git must already ignore it. Cargo uses the supplied
build directory as a separate target directory, so packaging does not replace
the repository's `target/release` binary.

Supply every tool and target explicitly. This Windows example uses the repository's
local Go toolchain and the host target reported by `rustc --version --verbose`:

```powershell
$root = (Resolve-Path .).Path
$rustHost = (& "$env:USERPROFILE\.cargo\bin\rustc.exe" --version --verbose |
    Select-String '^host: ' | ForEach-Object { $_.Line.Substring(6) })

& "$root\scripts\package-local.ps1" `
  -OutputDirectory "$root\.local\packages\windows-amd64" `
  -BuildDirectory "$root\.local\package-build\windows-amd64" `
  -Go "$root\.local\toolchains\go\bin\go.exe" `
  -Git "C:\Program Files\Git\cmd\git.exe" `
  -Cargo "$env:USERPROFILE\.cargo\bin\cargo.exe" `
  -Rustc "$env:USERPROFILE\.cargo\bin\rustc.exe" `
  -GoOS windows `
  -GoArch amd64 `
  -RustTarget $rustHost
```

Use the actual absolute Git path on the host; the example path is not a discovery
rule. The script checks Cargo metadata for the workspace root, package name and
version, builds with `Cargo.lock`, and records the selected tool paths, tool hashes,
versions and targets in `manifest.json`.

The package contains:

```text
bin/engorch.exe
bin/engorch-ri.exe
LICENSE
NOTICE
THIRD_PARTY.md
docs/guides/local-packaging.md
third_party/... retained licenses, notices and provenance notes
dependency-licenses/dependency-licenses.json
dependency-licenses/evidence/... target-filtered dependency license evidence
SHA256SUMS
manifest.json
```

Names omit `.exe` for non-Windows targets. `SHA256SUMS` covers every payload file;
`manifest.json` records its own bounded schema plus source, build, component,
smoke and payload identities. Verify an existing directory without rebuilding:

```powershell
& "$root\scripts\verify-local-package.ps1" `
  -PackageDirectory "$root\.local\packages\windows-amd64"
```

The verifier rejects missing, additional, linked, path-escaping, size-mismatched
or hash-mismatched payloads. It also executes `engorch help` and checks that
`engorch-ri` returns its expected canonical usage-error envelope.

## Dependency license collection

Packaging runs `scripts/collect-dependency-licenses.ps1` with the explicit Rust
target and tool paths. The collector uses `cargo metadata --offline --locked
--filter-platform <target>` and a Go module query with `GOPROXY=off` and
`GOSUMDB=off`. It reads already resolved package sources and does not download or
write dependency caches.

The Rust graph is split into normal, build, and development reachability. The
normal closure identifies target-filtered packages that could contribute code to
the distributed Rust binary; it does not prove that the linker included code
from every listed package. Build and development packages are recorded
separately and do not make distributed dependency evidence incomplete merely
because those closures are excluded from the binary.

Every record binds the Cargo package ID, version, source and `Cargo.lock`
checksum when present, or the Go module path, version and module sums. Primary
`license_file`, `LICENSE*`, `COPYING*`, `AUTHORS`, and `NOTICE*` files are copied
with byte counts and SHA-256 hashes. Known package-cache omissions use the exact
retained r-efi, protoc, protobuf, or tgrep evidence under `third_party`.
Normal Rust and Go dependencies without local or retained evidence fail closed.
Build and development evidence have independent status fields.

Run the collector without building binaries when reviewing a resolved graph:

```powershell
& "$root\scripts\collect-dependency-licenses.ps1" `
  -RepositoryRoot $root `
  -Cargo "$env:USERPROFILE\.cargo\bin\cargo.exe" `
  -Go "$root\.local\toolchains\go\bin\go.exe" `
  -RustTarget $rustHost `
  -RustPackage engorch-ri `
  -OutputDirectory "$root\.local\dependency-licenses\windows-amd64"
```

The output directory must be new. Its JSON has no timestamp or host-specific
cache paths, so equal repository inputs, target metadata, module graph and
license bytes produce equal files. `dependency_evidence_qualified` covers the
collected potentially distributed dependency evidence only.
`package_compliance_qualified` remains `false`: license choice analysis,
target-specific linker inspection, source-offer requirements, signing and a
release review remain manual work.

## Source and qualification labels

The source inventory hashes every tracked and nonignored untracked ordinary file
before and after the build. Any change during the build aborts packaging. An
unborn repository is labeled `development-unborn`; a repository with changes is
`development-dirty`; only an existing clean `HEAD` is labeled `commit-clean` and
records its commit and tree.

The artifact kind is correspondingly `local-development-package` or
`local-commit-package`. `release_qualified` is always `false`. A clean commit is
necessary for a future release receipt, but this local build does not establish
tests, cross-platform support, legal compliance, signing, reproducibility,
provider behavior, hosted behavior, or release readiness.

Only the explicitly selected host and targets receive smoke evidence. A native
Windows run supports a Windows observation only. Cross-compilation, if attempted,
does not make the output executable on the build host and will fail the mandatory
smoke checks. Build each supported target on a suitable host and report targets
that were not executed as `NOT RUN`.
