//! SCIP binary decoding and source-coordinate admission.

use crate::occurrence::{Encoding, SourceText, Span};
use std::collections::{BTreeMap, BTreeSet};

/// A decoded index whose supplied source bytes match the manifest input hashes.
/// This establishes declared provenance bindings, not producer authenticity.
pub struct Admitted {
    index: wire::Index,
    manifest: crate::manifest::Manifest,
    producer: String,
}

/// Explicit, versioned compatibility rule for scip-go's omitted position field.
/// Captured v0.2.7 source uses go/token byte columns in visitors.scipRange.
pub const SCIP_GO_027_POSITION_POLICY: &str =
    "engorch.scip-go.0.2.7.positions.v1:utf8-byte-columns";

/// Admit scip-go 0.2.7 with its source-verified UTF-8 byte-column convention.
/// The manifest must include `scip:position-policy` hashing the exact policy
/// constant above. Original index bytes remain bound by `scip:index`.
///
/// # Errors
/// Rejects missing policy provenance, other producers/versions, explicit
/// incompatible position encodings, and all ordinary admission failures.
pub fn admit_scip_go_027(
    bytes: &[u8],
    manifest: crate::manifest::Manifest,
    expected: &crate::manifest::Source,
    producer_id: &str,
    project_root: &str,
    sources: &BTreeMap<String, Vec<u8>>,
) -> Result<Admitted, String> {
    let producer = manifest
        .producers
        .iter()
        .find(|p| p.id == producer_id)
        .ok_or("SCIP producer absent")?;
    if producer.name != "scip-go"
        || producer.version != "0.2.7"
        || !producer.inputs.iter().any(|input| {
            input.name == "scip:position-policy"
                && input.sha256 == raw_hash(SCIP_GO_027_POSITION_POLICY.as_bytes())
        })
    {
        return Err("SCIP compatibility policy missing or producer mismatch".into());
    }
    admit_inner(
        bytes,
        manifest,
        expected,
        producer_id,
        project_root,
        sources,
        true,
    )
}

impl Admitted {
    /// Inspect the decoded index, including relationships and externals.
    /// An explicit compatibility profile may normalize unspecified encodings;
    /// the manifest still binds the original artifact and the profile rule.
    pub fn index(&self) -> &wire::Index {
        &self.index
    }
    /// Inspect normalized repository and producer provenance.
    pub fn manifest(&self) -> &crate::manifest::Manifest {
        &self.manifest
    }
    /// Identify the producer whose inputs were verified.
    pub fn producer(&self) -> &str {
        &self.producer
    }

    /// Build an immutable graph snapshot from admitted semantic observations.
    /// Source bytes are rechecked against the manifest before conversion.
    /// Exact duplicate occurrences coalesce. File-to-symbol edges distinguish
    /// definitions from references; no call edges or complete coverage is inferred.
    /// Rich SCIP documentation/diagnostics remain in the input artifact, whose
    /// hash is retained in the manifest, rather than in graph navigation records.
    ///
    /// # Errors
    /// Rejects substituted sources, invalid symbols and snapshot size/schema limits.
    pub fn snapshot(
        &self,
        sources: &BTreeMap<String, Vec<u8>>,
    ) -> Result<crate::snapshot::Artifact, String> {
        use crate::graph::{Edge, Node, NodeKind, Quality, Relation};
        let mut nodes: Vec<Node> = self
            .symbols()?
            .into_iter()
            .map(|symbol| Node {
                scip: Some(crate::graph::ScipName {
                    producer: self.producer.clone(),
                    symbol: symbol.raw,
                }),
                id: symbol.id,
                kind: NodeKind::Symbol,
                path: symbol.document,
            })
            .collect();
        let mut edges: BTreeMap<String, Edge> = self
            .relationship_edges()?
            .into_iter()
            .map(|edge| (edge.id.clone(), edge))
            .collect();
        let mut occurrences = BTreeMap::new();
        let producer = self
            .manifest
            .producers
            .iter()
            .find(|p| p.id == self.producer)
            .ok_or("SCIP producer disappeared")?;
        let inputs: BTreeMap<_, _> = producer
            .inputs
            .iter()
            .map(|input| (input.name.as_str(), input.sha256.as_str()))
            .collect();
        for document in &self.index.documents {
            let path = &document.relative_path;
            let source = sources
                .get(path)
                .ok_or("SCIP source missing during snapshot conversion")?;
            if source.len() > 64 << 20 {
                return Err("SCIP source exceeds bound".into());
            }
            let source_hash = raw_hash(source);
            if inputs.get(format!("source:{path}").as_str()).copied() != Some(source_hash.as_str())
            {
                return Err("SCIP source changed after admission".into());
            }
            let file = crate::canonical::hash("harness.ri.file.v1", path.as_bytes());
            nodes.push(Node {
                scip: None,
                id: file.clone(),
                kind: NodeKind::File,
                path: Some(path.clone()),
            });
            for located in locate_document(document, source)? {
                let symbol = if located.symbol.is_empty() {
                    None
                } else {
                    Some(SymbolIdentity::new(&self.producer, located.symbol, Some(path))?.id)
                };
                let key = crate::canonical::encode(&serde_json::json!({
                    "path":path,"source":source_hash,"producer":self.producer,
                    "span":located.span,"symbol":symbol,"roles":located.roles,
                }))?;
                let id = crate::canonical::hash("harness.ri.scip-occurrence.v1", &key);
                if let Some(target) = &symbol {
                    let relation = if located.roles.is_definition() {
                        Relation::Defines
                    } else {
                        Relation::References
                    };
                    let key = crate::canonical::encode(
                        &serde_json::json!({"file":file,"symbol":target,"relation":relation,"producer":self.producer}),
                    )?;
                    let edge_id = crate::canonical::hash("harness.ri.scip-file-edge.v1", &key);
                    edges.insert(
                        edge_id.clone(),
                        Edge {
                            id: edge_id,
                            from: file.clone(),
                            to: target.clone(),
                            relation,
                            producer: self.producer.clone(),
                            quality: Quality::Declared,
                        },
                    );
                }
                occurrences.insert(
                    id.clone(),
                    crate::occurrence::Occurrence {
                        id,
                        path: path.clone(),
                        source_sha256: source_hash.clone(),
                        span: located.span,
                        spelling: located.spelling.into(),
                        symbol,
                        roles: Some(located.roles),
                        producer: self.producer.clone(),
                        quality: Quality::Declared,
                    },
                );
            }
        }
        crate::snapshot::build_with_occurrences(
            self.manifest.clone(),
            &self.manifest.source,
            nodes,
            edges.into_values().collect(),
            vec![],
            occurrences.into_values().collect(),
        )
    }

    /// Enumerate exact symbol identities from occurrences, declarations and
    /// relationship targets. Locals are scoped to their containing document.
    /// Global strings remain opaque; this does not parse their descriptor grammar.
    ///
    /// # Errors
    /// Rejects invalid local IDs, local external symbols and excessive symbols.
    pub fn symbols(&self) -> Result<Vec<SymbolIdentity>, String> {
        let mut symbols = BTreeMap::new();
        let mut insert = |raw: &str, path: Option<&str>| -> Result<(), String> {
            if raw.is_empty() {
                return Ok(());
            }
            let symbol = SymbolIdentity::new(&self.producer, raw, path)?;
            symbols.insert(symbol.id.clone(), symbol);
            if symbols.len() > 100_000 {
                return Err("SCIP symbol count exceeds bound".into());
            }
            Ok(())
        };
        for document in &self.index.documents {
            let path = Some(document.relative_path.as_str());
            for occurrence in &document.occurrences {
                insert(&occurrence.symbol, path)?;
            }
            for info in &document.symbols {
                insert(&info.symbol, path)?;
                for relationship in &info.relationships {
                    insert(&relationship.symbol, path)?;
                }
            }
        }
        for info in &self.index.external_symbols {
            insert(&info.symbol, None)?;
            for relationship in &info.relationships {
                insert(&relationship.symbol, None)?;
            }
        }
        Ok(symbols.into_values().collect())
    }

    /// Convert explicitly declared SCIP relationships into provenance-bearing
    /// graph edges. Each true flag creates its own category; false flags do not
    /// prove absence. Reference-related edges retain producer direction, with
    /// bidirectional search expansion left to the query layer.
    ///
    /// # Errors
    /// Rejects empty relationship endpoints, invalid symbol scope and excessive
    /// edge counts. Exact duplicate declarations are coalesced deterministically.
    pub fn relationship_edges(&self) -> Result<Vec<crate::graph::Edge>, String> {
        use crate::graph::{Edge, Quality, Relation};
        let mut edges = BTreeMap::new();
        let mut add = |info: &wire::SymbolInformation, path: Option<&str>| -> Result<(), String> {
            for relationship in &info.relationships {
                let from = SymbolIdentity::new(&self.producer, &info.symbol, path)?;
                let to = SymbolIdentity::new(&self.producer, &relationship.symbol, path)?;
                for (present, relation) in [
                    (relationship.is_reference, Relation::ReferenceRelated),
                    (relationship.is_implementation, Relation::Implements),
                    (relationship.is_type_definition, Relation::TypeDefinition),
                    (relationship.is_definition, Relation::DefinitionRelated),
                ] {
                    if !present {
                        continue;
                    }
                    let key = crate::canonical::encode(&serde_json::json!({
                        "producer":self.producer,"from":from.id,"to":to.id,"relation":relation,
                    }))?;
                    let id = crate::canonical::hash("harness.ri.scip-relationship.v1", &key);
                    edges.insert(
                        id.clone(),
                        Edge {
                            id,
                            from: from.id.clone(),
                            to: to.id.clone(),
                            relation,
                            producer: self.producer.clone(),
                            quality: Quality::Declared,
                        },
                    );
                    if edges.len() > 1_000_000 {
                        return Err("SCIP relationship edge count exceeds bound".into());
                    }
                }
            }
            Ok(())
        };
        for document in &self.index.documents {
            for info in &document.symbols {
                add(info, Some(&document.relative_path))?;
            }
        }
        for info in &self.index.external_symbols {
            add(info, None)?;
        }
        Ok(edges.into_values().collect())
    }
}

/// An exact producer symbol key with document scope only for SCIP locals.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct SymbolIdentity {
    /// Domain-separated stable graph identity.
    pub id: String,
    /// Unmodified producer symbol string, never a display-name approximation.
    pub raw: String,
    /// Document scope for local symbols; null for global symbols.
    pub document: Option<String>,
}

impl SymbolIdentity {
    /// Construct a producer-scoped identity, retaining opaque global spelling.
    ///
    /// # Errors
    /// Rejects absent/oversized identifiers and malformed or unscoped locals.
    pub fn new(producer: &str, raw: &str, document: Option<&str>) -> Result<Self, String> {
        if producer.is_empty() || producer.len() > 256 || raw.is_empty() || raw.len() > 16384 {
            return Err("invalid SCIP symbol identity size".into());
        }
        let document = if let Some(local) = raw.strip_prefix("local ") {
            if local.is_empty()
                || !local
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b"_+-$".contains(&b))
            {
                return Err("invalid SCIP local symbol identifier".into());
            }
            let path = document.ok_or("local SCIP symbol lacks document scope")?;
            if path.is_empty() || path.len() > 4096 {
                return Err("invalid local symbol document".into());
            }
            Some(path.to_owned())
        } else {
            if raw.starts_with("local") {
                return Err("reserved SCIP local scheme prefix".into());
            }
            None
        };
        let key = crate::canonical::encode(
            &serde_json::json!({"producer":producer,"symbol":raw,"document":document}),
        )?;
        Ok(Self {
            id: crate::canonical::hash("harness.ri.scip-symbol.v1", &key),
            raw: raw.into(),
            document,
        })
    }
}

/// Bind binary SCIP and document bytes to an exact repository manifest.
/// `sources` contains committed regular-file bytes supplied by the controller.
/// Producer inputs must include `scip:index` and `source:<relative_path>` hashes.
/// The expected project root is compared as an opaque URI and never opened.
/// No complete-coverage records are inferred, including for an empty index.
///
/// # Errors
/// Rejects manifest/source mismatches, absent or conflicting producer metadata,
/// hash substitutions, invalid/duplicate paths, missing sources, invalid document
/// observations and excessive aggregate source/occurrence counts.
pub fn admit(
    bytes: &[u8],
    manifest: crate::manifest::Manifest,
    expected: &crate::manifest::Source,
    producer_id: &str,
    project_root: &str,
    sources: &BTreeMap<String, Vec<u8>>,
) -> Result<Admitted, String> {
    admit_inner(
        bytes,
        manifest,
        expected,
        producer_id,
        project_root,
        sources,
        false,
    )
}

fn admit_inner(
    bytes: &[u8],
    manifest: crate::manifest::Manifest,
    expected: &crate::manifest::Source,
    producer_id: &str,
    project_root: &str,
    sources: &BTreeMap<String, Vec<u8>>,
    scip_go_027: bool,
) -> Result<Admitted, String> {
    if bytes.is_empty() || bytes.len() > 64 << 20 {
        return Err("SCIP artifact size outside bounds".into());
    }
    let manifest = manifest.validate(expected)?;
    let producer = manifest
        .producers
        .iter()
        .find(|p| p.id == producer_id)
        .ok_or("SCIP producer absent from manifest")?;
    let inputs: BTreeMap<_, _> = producer
        .inputs
        .iter()
        .map(|input| (input.name.as_str(), input.sha256.as_str()))
        .collect();
    if inputs.get("scip:index").copied() != Some(raw_hash(bytes).as_str()) {
        return Err("SCIP artifact input hash mismatch".into());
    }
    let mut index = decode(bytes)?;
    if scip_go_027 {
        for document in &mut index.documents {
            match document.position_encoding {
                0 => document.position_encoding = 1,
                1 => {}
                _ => return Err("scip-go position policy conflicts with explicit encoding".into()),
            }
        }
    }
    let metadata = index.metadata.as_ref().ok_or("missing SCIP metadata")?;
    let tool = metadata
        .tool_info
        .as_ref()
        .ok_or("missing SCIP tool identity")?;
    if metadata.version != 0
        || metadata.text_document_encoding != 1
        || project_root.is_empty()
        || metadata.project_root != project_root
        || tool.name != producer.name
        || tool.version != producer.version
    {
        return Err("SCIP metadata differs from expected producer/root/encoding".into());
    }
    if index.documents.len() > 4095 {
        return Err("SCIP document count exceeds bound".into());
    }
    let mut paths = BTreeSet::new();
    let mut total_bytes = 0usize;
    let mut total_occurrences = 0usize;
    for document in &index.documents {
        let path = &document.relative_path;
        if path.is_empty()
            || path.len() > 4096
            || path.contains(['\\', ':'])
            || path.chars().any(char::is_control)
            || path
                .split('/')
                .any(|part| part.is_empty() || part == "." || part == "..")
            || !paths.insert(path)
        {
            return Err("invalid or duplicate SCIP document path".into());
        }
        let source = sources.get(path).ok_or("SCIP document source missing")?;
        total_bytes = total_bytes
            .checked_add(source.len())
            .ok_or("SCIP source size overflow")?;
        total_occurrences = total_occurrences
            .checked_add(document.occurrences.len())
            .ok_or("SCIP occurrence count overflow")?;
        if total_bytes > 64 << 20 || total_occurrences > 100_000 {
            return Err("SCIP aggregate source/occurrence bound exceeded".into());
        }
        if inputs.get(format!("source:{path}").as_str()).copied() != Some(raw_hash(source).as_str())
        {
            return Err("SCIP document source hash mismatch".into());
        }
        locate_document(document, source)?;
    }
    Ok(Admitted {
        index,
        manifest,
        producer: producer_id.into(),
    })
}

fn raw_hash(bytes: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    Sha256::digest(bytes)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

/// Complete generated bindings for the pinned upstream SCIP schema.
#[allow(missing_docs, clippy::all)]
pub mod wire {
    include!(concat!(env!("OUT_DIR"), "/scip.rs"));
}

/// Decode a bounded protobuf index, without trusting its provenance or coverage.
///
/// # Errors
/// Rejects empty or oversized artifacts and malformed protobuf messages.
pub fn decode(bytes: &[u8]) -> Result<wire::Index, String> {
    use prost::Message;
    if bytes.is_empty() || bytes.len() > 64 << 20 {
        return Err("SCIP artifact size outside bounds".into());
    }
    wire::Index::decode(bytes).map_err(|error| format!("invalid SCIP protobuf: {error}"))
}

/// One source-validated producer occurrence, before graph identity assignment.
#[derive(Debug, PartialEq, Eq)]
pub struct Located<'a> {
    /// Exact interval in caller-supplied source bytes.
    pub span: Span,
    /// Exact spelling, borrowed from those bytes.
    pub spelling: &'a str,
    /// Producer symbol string; empty remains unresolved.
    pub symbol: &'a str,
    /// Producer role flags without inferred roles.
    pub roles: Roles,
    /// Optional enclosing interval, checked to contain the occurrence.
    pub enclosing: Option<Span>,
}

/// Validate document observations against caller-owned immutable UTF-8 bytes.
/// The caller must separately bind these bytes and the index to repository and
/// producer identities. This function does not read `project_root` from disk.
///
/// # Errors
/// Rejects unsupported encoding, substituted embedded text, excessive occurrence
/// counts, inconsistent ranges, invalid role bits and non-enclosing spans.
#[allow(deprecated)] // Both legacy and typed SCIP representations are supported.
pub fn locate_document<'a>(
    document: &'a wire::Document,
    bytes: &'a [u8],
) -> Result<Vec<Located<'a>>, &'static str> {
    if document.occurrences.len() > 100_000 {
        return Err("SCIP document occurrence count exceeds bound");
    }
    let encoding = Encoding::from_scip(document.position_encoding)?;
    let source = SourceText::new(bytes)?;
    let text = std::str::from_utf8(bytes).map_err(|_| "source is not UTF-8")?;
    if !document.text.is_empty() && document.text != text {
        return Err("SCIP embedded document text differs from source");
    }
    document
        .occurrences
        .iter()
        .map(|occurrence| {
            use wire::occurrence::{TypedEnclosingRange as E, TypedRange as R};
            let typed = occurrence.typed_range.as_ref().map(|r| match r {
                R::SingleLineRange(r) => {
                    TypedRange::SingleLine([r.line, r.start_character, r.end_character])
                }
                R::MultiLineRange(r) => TypedRange::MultiLine([
                    r.start_line,
                    r.start_character,
                    r.end_line,
                    r.end_character,
                ]),
            });
            let interval = span(&source, encoding, &occurrence.range, typed)?;
            let enclosing_typed = occurrence.typed_enclosing_range.as_ref().map(|r| match r {
                E::SingleLineEnclosingRange(r) => {
                    TypedRange::SingleLine([r.line, r.start_character, r.end_character])
                }
                E::MultiLineEnclosingRange(r) => TypedRange::MultiLine([
                    r.start_line,
                    r.start_character,
                    r.end_line,
                    r.end_character,
                ]),
            });
            let enclosing = if enclosing_typed.is_some() || !occurrence.enclosing_range.is_empty() {
                let enclosing = span(
                    &source,
                    encoding,
                    &occurrence.enclosing_range,
                    enclosing_typed,
                )?;
                if enclosing.start > interval.start || enclosing.end < interval.end {
                    return Err("SCIP enclosing range does not contain occurrence");
                }
                Some(enclosing)
            } else {
                None
            };
            Ok(Located {
                span: interval,
                spelling: &text[interval.start..interval.end],
                symbol: &occurrence.symbol,
                roles: Roles::from_scip(occurrence.symbol_roles)?,
                enclosing,
            })
        })
        .collect()
}

/// Producer-declared symbol roles, preserving all seven specified SCIP flags.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(try_from = "i32", into = "u8")]
pub struct Roles(u8);

impl TryFrom<i32> for Roles {
    type Error = &'static str;
    fn try_from(bits: i32) -> Result<Self, Self::Error> {
        Self::from_scip(bits)
    }
}

impl From<Roles> for u8 {
    fn from(roles: Roles) -> Self {
        roles.bits()
    }
}

impl Roles {
    /// Admit the pinned schema's bitset without silently dropping unknown flags.
    ///
    /// # Errors
    /// Rejects negative values and flags outside the supported schema.
    pub fn from_scip(bits: i32) -> Result<Self, &'static str> {
        if !(0..=0x7f).contains(&bits) {
            return Err("unsupported SCIP symbol role bits");
        }
        Ok(Self(bits as u8))
    }

    /// Return the exact admitted producer bitset.
    pub fn bits(self) -> u8 {
        self.0
    }

    /// Whether the producer explicitly marks this occurrence as a definition.
    pub fn is_definition(self) -> bool {
        self.0 & 1 != 0
    }
}

/// Typed SCIP range alternatives, with original signed coordinates preserved.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TypedRange {
    /// Line, start character, end character.
    SingleLine([i32; 3]),
    /// Start line, start character, end line, end character.
    MultiLine([i32; 4]),
}

/// Convert both SCIP range encodings and enforce equivalence when both exist.
/// Empty legacy arrays mean absent; an explicit empty typed range remains valid.
///
/// # Errors
/// Rejects missing, malformed, out-of-source or conflicting ranges.
pub fn span(
    source: &SourceText<'_>,
    encoding: Encoding,
    legacy: &[i32],
    typed: Option<TypedRange>,
) -> Result<Span, &'static str> {
    let legacy = if legacy.is_empty() {
        None
    } else {
        Some(source.scip_span(legacy, encoding)?)
    };
    let typed = typed
        .map(|range| match range {
            TypedRange::SingleLine(values) => source.scip_span(&values, encoding),
            TypedRange::MultiLine(values) => source.scip_span(&values, encoding),
        })
        .transpose()?;
    match (legacy, typed) {
        (Some(a), Some(b)) if a != b => Err("conflicting SCIP range encodings"),
        (_, Some(value)) | (Some(value), None) => Ok(value),
        (None, None) => Err("missing SCIP occurrence range"),
    }
}
