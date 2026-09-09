# Third-party inventory

This inventory records the dependency baseline and subsequent source adaptations.
The Go module graph and lockfile hashes were refreshed after SQLite, JSON Schema,
GitHub client and OpenTelemetry integration on 2026-09-08. The complete resolved
Go graph is recorded in [the generated inventory](docs/provenance/go-module-license-inventory.md).
This includes upstream test/optional dependencies and is not a binary linkage claim.
It is not a final-tree release inventory.
It is evidence for review, not legal advice or a release-compliance conclusion.
`Cargo.lock` and `go.sum` remain the machine-readable authorities for resolved
package identities and checksums.

Dependency inventory inputs inspected on 2026-09-07; NOTICE and the OpenCode and
Vercel AI adaptation entries refreshed on 2026-09-08:

| Input | SHA-256 |
| --- | --- |
| `Cargo.lock` | `ca6d82a017e5e1f16ae4f2452e5c0238c1828b5f18fa669e1eac45dbf84986ae` |
| `go.mod` | `c84395ca3e4d7b0ba8af7b8d40cb7d90b49ba401530e7a1aa3ec372a31708a6b` |
| `go.sum` | `4febf638ca14a977d88904bfd626b805c5978aa6672f3f10307840b2a089b3eb` |
| `NOTICE` | `a915fd15057e102f1cb1c5f7a82276b7b0afda66dd5bdb16c703e7d901081edd` |

The Rust inventory was derived with `cargo metadata --locked --format-version 1`.
It contains 104 external packages in the resolved graph. “Normal closure” means
reachable through a normal dependency edge from `engorch-ri`; it can include
proc macros and target-specific packages needed to build, and does not prove that
every package contributes bytes to every final binary. “Build/test-only closure”
means there is no normal-dependency path; `tempfile` is also the one direct dev
dependency. Target predicates remain in Cargo metadata and the lock file, so this
cross-target inventory is broader than one Windows binary.

## Copied, adapted, generated, and pinned source

| Material | Treatment and exact provenance | Retained evidence |
| --- | --- | --- |
| AgentTree topology and mailbox | `internal/agenttree` adapts `extensions/core.ts` and `extensions/team-manager.ts` from `YoungseokCh/pi-multiagents-v2` revision `fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd`, MIT. | `third_party/pi-multiagents-v2/LICENSE` and `README.md` retain the notice, exact source hashes and changes. The Go adaptation uses durable replay-validated events and opaque identities; runtime execution and capacity remain separate. |
| TaskPool all-applicable capacity | `internal/taskpool/pool.go` adapts `src/pools.ts` from `harms-haus/pi-subagent-tasks` revision `2bae805c1a0bdd97e699e2c9601fb4e6624f6f53`, MIT. | `third_party/pi-subagent-tasks/LICENSE` and `README.md` retain notice, exact original source hash and changes. Durable Go adaptation; controller integration still pending. |
| Vercel AI SDK Anthropic fixtures | Four recorded JSON event-payload fixtures in `internal/providergateway/anthropic_sse_test.go` come from `vercel/ai` revision `85464f4e2026d9fc0274424c0171a25742836411`; test framing is constructed locally. Apache-2.0. | `third_party/vercel-ai/README.md` records exact payload identities and modifications; upstream license notice and full license retained. |
| OpenCode schema projection | `internal/opencode/provider_schema.go` adapts the OpenAI schema sanitizer from OpenCode v1.18.29 revision `16747470f976aca3d362ad730bcd3fe82ecc2c9a`, `packages/opencode/src/provider/transform.ts`. Declared MIT. | `third_party/opencode/README.md`; source SHA-256 `01b2442770a25f943fd7884ff3f3dac838344a568690e395a77b7bd17c8b0ab5`; retained license SHA-256 `625f0f619133f89bbbb2abe37369613dfa1885eba1e50d02170deb62bb42cb6b`. |
| CodeGraph identifier segmentation | `crates/ri/src/identifiers.rs` adapts `splitIdentifierSegments` from `colbymchenry/codegraph` revision `b9ca4b7981116909900368cc1686a1074cd4d4c1`. It is a bounded Rust port, not a vendored copy of the donor repository. Declared MIT. | `third_party/codegraph/README.md`; license SHA-256 `e6d98f98c666bebe065ac2492a0a19232cc318d4d67bac3ca42ffb77bacc8809`, byte-equal to the retained inspected upstream license. |
| Graphify Rust type walker | `crates/ri/src/structural.rs` adapts `_rust_collect_type_refs` from `Graphify-Labs/graphify` revision `c9f99018774e2e0380e9f65b3959944559a0d5f6`. Declared Apache-2.0 with retained legacy MIT material. | `third_party/graphify/README.md`; `LICENSE` `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30`, `LICENSE-MIT` `36ecff6bbb97aca1be64e95f42538cf796adde5864edac583e620643c096931a`, and `NOTICE` `3448ea175bba09f6fed8a743000139fb4c7112de83f164355b5540b158576621`; each is byte-equal to its retained inspected upstream file. |
| SCIP schema | `third_party/scip/scip.proto` is an unmodified source-vendored file from `sourcegraph/scip` revision `1c2b6db7e560d5233c944f36e4ac1377cc6963fc`. Rust bindings are generated at build time and are not committed. Declared Apache-2.0. | Schema SHA-256 `b38021b65ef90cbbf6af9c829ff75192859ad9b5da05439ef154bea4ceb2bf03`; license SHA-256 `c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4`; both byte-equal to retained inspected upstream files. See `third_party/scip/README.md`. |
| tgrep-core | Linked as a Cargo Git dependency, not source-vendored or modified, from `microsoft/tgrep` revision `e2007b52d2b8fe4176159d0da20c9ba4a46d5aab`, package `tgrep-core 1.0.4`. Declared MIT. | `third_party/tgrep/LICENSE` SHA-256 `5baa259ffd1a975780869d7d2925212224c92206cefb96fb2bf0b146650e5029`, byte-equal to the pinned Cargo checkout root license. |

The other projects discussed in `docs/research/oss-mechanisms.md` are research
inputs only. No source or prose from those donors is identified as copied or
adapted into this repository.

## Go module dependency

The table below retains the original TOML dependency evidence. Added direct
dependencies and all 113 modules in the resolved graph have version, archive sum
and license hashes in the generated inventory linked above. Unmodified direct
notices for go-github, jsonschema and modernc SQLite are additionally retained in
`third_party/dependencies/`. The collector now labels Go records as the resolved
module graph rather than incorrectly treating every module as a normal binary
dependency. Full release packaging remains unqualified.

| Use | Module | Version and source identity | Declared license evidence |
| --- | --- | --- | --- |
| Normal dependency; compiled into `engorch` where referenced | `github.com/pelletier/go-toml/v2` | `v2.4.3`; module sum `h1:GTRvJQutkOSftxIFD5xw9aepkYNuPWmVJpffdDPYVpY=`; go.mod sum `h1:2gIqNv+qfxSVS7cM2xJQKtLSTLUE9V8t9Stt+h56mCY=` | MIT; `third_party/dependencies/go-toml-v2/LICENSE` is byte-equal to the resolved module's primary license, SHA-256 `26844e4b53c5adec04e557fd7dfef281cc0205a7d355626b1c68b778b99e9e7b`. |

## Rust normal dependency closure

All rows below resolve from the crates.io registry unless the source column says
otherwise. License expressions are copied from each resolved package manifest,
not reduced to a project-selected license.

| Package | Version | Declared license | Source |
| --- | --- | --- | --- |
| `aho-corasick` | 1.1.5 | `Unlicense OR MIT` | crates.io |
| `anyhow` | 1.0.104 | `MIT OR Apache-2.0` | crates.io |
| `arrayvec` | 0.7.8 | `MIT OR Apache-2.0` | crates.io |
| `blake3` | 1.8.7 | `CC0-1.0 OR Apache-2.0 OR Apache-2.0 WITH LLVM-exception` | crates.io |
| `block-buffer` | 0.10.4 | `MIT OR Apache-2.0` | crates.io |
| `block-buffer` | 0.12.1 | `MIT OR Apache-2.0` | crates.io |
| `bstr` | 1.13.1 | `MIT OR Apache-2.0` | crates.io |
| `bytes` | 1.12.1 | `MIT` | crates.io |
| `cfg-if` | 1.0.4 | `MIT OR Apache-2.0` | crates.io |
| `const-oid` | 0.10.2 | `Apache-2.0 OR MIT` | crates.io |
| `constant_time_eq` | 0.4.2 | `CC0-1.0 OR MIT-0 OR Apache-2.0` | crates.io |
| `core_detect` | 1.0.0 | `MIT/Apache-2.0` | crates.io |
| `cpufeatures` | 0.2.17 | `MIT OR Apache-2.0` | crates.io |
| `cpufeatures` | 0.3.1 | `MIT OR Apache-2.0` | crates.io |
| `crossbeam-deque` | 0.8.8 | `MIT OR Apache-2.0` | crates.io |
| `crossbeam-epoch` | 0.9.21 | `MIT OR Apache-2.0` | crates.io |
| `crossbeam-utils` | 0.8.23 | `MIT OR Apache-2.0` | crates.io |
| `crypto-common` | 0.1.7 | `MIT OR Apache-2.0` | crates.io |
| `crypto-common` | 0.2.2 | `MIT OR Apache-2.0` | crates.io |
| `digest` | 0.10.7 | `MIT OR Apache-2.0` | crates.io |
| `digest` | 0.11.3 | `MIT OR Apache-2.0` | crates.io |
| `either` | 1.18.0 | `MIT OR Apache-2.0` | crates.io |
| `encoding_rs` | 0.8.40 | `(Apache-2.0 OR MIT) AND BSD-3-Clause` | crates.io |
| `equivalent` | 1.0.2 | `Apache-2.0 OR MIT` | crates.io |
| `generic-array` | 0.14.7 | `MIT` | crates.io |
| `globset` | 0.4.20 | `Unlicense OR MIT` | crates.io |
| `hashbrown` | 0.17.1 | `MIT OR Apache-2.0` | crates.io |
| `hybrid-array` | 0.4.14 | `MIT OR Apache-2.0` | crates.io |
| `ignore` | 0.4.33 | `Unlicense OR MIT` | crates.io |
| `indexmap` | 2.14.2 | `Apache-2.0 OR MIT` | crates.io |
| `itertools` | 0.14.0 | `MIT OR Apache-2.0` | crates.io |
| `itoa` | 1.0.18 | `MIT OR Apache-2.0` | crates.io |
| `libc` | 0.2.189 | `MIT OR Apache-2.0` | crates.io |
| `log` | 0.4.34 | `MIT OR Apache-2.0` | crates.io |
| `memchr` | 2.8.3 | `Unlicense OR MIT` | crates.io |
| `memmap2` | 0.9.11 | `MIT OR Apache-2.0` | crates.io |
| `multiversion` | 0.8.0 | `MIT OR Apache-2.0` | crates.io |
| `multiversion-macros` | 0.8.0 | `MIT OR Apache-2.0` | crates.io |
| `multiversion_no_op` | 1.0.0 | `Apache-2.0 OR MIT` | crates.io |
| `proc-macro2` | 1.0.107 | `MIT OR Apache-2.0` | crates.io |
| `prost` | 0.14.4 | `Apache-2.0` | crates.io |
| `prost-derive` | 0.14.4 | `Apache-2.0` | crates.io |
| `quote` | 1.0.47 | `MIT OR Apache-2.0` | crates.io |
| `rayon` | 1.12.0 | `MIT OR Apache-2.0` | crates.io |
| `rayon-core` | 1.13.0 | `MIT OR Apache-2.0` | crates.io |
| `regex` | 1.13.1 | `MIT OR Apache-2.0` | crates.io |
| `regex-automata` | 0.4.18 | `MIT OR Apache-2.0` | crates.io |
| `regex-syntax` | 0.8.11 | `MIT OR Apache-2.0` | crates.io |
| `same-file` | 1.0.6 | `Unlicense/MIT` | crates.io |
| `scopeguard` | 1.2.0 | `MIT OR Apache-2.0` | crates.io |
| `serde` | 1.0.229 | `MIT OR Apache-2.0` | crates.io |
| `serde_core` | 1.0.229 | `MIT OR Apache-2.0` | crates.io |
| `serde_derive` | 1.0.229 | `MIT OR Apache-2.0` | crates.io |
| `serde_json` | 1.0.151 | `MIT OR Apache-2.0` | crates.io |
| `sha1` | 0.10.7 | `MIT OR Apache-2.0` | crates.io |
| `sha2` | 0.11.0 | `MIT OR Apache-2.0` | crates.io |
| `simdutf8` | 0.1.5 | `MIT OR Apache-2.0` | crates.io |
| `streaming-iterator` | 0.1.9 | `MIT OR Apache-2.0` | crates.io |
| `syn` | 2.0.119 | `MIT OR Apache-2.0` | crates.io |
| `syn` | 3.0.5 | `MIT OR Apache-2.0` | crates.io |
| `target-features` | 0.1.6 | `MIT OR Apache-2.0` | crates.io |
| `tgrep-core` | 1.0.4 | `MIT` | Git revision `e2007b52d2b8fe4176159d0da20c9ba4a46d5aab` |
| `tree-sitter` | 0.27.0 | `MIT` | crates.io |
| `tree-sitter-language` | 0.1.8 | `MIT` | crates.io |
| `tree-sitter-rust` | 0.24.2 | `MIT` | crates.io |
| `typenum` | 1.20.1 | `MIT OR Apache-2.0` | crates.io |
| `unicode-ident` | 1.0.24 | `(MIT OR Apache-2.0) AND Unicode-3.0` | crates.io |
| `walkdir` | 2.5.0 | `Unlicense/MIT` | crates.io |
| `winapi-util` | 0.1.11 | `Unlicense OR MIT` | crates.io |
| `windows-link` | 0.2.1 | `MIT OR Apache-2.0` | crates.io |
| `windows-sys` | 0.61.2 | `MIT OR Apache-2.0` | crates.io |
| `zmij` | 1.0.23 | `MIT` | crates.io |

The direct normal declarations in `crates/ri/Cargo.toml` are `prost`, `regex`,
`serde`, `serde_json`, `sha1`, `sha2`, `tgrep-core`, `tree-sitter`, and
`tree-sitter-rust`. Versions above are resolved versions, so, for example, the
`sha1 = "0.10.6"` requirement resolves to 0.10.7.

## Rust build/test-only closure

| Package | Version | Declared license | Notes |
| --- | --- | --- | --- |
| `bitflags` | 2.13.1 | `MIT OR Apache-2.0` | transitive build/test closure |
| `cc` | 1.4.5 | `MIT OR Apache-2.0` | compiles tree-sitter C sources |
| `errno` | 0.3.14 | `MIT OR Apache-2.0` | target-specific transitive closure |
| `fastrand` | 2.5.0 | `Apache-2.0 OR MIT` | transitive build/test closure |
| `find-msvc-tools` | 0.1.12 | `MIT OR Apache-2.0` | compiler discovery during build |
| `fixedbitset` | 0.5.7 | `MIT OR Apache-2.0` | prost-build closure |
| `foldhash` | 0.1.5 | `Zlib` | prost-build closure |
| `getrandom` | 0.4.3 | `MIT OR Apache-2.0` | target-specific transitive closure |
| `hashbrown` | 0.15.5 | `MIT OR Apache-2.0` | prost-build closure |
| `heck` | 0.5.0 | `MIT OR Apache-2.0` | prost-build closure |
| `linux-raw-sys` | 0.12.1 | `Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT` | Linux-target transitive closure |
| `multimap` | 0.10.1 | `MIT OR Apache-2.0` | prost-build closure |
| `once_cell` | 1.21.4 | `MIT OR Apache-2.0` | transitive build/test closure |
| `petgraph` | 0.8.3 | `MIT OR Apache-2.0` | prost-build closure |
| `prettyplease` | 0.2.37 | `MIT OR Apache-2.0` | prost-build closure |
| `prost-build` | 0.14.4 | `Apache-2.0` | direct build dependency |
| `prost-types` | 0.14.4 | `Apache-2.0` | prost-build closure |
| `protoc-bin-vendored` | 3.2.0 | `MIT` | direct build dependency; selects a packaged compiler |
| `protoc-bin-vendored-linux-aarch_64` | 3.2.0 | `MIT` | packaged compiler/proto files for target family |
| `protoc-bin-vendored-linux-ppcle_64` | 3.2.0 | `MIT` | packaged compiler/proto files for target family |
| `protoc-bin-vendored-linux-s390_64` | 3.2.0 | `MIT` | packaged compiler/proto files for target family |
| `protoc-bin-vendored-linux-x86_32` | 3.2.0 | `MIT` | packaged compiler/proto files for target family |
| `protoc-bin-vendored-linux-x86_64` | 3.2.0 | `MIT` | packaged compiler/proto files for target family |
| `protoc-bin-vendored-macos-aarch_64` | 3.2.0 | `MIT` | packaged compiler/proto files for target family |
| `protoc-bin-vendored-macos-x86_64` | 3.2.0 | `MIT` | packaged compiler/proto files for target family |
| `protoc-bin-vendored-win32` | 3.2.0 | `MIT` | packaged compiler/proto files for Windows |
| `r-efi` | 6.0.0 | `MIT OR Apache-2.0 OR LGPL-2.1-or-later` | target-specific transitive closure |
| `rustix` | 1.1.4 | `Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT` | Unix/WASI transitive closure |
| `rustversion` | 1.0.23 | `MIT OR Apache-2.0` | compile-time version selection |
| `shlex` | 2.0.1 | `MIT OR Apache-2.0` | C build closure |
| `tempfile` | 3.27.0 | `MIT OR Apache-2.0` | direct dev dependency and protoc build closure |
| `version_check` | 0.9.5 | `MIT/Apache-2.0` | compile-time version selection |

No resolved package is exclusively development-only after overlap is accounted
for: the only direct dev dependency, `tempfile`, is also reachable through the
build closure.

## Retained build dependency licensing

| Material | Exact provenance and retained evidence | Distribution scope |
| --- | --- | --- |
| `r-efi 6.0.0` | The crates.io package records upstream commit `7e1b0322d31d625f81a5656096330934f9cd835d`. `third_party/dependencies/r-efi/AUTHORS` is byte-equal to that commit, SHA-256 `d027e91dbc9cdbb2f1190068e498bd6b61cff022b6a032b191021ba658d96111`. It is the revision's primary licensing and copyright file; the revision contains no standalone `LICENSE`, `NOTICE`, or `COPYING` files. | Target-specific resolved build/test closure. Retention supports distributions that include this crate; it does not prove contribution to a particular binary. |
| `protoc-bin-vendored 3.2.0` | The wrapper package records upstream commit `895c0433c3727a552970ce961e398e20e52d6353`. `third_party/dependencies/protoc-bin-vendored/LICENSE.txt` is byte-equal to that commit, SHA-256 `97647e63047ef75a82ee2928b335df94f45c87e08777dc033393c73294f3a57a`. | Build-tool wrapper and platform package source/tooling distributions. No claim that the wrapper is linked into `engorch-ri`. |
| Google Protocol Buffers compiler v31.1 payload | The platform crates record `v31.1`. The annotated upstream tag resolves to commit `74211c0dfc2777318ab53c2cd2c317a2ef9012de`. `third_party/dependencies/protobuf-v31.1/LICENSE` is byte-equal to that commit, SHA-256 `6e5e117324afd944dcf67f36cf329843bc1a92229a8cd9bb573d7a83130fea7d`; that upstream tree has no root `NOTICE`. | Google-built compiler binaries and `.proto` includes embedded in the platform crates. No claim that these build-time files are linked into `engorch-ri`. |

## Evidence limits and concrete gaps

- Local primary package directories were inspected for all 104 external Rust
  packages. Every package exposes a license expression, but local license text
  files were absent for `protoc-bin-vendored 3.2.0`, its eight platform packages,
  `r-efi 6.0.0`, and the `tgrep-core` package subdirectory. The exact upstream
  licensing material for these exceptions is now retained under `third_party`.
  For r-efi, upstream itself provides its triple-license terms and notices in
  `AUTHORS` rather than standalone full Apache/LGPL files.
- Most Cargo registry license texts are available in the local package cache but
  are not copied into the repository or local package. A release-time tool must
  derive the exact target-specific shipped closure and collect the applicable
  texts/notices. This document alone is not that collector.
- The local package inclusion list now contains `THIRD_PARTY.md` and the retained
  dependency licensing files. This is a static script check only; produce and
  verify a new package after the shared source tree is stable, and do not reuse
  the earlier development package.
- Static-link membership and target pruning were not inspected from a final
  release binary. A final package audit must bind its dependency report to the
  exact target, binary hashes, source commit, and resolved lock files.

## Qualification runtime patch artifact

The exact R29/R31 OpenCode diagnostic diff is included under
[third_party/opencode/qualification](third_party/opencode/qualification/README.md),
with upstream revision, retained MIT notice and build limitations. No OpenCode
binary or dependency tree is distributed.
