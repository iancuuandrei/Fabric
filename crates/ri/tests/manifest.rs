//! Provenance admission tests, including source and producer substitution.

use engorch_ri::graph::*;
use engorch_ri::manifest::*;

fn manifest() -> Manifest {
    Manifest {
        format: 1,
        source: Source {
            repository_id: "a".repeat(64),
            object_format: "sha1".into(),
            commit: "b".repeat(40),
            tree: "c".repeat(40),
        },
        producers: vec![Producer {
            id: "semantic".into(),
            name: "fixture".into(),
            version: "1.2.3".into(),
            artifact_sha256: "d".repeat(64),
            inputs: vec![Input {
                name: "source".into(),
                sha256: "e".repeat(64),
            }],
        }],
    }
}

#[test]
fn rejects_each_source_substitution_and_invalid_git_format() {
    let manifest = manifest();
    for field in ["repository", "commit", "tree", "format"] {
        let mut expected = manifest.source.clone();
        match field {
            "repository" => expected.repository_id = "f".repeat(64),
            "commit" => expected.commit = "f".repeat(40),
            "tree" => expected.tree = "f".repeat(40),
            _ => expected.object_format = "sha256".into(),
        }
        assert!(manifest.clone().validate(&expected).is_err());
    }
    let mut invalid = manifest.clone();
    invalid.source.commit = "x".repeat(40);
    let expected = invalid.source.clone();
    assert!(invalid.validate(&expected).is_err());
    let mut sha256 = manifest;
    sha256.source.object_format = "sha256".into();
    sha256.source.commit = "a".repeat(64);
    sha256.source.tree = "b".repeat(64);
    let expected = sha256.source.clone();
    assert!(sha256.validate(&expected).is_ok());
}

#[test]
fn rejects_ambiguous_producer_and_input_registry() {
    let mut duplicate = manifest();
    duplicate.producers.push(duplicate.producers[0].clone());
    let expected = duplicate.source.clone();
    assert!(duplicate.validate(&expected).is_err());
    let mut duplicate = manifest();
    let input = duplicate.producers[0].inputs[0].clone();
    duplicate.producers[0].inputs.push(input);
    assert!(duplicate.validate(&expected).is_err());
    let mut empty = manifest();
    empty.producers[0].inputs.clear();
    assert!(empty.validate(&expected).is_err());
}

#[test]
fn unregistered_coverage_cannot_prove_absence() {
    let manifest = manifest();
    let source = manifest.source.clone();
    let nodes = vec![Node {
        scip: None,
        id: "a".into(),
        kind: NodeKind::File,
        path: Some("file.go".into()),
    }];
    let mut coverage = Coverage {
        node: "a".into(),
        relation: Relation::Calls,
        direction: Direction::Outgoing,
        producer: "unregistered".into(),
        completeness: Completeness::Complete,
    };
    assert!(
        BoundGraph::build(
            manifest.clone(),
            &source,
            nodes.clone(),
            vec![],
            vec![coverage.clone()]
        )
        .is_err()
    );
    coverage.producer = "semantic".into();
    let graph = BoundGraph::build(manifest, &source, nodes, vec![], vec![coverage]).unwrap();
    assert_eq!(graph.manifest().source, source);
    assert!(
        graph
            .graph()
            .neighbors("a", Relation::Calls, Direction::Outgoing, "semantic")
            .unwrap()
            .absence_proven
    );
}
