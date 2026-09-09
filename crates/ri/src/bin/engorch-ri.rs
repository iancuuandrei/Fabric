//! Owned RI subprocess protocol with one-shot and bounded persistent framing.

use engorch_ri::{
    canonical,
    manifest::Source,
    query::{Query, Snapshot},
    snapshot::MAX_SNAPSHOT_BYTES,
};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    fs::File,
    io::{self, Read, Write},
    path::Path,
};

#[derive(Serialize, Deserialize)]
#[serde(tag = "operation", rename_all = "snake_case", deny_unknown_fields)]
enum Request {
    LexicalSearch {
        manifest_path: String,
        manifest_id: String,
        source: Source,
        source_root: String,
        index_path: String,
        build: engorch_ri::lexical_disk::DiskBuild,
        build_id: String,
        pattern: String,
        fixed: bool,
        case_insensitive: bool,
        #[serde(default, skip_serializing_if = "String::is_empty")]
        path_filter: String,
        #[serde(default, skip_serializing_if = "String::is_empty")]
        type_filter: String,
        limit: usize,
        after: Option<engorch_ri::lexical_index::LexicalCursor>,
        #[serde(skip_serializing_if = "Option::is_none")]
        overlay: Option<LexicalOverlayRequest>,
    },
    LexicalBuild {
        manifest_path: String,
        manifest_id: String,
        source: Source,
        source_root: String,
        output_path: String,
        batch_bytes: u64,
        batch_files: usize,
    },
    LexicalManifestFile {
        path: String,
        manifest_id: String,
        source: Source,
    },
    LexicalManifest {
        manifest: engorch_ri::lexical_manifest::LexicalManifest,
        source: Source,
    },
    ScipImportExpected {
        request: engorch_ri::import::Request,
    },
    ScipImport {
        request: engorch_ri::import::Request,
        output_path: String,
    },
    ChangedPage {
        before_path: String,
        before_id: String,
        before_source: Source,
        after_path: String,
        after_id: String,
        after_source: Source,
        limit: usize,
        cursor: Option<String>,
    },
    Changed {
        before_path: String,
        before_id: String,
        before_source: Source,
        after_path: String,
        after_id: String,
        after_source: Source,
    },
    Locate {
        path: String,
        snapshot_id: String,
        source: Source,
        query: engorch_ri::query::LocateQuery,
        after: Option<String>,
    },
    Coverage {
        path: String,
        snapshot_id: String,
        source: Source,
        node: String,
        relation: engorch_ri::graph::Relation,
        direction: engorch_ri::graph::Direction,
    },
    Path {
        path: String,
        snapshot_id: String,
        source: Source,
        query: engorch_ri::graph::PathQuery,
    },
    ScipSymbol {
        path: String,
        snapshot_id: String,
        source: Source,
        producer: String,
        symbol: String,
        document: Option<String>,
    },
    Occurrences {
        path: String,
        snapshot_id: String,
        source: Source,
        query: engorch_ri::query::OccurrenceQuery,
        after: Option<String>,
    },
    RustTypes {
        source_text: String,
        source_sha256: String,
    },
    Status {
        path: String,
        snapshot_id: String,
        source: Source,
    },
    Neighbors {
        path: String,
        snapshot_id: String,
        source: Source,
        query: Query,
        after: Option<String>,
    },
}

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct LexicalOverlayRequest {
    candidate: String,
    manifest_path: String,
    manifest_id: String,
    source_root: String,
    deleted: Vec<String>,
}

fn read_snapshot(path: &str, id: &str, source: &Source) -> Result<Snapshot, String> {
    let path = Path::new(path);
    if !path.is_absolute() {
        return Err("absolute snapshot artifact path required".into());
    }
    let meta = std::fs::symlink_metadata(path).map_err(|e| e.to_string())?;
    if !meta.file_type().is_file() || meta.len() > MAX_SNAPSHOT_BYTES as u64 {
        return Err("snapshot must be a bounded regular file".into());
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::MetadataExt;
        if meta.file_attributes() & 0x400 != 0 {
            return Err("snapshot reparse points are not admitted".into());
        }
    }
    let mut bytes = Vec::new();
    File::open(path)
        .map_err(|e| e.to_string())?
        .take(MAX_SNAPSHOT_BYTES as u64 + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| e.to_string())?;
    Snapshot::read(&bytes, id, source)
}

fn execute() -> Result<Value, String> {
    if std::env::args().skip(1).collect::<Vec<_>>() != ["--stdio"] {
        return Err("usage: engorch-ri --stdio; provide one canonical request on stdin".into());
    }
    let mut bytes = Vec::new();
    io::stdin()
        .take(canonical::MAX_BYTES as u64 + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| e.to_string())?;
    let request: Request = canonical::decode(&bytes)?;
    execute_request(request, &mut None)
}

type RetainedIndex = Option<(Vec<u8>, engorch_ri::lexical_disk::DiskIndex)>;

fn execute_request(request: Request, retained: &mut RetainedIndex) -> Result<Value, String> {
    match request {
        Request::LexicalSearch {
            manifest_path,
            manifest_id,
            source,
            source_root,
            index_path,
            build,
            build_id,
            pattern,
            fixed,
            case_insensitive,
            path_filter,
            type_filter,
            limit,
            after,
            overlay,
        } => {
            if !Path::new(&index_path).is_absolute() || !Path::new(&source_root).is_absolute() {
                return Err("absolute lexical artifact paths required".into());
            }
            let query = engorch_ri::lexical::LexicalQuery::compile_filtered(
                &pattern,
                fixed,
                case_insensitive,
                (!path_filter.is_empty()).then_some(path_filter.as_str()),
                (!type_filter.is_empty()).then_some(type_filter.as_str()),
            )?;
            let key = canonical::encode(
                &json!({"manifest_path":manifest_path,"manifest_id":manifest_id,"source":source,"source_root":source_root,"index_path":index_path,"build":build,"build_id":build_id}),
            )?;
            if retained
                .as_ref()
                .is_some_and(|(previous, _)| previous == &key)
            {
                // Retention saves mmap setup and decoded lookup/path metadata.
                // Byte verification remains mandatory without OS immutability.
                let verified = retained
                    .as_ref()
                    .ok_or("missing retained index")?
                    .1
                    .verify_manifest(Path::new(&manifest_path))
                    .and_then(|()| build.verify_files(Path::new(&index_path)));
                if let Err(error) = verified {
                    *retained = None;
                    return Err(error);
                }
            } else {
                // Evict before opening so distinct repository scopes cannot
                // accumulate mapped indexes in a long-running process.
                *retained = None;
                let manifest = engorch_ri::lexical_io::manifest(
                    Path::new(&manifest_path),
                    &manifest_id,
                    &source,
                )?;
                let index = engorch_ri::lexical_disk::DiskIndex::open(
                    Path::new(&index_path),
                    &build,
                    &build_id,
                    manifest,
                    &source,
                )?;
                *retained = Some((key, index));
            }
            let index = &retained.as_ref().ok_or("missing retained lexical index")?.1;
            let read_base = |file: &engorch_ri::lexical_manifest::LexicalFile| {
                engorch_ri::lexical_io::source(Path::new(&source_root), file)
            };
            let result = if let Some(overlay) = overlay {
                let changed = engorch_ri::lexical_io::manifest(
                    Path::new(&overlay.manifest_path),
                    &overlay.manifest_id,
                    &source,
                )?;
                // Admit the aggregate before loading any resident source bytes.
                let total = changed
                    .files
                    .iter()
                    .try_fold(0u64, |sum, file| sum.checked_add(file.bytes))
                    .ok_or("overlay source size overflow")?;
                if total > engorch_ri::lexical_index::MAX_RESIDENT_BYTES {
                    return Err("overlay source bytes exceed resident ceiling".into());
                }
                let sources = changed
                    .files
                    .iter()
                    .map(|file| {
                        engorch_ri::lexical_io::source(Path::new(&overlay.source_root), file)
                            .map(|bytes| (file.path.clone(), bytes))
                    })
                    .collect::<Result<std::collections::BTreeMap<_, _>, _>>()?;
                let merged = engorch_ri::lexical_disk::DiskOverlay::build(
                    index,
                    &overlay.candidate,
                    changed,
                    sources,
                    overlay.deleted,
                )?;
                merged.search_page(&query, limit, after.as_ref(), read_base)?
            } else {
                index.search_page(&query, limit, after.as_ref(), read_base)?
            };
            serde_json::to_value(result).map_err(|e| e.to_string())
        }
        Request::LexicalBuild {
            manifest_path,
            manifest_id,
            source,
            source_root,
            output_path,
            batch_bytes,
            batch_files,
        } => {
            if !Path::new(&output_path).is_absolute() || !Path::new(&source_root).is_absolute() {
                return Err("absolute lexical staging paths required".into());
            }
            let manifest =
                engorch_ri::lexical_io::manifest(Path::new(&manifest_path), &manifest_id, &source)?;
            let build = engorch_ri::lexical_disk::build_shards(
                Path::new(&output_path),
                manifest,
                &source,
                batch_bytes,
                batch_files,
                |file| engorch_ri::lexical_io::source(Path::new(&source_root), file),
            )?;
            Ok(json!({"build_id":build.id()?,"build":build}))
        }
        Request::LexicalManifestFile {
            path,
            manifest_id,
            source,
        } => {
            let path = Path::new(&path);
            if !path.is_absolute() {
                return Err("absolute lexical artifact path required".into());
            }
            let meta = std::fs::symlink_metadata(path).map_err(|e| e.to_string())?;
            if !meta.is_file() || meta.len() > 512 << 20 {
                return Err("invalid lexical artifact file".into());
            }
            #[cfg(windows)]
            {
                use std::os::windows::fs::MetadataExt;
                if meta.file_attributes() & 0x400 != 0 {
                    return Err("lexical artifact reparse point rejected".into());
                }
            }
            let reader = io::BufReader::new(File::open(path).map_err(|e| e.to_string())?);
            let manifest =
                engorch_ri::lexical_manifest::read_manifest(reader, &manifest_id, &source)?;
            Ok(json!({"manifest_id": manifest_id, "files": manifest.files.len(), "source": source}))
        }
        Request::LexicalManifest { manifest, source } => {
            let manifest = manifest.validate(&source)?;
            Ok(
                json!({"manifest_id": manifest.id()?, "files": manifest.files.len(), "source": source}),
            )
        }
        Request::ScipImportExpected { request } => {
            let source = request.source.clone();
            let artifact = engorch_ri::import::build(request)?;
            Ok(json!({"snapshot_id":artifact.id,"bytes":artifact.bytes.len(),"source":source}))
        }
        Request::ScipImport {
            request,
            output_path,
        } => {
            let source = request.source.clone();
            let (snapshot_id, bytes) = engorch_ri::import::stage(request, &output_path)?;
            Ok(
                json!({"snapshot_id":snapshot_id,"bytes":bytes,"source":source,"output_path":output_path}),
            )
        }
        Request::ChangedPage {
            before_path,
            before_id,
            before_source,
            after_path,
            after_id,
            after_source,
            limit,
            cursor,
        } => {
            let before = read_snapshot(&before_path, &before_id, &before_source)?;
            let after = read_snapshot(&after_path, &after_id, &after_source)?;
            before.changed_page(&after, limit, cursor.as_deref())
        }
        Request::Changed {
            before_path,
            before_id,
            before_source,
            after_path,
            after_id,
            after_source,
        } => {
            let before = read_snapshot(&before_path, &before_id, &before_source)?;
            let after = read_snapshot(&after_path, &after_id, &after_source)?;
            Ok(
                json!({"before_id":before_id,"after_id":after_id,"changes":before.changes_to(&after)?}),
            )
        }
        Request::Locate {
            path,
            snapshot_id,
            source,
            query,
            after,
        } => {
            let snapshot = read_snapshot(&path, &snapshot_id, &source)?;
            serde_json::to_value(snapshot.locate(&query, after.as_deref())?)
                .map_err(|e| e.to_string())
        }
        Request::Coverage {
            path,
            snapshot_id,
            source,
            node,
            relation,
            direction,
        } => {
            let snapshot = read_snapshot(&path, &snapshot_id, &source)?;
            Ok(
                json!({"snapshot_id":snapshot_id,"node":node,"relation":relation,"direction":direction,
                "declarations":snapshot.coverage(&node,relation,direction)?}),
            )
        }
        Request::Path {
            path,
            snapshot_id,
            source,
            query,
        } => {
            let snapshot = read_snapshot(&path, &snapshot_id, &source)?;
            Ok(json!({"snapshot_id":snapshot_id,"query":query,"evidence":snapshot.path(&query)?}))
        }
        Request::ScipSymbol {
            path,
            snapshot_id,
            source,
            producer,
            symbol,
            document,
        } => {
            let snapshot = read_snapshot(&path, &snapshot_id, &source)?;
            Ok(
                json!({"snapshot_id":snapshot_id,"node":snapshot.scip_symbol(&producer,&symbol,document.as_deref())?,"absence_proven":false}),
            )
        }
        Request::Occurrences {
            path,
            snapshot_id,
            source,
            query,
            after,
        } => {
            let snapshot = read_snapshot(&path, &snapshot_id, &source)?;
            serde_json::to_value(snapshot.occurrences(&query, after.as_deref())?)
                .map_err(|e| e.to_string())
        }
        Request::RustTypes {
            source_text,
            source_sha256,
        } => {
            let result = engorch_ri::structural::rust_types(source_text.as_bytes())?;
            if result.source_sha256 != source_sha256 {
                return Err("structural source hash mismatch".into());
            }
            serde_json::to_value(result).map_err(|e| e.to_string())
        }
        Request::Status {
            path,
            snapshot_id,
            source,
        } => {
            let snapshot = read_snapshot(&path, &snapshot_id, &source)?;
            let (nodes, edges, coverage) = snapshot.counts();
            Ok(
                json!({"snapshot_id":snapshot_id,"source":snapshot.source(),"nodes":nodes,"edges":edges,"coverage_records":coverage,"occurrences":snapshot.occurrence_count(),"producers":snapshot.producer_ids()}),
            )
        }
        Request::Neighbors {
            path,
            snapshot_id,
            source,
            query,
            after,
        } => {
            let snapshot = read_snapshot(&path, &snapshot_id, &source)?;
            serde_json::to_value(snapshot.neighbors(&query, after.as_deref())?)
                .map_err(|e| e.to_string())
        }
    }
}

fn main() {
    if std::env::args().skip(1).collect::<Vec<_>>() == ["--stdio-stream"] {
        let mut retained = None;
        let result =
            engorch_ri::transport::serve(io::stdin().lock(), io::stdout().lock(), |value| {
                let bytes = canonical::encode(&value)?;
                execute_request(canonical::decode(&bytes)?, &mut retained)
            });
        if let Err(error) = result {
            eprintln!("{error}");
            std::process::exit(2);
        }
        return;
    }
    let (response, code) = match execute() {
        Ok(result) => (json!({"version":1,"ok":true,"result":result}), 0),
        Err(error) => (json!({"version":1,"ok":false,"error":error}), 2),
    };
    let encoded = match canonical::encode(&response) {
        Ok(bytes) => bytes,
        Err(_) => {
            let _ = io::stdout().write_all(
                b"{\"error\":\"response exceeds protocol bounds\",\"ok\":false,\"version\":1}",
            );
            std::process::exit(2);
        }
    };
    if io::stdout().write_all(&encoded).is_err() {
        std::process::exit(2);
    }
    std::process::exit(code);
}
