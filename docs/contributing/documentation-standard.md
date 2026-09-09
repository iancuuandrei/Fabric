# Documentation standard

Normative project convention, informed by the
[research synthesis](../research/documentation-practices-2026.md).

Documentation MUST change with the behavior it describes. Each fact has one
authoritative home: specifications define invariants, ADRs explain decisions,
source comments define API use, and guides explain tasks. Historical decisions
MUST NOT masquerade as current implementation status.

Use plain language, sentence-case headings, working relative links and the
simplest executable example first. Commands omit shell prompts. Separate local,
fixture, provider and hosted evidence. Unknown results are never zero or PASS.

Significant Go packages use `doc.go`; Rust crates use module-level rustdoc.
Exported APIs document caller-visible behavior, errors, effects, lifecycle and
concurrency where material. Implementation comments explain non-obvious reasons.
Critical journal, identity, transition and reconciliation functions document
their invariants. Examples SHOULD execute as tests. Do not add README files that
duplicate native package documentation.

Consequential decisions use numbered ADRs with status, date, context/problem,
decision, alternatives, rationale, consequences, compatibility, validation and
references. Status is PROPOSED, ACCEPTED, SUPERSEDED or REJECTED. Specifications
describe accepted contracts using MUST/MUST NOT/SHOULD/MAY; implementation status
is reported separately. A specification is not evidence that code exists.

Checks MUST catch broken local links, duplicate ADR IDs, invalid ADR statuses,
undocumented exported APIs and stale generated references when those references
exist. CLI/config examples must be checked against their actual parser once
implemented. Conceptual prose is authored, not generated.
