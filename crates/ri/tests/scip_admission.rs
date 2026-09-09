//! Source and producer substitution tests for binary index admission.
use engorch_ri::{
    manifest::{Input, Manifest, Producer, Source},
    scip::{admit, wire},
};
use prost::Message;
use std::collections::BTreeMap;

fn hash(bytes: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

fn fixture() -> (wire::Index, Manifest, BTreeMap<String, Vec<u8>>) {
    let index = wire::Index {
        metadata: Some(wire::Metadata {
            project_root: "file:///fixture".into(),
            text_document_encoding: 1,
            tool_info: Some(wire::ToolInfo {
                name: "fixture-indexer".into(),
                version: "1".into(),
                arguments: vec![],
            }),
            ..Default::default()
        }),
        documents: vec![wire::Document {
            relative_path: "a.rs".into(),
            position_encoding: 1,
            ..Default::default()
        }],
        ..Default::default()
    };
    let sources = BTreeMap::from([("a.rs".into(), b"fn a() {}".to_vec())]);
    let manifest = Manifest {
        format: 1,
        source: Source {
            repository_id: "a".repeat(64),
            object_format: "sha1".into(),
            commit: "b".repeat(40),
            tree: "c".repeat(40),
        },
        producers: vec![Producer {
            id: "semantic".into(),
            name: "fixture-indexer".into(),
            version: "1".into(),
            artifact_sha256: "d".repeat(64),
            inputs: vec![
                Input {
                    name: "scip:index".into(),
                    sha256: hash(&index.encode_to_vec()),
                },
                Input {
                    name: "source:a.rs".into(),
                    sha256: hash(&sources["a.rs"]),
                },
            ],
        }],
    };
    (index, manifest, sources)
}

#[test]
fn exact_inputs_admit_but_substitutions_do_not() {
    let (index, manifest, sources) = fixture();
    let bytes = index.encode_to_vec();
    let run = |m: Manifest, s: &BTreeMap<String, Vec<u8>>, root: &str| {
        admit(&bytes, m, &manifest.source, "semantic", root, s)
    };
    let admitted = run(manifest.clone(), &sources, "file:///fixture").unwrap();
    assert_eq!(admitted.index(), &index);
    assert_eq!(admitted.producer(), "semantic");
    assert_eq!(admitted.manifest().source, manifest.source);
    assert!(run(manifest.clone(), &sources, "file:///other").is_err());
    assert!(run(manifest.clone(), &BTreeMap::new(), "file:///fixture").is_err());
    let mut changed = sources.clone();
    changed.insert("a.rs".into(), b"fn b() {}".to_vec());
    assert!(run(manifest.clone(), &changed, "file:///fixture").is_err());
    let mut changed = manifest.clone();
    changed.producers[0].version = "2".into();
    assert!(run(changed, &sources, "file:///fixture").is_err());
    let mut changed = manifest.clone();
    changed.source.commit = "e".repeat(40);
    assert!(run(changed, &sources, "file:///fixture").is_err());
    let mut changed = manifest.clone();
    changed.producers[0].inputs[0].sha256 = "0".repeat(64);
    assert!(run(changed, &sources, "file:///fixture").is_err());
}

#[test]
fn duplicate_documents_and_ambiguous_encodings_fail_even_with_matching_hash() {
    let (index, manifest, sources) = fixture();
    for mutation in 0..4 {
        let mut index = index.clone();
        match mutation {
            0 => index.documents.push(index.documents[0].clone()),
            1 => index.documents[0].relative_path = "../a.rs".into(),
            2 => index.documents[0].position_encoding = 0,
            _ => index.metadata.as_mut().unwrap().text_document_encoding = 2,
        }
        let bytes = index.encode_to_vec();
        let mut m = manifest.clone();
        m.producers[0].inputs[0].sha256 = hash(&bytes);
        assert!(
            admit(
                &bytes,
                m,
                &manifest.source,
                "semantic",
                "file:///fixture",
                &sources
            )
            .is_err()
        );
    }
}

#[test]
fn symbol_identity_scopes_locals_without_normalizing_globals() {
    use engorch_ri::scip::SymbolIdentity as S;
    let a = S::new("p", "local 1", Some("a.rs")).unwrap();
    let b = S::new("p", "local 1", Some("b.rs")).unwrap();
    assert_ne!(a.id, b.id);
    assert_ne!(a.id, S::new("other", "local 1", Some("a.rs")).unwrap().id);
    let global = "scip-rust cargo package 1.0 Thing#";
    assert_eq!(
        S::new("p", global, Some("a.rs")).unwrap(),
        S::new("p", global, Some("b.rs")).unwrap()
    );
    assert_eq!(S::new("p", global, None).unwrap().raw, global);
    for raw in ["local ", "local x/y", "local x y", "localish x"] {
        assert!(S::new("p", raw, Some("a.rs")).is_err());
    }
    assert!(S::new("p", "local 1", None).is_err());
}

#[test]
fn admitted_symbols_include_external_relationship_targets() {
    let (mut index, mut manifest, sources) = fixture();
    let global = "scip-rust cargo package 1.0 Thing#";
    let target = "scip-rust cargo package 1.0 Trait#";
    index.external_symbols.push(wire::SymbolInformation {
        symbol: global.into(),
        relationships: vec![wire::Relationship {
            symbol: target.into(),
            is_implementation: true,
            ..Default::default()
        }],
        ..Default::default()
    });
    let bytes = index.encode_to_vec();
    manifest.producers[0].inputs[0].sha256 = hash(&bytes);
    let admitted = admit(
        &bytes,
        manifest.clone(),
        &manifest.source,
        "semantic",
        "file:///fixture",
        &sources,
    )
    .unwrap();
    let symbols = admitted.symbols().unwrap();
    assert_eq!(symbols.len(), 2);
    assert!(symbols.iter().any(|s| s.raw == target));
    assert!(symbols.iter().all(|s| s.document.is_none()));
    let edges = admitted.relationship_edges().unwrap();
    assert_eq!(edges.len(), 1);
    assert_eq!(edges[0].relation, engorch_ri::graph::Relation::Implements);
    assert_eq!(edges[0].quality, engorch_ri::graph::Quality::Declared);
    let from = symbols.iter().find(|s| s.raw == global).unwrap();
    let to = symbols.iter().find(|s| s.raw == target).unwrap();
    assert_eq!(edges[0].from, from.id);
    assert_eq!(edges[0].to, to.id);
    index.external_symbols[0].symbol = "local 1".into();
    let bytes = index.encode_to_vec();
    manifest.producers[0].inputs[0].sha256 = hash(&bytes);
    let admitted = admit(
        &bytes,
        manifest.clone(),
        &manifest.source,
        "semantic",
        "file:///fixture",
        &sources,
    )
    .unwrap();
    assert!(admitted.symbols().is_err());
    assert!(admitted.relationship_edges().is_err());
}

#[test]
fn relationship_flags_are_distinct_and_duplicate_declarations_coalesce() {
    let (mut index, mut manifest, sources) = fixture();
    let relationship = wire::Relationship {
        symbol: "scip-rust cargo p 1 Target#".into(),
        is_reference: true,
        is_implementation: true,
        is_type_definition: true,
        is_definition: true,
    };
    index.documents[0].symbols.push(wire::SymbolInformation {
        symbol: "local 1".into(),
        relationships: vec![relationship.clone(), relationship],
        ..Default::default()
    });
    let bytes = index.encode_to_vec();
    manifest.producers[0].inputs[0].sha256 = hash(&bytes);
    let admitted = admit(
        &bytes,
        manifest.clone(),
        &manifest.source,
        "semantic",
        "file:///fixture",
        &sources,
    )
    .unwrap();
    let edges = admitted.relationship_edges().unwrap();
    assert_eq!(edges.len(), 4);
    let categories: std::collections::BTreeSet<_> = edges.iter().map(|e| e.relation).collect();
    use engorch_ri::graph::Relation::*;
    assert_eq!(
        categories,
        [
            ReferenceRelated,
            Implements,
            TypeDefinition,
            DefinitionRelated
        ]
        .into_iter()
        .collect()
    );
    assert!(!categories.contains(&References));
    assert!(!categories.contains(&Defines));
}

#[test]
#[allow(deprecated)]
fn binary_index_to_snapshot_preserves_local_scope_roles_and_source_binding() {
    let (mut index, mut manifest, mut sources) = fixture();
    index.documents[0].occurrences = vec![
        wire::Occurrence {
            range: vec![0, 3, 4],
            symbol: "local 1".into(),
            symbol_roles: 1,
            ..Default::default()
        },
        wire::Occurrence {
            range: vec![0, 3, 4],
            symbol: "local 1".into(),
            symbol_roles: 0,
            ..Default::default()
        },
    ];
    let mut second = index.documents[0].clone();
    second.relative_path = "b.rs".into();
    index.documents.push(second);
    sources.insert("b.rs".into(), sources["a.rs"].clone());
    manifest.producers[0].inputs.push(Input {
        name: "source:b.rs".into(),
        sha256: hash(&sources["b.rs"]),
    });
    let bytes = index.encode_to_vec();
    manifest.producers[0].inputs[0].sha256 = hash(&bytes);
    let admitted = admit(
        &bytes,
        manifest.clone(),
        &manifest.source,
        "semantic",
        "file:///fixture",
        &sources,
    )
    .unwrap();
    let artifact = admitted.snapshot(&sources).unwrap();
    let loaded =
        engorch_ri::snapshot::read(&artifact.bytes, &artifact.id, &manifest.source).unwrap();
    assert_eq!(loaded.occurrences().records().len(), 4);
    let a = engorch_ri::scip::SymbolIdentity::new("semantic", "local 1", Some("a.rs")).unwrap();
    let b = engorch_ri::scip::SymbolIdentity::new("semantic", "local 1", Some("b.rs")).unwrap();
    assert_ne!(a.id, b.id);
    assert_eq!(loaded.occurrences().definitions(&a.id).len(), 1);
    assert_eq!(loaded.occurrences().references(&a.id).len(), 1);
    assert_eq!(loaded.occurrences().definitions(&b.id)[0].path, "b.rs");
    assert_eq!(loaded.graph().counts(), (4, 4, 0));
    let name = loaded.graph().node(&a.id).unwrap().scip.as_ref().unwrap();
    assert_eq!(name.symbol, "local 1");
    assert_eq!(name.producer, "semantic");
    let handle =
        engorch_ri::query::Snapshot::read(&artifact.bytes, &artifact.id, &manifest.source).unwrap();
    assert_eq!(
        handle
            .scip_symbol("semantic", "local 1", Some("a.rs"))
            .unwrap()
            .unwrap()
            .id,
        a.id
    );
    assert_eq!(
        handle
            .scip_symbol("semantic", "local 1", Some("b.rs"))
            .unwrap()
            .unwrap()
            .id,
        b.id
    );
    assert!(handle.scip_symbol("semantic", "local 1", None).is_err());
    assert!(
        handle
            .scip_symbol("semantic", "local 2", Some("a.rs"))
            .unwrap()
            .is_none()
    );
    assert!(
        handle
            .scip_symbol("unknown", "local 1", Some("a.rs"))
            .is_err()
    );
    let mut altered = Vec::new();
    for line in artifact
        .bytes
        .split(|b| *b == b'\n')
        .filter(|line| !line.is_empty())
    {
        let mut record: serde_json::Value = serde_json::from_slice(line).unwrap();
        if record["kind"] == "node" && record["value"]["id"] == a.id {
            record["value"]["scip"]["symbol"] = "local 2".into();
        }
        altered.extend(engorch_ri::canonical::encode(&record).unwrap());
        altered.push(b'\n');
    }
    let altered_hash = engorch_ri::canonical::hash("harness.ri.snapshot.v1", &altered);
    assert!(engorch_ri::snapshot::read(&altered, &altered_hash, &manifest.source).is_err());
    assert_eq!(artifact.bytes, admitted.snapshot(&sources).unwrap().bytes);
    sources.insert("a.rs".into(), b"fn z() {}".to_vec());
    assert!(admitted.snapshot(&sources).is_err());
}
