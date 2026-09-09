//! Disk admission uses explicit artifact selection and exact input hashes.
use engorch_ri::{
    import::{self, Policy, Request},
    manifest::{Input, Manifest, Producer, Source},
    scip::wire,
};
use prost::Message;
use sha2::{Digest, Sha256};

#[test]
fn explicit_disk_import_preserves_binding_and_rejects_substitution() {
    let root = std::env::temp_dir().join(format!("engorch-import-{}", std::process::id()));
    std::fs::create_dir(&root).unwrap();
    let path = root.join("index.scip");
    let bytes = wire::Index {
        metadata: Some(wire::Metadata {
            tool_info: Some(wire::ToolInfo {
                name: "fixture".into(),
                version: "1".into(),
                arguments: vec![],
            }),
            project_root: "file:///not-a-disk-authority".into(),
            text_document_encoding: 1,
            ..Default::default()
        }),
        ..Default::default()
    }
    .encode_to_vec();
    std::fs::write(&path, &bytes).unwrap();
    let source = Source {
        repository_id: "a".repeat(64),
        object_format: "sha1".into(),
        commit: "b".repeat(40),
        tree: "c".repeat(40),
    };
    let make = || Request {
        index_path: path.to_str().unwrap().into(),
        source: source.clone(),
        producer: "p".into(),
        project_root: "file:///not-a-disk-authority".into(),
        policy: Policy::Strict,
        sources: Default::default(),
        manifest: Manifest {
            format: 1,
            source: source.clone(),
            producers: vec![Producer {
                id: "p".into(),
                name: "fixture".into(),
                version: "1".into(),
                artifact_sha256: "d".repeat(64),
                inputs: vec![Input {
                    name: "scip:index".into(),
                    sha256: Sha256::digest(&bytes)
                        .iter()
                        .map(|b| format!("{b:02x}"))
                        .collect(),
                }],
            }],
        },
    };
    let artifact = import::build(make()).unwrap();
    engorch_ri::query::Snapshot::read(&artifact.bytes, &artifact.id, &source).unwrap();
    let staging = root.join("snapshot.staging");
    let message = engorch_ri::canonical::encode(&serde_json::json!({
        "operation": "scip_import", "request": make(), "output_path": staging.to_str().unwrap()
    }))
    .unwrap();
    let invoke = || {
        use std::io::Write;
        use std::process::{Command, Stdio};
        let mut child = Command::new(env!("CARGO_BIN_EXE_engorch-ri"))
            .arg("--stdio")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()
            .unwrap();
        child.stdin.take().unwrap().write_all(&message).unwrap();
        child.wait_with_output().unwrap()
    };
    let response = invoke();
    assert!(response.status.success());
    let response: serde_json::Value = serde_json::from_slice(&response.stdout).unwrap();
    assert_eq!(response["result"]["snapshot_id"], artifact.id);
    assert_eq!(response["result"]["bytes"], artifact.bytes.len());
    assert_eq!(std::fs::read(&staging).unwrap(), artifact.bytes);
    assert!(!invoke().status.success());
    assert_eq!(std::fs::read(&staging).unwrap(), artifact.bytes);
    std::fs::remove_file(staging).unwrap();
    assert!(import::read_file(path.to_str().unwrap(), bytes.len() - 1).is_err());
    assert!(import::read_file("relative.scip", 100).is_err());
    assert!(import::read_file(root.to_str().unwrap(), 100).is_err());
    let mut unbound = make();
    unbound
        .sources
        .insert("unbound.rs".into(), path.to_str().unwrap().into());
    assert!(import::build(unbound).is_err());
    std::fs::write(&path, b"substituted").unwrap();
    assert!(import::build(make()).is_err());
    std::fs::remove_file(path).unwrap();
    std::fs::remove_dir(root).unwrap();
}
