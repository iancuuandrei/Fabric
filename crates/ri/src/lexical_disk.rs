//! Fresh-directory construction of verified, independent lexical disk shards.
//! The controller owns staging isolation and atomic publication. Partial output
//! on failure is never a completed artifact and is deliberately not published.

use crate::{
    lexical_manifest::{LexicalFile, LexicalManifest},
    manifest::Source,
};
use sha2::{Digest, Sha256};
use std::{fs, io::Read, path::Path};
use tgrep_core::{builder::append_overlay_to_index, live::LiveIndex, reader::IndexReader};

/// One immutable shard with exact identities of files consumed by IndexReader.
#[derive(Debug, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DiskShard {
    /// Generated relative directory, independent of repository path spelling.
    pub directory: String,
    /// Number of indexed files, including empty and binary sources.
    pub files: usize,
    /// SHA-256 of lookup.bin, index.bin and files.bin respectively.
    pub hashes: [String; 3],
}

/// Successful staging receipt. It is not a publication receipt.
#[derive(Debug, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DiskBuild {
    /// Exact source manifest identity.
    pub manifest_id: String,
    /// Disjoint shards in admitted path order.
    pub shards: Vec<DiskShard>,
}

impl DiskBuild {
    /// Recheck the exact shard bytes before reusing retained mmap readers.
    /// Caller-owned immutable lifetime isolation is still required during reads.
    pub fn verify_files(&self, root: &Path) -> Result<(), String> {
        for (n, shard) in self.shards.iter().enumerate() {
            if shard.directory != format!("shard-{n:06}") {
                return Err("invalid lexical shard directory".into());
            }
            for (name, expected) in ["lookup.bin", "index.bin", "files.bin"]
                .iter()
                .zip(&shard.hashes)
            {
                if file_hash(&root.join(&shard.directory).join(name))? != *expected {
                    return Err("lexical shard bytes changed".into());
                }
            }
        }
        Ok(())
    }

    /// Content identity for a controller-retained trusted build receipt.
    /// Does not authenticate receipts supplied by an untrusted worker.
    pub fn id(&self) -> Result<String, String> {
        let mut hash = Sha256::new();
        hash.update(b"harness.ri.lexical-disk-build.v1\n");
        hash.update(self.manifest_id.as_bytes());
        hash.update(b"\n");
        for shard in &self.shards {
            hash.update(crate::canonical::encode(
                &serde_json::to_value(shard).map_err(|e| e.to_string())?,
            )?);
            hash.update(b"\n");
        }
        Ok(hash.finalize().iter().map(|b| format!("{b:02x}")).collect())
    }
}

/// Validated mmap readers from a controller-owned immutable artifact directory.
/// The controller must prevent concurrent writes for the lifetime of this value;
/// hashing before mmap is not OS isolation or protection from concurrent mutation.
pub struct DiskIndex {
    readers: Vec<IndexReader>,
    manifest: LexicalManifest,
    manifest_id: String,
}

impl DiskIndex {
    /// Verify the on-disk manifest against the retained validated metadata,
    /// buffering one record instead of loading another complete manifest.
    pub fn verify_manifest(&self, path: &Path) -> Result<(), String> {
        crate::lexical_manifest::verify_manifest(
            std::io::BufReader::new(crate::lexical_io::regular(path, 512 << 20)?),
            &self.manifest,
        )
    }

    /// Verify a trusted receipt, all exact reader bytes and full manifest path
    /// coverage before exposing any candidate restriction. No partial admission.
    pub fn open(
        root: &Path,
        receipt: &DiskBuild,
        expected_receipt: &str,
        manifest: LexicalManifest,
        expected: &Source,
    ) -> Result<Self, String> {
        let manifest = manifest.validate(expected)?;
        if receipt.id()? != expected_receipt
            || receipt.manifest_id != manifest.id()?
            || receipt.shards.len() > manifest.files.len()
        {
            return Err("lexical disk receipt identity mismatch".into());
        }
        let mut paths = std::collections::BTreeSet::new();
        let mut readers = Vec::new();
        for (n, shard) in receipt.shards.iter().enumerate() {
            if shard.directory != format!("shard-{n:06}")
                || shard.files == 0
                || shard.files > 10_000
            {
                return Err("invalid lexical shard descriptor".into());
            }
            let directory = root.join(&shard.directory);
            for (name, expected_hash) in ["lookup.bin", "index.bin", "files.bin"]
                .iter()
                .zip(&shard.hashes)
            {
                if file_hash(&directory.join(name))? != *expected_hash {
                    return Err("lexical shard bytes changed".into());
                }
            }
            let reader = IndexReader::open(&directory).map_err(|e| e.to_string())?;
            reader.validate_lookup()?;
            if reader.num_files() != shard.files {
                return Err("lexical shard file count differs".into());
            }
            for path in reader.all_paths() {
                if !paths.insert(path.clone()) {
                    return Err("duplicate lexical shard path".into());
                }
            }
            readers.push(reader);
        }
        if paths
            .iter()
            .map(String::as_str)
            .ne(manifest.files.iter().map(|f| f.path.as_str()))
        {
            return Err("lexical disk scope differs from manifest".into());
        }
        Ok(Self {
            readers,
            manifest,
            manifest_id: receipt.manifest_id.clone(),
        })
    }

    /// Match verified source bytes for disk candidates, one file at a time.
    /// The callback must enforce its allocation bound. No mutable source bytes
    /// are accepted merely because their path was present in the disk index.
    pub fn search_page(
        &self,
        query: &crate::lexical::LexicalQuery,
        limit: usize,
        after: Option<&crate::lexical_index::LexicalCursor>,
        read_source: impl FnMut(&LexicalFile) -> Result<Vec<u8>, String>,
    ) -> Result<crate::lexical_index::LexicalResult, String> {
        self.search_view(query, limit, after, read_source, None)
    }

    fn search_view(
        &self,
        query: &crate::lexical::LexicalQuery,
        limit: usize,
        after: Option<&crate::lexical_index::LexicalCursor>,
        mut read_source: impl FnMut(&LexicalFile) -> Result<Vec<u8>, String>,
        overlay: Option<&DiskOverlay<'_>>,
    ) -> Result<crate::lexical_index::LexicalResult, String> {
        use crate::lexical_index::{LexicalCursor, LexicalHit, LexicalResult};
        let view_id = overlay.map_or(self.manifest_id.as_str(), |o| o.id.as_str());
        if limit == 0 || limit > 10_000 {
            return Err("invalid lexical result limit".into());
        }
        if after.is_some_and(|c| c.manifest_id != view_id || c.query_id != query.id()) {
            return Err("lexical cursor identity mismatch".into());
        }
        let (paths, mut full_scan) = self.candidates(query)?;
        let mut paths: std::collections::BTreeSet<String> = paths
            .into_iter()
            .filter(|p| query.includes_path(p))
            .filter(|p| !overlay.is_some_and(|o| o.shadow.contains(p)))
            .collect();
        if let Some(o) = overlay {
            let selected = query.candidates(&o.changed.postings.all_file_ids(), |tri| {
                o.changed.postings.lookup_trigram(tri)
            });
            full_scan |= selected.full_scan;
            for id in selected.files {
                let path = o
                    .changed
                    .postings
                    .file_path(id)
                    .ok_or("missing overlay path")?;
                if query.includes_path(path) {
                    paths.insert(path.into());
                }
            }
        }
        let mut result = LexicalResult {
            query_id: query.id().into(),
            next: None,
            manifest_id: view_id.into(),
            full_scan,
            candidate_files: paths.len(),
            searched_files: 0,
            truncated: false,
            matches: Vec::new(),
        };
        let mut boundary_seen = after.is_none();
        for path in paths {
            let owned;
            let (file, bytes) =
                if let Some(entry) = overlay.and_then(|o| o.changed.files.get(&path)) {
                    (&entry.0, entry.1.as_slice())
                } else {
                    let n = self
                        .manifest
                        .files
                        .binary_search_by(|f| f.path.cmp(&path))
                        .map_err(|_| "unadmitted disk candidate")?;
                    let file = &self.manifest.files[n];
                    owned = read_source(file)?;
                    (file, owned.as_slice())
                };
            file.verify_git_blob(bytes, &self.manifest.source.object_format)?;
            result.searched_files += 1;
            for range in query.ranges(bytes) {
                if !boundary_seen {
                    if after.is_some_and(|c| c.path == path && c.range == [range.start, range.end])
                    {
                        boundary_seen = true;
                    }
                    continue;
                }
                if result.matches.len() == limit {
                    result.truncated = true;
                    let last = result
                        .matches
                        .last()
                        .ok_or("missing lexical page boundary")?;
                    result.next = Some(LexicalCursor {
                        manifest_id: view_id.into(),
                        query_id: query.id().into(),
                        path: last.path.clone(),
                        range: last.range,
                    });
                    return Ok(result);
                }
                result.matches.push(LexicalHit {
                    path: path.clone(),
                    blob: file.blob.clone(),
                    range: [range.start, range.end],
                });
            }
        }
        if !boundary_seen {
            return Err("lexical cursor match missing".into());
        }
        Ok(result)
    }

    /// Sorted candidate paths and whether full-scope evaluation was required.
    /// Callers still verify admitted source bytes and execute the exact matcher.
    pub fn candidates(
        &self,
        query: &crate::lexical::LexicalQuery,
    ) -> Result<(Vec<String>, bool), String> {
        let mut paths = std::collections::BTreeSet::new();
        let mut full_scan = false;
        for reader in &self.readers {
            let selected =
                query.candidates(&reader.all_file_ids(), |tri| reader.lookup_trigram(tri));
            full_scan |= selected.full_scan;
            for id in selected.files {
                paths.insert(
                    reader
                        .file_path(id)
                        .ok_or("invalid disk candidate locator")?
                        .to_string(),
                );
            }
        }
        Ok((paths.into_iter().collect(), full_scan))
    }
}

/// In-memory changed postings over a borrowed immutable disk base.
pub struct DiskOverlay<'a> {
    base: &'a DiskIndex,
    changed: crate::lexical_index::ResidentIndex,
    shadow: std::collections::BTreeSet<String>,
    id: String,
}

impl<'a> DiskOverlay<'a> {
    /// Bind verified changed bytes and tombstones to the candidate fingerprint.
    pub fn build(
        base: &'a DiskIndex,
        candidate: &str,
        changed: LexicalManifest,
        sources: std::collections::BTreeMap<String, Vec<u8>>,
        deleted: Vec<String>,
    ) -> Result<Self, String> {
        if candidate.len() != 64
            || !candidate
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        {
            return Err("invalid candidate fingerprint".into());
        }
        let changed =
            crate::lexical_index::ResidentIndex::build(changed, &base.manifest.source, sources)?;
        let mut shadow: std::collections::BTreeSet<String> =
            changed.files.keys().cloned().collect();
        let mut tombstones = std::collections::BTreeSet::new();
        for path in deleted {
            if base
                .manifest
                .files
                .binary_search_by(|f| f.path.cmp(&path))
                .is_err()
                || !shadow.insert(path.clone())
                || !tombstones.insert(path)
            {
                return Err("invalid or conflicting lexical tombstone".into());
            }
        }
        let identity = crate::canonical::encode(
            &serde_json::json!({"base": base.manifest_id, "candidate": candidate, "changed": changed.id, "deleted": tombstones}),
        )?;
        let id = crate::canonical::hash("harness.ri.lexical-overlay.v1", &identity);
        Ok(Self {
            base,
            changed,
            shadow,
            id,
        })
    }

    /// Search merged candidates; base reads occur only for unshadowed paths.
    pub fn search_page(
        &self,
        query: &crate::lexical::LexicalQuery,
        limit: usize,
        after: Option<&crate::lexical_index::LexicalCursor>,
        read_source: impl FnMut(&LexicalFile) -> Result<Vec<u8>, String>,
    ) -> Result<crate::lexical_index::LexicalResult, String> {
        self.base
            .search_view(query, limit, after, read_source, Some(self))
    }
}

fn file_hash(path: &Path) -> Result<String, String> {
    // Match the controller's per-shard-file ceiling. LimitReader-style counting
    // also rejects growth after metadata admission without reading indefinitely.
    const MAXIMUM: u64 = 1 << 30;
    let mut file = crate::lexical_io::regular(path, MAXIMUM)?.take(MAXIMUM + 1);
    let mut digest = Sha256::new();
    let mut buffer = [0u8; 64 * 1024];
    let mut total = 0u64;
    loop {
        let n = file.read(&mut buffer).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        total += n as u64;
        if total > MAXIMUM {
            return Err("lexical shard file exceeds byte ceiling".into());
        }
        digest.update(&buffer[..n]);
    }
    Ok(digest
        .finalize()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect())
}

fn flush(root: &Path, batch: LiveIndex, shards: &mut Vec<DiskShard>) -> Result<(), String> {
    let (paths, postings) = batch.snapshot_for_disk();
    let directory = format!("shard-{:06}", shards.len());
    let output = root.join(&directory);
    append_overlay_to_index(
        root,
        &output,
        &IndexReader::empty(),
        &paths,
        &postings,
        true,
    )
    .map_err(|e| e.to_string())?;
    let reader = IndexReader::open(&output).map_err(|e| e.to_string())?;
    reader.validate_lookup()?;
    if reader.all_paths() != paths.as_slice() {
        return Err("disk shard path readback mismatch".into());
    }
    let hashes = [
        file_hash(&output.join("lookup.bin"))?,
        file_hash(&output.join("index.bin"))?,
        file_hash(&output.join("files.bin"))?,
    ];
    shards.push(DiskShard {
        directory,
        files: paths.len(),
        hashes,
    });
    Ok(())
}

/// Build disjoint shards without rewriting preceding shards. The source callback
/// must itself bound allocation and read admitted objects, not mutable paths.
/// Batch ceilings bound source bytes and file count; posting overhead is additional.
/// Oversized individual files fail explicitly, never silently disappear.
pub fn build_shards(
    root: &Path,
    manifest: LexicalManifest,
    expected: &Source,
    batch_bytes: u64,
    batch_files: usize,
    mut read_source: impl FnMut(&LexicalFile) -> Result<Vec<u8>, String>,
) -> Result<DiskBuild, String> {
    let manifest = manifest.validate(expected)?;
    if batch_bytes == 0
        || batch_bytes > 64 << 20
        || batch_files == 0
        || batch_files > 10_000
        || manifest.files.iter().any(|f| f.bytes > batch_bytes)
    {
        return Err("invalid lexical disk batch ceiling".into());
    }
    let manifest_id = manifest.id()?;
    // Existing output, including a partial previous build, must never be reused.
    fs::create_dir(root).map_err(|e| e.to_string())?;
    let mut batch = LiveIndex::new();
    let mut used = 0u64;
    let mut count = 0usize;
    let mut shards = Vec::new();
    for file in manifest.files {
        if count > 0 && (used + file.bytes > batch_bytes || count == batch_files) {
            flush(root, batch, &mut shards)?;
            batch = LiveIndex::new();
            used = 0;
            count = 0;
        }
        let bytes = read_source(&file)?;
        file.verify_git_blob(&bytes, &expected.object_format)?;
        batch.upsert_file(&file.path, &bytes);
        used += file.bytes;
        count += 1;
    }
    if count > 0 {
        flush(root, batch, &mut shards)?;
    }
    Ok(DiskBuild {
        manifest_id,
        shards,
    })
}

#[cfg(test)]
mod artifact_bounds {
    use super::file_hash;

    #[test]
    fn shard_hash_rejects_oversized_and_nonregular_inputs() {
        let root = tempfile::tempdir().unwrap();
        assert!(file_hash(root.path()).is_err());
        let path = root.path().join("oversized");
        let file = std::fs::File::create(&path).unwrap();
        file.set_len((1 << 30) + 1).unwrap();
        drop(file);
        assert!(file_hash(&path).is_err());
        let small = root.path().join("small");
        std::fs::write(&small, b"abc").unwrap();
        assert_eq!(
            file_hash(&small).unwrap(),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }
}
