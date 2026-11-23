//! Go intelligence facts tests.
use fabric_ri::intelligence::go_file_facts;
use sha2::{Digest, Sha256};
fn h(v: &[u8]) -> String {
    Sha256::digest(v)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}
#[test]
fn cold_warm_and_forged_cache_reparse() {
    let s = b"package p\nfunc F(){}";
    let d = tempfile::tempdir().unwrap();
    let p = "a".repeat(64);
    let a = go_file_facts("a.go", s, &h(s), &p, Some(d.path())).unwrap();
    assert_eq!(a.parse_count, 1);
    assert_eq!(
        go_file_facts("a.go", s, &h(s), &p, Some(d.path()))
            .unwrap()
            .parse_count,
        0
    );
    std::fs::write(d.path().join(format!("{}.json", a.cache_key)), b"bad").unwrap();
    assert_eq!(
        go_file_facts("a.go", s, &h(s), &p, Some(d.path()))
            .unwrap()
            .parse_count,
        1
    )
}

#[test]
fn concurrent_cache_writers_keep_results_bounded_and_reusable() {
    let directory = tempfile::tempdir().unwrap();
    let cache = directory.path().to_path_buf();
    let producer = "a".repeat(64);
    let count = 32usize;
    let mut workers = Vec::new();
    for index in 0..count {
        let cache = cache.clone();
        let producer = producer.clone();
        workers.push(std::thread::spawn(move || {
            let path = format!("pkg/file{index}.go");
            let source = format!("package p\nfunc F{index}() {{}}\n").into_bytes();
            let facts =
                go_file_facts(&path, &source, &h(&source), &producer, Some(&cache)).unwrap();
            (path, source, producer, facts)
        }));
    }
    for worker in workers {
        let (path, source, producer, facts) = worker.join().unwrap();
        assert_eq!(facts.parse_count, 1);
        assert_eq!(facts.source_sha256, h(&source));
        assert_eq!(facts.producer_sha256, producer);
        assert_eq!(facts.path, path);
    }

    // Contended writers may skip publication. A later ordinary call fills any
    // missing entries, and every subsequent result must be the exact fact hit.
    for index in 0..count {
        let path = format!("pkg/file{index}.go");
        let source = format!("package p\nfunc F{index}() {{}}\n").into_bytes();
        let _ = go_file_facts(&path, &source, &h(&source), &producer, Some(&cache)).unwrap();
    }
    for index in 0..count {
        let path = format!("pkg/file{index}.go");
        let source = format!("package p\nfunc F{index}() {{}}\n").into_bytes();
        let warm = go_file_facts(&path, &source, &h(&source), &producer, Some(&cache)).unwrap();
        assert_eq!(
            warm.parse_count, 0,
            "fact {index} was not reusable after publication"
        );
    }
    let entries: Vec<_> = std::fs::read_dir(&cache)
        .unwrap()
        .map(|entry| entry.unwrap())
        .collect();
    let json_entries: Vec<_> = entries
        .iter()
        .filter(|entry| entry.file_name().to_string_lossy().ends_with(".json"))
        .collect();
    let bytes: u64 = json_entries
        .iter()
        .map(|entry| entry.metadata().unwrap().len())
        .sum();
    assert_eq!(json_entries.len(), count);
    assert!(json_entries.len() <= 1024);
    let resident_bytes: u64 = entries
        .iter()
        .map(|entry| entry.metadata().unwrap().len())
        .sum();
    assert!(bytes <= resident_bytes);
    assert!(resident_bytes <= 64 << 20);
}
#[test]
fn unicode_and_escaped_bytes_are_decoded() {
    let s = "package p\nimport ( . `raw\r/path`; _ \"caf\\xC3\\xA9\" )\ntype A = B\n";
    let f = go_file_facts(
        "a.go",
        s.as_bytes(),
        &h(s.as_bytes()),
        &"b".repeat(64),
        None,
    )
    .unwrap();
    assert_eq!(f.imports[0].path, "raw/path");
    assert_eq!(f.imports[1].path, "café");
    assert_eq!(f.imports[0].alias.as_deref(), Some("."));
    assert!(f.declarations.iter().any(|x| x.name == "A"));
}

#[test]
fn cache_metadata_ranges_and_order_are_checked_even_with_matching_body_digest() {
    use fabric_ri::canonical;
    let source = b"package p\nfunc F(){}\nfunc G(){F()}\n";
    let directory = tempfile::tempdir().unwrap();
    let producer = "c".repeat(64);
    let original = go_file_facts(
        "a.go",
        source,
        &h(source),
        &producer,
        Some(directory.path()),
    )
    .unwrap();
    let path = directory
        .path()
        .join(format!("{}.json", original.cache_key));
    for mutation in [
        "schema", "path", "parser", "language", "range", "name", "order", "coverage", "marker",
    ] {
        let mut fact = serde_json::to_value(&original).unwrap();
        match mutation {
            "schema" => fact["schema"] = "wrong".into(),
            "path" => fact["path"] = "other.go".into(),
            "parser" => fact["parser_version"] = "wrong".into(),
            "language" => fact["language"] = "wrong".into(),
            "range" => fact["declarations"][0]["range"]["end_byte"] = (source.len() + 1).into(),
            "name" => fact["declarations"][0]["name"] = "Forged".into(),
            "order" => fact["declarations"].as_array_mut().unwrap().reverse(),
            "coverage" => fact["coverage"] = "COMPLETE".into(),
            "marker" => fact["generated_markers"] = serde_json::json!(["//go:generate forged"]),
            _ => unreachable!(),
        }
        fact["body_sha256"] = "".into();
        fact["cache"] = "".into();
        fact["parse_count"] = 0.into();
        fact["body_sha256"] = h(&canonical::encode(&fact).unwrap()).into();
        std::fs::write(&path, canonical::encode(&fact).unwrap()).unwrap();
        let reparsed = go_file_facts(
            "a.go",
            source,
            &h(source),
            &producer,
            Some(directory.path()),
        )
        .unwrap();
        assert_eq!(reparsed.parse_count, 1, "mutation {mutation}");
        assert_eq!(reparsed.body_sha256, original.body_sha256);
    }
}

#[test]
fn source_bindings_cache_keys_and_read_bounds_hold() {
    let source = b"package p\nfunc F(){}";
    let producer = "d".repeat(64);
    let directory = tempfile::tempdir().unwrap();
    let original = go_file_facts(
        "a.go",
        source,
        &h(source),
        &producer,
        Some(directory.path()),
    )
    .unwrap();
    assert_ne!(
        original.cache_key,
        go_file_facts("renamed.go", source, &h(source), &producer, None)
            .unwrap()
            .cache_key
    );
    assert_ne!(
        original.cache_key,
        go_file_facts("a.go", source, &h(source), &"e".repeat(64), None)
            .unwrap()
            .cache_key
    );
    assert!(go_file_facts("../bad.go", source, &h(source), &producer, None).is_err());
    assert!(go_file_facts("a.go", source, &"0".repeat(64), &producer, None).is_err());
    assert!(go_file_facts("a.go", b"\xff", &h(b"\xff"), &producer, None).is_err());
    assert!(
        go_file_facts(
            "a.go",
            source,
            &h(source),
            &producer,
            Some(std::path::Path::new("relative"))
        )
        .is_err()
    );
    let huge = vec![b' '; (1 << 20) + 1];
    assert!(go_file_facts("a.go", &huge, &h(&huge), &producer, None).is_err());
    let path = directory
        .path()
        .join(format!("{}.json", original.cache_key));
    std::fs::write(path, vec![b' '; fabric_ri::canonical::MAX_BYTES + 1]).unwrap();
    assert_eq!(
        go_file_facts(
            "a.go",
            source,
            &h(source),
            &producer,
            Some(directory.path())
        )
        .unwrap()
        .parse_count,
        1
    );
}

#[cfg(unix)]
#[test]
fn symlink_cache_ancestors_are_rejected() {
    use std::os::unix::fs::symlink;
    let root = tempfile::tempdir().unwrap();
    let real = root.path().join("real");
    std::fs::create_dir(&real).unwrap();
    let link = root.path().join("link");
    symlink(&real, &link).unwrap();
    let source = b"package p";
    assert!(
        go_file_facts(
            "a.go",
            source,
            &h(source),
            &"f".repeat(64),
            Some(&link.join("child"))
        )
        .is_err()
    );
}
