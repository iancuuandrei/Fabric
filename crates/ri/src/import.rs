//! Explicit disk-input admission. No project-root URI is used as a filesystem path.
use crate::{
    manifest::{Manifest, Source},
    scip,
    snapshot::Artifact,
};
use std::{
    collections::BTreeMap,
    fs::File,
    io::{Read, Write},
    path::Path,
};

/// Create a new staging artifact after complete admission, never replacing a path.
/// The controller must journal intent before calling and reconcile interrupted
/// writes explicitly. A successful return is not content-addressed publication.
///
/// # Errors
/// Rejects relative/existing output paths, invalid inputs and write/sync failures.
/// A write failure can leave partial staging bytes; no automatic deletion occurs.
pub fn stage(request: Request, output: &str) -> Result<(String, usize), String> {
    if !Path::new(output).is_absolute() {
        return Err("absolute import staging path required".into());
    }
    let artifact = build(request)?;
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(output).map_err(|e| e.to_string())?;
    file.write_all(&artifact.bytes).map_err(|e| e.to_string())?;
    file.sync_all().map_err(|e| e.to_string())?;
    Ok((artifact.id, artifact.bytes.len()))
}

/// Selected position policy; compatibility is never inferred from missing fields.
#[derive(Debug, Clone, Copy, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Policy {
    /// Require explicit SCIP coordinate encodings.
    Strict,
    /// Require the pinned scip-go 0.2.7 provenance policy.
    ScipGo027,
}

/// Exact input selection supplied by the controller, independent of index paths.
#[derive(serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Request {
    /// Absolute binary SCIP artifact path.
    pub index_path: String,
    /// Declared provenance and all expected input hashes.
    pub manifest: Manifest,
    /// Independently observed repository binding.
    pub source: Source,
    /// Registered semantic producer identity.
    pub producer: String,
    /// Exact metadata URI, compared as text only.
    pub project_root: String,
    /// Repository-relative source names mapped to explicit absolute disk paths.
    pub sources: BTreeMap<String, String>,
    /// Explicit coordinate compatibility selection.
    pub policy: Policy,
}

/// Read a bounded regular artifact without following a final symlink/reparse point.
/// Hash verification by the caller remains necessary; this is not OS confinement.
///
/// # Errors
/// Rejects relative paths, nonregular files, final reparse points and excess bytes.
pub fn read_file(path: &str, limit: usize) -> Result<Vec<u8>, String> {
    let path = Path::new(path);
    if !path.is_absolute() {
        return Err("absolute artifact path required".into());
    }
    let meta = std::fs::symlink_metadata(path).map_err(|e| e.to_string())?;
    if !meta.is_file() || meta.len() > limit as u64 {
        return Err("artifact must be a bounded regular file".into());
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        if meta.file_attributes() & 0x400 != 0 {
            return Err("artifact reparse point rejected".into());
        }
    }
    let mut bytes = Vec::new();
    File::open(path)
        .map_err(|e| e.to_string())?
        .take(limit as u64 + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| e.to_string())?;
    if bytes.len() > limit {
        return Err("artifact exceeds byte limit".into());
    }
    Ok(bytes)
}

/// Import explicitly selected immutable inputs into validated snapshot bytes.
/// The controller owns committed-source acquisition, publication and journaling.
///
/// # Errors
/// Rejects unbound source selections, oversized inputs and SCIP admission failures.
pub fn build(request: Request) -> Result<Artifact, String> {
    let manifest = request.manifest.validate(&request.source)?;
    let producer = manifest
        .producers
        .iter()
        .find(|p| p.id == request.producer)
        .ok_or("import producer not registered")?;
    if request.sources.len() > 4095 {
        return Err("too many import sources".into());
    }
    for path in request.sources.keys() {
        if !producer
            .inputs
            .iter()
            .any(|i| i.name == format!("source:{path}"))
        {
            return Err("import source is not bound in producer manifest".into());
        }
    }
    let index = read_file(&request.index_path, 64 << 20)?;
    let mut sources = BTreeMap::new();
    let mut remaining = 64 << 20;
    for (name, path) in request.sources {
        let bytes = read_file(&path, remaining)?;
        remaining -= bytes.len();
        sources.insert(name, bytes);
    }
    let admit = match request.policy {
        Policy::Strict => scip::admit,
        Policy::ScipGo027 => scip::admit_scip_go_027,
    };
    admit(
        &index,
        manifest,
        &request.source,
        &request.producer,
        &request.project_root,
        &sources,
    )?
    .snapshot(&sources)
}
