# protoc-bin-vendored provenance

Cargo resolves `protoc-bin-vendored 3.2.0` and its eight platform packages.
The wrapper crate's `.cargo_vcs_info.json` binds it to upstream commit
`895c0433c3727a552970ce961e398e20e52d6353` at path
`protoc-bin-vendored`. `LICENSE.txt` is an exact copy from that commit and has
SHA-256 `97647e63047ef75a82ee2928b335df94f45c87e08777dc033393c73294f3a57a`.

The MIT text covers the Rust wrapper project. The platform crates also carry
Google-built Protocol Buffer compiler v31.1 payloads and Google `.proto`
includes; their distinct upstream license is retained in the adjacent
`../protobuf-v31.1` directory.

These crates are build dependencies. Retention here covers source or tooling
packages that include the wrapper or platform payloads; it does not claim that
the compiler or wrapper is linked into the final `engorch-ri` binary.
