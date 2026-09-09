//! Opt-in test of an actual scip-go binary artifact over the repository fixture.
use engorch_ri::{
    manifest::{Input, Manifest, Producer, Source},
    scip,
};
use std::collections::BTreeMap;

fn hash(bytes: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

#[test]
#[ignore = "requires ENGORCH_SCIP_GO_INDEX and ENGORCH_SCIP_GO_BINARY from an actual producer run"]
fn real_scip_go_artifact_reaches_source_validated_snapshot() {
    let bytes = std::fs::read(std::env::var("ENGORCH_SCIP_GO_INDEX").unwrap()).unwrap();
    let executable = std::fs::read(std::env::var("ENGORCH_SCIP_GO_BINARY").unwrap()).unwrap();
    let source = std::fs::read(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../testdata/scip-go/library.go"
    ))
    .unwrap();
    let decoded = scip::decode(&bytes).unwrap();
    let root = decoded.metadata.as_ref().unwrap().project_root.clone();
    // Synthetic repository identity: this test qualifies the producer/importer,
    // not a controller observation of a committed checkout.
    let expected = Source {
        repository_id: "a".repeat(64),
        object_format: "sha1".into(),
        commit: "b".repeat(40),
        tree: "c".repeat(40),
    };
    let mut manifest = Manifest {
        format: 1,
        source: expected.clone(),
        producers: vec![Producer {
            id: "go-semantic".into(),
            name: "scip-go".into(),
            version: "0.2.7".into(),
            artifact_sha256: hash(&executable),
            inputs: vec![
                Input {
                    name: "scip:index".into(),
                    sha256: hash(&bytes),
                },
                Input {
                    name: "source:library.go".into(),
                    sha256: hash(&source),
                },
                Input {
                    name: "scip:position-policy".into(),
                    sha256: hash(scip::SCIP_GO_027_POSITION_POLICY.as_bytes()),
                },
            ],
        }],
    };
    let sources = BTreeMap::from([("library.go".into(), source)]);
    assert!(
        scip::admit(
            &bytes,
            manifest.clone(),
            &expected,
            "go-semantic",
            &root,
            &sources
        )
        .is_err()
    );
    let admitted = scip::admit_scip_go_027(
        &bytes,
        manifest.clone(),
        &expected,
        "go-semantic",
        &root,
        &sources,
    )
    .unwrap();
    let artifact = admitted.snapshot(&sources).unwrap();
    let loaded = engorch_ri::snapshot::read(&artifact.bytes, &artifact.id, &expected).unwrap();
    assert!(
        loaded
            .occurrences()
            .records()
            .iter()
            .any(|o| o.spelling == "persoană" && o.roles.unwrap().is_definition())
    );
    assert!(
        loaded
            .occurrences()
            .records()
            .iter()
            .any(|o| o.spelling == "persoană" && !o.roles.unwrap().is_definition())
    );
    assert_eq!(loaded.occurrences().records().len(), 21);
    manifest.producers[0].inputs.pop();
    assert!(
        scip::admit_scip_go_027(&bytes, manifest, &expected, "go-semantic", &root, &sources)
            .is_err()
    );
}
