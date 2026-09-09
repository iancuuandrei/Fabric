//! Canonical lexical scope identities. Git membership is admitted by the
//! controller; byte digests are checked again before content is indexed.

use crate::{canonical, manifest::Source};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::io::{BufRead, Read};

#[derive(Serialize, Deserialize)]
#[serde(
    tag = "kind",
    content = "value",
    rename_all = "snake_case",
    deny_unknown_fields
)]
enum Record {
    Source(Source),
    File(LexicalFile),
}

/// Compare canonical JSONL against an already validated retained manifest using
/// one bounded record buffer. No second file-metadata collection is allocated.
pub fn verify_manifest(mut reader: impl BufRead, expected: &LexicalManifest) -> Result<(), String> {
    let mut line = Vec::new();
    let mut total = 0usize;
    let records = std::iter::once(Record::Source(expected.source.clone()))
        .chain(expected.files.iter().map(|file| Record::File(file.clone())));
    for record in records {
        line.clear();
        let n = reader
            .by_ref()
            .take((canonical::MAX_BYTES + 2) as u64)
            .read_until(b'\n', &mut line)
            .map_err(|e| e.to_string())?;
        total = total
            .checked_add(n)
            .ok_or("lexical manifest size overflow")?;
        if total > 512 << 20
            || n == 0
            || n > canonical::MAX_BYTES + 1
            || line.last() != Some(&b'\n')
        {
            return Err("oversized or torn retained manifest record".into());
        }
        let canonical =
            canonical::encode(&serde_json::to_value(record).map_err(|e| e.to_string())?)?;
        if line[..n - 1] != canonical {
            return Err("retained lexical manifest changed".into());
        }
    }
    if !reader.fill_buf().map_err(|e| e.to_string())?.is_empty() {
        return Err("retained lexical manifest has extra records".into());
    }
    Ok(())
}

/// Read bounded canonical JSONL records and verify the expected logical identity.
/// The first record binds the source; subsequent paths must be strictly sorted.
/// Total input is capped at 512MiB and each record at the canonical 1MiB limit.
pub fn read_manifest(
    mut reader: impl BufRead,
    expected_id: &str,
    expected: &Source,
) -> Result<LexicalManifest, String> {
    let mut source = None;
    let mut files: Vec<LexicalFile> = Vec::new();
    let mut total = 0usize;
    let mut line = Vec::new();
    loop {
        line.clear();
        let n = reader
            .by_ref()
            .take((canonical::MAX_BYTES + 2) as u64)
            .read_until(b'\n', &mut line)
            .map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        total = total
            .checked_add(n)
            .ok_or("lexical manifest size overflow")?;
        if total > 512 << 20 || n > canonical::MAX_BYTES + 1 || line.last() != Some(&b'\n') {
            return Err("oversized or torn lexical manifest record".into());
        }
        match canonical::decode::<Record>(&line[..n - 1])? {
            Record::Source(value) if source.is_none() && files.is_empty() => source = Some(value),
            Record::File(file) if source.is_some() => {
                if files.len() >= 500_000 || files.last().is_some_and(|last| last.path >= file.path)
                {
                    return Err("invalid lexical manifest file order or count".into());
                }
                files.push(file);
            }
            _ => return Err("invalid lexical manifest record order".into()),
        }
    }
    let manifest = LexicalManifest {
        version: 1,
        source: source.ok_or("lexical source record missing")?,
        files,
    }
    .validate(expected)?;
    if manifest.id()? != expected_id {
        return Err("lexical manifest identity mismatch".into());
    }
    Ok(manifest)
}

/// One controller-admitted regular file in the lexical scope.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct LexicalFile {
    /// Slash-separated repository-relative path.
    pub path: String,
    /// Git blob object identity in the source's object format.
    pub blob: String,
    /// SHA-256 of exact source bytes, without encoding conversion.
    pub sha256: String,
    /// Exact byte count.
    pub bytes: u64,
}

/// Immutable lexical input scope; omitted files cannot be inferred absent.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct LexicalManifest {
    /// Format version, currently one.
    pub version: u32,
    /// Exact repository observation supplied by the controller.
    pub source: Source,
    /// Explicit sorted scope; validation rejects duplicate paths.
    pub files: Vec<LexicalFile>,
}

fn digest(value: &str, length: usize) -> bool {
    value.len() == length
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

impl LexicalManifest {
    /// Normalize and validate an explicitly admitted scope, binding its source.
    /// This does not prove Git tree membership or complete repository coverage.
    pub fn validate(mut self, expected: &Source) -> Result<Self, String> {
        self.validate_fields(expected)?;
        self.files.sort_by(|a, b| a.path.cmp(&b.path));
        if self
            .files
            .windows(2)
            .any(|pair| pair[0].path == pair[1].path)
        {
            return Err("duplicate lexical path".into());
        }
        Ok(self)
    }

    fn validate_fields(&self, expected: &Source) -> Result<(), String> {
        self.source.validate()?;
        if self.version != 1 || &self.source != expected || self.files.len() > 500_000 {
            return Err("invalid lexical scope identity or size".into());
        }
        let oid_len = if self.source.object_format == "sha1" {
            40
        } else {
            64
        };
        for file in &self.files {
            if file.path.is_empty()
                || file.path.len() > 4096
                || file
                    .path
                    .chars()
                    .any(|c| c.is_control() || c == '\\' || c == ':')
                || file
                    .path
                    .split('/')
                    .any(|part| part.is_empty() || part == "." || part == "..")
                || !digest(&file.blob, oid_len)
                || !digest(&file.sha256, 64)
                || file.bytes > 9_007_199_254_740_991
            {
                return Err("invalid lexical file binding".into());
            }
        }
        Ok(())
    }

    /// Content identity calculated incrementally from bounded canonical records.
    /// Input ordering is normalized; no giant JSON array allocation is required.
    pub fn id(&self) -> Result<String, String> {
        self.validate_fields(&self.source)?;
        // Sort borrowed pointers, never duplicate the path/blob/hash strings.
        let mut files: Vec<_> = self.files.iter().collect();
        files.sort_by(|a, b| a.path.cmp(&b.path));
        if files.windows(2).any(|pair| pair[0].path == pair[1].path) {
            return Err("duplicate lexical path".into());
        }
        let mut hash = Sha256::new();
        hash.update(b"harness.ri.lexical-manifest.v1\0");
        hash.update(canonical::encode(
            &serde_json::to_value(&self.source).map_err(|e| e.to_string())?,
        )?);
        hash.update(b"\n");
        for file in files {
            hash.update(canonical::encode(
                &serde_json::to_value(file).map_err(|e| e.to_string())?,
            )?);
            hash.update(b"\n");
        }
        Ok(hash.finalize().iter().map(|b| format!("{b:02x}")).collect())
    }
}

impl LexicalFile {
    /// Verify content and the Git blob hash, including Git's type/size header.
    /// This proves the bytes match the claimed blob, not tree membership.
    pub fn verify_git_blob(&self, bytes: &[u8], object_format: &str) -> Result<(), String> {
        self.verify_bytes(bytes)?;
        let header = format!("blob {}\0", bytes.len());
        let oid = match object_format {
            "sha1" => {
                use sha1::Digest as Sha1Digest;
                let mut hash = sha1::Sha1::new();
                hash.update(header.as_bytes());
                hash.update(bytes);
                hash.finalize()
                    .iter()
                    .map(|b| format!("{b:02x}"))
                    .collect::<String>()
            }
            "sha256" => {
                let mut hash = Sha256::new();
                hash.update(header.as_bytes());
                hash.update(bytes);
                hash.finalize()
                    .iter()
                    .map(|b| format!("{b:02x}"))
                    .collect::<String>()
            }
            _ => return Err("unsupported Git object format".into()),
        };
        if oid != self.blob {
            return Err("lexical Git blob identity mismatch".into());
        }
        Ok(())
    }

    /// Check exact content bytes against the admitted length and SHA-256.
    pub fn verify_bytes(&self, bytes: &[u8]) -> Result<(), String> {
        if self.bytes != bytes.len() as u64
            || self.sha256
                != Sha256::digest(bytes)
                    .iter()
                    .map(|b| format!("{b:02x}"))
                    .collect::<String>()
        {
            return Err("lexical source bytes differ from admission".into());
        }
        Ok(())
    }
}
