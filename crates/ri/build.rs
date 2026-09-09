//! Generate the complete pinned SCIP protocol with a bundled compiler.
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let schema = "../../third_party/scip/scip.proto";
    println!("cargo:rerun-if-changed={schema}");
    prost_build::Config::new()
        // The upstream prose contains BNF and diagrams, not Rust doctests.
        // Keep that documentation in the unchanged vendored schema.
        .disable_comments(["."])
        .protoc_executable(protoc_bin_vendored::protoc_bin_path()?)
        .compile_protos(&[schema], &["../../third_party/scip"])?;
    Ok(())
}
