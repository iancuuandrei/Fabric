# SCIP protocol

The unmodified `scip.proto` is from sourcegraph/scip revision
`1c2b6db7e560d5233c944f36e4ac1377cc6963fc`, under Apache-2.0.
The upstream license is retained alongside it. Rust bindings are generated at
build time using Prost and the Cargo-locked bundled protoc compiler.

EngOrch owns the admission rules around these bindings. Decoding a protobuf
does not establish repository identity, producer authenticity or completeness.
