//! Verified source indexing, deterministic matching and explicit truncation.
use engorch_ri::{
    lexical::LexicalQuery,
    lexical_index::ResidentIndex,
    lexical_manifest::{LexicalFile, LexicalManifest},
    manifest::Source,
};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;

#[test]
fn stdio_overlay_shadows_base_and_binds_cursor() {
    use engorch_ri::{canonical, lexical_disk::build_shards};
    use serde_json::{Value, json};
    use std::{
        io::Write,
        process::{Command, Stdio},
    };
    let (m, sources) = inputs();
    let temp = tempfile::tempdir().unwrap();
    let root = temp.path();
    let write_manifest = |name: &str, manifest: &LexicalManifest| {
        let path = root.join(name);
        let mut bytes =
            canonical::encode(&json!({"kind":"source","value":manifest.source})).unwrap();
        bytes.push(b'\n');
        let mut files = manifest.files.clone();
        files.sort_by(|a, b| a.path.cmp(&b.path));
        for file in files {
            bytes.extend(canonical::encode(&json!({"kind":"file","value":file})).unwrap());
            bytes.push(b'\n');
        }
        std::fs::write(&path, bytes).unwrap();
        path
    };
    let manifest_path = write_manifest("base.jsonl", &m);
    let source_root = root.join("sources");
    std::fs::create_dir(&source_root).unwrap();
    for file in &m.files {
        std::fs::write(source_root.join(&file.sha256), &sources[&file.path]).unwrap();
    }
    let index_path = root.join("index");
    let build = build_shards(&index_path, m.clone(), &m.source, 64, 10, |f| {
        Ok(sources[&f.path].clone())
    })
    .unwrap();
    let mut changed = m.clone();
    let mut replacement = m.files.iter().find(|f| f.path == "empty").unwrap().clone();
    replacement.path = "a.rs".into();
    let mut created = m.files.iter().find(|f| f.path == "b.rs").unwrap().clone();
    created.path = "new.rs".into();
    changed.files = vec![replacement, created];
    let changed_path = write_manifest("changed.jsonl", &changed);
    let mut request = json!({"operation":"lexical_search", "manifest_path":manifest_path,
        "manifest_id":m.id().unwrap(),"source":m.source,"source_root":source_root,
        "index_path":index_path,"build_id":build.id().unwrap(),"build":build,
        "pattern":"foo","fixed":true,"case_insensitive":false,"limit":1,"after":null,
        "overlay":{"candidate":"e".repeat(64),"manifest_path":changed_path,
            "manifest_id":changed.id().unwrap(),"source_root":source_root,"deleted":["b.rs"]}});
    let invoke = |request: &Value| {
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
            .write_all(&canonical::encode(request).unwrap())
            .unwrap();
        let result = child.wait_with_output().unwrap();
        let value: Value = serde_json::from_slice(&result.stdout).unwrap();
        (result.status.success(), value)
    };
    let (ok, first) = invoke(&request);
    assert!(ok, "{first}");
    assert_eq!(first["result"]["matches"][0]["path"], "new.rs");
    assert_eq!(first["result"]["matches"][0]["range"], json!([0, 3]));
    assert_eq!(first["result"]["truncated"], true);
    request["after"] = first["result"]["next"].clone();
    let (ok, second) = invoke(&request);
    assert!(ok, "{second}");
    assert_eq!(second["result"]["matches"][0]["range"], json!([4, 7]));
    assert_eq!(second["result"]["truncated"], false);
    request["overlay"]["candidate"] = json!("f".repeat(64));
    assert!(!invoke(&request).0);
    request["after"] = Value::Null;
    request.as_object_mut().unwrap().remove("overlay");
    let (ok, base) = invoke(&request);
    assert!(ok, "{base}");
    assert_eq!(base["result"]["matches"][0]["path"], "a.rs");
}

#[test]
fn disk_overlay_matches_resident_view_and_never_reads_shadowed_base() {
    use engorch_ri::{
        lexical_disk::{DiskIndex, DiskOverlay, build_shards},
        lexical_index::CandidateOverlay,
    };
    let (m, sources) = inputs();
    let temp = tempfile::tempdir().unwrap();
    let output = temp.path().join("index");
    let receipt = build_shards(&output, m.clone(), &m.source, 8, 1, |f| {
        Ok(sources[&f.path].clone())
    })
    .unwrap();
    let disk = DiskIndex::open(
        &output,
        &receipt,
        &receipt.id().unwrap(),
        m.clone(),
        &m.source,
    )
    .unwrap();
    let resident = ResidentIndex::build(m.clone(), &m.source, sources.clone()).unwrap();
    let mut changed = m.clone();
    let mut replacement = changed
        .files
        .iter()
        .find(|f| f.path == "empty")
        .unwrap()
        .clone();
    replacement.path = "a.rs".into();
    let mut created = changed
        .files
        .iter()
        .find(|f| f.path == "a.rs")
        .unwrap()
        .clone();
    created.path = "new.rs".into();
    changed.files = vec![replacement, created];
    let changed_bytes =
        BTreeMap::from([("a.rs".into(), vec![]), ("new.rs".into(), b"foo".to_vec())]);
    let overlay = DiskOverlay::build(
        &disk,
        &"e".repeat(64),
        changed.clone(),
        changed_bytes.clone(),
        vec!["b.rs".into()],
    )
    .unwrap();
    let expected = CandidateOverlay::build(
        &resident,
        &"e".repeat(64),
        changed,
        changed_bytes,
        vec!["b.rs".into()],
    )
    .unwrap();
    for pattern in ["foo", ""] {
        let query = LexicalQuery::compile(pattern, true, false).unwrap();
        let all = expected.search_page(&query, 100, None).unwrap();
        let mut next = None;
        let mut actual = Vec::new();
        loop {
            let page = overlay
                .search_page(&query, 1, next.as_ref(), |f| {
                    assert_eq!(f.path, "empty", "must not read shadowed base bytes");
                    Ok(sources[&f.path].clone())
                })
                .unwrap();
            assert_eq!(page.manifest_id, all.manifest_id);
            actual.extend(page.matches);
            next = page.next;
            if next.is_none() {
                break;
            }
        }
        assert_eq!(actual, all.matches);
    }
    let base_filter =
        LexicalQuery::compile_filtered("foo", true, false, Some("a.rs"), Some("rs")).unwrap();
    assert_eq!(resident.search(&base_filter, 1).unwrap().matches.len(), 1);
    let overlay_filter =
        LexicalQuery::compile_filtered("foo", true, false, Some("new.rs"), Some("rs")).unwrap();
    let expected_page = expected.search_page(&overlay_filter, 1, None).unwrap();
    let disk_page = overlay
        .search_page(&overlay_filter, 1, None, |f| Ok(sources[&f.path].clone()))
        .unwrap();
    assert_eq!(disk_page.matches, expected_page.matches);
    assert_eq!(disk_page.candidate_files, 1);
    let broad = LexicalQuery::compile("", true, false).unwrap();
    let broad_cursor = expected.search_page(&broad, 1, None).unwrap().next.unwrap();
    assert!(
        overlay
            .search_page(&overlay_filter, 1, Some(&broad_cursor), |f| {
                Ok(sources[&f.path].clone())
            })
            .is_err()
    );
}

#[test]
fn disk_matching_pages_equal_resident_and_reject_source_substitution() {
    use engorch_ri::lexical_disk::{DiskIndex, build_shards};
    let (m, sources) = inputs();
    let temp = tempfile::tempdir().unwrap();
    let output = temp.path().join("index");
    let receipt = build_shards(&output, m.clone(), &m.source, 8, 1, |f| {
        Ok(sources[&f.path].clone())
    })
    .unwrap();
    let disk = DiskIndex::open(
        &output,
        &receipt,
        &receipt.id().unwrap(),
        m.clone(),
        &m.source,
    )
    .unwrap();
    let resident = ResidentIndex::build(m.clone(), &m.source, sources.clone()).unwrap();
    for pattern in ["foo", "", "(?i)FOO", "missing"] {
        let q = LexicalQuery::compile(pattern, false, false).unwrap();
        let all = resident.search(&q, 100).unwrap();
        let mut next = None;
        let mut hits = Vec::new();
        loop {
            let page = disk
                .search_page(&q, 1, next.as_ref(), |f| Ok(sources[&f.path].clone()))
                .unwrap();
            hits.extend(page.matches);
            next = page.next;
            if next.is_none() {
                break;
            }
        }
        assert_eq!(hits, all.matches, "{pattern}");
    }
    let q = LexicalQuery::compile("foo", true, false).unwrap();
    assert!(
        disk.search_page(&q, 10, None, |_| Ok(b"bar".to_vec()))
            .is_err()
    );
    let cursor = disk
        .search_page(&q, 1, None, |f| Ok(sources[&f.path].clone()))
        .unwrap()
        .next
        .unwrap();
    let other = LexicalQuery::compile("foo", true, true).unwrap();
    assert!(
        disk.search_page(&other, 1, Some(&cursor), |_| panic!(
            "reject query before source reads"
        ))
        .is_err()
    );
}

#[test]
fn disk_build_shards_exact_sources_without_reusing_output() {
    use engorch_ri::lexical_disk::build_shards;
    let (m, sources) = inputs();
    let temp = tempfile::tempdir().unwrap();
    let output = temp.path().join("index");
    let mut reads = 0;
    let built = build_shards(&output, m.clone(), &m.source, 8, 1, |file| {
        reads += 1;
        Ok(sources[&file.path].clone())
    })
    .unwrap();
    assert_eq!(reads, 3);
    assert_eq!(built.shards.len(), 3);
    assert_eq!(built.manifest_id, m.id().unwrap());
    for shard in &built.shards {
        assert_eq!(shard.files, 1);
        assert!(shard.hashes.iter().all(|h| h.len() == 64));
        let reader = tgrep_core::reader::IndexReader::open(&output.join(&shard.directory)).unwrap();
        assert_eq!(reader.num_files(), 1);
    }
    assert!(
        build_shards(&output, m.clone(), &m.source, 8, 1, |_| panic!(
            "must reject existing directory before reading"
        ))
        .is_err()
    );
    let oversized = temp.path().join("oversized");
    assert!(
        build_shards(&oversized, m.clone(), &m.source, 1, 1, |_| panic!(
            "must reject oversize admission before reading"
        ))
        .is_err()
    );
    assert!(!oversized.exists());
    assert!(
        build_shards(
            &temp.path().join("bad"),
            m.clone(),
            &m.source,
            8,
            1,
            |_| Ok(b"wrong".to_vec())
        )
        .is_err()
    );
}

#[test]
fn disk_reader_rejects_changed_bytes_and_scope() {
    use engorch_ri::lexical_disk::{DiskIndex, build_shards};
    let (m, sources) = inputs();
    let temp = tempfile::tempdir().unwrap();
    let output = temp.path().join("index");
    let built = build_shards(&output, m.clone(), &m.source, 8, 1, |f| {
        Ok(sources[&f.path].clone())
    })
    .unwrap();
    let id = built.id().unwrap();
    {
        let disk = DiskIndex::open(&output, &built, &id, m.clone(), &m.source).unwrap();
        assert_eq!(
            disk.candidates(&LexicalQuery::compile("foo", true, false).unwrap())
                .unwrap(),
            (vec!["a.rs".into(), "b.rs".into()], false)
        );
        assert_eq!(
            disk.candidates(&LexicalQuery::compile("", true, false).unwrap())
                .unwrap(),
            (vec!["a.rs".into(), "b.rs".into(), "empty".into()], true)
        );
    }
    assert!(DiskIndex::open(&output, &built, &"0".repeat(64), m.clone(), &m.source).is_err());
    let mut missing = m.clone();
    missing.files.pop();
    assert!(DiskIndex::open(&output, &built, &id, missing, &m.source).is_err());
    // No active mappings when modifying the fixture on Windows.
    let path = output.join(&built.shards[0].directory).join("index.bin");
    let mut bytes = std::fs::read(&path).unwrap();
    bytes.push(0);
    std::fs::write(path, bytes).unwrap();
    assert!(DiskIndex::open(&output, &built, &id, m.clone(), &m.source).is_err());
}

#[test]
fn overlay_shadows_before_limits_and_preserves_base() {
    use engorch_ri::lexical_index::CandidateOverlay;
    let (m, sources) = inputs();
    let base = ResidentIndex::build(m.clone(), &m.source, sources).unwrap();
    let mut changed = m.clone();
    changed.files.retain(|f| f.path == "a.rs");
    let mut created = changed.files[0].clone();
    created.path = "new.rs".into();
    changed.files.push(created);
    let bytes = b"bar".to_vec();
    let mut hash = Sha256::new();
    hash.update(b"blob 3\0bar");
    changed.files[0].blob = hash.finalize().iter().map(|b| format!("{b:02x}")).collect();
    changed.files[0].sha256 = Sha256::digest(&bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    let changed_bytes =
        BTreeMap::from([("a.rs".into(), bytes), ("new.rs".into(), b"foo".to_vec())]);
    let overlay = CandidateOverlay::build(
        &base,
        &"e".repeat(64),
        changed.clone(),
        changed_bytes.clone(),
        vec!["b.rs".into()],
    )
    .unwrap();
    let q = LexicalQuery::compile("foo", true, false).unwrap();
    let result = overlay.search_page(&q, 1, None).unwrap();
    assert_eq!(result.matches.len(), 1);
    assert_eq!(result.matches[0].path, "new.rs");
    assert!(!result.truncated);
    assert_eq!(base.search(&q, 100).unwrap().matches.len(), 3);
    let empty = LexicalQuery::compile("", true, false).unwrap();
    let first = overlay.search_page(&empty, 1, None).unwrap();
    let cursor = first.next.unwrap();
    assert!(base.search_page(&empty, 1, Some(&cursor)).is_err());
    let other = CandidateOverlay::build(
        &base,
        &"f".repeat(64),
        changed.clone(),
        changed_bytes.clone(),
        vec!["b.rs".into()],
    )
    .unwrap();
    assert!(other.search_page(&empty, 1, Some(&cursor)).is_err());
    assert!(
        CandidateOverlay::build(
            &base,
            &"e".repeat(64),
            changed,
            changed_bytes,
            vec!["a.rs".into()]
        )
        .is_err()
    );
}

fn inputs() -> (LexicalManifest, BTreeMap<String, Vec<u8>>) {
    let sources = BTreeMap::from([
        ("b.rs".into(), b"foo foo".to_vec()),
        ("a.rs".into(), b"foo".to_vec()),
        ("empty".into(), vec![]),
    ]);
    let files = sources
        .iter()
        .map(|(path, bytes): (&String, &Vec<u8>)| {
            let mut blob = Sha256::new();
            blob.update(format!("blob {}\0", bytes.len()).as_bytes());
            blob.update(bytes);
            LexicalFile {
                path: path.clone(),
                blob: blob.finalize().iter().map(|b| format!("{b:02x}")).collect(),
                sha256: Sha256::digest(bytes)
                    .iter()
                    .map(|b| format!("{b:02x}"))
                    .collect(),
                bytes: bytes.len() as u64,
            }
        })
        .collect();
    (
        LexicalManifest {
            version: 1,
            source: Source {
                repository_id: "a".repeat(64),
                object_format: "sha256".into(),
                commit: "b".repeat(64),
                tree: "c".repeat(64),
            },
            files,
        },
        sources,
    )
}

#[test]
fn matches_are_exact_ordered_and_bounded() {
    let (m, sources) = inputs();
    let index = ResidentIndex::build(m.clone(), &m.source, sources).unwrap();
    let q = LexicalQuery::compile("foo", true, false).unwrap();
    let all = index.search(&q, 3).unwrap();
    assert!(!all.truncated);
    assert!(!all.full_scan);
    assert_eq!(
        all.matches
            .iter()
            .map(|m| (m.path.as_str(), m.range))
            .collect::<Vec<_>>(),
        vec![("a.rs", [0, 3]), ("b.rs", [0, 3]), ("b.rs", [4, 7])]
    );
    assert_eq!(all.manifest_id, m.id().unwrap());
    let short = index.search(&q, 2).unwrap();
    assert!(short.truncated);
    assert_eq!(short.matches, all.matches[..2]);
    let fallback = index
        .search(&LexicalQuery::compile("", true, false).unwrap(), 100)
        .unwrap();
    assert!(fallback.full_scan);
    assert_eq!(fallback.searched_files, 3);
    assert!(fallback.matches.iter().any(|m| m.path == "empty"));
}

#[test]
fn incomplete_and_substituted_scope_rejects_build() {
    let (m, sources) = inputs();
    let mut missing = sources.clone();
    missing.remove("empty");
    assert!(ResidentIndex::build(m.clone(), &m.source, missing).is_err());
    let mut changed = sources.clone();
    changed.insert("a.rs".into(), b"bar".to_vec());
    assert!(ResidentIndex::build(m.clone(), &m.source, changed).is_err());
    let mut extra = sources;
    extra.insert("extra".into(), vec![]);
    assert!(ResidentIndex::build(m.clone(), &m.source, extra).is_err());
}

#[test]
fn pagination_matches_full_search_and_rejects_foreign_cursors() {
    let (m, sources) = inputs();
    let index = ResidentIndex::build(m.clone(), &m.source, sources.clone()).unwrap();
    for pattern in ["foo", ""] {
        let query = LexicalQuery::compile(pattern, true, false).unwrap();
        let all = index.search(&query, 100).unwrap();
        let mut page = index.search(&query, 1).unwrap();
        let first_cursor = page.next.clone().unwrap();
        let mut matches = page.matches;
        while let Some(cursor) = page.next {
            page = index.search_page(&query, 1, Some(&cursor)).unwrap();
            matches.extend(page.matches);
        }
        assert_eq!(matches, all.matches);
        let changed_query = LexicalQuery::compile(pattern, true, true).unwrap();
        assert!(
            index
                .search_page(&changed_query, 1, Some(&first_cursor))
                .is_err()
        );
        let mut changed_manifest = m.clone();
        changed_manifest.source.commit = "d".repeat(64);
        let changed = ResidentIndex::build(
            changed_manifest.clone(),
            &changed_manifest.source,
            sources.clone(),
        )
        .unwrap();
        assert!(changed.search_page(&query, 1, Some(&first_cursor)).is_err());
        let mut value = serde_json::to_value(first_cursor).unwrap();
        value["range"] = serde_json::json!([999, 1000]);
        let invalid = serde_json::from_value(value).unwrap();
        assert!(index.search_page(&query, 1, Some(&invalid)).is_err());
    }
}
