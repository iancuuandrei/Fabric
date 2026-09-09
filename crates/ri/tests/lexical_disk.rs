//! Qualify upstream disk append over exact-byte posting batches, not its walker.
use engorch_ri::lexical::LexicalQuery;
use std::collections::BTreeMap;
use tgrep_core::{builder::append_overlay_to_index, live::LiveIndex, reader::IndexReader};

#[test]
fn append_batches_preserve_binary_short_and_empty_source_candidates() {
    let root = tempfile::tempdir().unwrap();
    let sources: BTreeMap<String, Vec<u8>> = BTreeMap::from([
        ("a".into(), b"foo\0bar\xff".to_vec()),
        ("b".into(), b"".to_vec()),
        ("c".into(), b"a".to_vec()),
        ("d".into(), "ΣfooK".as_bytes().to_vec()),
        ("e".into(), b"foo foo".to_vec()),
    ]);
    let mut reader = IndexReader::empty();
    for (n, (path, content)) in sources.iter().enumerate() {
        let mut batch = LiveIndex::new();
        batch.upsert_file(path, content);
        let (paths, postings) = batch.snapshot_for_disk();
        let output = root.path().join(format!("generation-{n}"));
        append_overlay_to_index(root.path(), &output, &reader, &paths, &postings, true).unwrap();
        reader = IndexReader::open(&output).unwrap();
        reader.validate_lookup().unwrap();
        assert_eq!(reader.num_files(), n + 1);
    }
    for pattern in ["foo", "a", "", "foo.*bar", "(?i)Σ", "K", "foo|a"] {
        let query = LexicalQuery::compile(pattern, false, false).unwrap();
        let candidates = query.candidates(&reader.all_file_ids(), |tri| reader.lookup_trigram(tri));
        let mut actual = BTreeMap::new();
        for id in candidates.files {
            let path = reader.file_path(id).unwrap();
            let ranges: Vec<_> = query.ranges(&sources[path]).collect();
            if !ranges.is_empty() {
                actual.insert(path.to_string(), ranges);
            }
        }
        let expected: BTreeMap<_, _> = sources
            .iter()
            .filter_map(|(path, bytes)| {
                let ranges: Vec<_> = query.ranges(bytes).collect();
                (!ranges.is_empty()).then(|| (path.clone(), ranges))
            })
            .collect();
        assert_eq!(actual, expected, "disk candidate false negative: {pattern}");
    }
}
