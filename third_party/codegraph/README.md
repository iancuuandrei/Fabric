# CodeGraph adaptation

Source: [CodeGraph](https://github.com/colbymchenry/codegraph), revision
`b9ca4b7981116909900368cc1686a1074cd4d4c1`.

`crates/ri/src/identifiers.rs` adapts `splitIdentifierSegments` from the inspected
[`src/search/identifier-segments.ts`](https://github.com/colbymchenry/codegraph/blob/b9ca4b7981116909900368cc1686a1074cd4d4c1/src/search/identifier-segments.ts)
source. The camel-case and acronym boundary algorithm
is ported from TypeScript regular expressions to a bounded Rust character scan.
The original MIT license is retained in [LICENSE](LICENSE).

Changes: preserve short, numeric and later segments; reject oversized input
explicitly; use Rust Unicode alphabetic/numeric properties; retain first-seen
ordering; do not strip accents. The original prompt/prose gate, stopword list,
rarity ranking and segment caps are not part of this adaptation. This code is
for deterministic symbol lookup, without authority over task context selection.
