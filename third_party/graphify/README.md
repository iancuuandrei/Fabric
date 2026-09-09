# Graphify adaptation

`crates/ri/src/structural.rs` adapts `_rust_collect_type_refs` from
[graphify/extractors/rust.py](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/graphify/extractors/rust.py),
revision `c9f99018774e2e0380e9f65b3959944559a0d5f6`.

The adaptation ports the type-expression walker to Rust and tree-sitter Rust
bindings. It accepts immutable bytes, retains qualified spellings and exact byte
ranges, uses iterative traversal with explicit limits, and reports incomplete
semantic coverage. It does not adopt Graphify's method blocklist or heuristic
symbol-resolution decisions. Repeated source occurrences remain distinguishable.

The upstream [LICENSE](LICENSE), [NOTICE](NOTICE) and legacy
[LICENSE-MIT](LICENSE-MIT) are retained. Copyright 2026 Safi Shamsi and the
Graphify contributors; see those files for applicable terms and attribution.
