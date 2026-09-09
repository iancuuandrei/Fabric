//! Exact source/producer change separation tests.
use engorch_ri::{changed, manifest::*};

#[test]
fn change_pages_stop_at_byte_budget_without_losing_continuation() {
    let changes = changed::Changes {
        source_identity_changed: false,
        changed_producers: vec![],
        sources: (0..128)
            .map(|index| changed::SourceChange {
                producer: "p".into(),
                path: format!("{index:03}{}", "x".repeat(4000)),
                before: Some("a".repeat(64)),
                after: Some("b".repeat(64)),
            })
            .collect(),
    };
    let first = changes.page(128, None).unwrap();
    assert!(first.sources.len() < 128);
    assert!(first.next.is_some());
    let second = changes.page(128, first.next.as_ref()).unwrap();
    assert_eq!(first.sources.len() + second.sources.len(), 128);
    assert!(second.next.is_none());
}

#[test]
fn source_changes_are_separate_from_producer_configuration() {
    let before = Manifest {
        format: 1,
        source: Source {
            repository_id: "a".repeat(64),
            object_format: "sha1".into(),
            commit: "b".repeat(40),
            tree: "c".repeat(40),
        },
        producers: vec![Producer {
            id: "p".into(),
            name: "indexer".into(),
            version: "1".into(),
            artifact_sha256: "d".repeat(64),
            inputs: vec![Input {
                name: "source:a.rs".into(),
                sha256: "e".repeat(64),
            }],
        }],
    };
    let unchanged = changed::compare(&before, &before).unwrap();
    assert!(unchanged.sources.is_empty() && unchanged.changed_producers.is_empty());
    let mut after = before.clone();
    after.source.repository_id = "f".repeat(64);
    after.source.commit = "f".repeat(40);
    after.producers[0].inputs[0].sha256 = "f".repeat(64);
    let diff = changed::compare(&before, &after).unwrap();
    assert!(diff.source_identity_changed);
    assert_eq!(diff.sources.len(), 1);
    assert!(diff.changed_producers.is_empty());
    after.producers[0].version = "2".into();
    assert_eq!(
        changed::compare(&before, &after).unwrap().changed_producers,
        vec!["p"]
    );
    after.producers[0].inputs[0].name = "source:b.rs".into();
    let diff = changed::compare(&before, &after).unwrap();
    assert_eq!(diff.sources.len(), 2);
    assert!(diff.sources[0].after.is_none());
    assert!(diff.sources[1].before.is_none());
    let first = diff.page(1, None).unwrap();
    assert_eq!(first.sources[0].path, "a.rs");
    let second = diff.page(1, first.next.as_ref()).unwrap();
    assert_eq!(second.sources[0].path, "b.rs");
    assert!(second.next.is_none());
    assert!(diff.page(0, None).is_err());
    assert!(diff.page(129, None).is_err());
    let old = engorch_ri::snapshot::build(before.clone(), &before.source, vec![], vec![], vec![])
        .unwrap();
    let new =
        engorch_ri::snapshot::build(after.clone(), &after.source, vec![], vec![], vec![]).unwrap();
    let old = engorch_ri::query::Snapshot::read(&old.bytes, &old.id, &before.source).unwrap();
    let new = engorch_ri::query::Snapshot::read(&new.bytes, &new.id, &after.source).unwrap();
    let page = old.changed_page(&new, 1, None).unwrap();
    let cursor = page["next_after"].as_str().unwrap();
    let next = old.changed_page(&new, 1, Some(cursor)).unwrap();
    assert_eq!(next["sources"][0]["path"], "b.rs");
    assert!(next["next_after"].is_null());
    assert!(old.changed_page(&new, 2, Some(cursor)).is_err());
    assert!(new.changed_page(&old, 1, Some(cursor)).is_err());
}
