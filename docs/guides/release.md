# Distribution and native-host release builds

## Published packages

| Package | Exact source | Scope and evidence |
|---|---|---|
| [v2.0.1 Windows amd64](https://github.com/iancuuandrei/Fabric/releases/tag/v2.0.1) | Tag `v2.0.1` | Latest published v2 package (`fabric_2.0.1_windows_amd64.zip`, `fabric.exe` + `fabric-ri.exe`); unsigned local Windows build with CI Go/Rust verification |
| [v1.1.0 Windows amd64](https://github.com/iancuuandrei/Fabric/releases/tag/v1.1.0) | `7ac130d4ea682559fb3b72a8ca096a7bdd5a3e9b` | Historical v1 package; [acceptance with documented cohort limits](https://github.com/iancuuandrei/Fabric/releases/download/v1.1.0/v11-release-acceptance.json) |
| [v1.0.0 Windows amd64](https://github.com/iancuuandrei/Fabric/releases/tag/v1.0.0) | Original immutable release commit | [Recorded installed-task walkthrough](../getting-started/installed-autonomous-task.md) and [acceptance](../evaluation/v1-release-acceptance.md) |

The published v1.1.0 archive is `fabric_1.1.0_windows_amd64.zip`, 40,089,244 bytes,
SHA256 `daadc14fbcce62c5cd73eb5edb727a9bc9d72d30c22c3e4b4ffc09e3858a1f58`.
Its published acceptance record includes an installed Muse task and retained
6/8 original-cohort limits. The newer eight-task coverage ledger and v1.1.3
diagnostic fixes apply to later development source, not this package. Packages
are unsigned; Linux live installation remains NOT RUN in the v1.1.0 record.

## Source history and rolling versions

`dev` retains every incremental commit and patch version, such as `v1.1.3`.
`main` contains one rolling major/minor snapshot, such as `v1.1`. Each trusted
push to dev synchronizes the exact source tree through
`automation:fabric-v1-sol-supervisor`. After inspecting a failed synchronization,
rerun the latest trusted dev workflow if its SHA is still current, or publish
the next reviewed development increment.

Within the same major/minor line, the workflow rewrites only the latest main
checkpoint, preserving its title, parent, original author date and committer
date. It uses an exact expected main SHA with force-with-lease. A newer line
creates one additional checkpoint; an older line is refused. Development
history is never merged into main, and dev history is never rewritten by this
synchronization. Routine updates do not create a PR for every development push.

The authorized initial consolidation leaves the historical v0.0.0/v0.0.1
bootstrap checkpoints, then v1.0 and v1.1. The v1.1 tree includes the later
v1.1.1-v1.1.3 fixes, while its date comes from the original v1.1.0 checkpoint.
Existing published tags/packages remain on their original commits. Main SHA
changes are expected even though the snapshot date remains fixed; use exact
source SHA and acceptance evidence, not its displayed date, for qualification.
See [AGENTS.md](../../AGENTS.md) for attribution, history and evidence rules.
The [source-history guide](../contributing/source-history.md) provides the
branch diagrams, synchronization guards and recovery procedure.

Local release artifacts are unsigned integrity-checked bundles. Every
`release.json` and per-archive `manifest.json` produced here carries
`release_qualified = false`. The build scripts do not set that field from a
separate acceptance record and do not tag, upload, publish, sign or qualify a
release. The v1.0.0 Windows package has separate recorded technical acceptance;
its artifact metadata remains unsigned and `release_qualified = false`. No build
command described below has been executed by this guide.

## Current development packaging evidence

Two independent native Windows v1.0.27 builds from clean source
`6205de9b6b18d503cd45969a8a64660cb6bc65bb` produced the same 39,311,517-byte
archive, SHA256
`dc2e8dd8ccdff6c03deeb6aa7ee5c95fbc498813c23ab96e964f5fb644b0fada`.
Both release verifiers and offline help/version/reference/fake-init/doctor
checks passed. Their manifests keep `release_qualified: false`. These local
artifacts are not a new published or installed-real-task release; the accepted
v1.0.0 distribution below retains its exact identity. See the
[product ledger](../roadmap/v2-product-plan.md) for evidence and remaining gates.

## Platform rule

For the user-facing installed v1.0.0 flow, see the
[Windows installed-task guide](../getting-started/installed-autonomous-task.md).
The accepted package and task evidence are summarized in
[Accepted v1.0.0 Windows distribution](#accepted-v100-windows-distribution).

`scripts/release-build.ps1` builds only explicitly declared platforms:

- Supported values: `windows-amd64`, `linux-amd64`.
- `-Platforms` defaults to the native host only (detected via `go env
  GOOS/GOARCH`). There is no implicit dual-platform bundle.
- A cross-Rust target without its OS-matched linker fails fast with a
  clear error instead of producing a misleading partial bundle:
  - `linux-amd64` on Windows requires `x86_64-linux-gnu-gcc`.
  - `windows-amd64` requires a native Windows host with Visual Studio C++ Build Tools.
- There is no merge-releases step: each host produces its own
  one-platform (or explicitly listed) output directory.

## Windows (actual command)

Prerequisites: PowerShell 5.1 or 7, native Go and Rust with the
`x86_64-pc-windows-msvc` target, and Visual Studio 2022 C++ Build Tools for
the Rust crate. The audit host used Go 1.27.1 and Rust 1.94.1; the exact
versions used are recorded into each manifest under `toolchain`.

```powershell
pwsh -File scripts/release-build.ps1 `
  -Version v1.0.0 `
  -OutputDirectory C:\tmp\fabric-rel-win `
  -Platforms windows-amd64
```

Omit `-Platforms` on Windows to get the same native-only result. The
output directory must be new and outside the source tree; the tree must
be at an exact clean commit (`git status --porcelain` empty,
`HEAD` resolved), and the script rechecks cleanliness after the builds
before writing `SHA256SUMS` / `release.json`.

## Linux (native prerequisite)

The same PowerShell script runs under `pwsh` on Linux. Prerequisites:
`pwsh`, a pinned native Go/Rust toolchain, the
`x86_64-unknown-linux-gnu` target, and a native linker:

```sh
pwsh -File scripts/release-build.ps1 \
  -Version v1.0.0 \
  -OutputDirectory /tmp/fabric-rel-linux \
  -Platforms linux-amd64
```

Windows builds require a native Windows host. Linux remains an optional
platform until its build and acceptance path have actually been validated.

## Reproducibility: two independent same-source builds

A single local build proves nothing about reproducibility. For a given
commit and `SOURCE_DATE_EPOCH`:

1. Select the platforms declared by this release. Windows is required for v1.
2. Build each declared platform twice from the same clean commit, using
   independent output directories and its native toolchain.
3. Compare the per-platform archive hashes (`SHA256SUMS` and
   `release.json`) across independent rebuilds with identical
   sources, epoch, and toolchain versions (recorded as `toolchain.go`,
   `toolchain.cargo`, `toolchain.rustc` plus `platform.rust_target`).

Windows RI builds use a static CRT, deterministic MSVC linking and source
and target-directory path remapping. Matching RI executable hashes alone
do not qualify the complete release archive.

Matching archive hashes are required for release acceptance. ZIP
bytes are deterministic (ordinal entry sort, `NoCompression`, normalized
timestamps), but determinism still assumes the same toolchain.

## Verification

```powershell
pwsh -File scripts/release-verify.ps1 -ReleaseDirectory <release-dir>
```

The verifier accepts a one-platform or two-platform release, requires
exact per-archive component identities (`fabric[.exe]`,
`fabric-ri[.exe]`, unique name and path, hash match), the declared Rust
target, and the artifact filename carrying the exact release version.
It checks integrity only, and `release_qualified` must remain `false`.

## Accepted v1.0.0 Windows distribution

The v1.0.0 Windows amd64 package was independently reproduced twice from clean
source commit `a4ca05692465bedc6b98d6ffbb8219b6d076f5d1`, with
`SOURCE_DATE_EPOCH=1791012510`, Go 1.27.1 and Rust 1.94.1. Both archive builds
matched SHA-256
`7add99a1146bfe8a780dd38238904cf74f9fdb7dffd1a36e5211d045988a21e6`.
The artifact is `fabric_1.0.0_windows_amd64.zip` and contains `fabric.exe` and
`engorch-ri.exe`. Its manifest intentionally says `release_qualified: false`:
the package is unsigned, and the checksum establishes byte integrity rather
than publisher authenticity.

The installed package completed a real autonomous repository task: the run
reached `READY`, reviewer decision was `approve` with zero findings, and the
candidate passed independent native and held-out checks. An explorer used the
installed RI executable for four successful searches, three with validated
nonempty results; those observations are bound to the installed executable
hash. The run used stock Codex with `gpt-6-luna` at high effort; its ID is
`7465e3271f8f13d754edaa0fe41f00866963997f02984274bf1ff274a67d7a86`. The exact
run, package, source and acceptance evidence are in the
[v1.0.0 acceptance record](../evaluation/v1-release-acceptance.md) and its
[machine-readable receipt](../../evals/v1/results/release-v1.0.0-acceptance-20261003.json).

This acceptance applies to Windows amd64 only. Linux amd64 is an optional build
target whose actual installation and task path are not qualified here. The
recorded run demonstrates the tested configuration and task; it does not
establish general model quality, unrestricted task success, an operating-system
sandbox, signed-release authenticity or performance across repositories.

For the user-facing installed workflow, see the
[Windows installed-task guide](../getting-started/installed-autonomous-task.md).
The commands above remain the local source-build and verification procedure;
they do not publish or qualify a release.

## Install and run a Windows bundle

Keep `release.json`, `SHA256SUMS`, and the Windows ZIP together in the
release directory. Obtain them from the same release or build them using
the command above. The bundle includes Fabric and Rust repository
intelligence; install the configured Codex runtime separately and sign in
before starting a real task.

```powershell
$release = 'C:\tmp\fabric-rel-win'
$install = "$env:LOCALAPPDATA\Fabric\v1.0.0"
New-Item -ItemType Directory -Force (Split-Path $install) | Out-Null
pwsh -File scripts/release-verify.ps1 -ReleaseDirectory $release
pwsh -File scripts/install-fabric.ps1 -ReleaseDirectory $release -InstallDirectory $install
$fabric = Join-Path $install 'fabric.exe'
& $fabric version
# In the repository you want Fabric to change:
& $fabric init --codex 'C:\tools\codex.exe' --model gpt-6-luna --effort high
& $fabric run --autonomous 'Implement the requested feature and add regression tests'
& $fabric status
& $fabric inspect
& $fabric diff
```

Replace the runtime path and objective with your installed runtime and task.
The install target must not already exist. The installer verifies archive
and component hashes and refuses to merge files into an existing installation.
Use the full executable path as shown, or add that version's directory to
your user PATH. See [autonomous mode](autonomous-task.md) for configuration,
verification results and blocked-run diagnostics.

## Upgrade without overwriting project state

Install a newer verified bundle into a new version directory beside the old
one, then select its `fabric.exe` explicitly or update your user PATH. Keep
the old directory until the new version has passed your checks. Installation
does not replace repository `.harness` configuration, journals or candidate
worktrees. Do not reinitialize an existing project as an upgrade step.
Before resuming a saved run, inspect it with the selected version and follow
its reported compatibility or blocked-state instructions; an unknown external
effect is not permission to rerun the task. Removing an old executable
directory is optional and does not require deleting project evidence.
