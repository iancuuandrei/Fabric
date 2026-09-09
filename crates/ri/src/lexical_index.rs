//! Immutable resident lexical index over verified source bytes. Disk-backed base
//! construction and candidate overlays are separate integration steps.

use crate::{
    lexical::LexicalQuery,
    lexical_manifest::{LexicalFile, LexicalManifest},
    manifest::Source,
};
use std::collections::{BTreeMap, BTreeSet};
use tgrep_core::live::LiveIndex;

/// Resident source-byte ceiling. Posting allocation is additional and is not
/// represented by this number; this is not a total-process memory budget.
pub const MAX_RESIDENT_BYTES: u64 = 64 << 20;

/// Exact lexical hit, with provenance copied from a validated manifest.
#[derive(Debug, PartialEq, Eq, serde::Serialize)]
pub struct LexicalHit {
    /// Repository-relative source path.
    pub path: String,
    /// Verified Git blob identity.
    pub blob: String,
    /// Exact half-open source byte range.
    pub range: [usize; 2],
}

/// Bounded exact matches in path/byte order over an explicit admitted scope.
#[derive(Debug, serde::Serialize)]
pub struct LexicalResult {
    /// Exact query/profile identity.
    pub query_id: String,
    /// Last returned hit when more exact results exist.
    pub next: Option<LexicalCursor>,
    /// Content identity of the admitted lexical manifest.
    pub manifest_id: String,
    /// Whether candidate filtering was bypassed.
    pub full_scan: bool,
    /// Number of candidate files before exact matching.
    pub candidate_files: usize,
    /// Number of files actually inspected before any result truncation.
    pub searched_files: usize,
    /// True only when an additional exact hit beyond the output limit was found.
    pub truncated: bool,
    /// Exact matches; absence applies only to this explicit scope.
    pub matches: Vec<LexicalHit>,
}

/// Continuation bound to exact sources and matcher options; not authorization.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct LexicalCursor {
    pub(crate) manifest_id: String,
    pub(crate) query_id: String,
    pub(crate) path: String,
    pub(crate) range: [usize; 2],
}

/// A sealed in-memory index. No mutation or ambient filesystem reads are exposed.
pub struct ResidentIndex {
    pub(crate) id: String,
    source: Source,
    pub(crate) postings: LiveIndex,
    pub(crate) files: BTreeMap<String, (LexicalFile, Vec<u8>)>,
}

impl ResidentIndex {
    /// Build from an exact source set. Missing/extra/substituted bytes reject the
    /// entire build; no partial index is returned with implied complete coverage.
    pub fn build(
        manifest: LexicalManifest,
        expected: &Source,
        mut sources: BTreeMap<String, Vec<u8>>,
    ) -> Result<Self, String> {
        let manifest = manifest.validate(expected)?;
        let total = manifest
            .files
            .iter()
            .try_fold(0u64, |sum, file| sum.checked_add(file.bytes))
            .ok_or("lexical size overflow")?;
        if total > MAX_RESIDENT_BYTES || sources.len() != manifest.files.len() {
            return Err("resident lexical source scope exceeds bounds or differs".into());
        }
        let id = manifest.id()?;
        let mut files = BTreeMap::new();
        let mut postings = LiveIndex::new();
        for file in manifest.files {
            let bytes = sources.remove(&file.path).ok_or("lexical source missing")?;
            file.verify_git_blob(&bytes, &expected.object_format)?;
            postings.upsert_file(&file.path, &bytes);
            files.insert(file.path.clone(), (file, bytes));
        }
        if !sources.is_empty() {
            return Err("unadmitted lexical sources".into());
        }
        Ok(Self {
            id,
            source: expected.clone(),
            postings,
            files,
        })
    }

    /// Search admitted bytes with a maximum of 10,000 returned exact hits.
    /// A limit never turns remaining unknown results into proven absence.
    pub fn search(&self, query: &LexicalQuery, limit: usize) -> Result<LexicalResult, String> {
        self.search_page(query, limit, None)
    }

    /// Resume after an exact previously returned match. Rejects stale source or
    /// query bindings and boundaries that are not actual matches.
    pub fn search_page(
        &self,
        query: &LexicalQuery,
        limit: usize,
        after: Option<&LexicalCursor>,
    ) -> Result<LexicalResult, String> {
        self.search_view(query, limit, after, None)
    }

    fn search_view(
        &self,
        query: &LexicalQuery,
        limit: usize,
        after: Option<&LexicalCursor>,
        overlay: Option<&CandidateOverlay<'_>>,
    ) -> Result<LexicalResult, String> {
        let view_id = overlay.map_or(self.id.as_str(), |o| o.id.as_str());
        if limit == 0 || limit > 10_000 {
            return Err("invalid lexical result limit".into());
        }
        if after.is_some_and(|c| c.manifest_id != view_id || c.query_id != query.id()) {
            return Err("lexical cursor identity mismatch".into());
        }
        let mut boundary_seen = after.is_none();
        let mut paths = BTreeMap::new();
        let mut full_scan = false;
        for index in std::iter::once(self).chain(overlay.map(|o| &o.changed)) {
            let candidates = query.candidates(&index.postings.all_file_ids(), |tri| {
                index.postings.lookup_trigram(tri)
            });
            full_scan |= candidates.full_scan;
            for id in candidates.files {
                let path = index
                    .postings
                    .file_path(id)
                    .ok_or("lexical posting identity missing")?;
                if !query.includes_path(path) {
                    continue;
                }
                if std::ptr::eq(index, self) && overlay.is_some_and(|o| o.shadow.contains(path)) {
                    continue;
                }
                paths.insert(
                    path,
                    index
                        .files
                        .get(path)
                        .ok_or("lexical source identity missing")?,
                );
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
        for (path, (file, bytes)) in paths {
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
                    path: path.into(),
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
}

/// Immutable candidate view borrowing its base; only changed bytes are indexed.
/// Candidate identity is supplied by the controller and is not filesystem proof.
pub struct CandidateOverlay<'a> {
    base: &'a ResidentIndex,
    changed: ResidentIndex,
    shadow: BTreeSet<String>,
    id: String,
}

impl<'a> CandidateOverlay<'a> {
    /// Admit verified replacements/creations and explicit deletion tombstones.
    /// Changed paths and candidate fingerprint participate in the view identity.
    pub fn build(
        base: &'a ResidentIndex,
        candidate: &str,
        changed: LexicalManifest,
        sources: BTreeMap<String, Vec<u8>>,
        deleted: Vec<String>,
    ) -> Result<Self, String> {
        if candidate.len() != 64
            || !candidate
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        {
            return Err("invalid candidate fingerprint".into());
        }
        let changed = ResidentIndex::build(changed, &base.source, sources)?;
        let mut shadow: BTreeSet<String> = changed.files.keys().cloned().collect();
        let mut tombstones = BTreeSet::new();
        for path in deleted {
            if !base.files.contains_key(&path)
                || !shadow.insert(path.clone())
                || !tombstones.insert(path)
            {
                return Err("invalid or conflicting lexical tombstone".into());
            }
        }
        let identity = crate::canonical::encode(
            &serde_json::json!({"base": base.id, "candidate": candidate, "changed": changed.id, "deleted": tombstones}),
        )?;
        let id = crate::canonical::hash("harness.ri.lexical-overlay.v1", &identity);
        Ok(Self {
            base,
            changed,
            shadow,
            id,
        })
    }

    /// Search the candidate, shadowing base entries before output limits apply.
    pub fn search_page(
        &self,
        query: &LexicalQuery,
        limit: usize,
        after: Option<&LexicalCursor>,
    ) -> Result<LexicalResult, String> {
        self.base.search_view(query, limit, after, Some(self))
    }
}
