//! Snapshot byte identity and record-order qualification.

use engorch_ri::query::{Query, Snapshot};
use engorch_ri::{canonical, graph::*, manifest::*, snapshot};

fn fixture() -> (Manifest, Vec<Node>) {
    (
        Manifest {
            format: 1,
            source: Source {
                repository_id: "a".repeat(64),
                object_format: "sha1".into(),
                commit: "b".repeat(40),
                tree: "c".repeat(40),
            },
            producers: vec![Producer {
                id: "p".into(),
                name: "fixture".into(),
                version: "1".into(),
                artifact_sha256: "d".repeat(64),
                inputs: vec![Input {
                    name: "source".into(),
                    sha256: "e".repeat(64),
                }],
            }],
        },
        vec![
            Node {
                scip: None,
                id: "z".into(),
                kind: NodeKind::File,
                path: Some("z.rs".into()),
            },
            Node {
                scip: None,
                id: "a".into(),
                kind: NodeKind::File,
                path: Some("a.rs".into()),
            },
        ],
    )
}

#[test]
fn semantic_roles_survive_snapshot_and_keep_unknown_separate() {
    let (mut manifest, mut nodes) = fixture();
    let expected = manifest.source.clone();
    let types = engorch_ri::structural::rust_types(b"type A = crate::Thing;").unwrap();
    manifest.producers[0].inputs = vec![Input {
        name: "source:a.rs".into(),
        sha256: types.source_sha256.clone(),
    }];
    nodes.push(Node {
        scip: None,
        id: "thing".into(),
        kind: NodeKind::Symbol,
        path: None,
    });
    let original = types.occurrences("a.rs", "p").unwrap().remove(0);
    let mut records = Vec::new();
    for (id, bits) in [
        ("definition", Some(9)),
        ("reference", Some(0)),
        ("reference2", Some(8)),
        ("unknown", None),
    ] {
        let mut record = original.clone();
        record.id = id.into();
        record.symbol = Some("thing".into());
        record.roles = bits.map(|bits| engorch_ri::scip::Roles::from_scip(bits).unwrap());
        records.push(record);
    }
    let artifact =
        snapshot::build_with_occurrences(manifest, &expected, nodes, vec![], vec![], records)
            .unwrap();
    let loaded = snapshot::read(&artifact.bytes, &artifact.id, &expected).unwrap();
    assert_eq!(loaded.occurrences().for_symbol("thing").len(), 4);
    assert_eq!(
        loaded.occurrences().definitions("thing")[0].id,
        "definition"
    );
    assert_eq!(loaded.occurrences().references("thing")[0].id, "reference");
    assert_eq!(loaded.occurrences().definitions("thing").len(), 1);
    assert_eq!(loaded.occurrences().references("thing").len(), 2);
    let handle = Snapshot::read(&artifact.bytes, &artifact.id, &expected).unwrap();
    let mut locate = engorch_ri::query::LocateQuery {
        path: "a.rs".into(),
        offset: original.span.start,
        producer: "p".into(),
        limit: 1,
    };
    let located = handle.locate(&locate, None).unwrap();
    assert_eq!(located.occurrences.len(), 1);
    assert!(located.next_after.is_some());
    let next = handle
        .locate(&locate, located.next_after.as_deref())
        .unwrap();
    assert_ne!(located.occurrences[0].id, next.occurrences[0].id);
    locate.offset = original.span.end;
    assert!(handle.locate(&locate, None).unwrap().occurrences.is_empty());
    assert!(
        handle
            .locate(&locate, located.next_after.as_deref())
            .is_err()
    );
    locate.path = "missing.rs".into();
    assert!(handle.locate(&locate, None).is_err());
    let mut query = engorch_ri::query::OccurrenceQuery {
        symbol: "thing".into(),
        producer: "p".into(),
        definitions: false,
        limit: 1,
    };
    let first = handle.occurrences(&query, None).unwrap();
    assert_eq!(first.occurrences[0].id, "reference");
    let second = handle
        .occurrences(&query, first.next_after.as_deref())
        .unwrap();
    assert_eq!(second.occurrences[0].id, "reference2");
    assert!(second.next_after.is_none());
    assert!(!second.absence_proven);
    query.definitions = true;
    assert!(
        handle
            .occurrences(&query, first.next_after.as_deref())
            .is_err()
    );
    assert_eq!(
        handle.occurrences(&query, None).unwrap().occurrences[0].id,
        "definition"
    );
    query.limit = 0;
    assert!(handle.occurrences(&query, None).is_err());
    // Exercise the same pagination through the owned binary protocol.
    use std::io::Write;
    use std::process::{Command, Stdio};
    let path = std::env::temp_dir().join(format!(
        "engorch-occurrences-{}-{}.jsonl",
        std::process::id(),
        artifact.id
    ));
    let mut file = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(&path)
        .unwrap();
    file.write_all(&artifact.bytes).unwrap();
    drop(file);
    let invoke = |request: serde_json::Value| {
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
        (output.status.success(), response)
    };
    query.limit = 1;
    query.definitions = false;
    let mut request = serde_json::json!({"operation":"occurrences","path":path,"snapshot_id":artifact.id,"source":expected,"query":query,"after":null});
    let (success, first) = invoke(request.clone());
    assert!(success);
    assert_eq!(first["result"]["occurrences"][0]["id"], "reference");
    request["after"] = first["result"]["next_after"].clone();
    let (success, second) = invoke(request.clone());
    assert!(success);
    assert_eq!(second["result"]["occurrences"][0]["id"], "reference2");
    assert!(second["result"]["next_after"].is_null());
    request["query"]["definitions"] = true.into();
    assert!(!invoke(request).0);
    std::fs::remove_file(path).unwrap();
    let mut value = serde_json::to_value(&original).unwrap();
    for invalid in [-1, 128, 256] {
        value["roles"] = invalid.into();
        assert!(
            serde_json::from_value::<engorch_ri::occurrence::Occurrence>(value.clone()).is_err()
        );
    }
}

#[test]
fn occurrences_roundtrip_only_with_exact_file_and_producer_input() {
    let (mut manifest, nodes) = fixture();
    let expected = manifest.source.clone();
    let types = engorch_ri::structural::rust_types(b"type A = crate::Thing;").unwrap();
    manifest.producers[0].inputs = vec![Input {
        name: "source:a.rs".into(),
        sha256: types.source_sha256.clone(),
    }];
    let occurrences = types.occurrences("a.rs", "p").unwrap();
    let artifact = snapshot::build_with_occurrences(
        manifest.clone(),
        &expected,
        nodes.clone(),
        vec![],
        vec![],
        occurrences.clone(),
    )
    .unwrap();
    let loaded = snapshot::read(&artifact.bytes, &artifact.id, &expected).unwrap();
    assert_eq!(loaded.occurrences().records(), occurrences);
    assert_eq!(loaded.occurrences().at_path("a.rs").len(), 1);
    assert!(loaded.occurrences().for_symbol("crate::Thing").is_empty());
    for field in ["producer", "path", "hash", "range", "symbol"] {
        let mut changed = occurrences.clone();
        match field {
            "producer" => changed[0].producer = "other".into(),
            "path" => changed[0].path = "missing.rs".into(),
            "hash" => changed[0].source_sha256 = "0".repeat(64),
            "range" => changed[0].span.end += 1,
            _ => changed[0].symbol = Some("unregistered-symbol".into()),
        }
        assert!(
            snapshot::build_with_occurrences(
                manifest.clone(),
                &expected,
                nodes.clone(),
                vec![],
                vec![],
                changed
            )
            .is_err(),
            "{field}"
        );
    }
    let mut repeated = occurrences.clone();
    repeated.extend(occurrences);
    assert!(
        snapshot::build_with_occurrences(manifest, &expected, nodes, vec![], vec![], repeated)
            .is_err()
    );
}

#[test]
fn actual_ri_process_reads_disk_artifact_and_rejects_changed_bytes() {
    use std::{
        io::Write,
        process::{Command, Stdio},
    };
    let (manifest, nodes) = fixture();
    let source = manifest.source.clone();
    let artifact = snapshot::build(manifest, &source, nodes, vec![], vec![]).unwrap();
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let directory =
        std::env::temp_dir().join(format!("engorch-ri-process-{}-{nonce}", std::process::id()));
    std::fs::create_dir(&directory).unwrap();
    let path = directory.join("snapshot.jsonl");
    std::fs::write(&path, &artifact.bytes).unwrap();
    let invoke = |request: serde_json::Value| {
        let mut child = Command::new(env!("CARGO_BIN_EXE_engorch-ri"))
            .arg("--stdio")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
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
        (output.status.success(), response)
    };
    let request = serde_json::json!({"operation":"status","path":path,"snapshot_id":artifact.id,"source":source});
    let (success, response) = invoke(request.clone());
    assert!(success);
    assert_eq!(response["result"]["nodes"], 2);
    assert_eq!(response["result"]["producers"], serde_json::json!(["p"]));
    let (success, comparison) = invoke(serde_json::json!({"operation":"changed",
        "before_path":path,"before_id":artifact.id,"before_source":source,
        "after_path":path,"after_id":artifact.id,"after_source":source}));
    assert!(success);
    assert_eq!(comparison["result"]["before_id"], artifact.id);
    assert_eq!(
        comparison["result"]["changes"]["source_identity_changed"],
        false
    );
    assert_eq!(
        comparison["result"]["changes"]["sources"],
        serde_json::json!([])
    );
    let (success, _) = invoke(serde_json::json!({"operation":"changed",
        "before_path":path,"before_id":artifact.id,"before_source":source,
        "after_path":path,"after_id":"0".repeat(64),"after_source":source}));
    assert!(!success);
    assert_eq!(response["result"]["snapshot_id"], artifact.id);
    let query = Query {
        node: "a".into(),
        relation: Relation::Calls,
        direction: Direction::Outgoing,
        producer: "p".into(),
        limit: 1,
    };
    let (success, response) = invoke(
        serde_json::json!({"operation":"neighbors","path":path,"snapshot_id":artifact.id,"source":source,"query":query,"after":null}),
    );
    assert!(success);
    assert_eq!(response["result"]["evidence"]["absence_proven"], false);
    let (success, response) = invoke(
        serde_json::json!({"operation":"coverage","path":path,"snapshot_id":artifact.id,"source":source,"node":"a","relation":"CALLS","direction":"OUTGOING"}),
    );
    assert!(success);
    assert_eq!(response["result"]["declarations"][0]["producer"], "p");
    assert!(response["result"]["declarations"][0]["completeness"].is_null());
    let (success, response) = invoke(
        serde_json::json!({"operation":"path","path":path,"snapshot_id":artifact.id,"source":source,"query":{
            "from":"a","to":"z","relation":"CALLS","direction":"OUTGOING","producer":"p","max_depth":4,"max_edges":100
        }}),
    );
    assert!(success);
    assert_eq!(response["result"]["evidence"]["found"], false);
    assert_eq!(response["result"]["evidence"]["absence_proven"], false);
    std::fs::write(&path, b"changed\n").unwrap();
    let (success, response) = invoke(request);
    assert!(!success);
    assert_eq!(response["ok"], false);
    std::fs::remove_file(&path).unwrap();
    std::fs::remove_dir(&directory).unwrap();
}

#[test]
fn query_pages_bind_every_parameter_and_snapshot() {
    let (manifest, nodes) = fixture();
    let source = manifest.source.clone();
    let edges: Vec<_> = ["one", "two", "three"]
        .into_iter()
        .map(|id| Edge {
            id: id.into(),
            from: "a".into(),
            to: "z".into(),
            relation: Relation::References,
            producer: "p".into(),
            quality: Quality::Observed,
        })
        .collect();
    let artifact =
        snapshot::build(manifest.clone(), &source, nodes.clone(), edges, vec![]).unwrap();
    let handle = Snapshot::read(&artifact.bytes, &artifact.id, &source).unwrap();
    let query = Query {
        node: "a".into(),
        relation: Relation::References,
        direction: Direction::Outgoing,
        producer: "p".into(),
        limit: 1,
    };
    let first = handle.neighbors(&query, None).unwrap();
    let cursor = first.next_after.as_deref().unwrap();
    let second = handle.neighbors(&query, Some(cursor)).unwrap();
    let third = handle
        .neighbors(&query, second.next_after.as_deref())
        .unwrap();
    assert_eq!(
        [
            first.evidence.edges[0].id.as_str(),
            second.evidence.edges[0].id.as_str(),
            third.evidence.edges[0].id.as_str()
        ],
        ["one", "three", "two"]
    );
    assert!(third.next_after.is_none());
    for field in ["node", "relation", "direction", "limit"] {
        let mut changed = query.clone();
        match field {
            "node" => changed.node = "z".into(),
            "relation" => changed.relation = Relation::Calls,
            "direction" => changed.direction = Direction::Incoming,
            _ => changed.limit = 2,
        }
        assert!(handle.neighbors(&changed, Some(cursor)).is_err());
    }
    let other = snapshot::build(manifest, &source, nodes, vec![], vec![]).unwrap();
    let other = Snapshot::read(&other.bytes, &other.id, &source).unwrap();
    assert!(other.neighbors(&query, Some(cursor)).is_err());
}

#[test]
fn empty_later_page_does_not_establish_absence() {
    let nodes = vec![Node {
        scip: None,
        id: "a".into(),
        kind: NodeKind::File,
        path: None,
    }];
    let edge = Edge {
        id: "e".into(),
        from: "a".into(),
        to: "a".into(),
        relation: Relation::Calls,
        producer: "p".into(),
        quality: Quality::Observed,
    };
    let coverage = Coverage {
        node: "a".into(),
        relation: Relation::Calls,
        direction: Direction::Outgoing,
        producer: "p".into(),
        completeness: Completeness::Complete,
    };
    let graph = Graph::build(nodes, vec![edge], vec![coverage]).unwrap();
    let (page, more) = graph
        .neighbor_page("a", Relation::Calls, Direction::Outgoing, "p", "e", 1)
        .unwrap();
    assert!(page.edges.is_empty());
    assert!(!more);
    assert!(!page.absence_proven);
}

#[test]
fn equivalent_input_order_has_identical_artifact_bytes() {
    let (manifest, mut nodes) = fixture();
    let expected = manifest.source.clone();
    let first =
        snapshot::build(manifest.clone(), &expected, nodes.clone(), vec![], vec![]).unwrap();
    nodes.reverse();
    let second = snapshot::build(manifest, &expected, nodes, vec![], vec![]).unwrap();
    assert_eq!(first.bytes, second.bytes);
    assert_eq!(first.id, second.id);
    let graph = snapshot::read(&first.bytes, &first.id, &expected).unwrap();
    assert_eq!(
        graph.graph().node("a").unwrap().path.as_deref(),
        Some("a.rs")
    );
}

#[test]
fn changed_torn_reordered_or_foreign_artifacts_are_rejected() {
    let (manifest, nodes) = fixture();
    let expected = manifest.source.clone();
    let artifact = snapshot::build(manifest, &expected, nodes, vec![], vec![]).unwrap();
    let mut changed = artifact.bytes.clone();
    changed[0] = b'[';
    assert!(snapshot::read(&changed, &artifact.id, &expected).is_err());
    assert!(
        snapshot::read(
            &artifact.bytes[..artifact.bytes.len() - 1],
            &artifact.id,
            &expected
        )
        .is_err()
    );
    let mut foreign = expected.clone();
    foreign.tree = "f".repeat(40);
    assert!(snapshot::read(&artifact.bytes, &artifact.id, &foreign).is_err());
    let mut records: Vec<_> = artifact.bytes.split_inclusive(|&b| b == b'\n').collect();
    records.swap(1, 2);
    let reordered = records.concat();
    let hash = canonical::hash("harness.ri.snapshot.v1", &reordered);
    assert!(snapshot::read(&reordered, &hash, &expected).is_err());
}
