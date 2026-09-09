//! Structural extraction tests using the real Rust grammar.

use engorch_ri::{
    graph::Completeness,
    structural::{TypeRole, rust_types},
};

#[test]
fn oversized_ast_returns_error_instead_of_partial_occurrences() {
    let source = format!("struct Large {{ {} }}", "field: Type,".repeat(40_000));
    assert!(source.len() < 1 << 20);
    assert!(rust_types(source.as_bytes()).is_err());
}

#[test]
fn preserves_qualified_types_generic_roles_and_exact_spans() {
    let source = "// șir\nfn f(x: Option<&crate::model::Item>) -> std::result::Result<Other, Error> { todo!() }";
    let result = rust_types(source.as_bytes()).unwrap();
    assert!(!result.syntax_errors);
    assert_eq!(result.semantic_coverage, Completeness::Partial);
    let names: Vec<_> = result.references.iter().map(|r| r.name.as_str()).collect();
    assert_eq!(
        names,
        [
            "Option",
            "crate::model::Item",
            "std::result::Result",
            "Other",
            "Error"
        ]
    );
    assert_eq!(result.references[1].role, TypeRole::GenericArgument);
    assert_eq!(result.references[2].role, TypeRole::Type);
    for reference in result.references {
        assert_eq!(
            &source[reference.start_byte..reference.end_byte],
            reference.name
        );
    }
}

#[test]
fn occurrences_are_not_merged_or_promoted_to_semantic_absence() {
    let source = b"struct S { a: crate::A, b: (crate::A, [B; 2], u32) }";
    let result = rust_types(source).unwrap();
    assert_eq!(
        result
            .references
            .iter()
            .map(|r| r.name.as_str())
            .collect::<Vec<_>>(),
        ["crate::A", "crate::A", "B"]
    );
    assert_ne!(
        result.references[0].start_byte,
        result.references[1].start_byte
    );
    let empty = rust_types(b"fn f(x: u32) {}").unwrap();
    assert!(empty.references.is_empty());
    assert_eq!(empty.semantic_coverage, Completeness::Partial);
    let broken = rust_types(b"fn broken( -> { let x:").unwrap();
    assert!(broken.syntax_errors);
    assert_eq!(broken.semantic_coverage, Completeness::Partial);
    assert!(rust_types(&[255]).is_err());
    assert!(rust_types(&vec![b' '; (1 << 20) + 1]).is_err());
}

#[test]
fn actual_process_structural_operation_binds_exact_source_bytes() {
    use engorch_ri::canonical;
    use std::{
        io::Write,
        process::{Command, Stdio},
    };
    let source = "type Alias = crate::Thing;";
    let expected = rust_types(source.as_bytes()).unwrap();
    for hash in [expected.source_sha256.clone(), "0".repeat(64)] {
        let request =
            serde_json::json!({"operation":"rust_types","source_text":source,"source_sha256":hash});
        let mut child = Command::new(env!("CARGO_BIN_EXE_engorch-ri"))
            .arg("--stdio")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()
            .unwrap();
        child
            .stdin
            .take()
            .unwrap()
            .write_all(&canonical::encode(&request).unwrap())
            .unwrap();
        let output = child.wait_with_output().unwrap();
        let response: serde_json::Value = canonical::decode(&output.stdout).unwrap();
        if hash == expected.source_sha256 {
            assert!(output.status.success());
            assert_eq!(response["result"]["references"][0]["name"], "crate::Thing");
        } else {
            assert!(!output.status.success());
            assert_eq!(response["ok"], false);
        }
    }
}
