//! Exact manifest comparison without guessing semantic impact from source changes.
use crate::manifest::Manifest;
use std::collections::{BTreeMap, BTreeSet};

/// One producer's changed exact source input binding.
#[derive(Debug, PartialEq, Eq, serde::Serialize)]
pub struct SourceChange {
    /// Producer identity whose inputs are compared.
    pub producer: String,
    /// Relative path from the source input name.
    pub path: String,
    /// Previous source digest, null for an added input.
    pub before: Option<String>,
    /// Current source digest, null for a removed input.
    pub after: Option<String>,
}

/// Manifest differences, separated from unproved graph impact.
#[derive(Debug, serde::Serialize)]
pub struct Changes {
    /// Whether the exact commit/tree binding changed.
    pub source_identity_changed: bool,
    /// Producers added, removed or changed in implementation/configuration inputs.
    pub changed_producers: Vec<String>,
    /// Exact source-input changes, ordered by producer and path.
    pub sources: Vec<SourceChange>,
}

/// Exclusive position in the deterministic source-change ordering.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Position {
    /// Producer identity of the last returned change.
    pub producer: String,
    /// Source path of the last returned change.
    pub path: String,
}

/// Finite source-change projection; producer changes remain separately bounded.
#[derive(serde::Serialize)]
pub struct Page<'a> {
    /// Whether commit/tree identity changed.
    pub source_identity_changed: bool,
    /// All changed producers, at most 128 across the two registries.
    pub changed_producers: &'a [String],
    /// At most the requested number of source changes.
    pub sources: Vec<&'a SourceChange>,
    /// Exclusive position for the next page; outer protocol must bind artifact IDs.
    pub next: Option<Position>,
}

impl Changes {
    /// Page already computed differences without copying source records.
    ///
    /// # Errors
    /// Rejects limits outside 1..128 and oversized/empty continuation fields.
    pub fn page(&self, limit: usize, after: Option<&Position>) -> Result<Page<'_>, &'static str> {
        if !(1..=128).contains(&limit)
            || after.is_some_and(|p| {
                p.producer.is_empty()
                    || p.producer.len() > 256
                    || p.path.is_empty()
                    || p.path.len() > 4096
            })
        {
            return Err("invalid change page bounds");
        }
        let mut sources = Vec::new();
        let mut encoded_bytes = 0usize;
        let mut more = false;
        for change in self.sources.iter().filter(|change| {
            after.is_none_or(|p| (&change.producer, &change.path) > (&p.producer, &p.path))
        }) {
            let size = serde_json::to_vec(change)
                .map_err(|_| "change serialization failed")?
                .len()
                + 1;
            if size > 512 << 10 {
                return Err("single change exceeds page byte budget");
            }
            if sources.len() == limit || encoded_bytes + size > 512 << 10 {
                more = true;
                break;
            }
            encoded_bytes += size;
            sources.push(change);
        }
        let next = if more {
            let last = sources.last().ok_or("missing change continuation")?;
            Some(Position {
                producer: last.producer.clone(),
                path: last.path.clone(),
            })
        } else {
            None
        };
        Ok(Page {
            source_identity_changed: self.source_identity_changed,
            changed_producers: &self.changed_producers,
            sources,
            next,
        })
    }
}

/// Compare admitted manifests selected by the caller. Repository identity hashes
/// include commit state; the caller must establish common repository authority.
/// Input artifact and policy changes count as producer changes, independently
/// of source differences. Missing inputs mean absent declarations, not proof
/// that a file was added/deleted in Git. No blast radius is inferred.
///
/// # Errors
/// Rejects invalid manifests or different Git object formats.
pub fn compare(before: &Manifest, after: &Manifest) -> Result<Changes, String> {
    let before = before.clone().validate(&before.source)?;
    let after = after.clone().validate(&after.source)?;
    if before.source.object_format != after.source.object_format {
        return Err("cannot compare different Git object formats".into());
    }
    let old: BTreeMap<_, _> = before
        .producers
        .iter()
        .map(|p| (p.id.as_str(), p))
        .collect();
    let new: BTreeMap<_, _> = after.producers.iter().map(|p| (p.id.as_str(), p)).collect();
    let keys: BTreeSet<_> = old.keys().chain(new.keys()).copied().collect();
    let mut result = Changes {
        source_identity_changed: before.source != after.source,
        changed_producers: vec![],
        sources: vec![],
    };
    for id in keys {
        let a = old.get(id).copied();
        let b = new.get(id).copied();
        let identity = |p: &crate::manifest::Producer| {
            (
                p.name.clone(),
                p.version.clone(),
                p.artifact_sha256.clone(),
                p.inputs
                    .iter()
                    .filter(|i| !i.name.starts_with("source:"))
                    .cloned()
                    .collect::<Vec<_>>(),
            )
        };
        if a.map(identity) != b.map(identity) {
            result.changed_producers.push(id.into());
        }
        let inputs = |p: Option<&crate::manifest::Producer>| -> BTreeMap<String, String> {
            p.into_iter()
                .flat_map(|p| &p.inputs)
                .filter_map(|i| {
                    i.name
                        .strip_prefix("source:")
                        .map(|path| (path.to_owned(), i.sha256.clone()))
                })
                .collect()
        };
        let a = inputs(a);
        let b = inputs(b);
        let paths: BTreeSet<_> = a.keys().chain(b.keys()).collect();
        for path in paths {
            if a.get(path) != b.get(path) {
                result.sources.push(SourceChange {
                    producer: id.into(),
                    path: path.clone(),
                    before: a.get(path).cloned(),
                    after: b.get(path).cloned(),
                });
            }
        }
    }
    Ok(result)
}
