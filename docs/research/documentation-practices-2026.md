# Documentation practices research, 2026

Non-normative. Five independent read-only GPT-5.6 Luna researchers investigated
Google, Microsoft, GitHub, Go/Rust, and infrastructure repositories on 2026-09-06.
The maintainer synthesis below selects mechanisms; researcher recommendations
are not project policy. No upstream prose or implementation is copied.

## Findings and decisions

| Source and revision/date | Concrete practice | Decision, application and tradeoff |
| --- | --- | --- |
| [Google engineering practices](https://github.com/google/eng-practices/tree/3bb3ec25b3b0199f4940b1aa75f0ac5c5753301c), retrieved 2026-09-06; archived | Review design, complexity, tests and documentation; distinguish technical defects from preference | Adapt a compact design review checklist. Reviewable internal checkpoints fit our single initial PR; do not import Google's approval organization. |
| [Google documentation guide](https://github.com/google/styleguide/tree/1809c769de31ba388c755ad15dd057a9ba8531fd/docguide), retrieved 2026-09-06 | Minimum viable current docs, same-change maintenance, historical design archives, tests for API contracts | Adopt one authoritative home and same-change updates. Reject a README in every package because native Go/Rust module docs avoid duplicate facts. |
| [Google Go guide](https://github.com/google/styleguide/tree/1809c769de31ba388c755ad15dd057a9ba8531fd/go), retrieved 2026-09-06 | Least mechanism, caller-visible comments, runnable examples | Adapt to idiomatic Go and standard tooling. Comments explain guarantees and reasons rather than repeat signatures. |
| [Microsoft API documentation](https://learn.microsoft.com/en-us/contribute/content/dotnet/api-documentation), updated 2025-06-17 | Source comments are API documentation authority; contextual executable examples | Adapt to Go doc comments and rustdoc, rejecting XML tags and a separate hand-maintained API catalogue. |
| [Azure Go guidelines](https://azure.github.io/azure-sdk/golang_implementation.html), retrieved 2026-09-06 | Explicit context cancellation, diagnosable errors, continuation contracts | Adapt documentation of cancellation and resumption. Stopping observation does not establish whether a remote effect stopped. Reject Azure pipeline and HTTP-specific error conventions. |
| [Microsoft style guide](https://github.com/MicrosoftDocs/microsoft-style-guide), archived 2025-07-28 | Clear, crisp, accessible prose | Adapt as historical editorial advice, not a live normative dependency. |
| [GitHub documentation philosophy](https://docs.github.com/en/contributing/writing-for-github-docs/about-githubs-documentation-philosophy), retrieved 2026-09-06 | Organize by reader outcomes, task-based content, accessibility | Adopt quickstart/concept/how-to/reference separation. Reject Liquid, site infrastructure and versioning machinery at this scale. |
| [GitHub contributor index](https://github.com/github/docs/blob/ec3629a841129ae28189d7bb2274a7b3d40c5095/contributing/README.md), retrieved 2026-09-06 | Small index links to canonical contributor guidance | Adapt concise root CONTRIBUTING and deeper docs. Avoid frontmatter conventions that require a site build. |
| [Go doc comments](https://go.dev/doc/comment), retrieved 2026-09-06 | Exported API comments describe behavior, failures and special cases | Adopt native comments and executable Example tests. Effective Go is useful background but is explicitly not actively updated. |
| [Rust API Guidelines](https://github.com/rust-lang/api-guidelines/tree/97a0969cb07fe4cabb0eed8a56234053f47d83dc), retrieved 2026-09-06 | Crate docs, meaningful errors, examples, panic/safety contracts | Adopt proportional documentation and deny missing public docs. Avoid blanket Clippy restriction lints that conflict or add noise. |
| [go-containerregistry transport rationale](https://github.com/google/go-containerregistry/blob/8a72a424fdecb4caa14f2d525e5d2503331442b5/pkg/v1/remote/transport/README.md), commit 2026-09-04 | Explain boundary rationale near implementation | Adapt package-level responsibility/ownership docs. Do not reproduce registry-specific abstractions. |
| [Microsoft Go migration guide](https://github.com/microsoft/go/blob/a478953b2f7208a1b96241da165393c7462745d1/eng/doc/MigrationGuide.md), commit 2026-09-04 | Toolchain identity and build/runtime parity | Adopt versioned reproducible instructions and final binary measurements. Reject unrelated system-crypto policy. |
| [OpenAI Go generation evidence](https://github.com/openai/openai-go/commit/52e95a974582ebd8d3b23f349e1bcdbbb899c0ac), commit 2026-09-05 | Generated material records inputs and generator identity | Adapt deterministic CLI reference generation. Do not generate conceptual explanations. |
| [Actions runner contribution guide](https://github.com/actions/runner/blob/0b0ac2fdabf53d69add6175026945b8afc8549a5/docs/contribute.md), commit 2026-08-31 | Architectural decisions before implementation; distinguish development artifacts from supported distributions | Adopt meaningful ADRs and precise qualification labels. Reject a mandatory upstream issue-approval ceremony. |
| [Cargo generated documentation change](https://github.com/rust-lang/cargo/commit/77fb9722e3e5ec33ca7adc1e99c9e15e512bb21f), commit 2026-09-05 | Generator and generated reference change together | Adopt drift checks. A generator is justified for finite command/reference data, not every document. |
| [Go failure-handling change](https://github.com/golang/go/commit/38d1265e1a015add1d0b8651f5c2ea3f06199765), commit 2026-09-04 | In-memory partial results do not automatically become reusable durable artifacts | Adapt evidence documentation: retain failed attempts as failure receipts, never present their payloads as qualified successful artifacts. |

## Synthesis

Research favors small documentation with clear authority, executable examples and
failure semantics close to APIs. The project standard will preserve these lessons
without adopting another organization's workflow or site stack. Specifications
will reject unknown authority-bearing fields; a suggestion to preserve arbitrary
provider fields must not become implicit forward-compatible authorization.

The independent researchers proposed some incompatible conventions, including
README-per-package versus native package docs, and broad retry recommendations
versus uncertain-effect reconciliation. We select native package docs and no
automatic retry of UNKNOWN effects. These decisions follow this project's risks.
