//! File adapters for controller-owned immutable lexical staging artifacts.
//! Leaf checks and content binding do not replace ancestor/lifetime isolation.

use crate::{
    lexical_manifest::{LexicalFile, LexicalManifest},
    manifest::Source,
};
use std::{
    fs::File,
    io::{BufReader, Read},
    path::Path,
};

pub(crate) fn regular(path: &Path, maximum: u64) -> Result<File, String> {
    if !path.is_absolute() {
        return Err("absolute lexical artifact path required".into());
    }
    let metadata = std::fs::symlink_metadata(path).map_err(|e| e.to_string())?;
    if !metadata.is_file() || metadata.len() > maximum {
        return Err("invalid lexical artifact file".into());
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        if metadata.file_attributes() & 0x400 != 0 {
            return Err("lexical artifact reparse point rejected".into());
        }
    }
    let file = File::open(path).map_err(|e| e.to_string())?;
    let opened = file.metadata().map_err(|e| e.to_string())?;
    if !opened.is_file() || opened.len() > maximum {
        return Err("invalid opened lexical artifact file".into());
    }
    Ok(file)
}

/// Read the bounded-record manifest format from an absolute artifact path.
pub fn manifest(path: &Path, id: &str, source: &Source) -> Result<LexicalManifest, String> {
    crate::lexical_manifest::read_manifest(BufReader::new(regular(path, 512 << 20)?), id, source)
}

/// Read a raw source artifact named by its admitted SHA-256, never its repository
/// path. Enforces declared bytes and the 64MiB source limit before allocation.
pub fn source(root: &Path, file: &LexicalFile) -> Result<Vec<u8>, String> {
    if file.sha256.len() != 64
        || !file
            .sha256
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        || file.bytes > 64 << 20
    {
        return Err("invalid lexical source artifact binding".into());
    }
    let mut bytes = Vec::new();
    regular(&root.join(&file.sha256), file.bytes)?
        .take(file.bytes + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| e.to_string())?;
    file.verify_bytes(&bytes)?;
    Ok(bytes)
}
