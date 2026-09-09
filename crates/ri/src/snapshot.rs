//! Content-addressed snapshot bytes with deterministic record order.
//!
//! No file is opened or replaced here. Publication is a separate controller
//! effect. Readers require an expected artifact hash and source identity.

use serde::{Deserialize, Serialize};

use crate::{
    canonical,
    graph::{Coverage, Edge, Node},
    manifest::{BoundGraph, Manifest, Source},
};

/// Maximum total snapshot bytes, including newline delimiters.
pub const MAX_SNAPSHOT_BYTES: usize = 64 << 20;

#[derive(Serialize, Deserialize)]
#[serde(
    tag = "kind",
    content = "value",
    rename_all = "snake_case",
    deny_unknown_fields
)]
enum Record {
    Manifest(Manifest),
    Node(Node),
    Edge(Edge),
    Coverage(Coverage),
    Occurrence(crate::occurrence::Occurrence),
}

/// Exact snapshot bytes and their domain-separated content identity.
pub struct Artifact {
    /// Canonical JSONL, including a final newline.
    pub bytes: Vec<u8>,
    /// SHA-256 over the snapshot domain and all exact bytes.
    pub id: String,
}

fn append(bytes: &mut Vec<u8>, record: Record) -> Result<(), String> {
    let value = serde_json::to_value(record).map_err(|e| e.to_string())?;
    let encoded = canonical::encode(&value)?;
    if bytes.len() + encoded.len() + 1 > MAX_SNAPSHOT_BYTES {
        return Err("snapshot exceeds byte bound".into());
    }
    bytes.extend(encoded);
    bytes.push(b'\n');
    Ok(())
}

/// Validate inputs, normalize order and construct an immutable snapshot artifact.
///
/// # Errors
/// Rejects invalid graph/provenance, oversize records or total bytes. Output does
/// not depend on input ordering and contains no timestamps or ambient host state.
pub fn build(
    manifest: Manifest,
    expected: &Source,
    nodes: Vec<Node>,
    edges: Vec<Edge>,
    coverage: Vec<Coverage>,
) -> Result<Artifact, String> {
    build_with_occurrences(manifest, expected, nodes, edges, coverage, Vec::new())
}

/// Construct canonical snapshot bytes including validated source occurrences.
///
/// # Errors
/// Rejects invalid provenance, graph, occurrence bindings and size/order bounds.
pub fn build_with_occurrences(
    manifest: Manifest,
    expected: &Source,
    mut nodes: Vec<Node>,
    mut edges: Vec<Edge>,
    mut coverage: Vec<Coverage>,
    occurrences: Vec<crate::occurrence::Occurrence>,
) -> Result<Artifact, String> {
    let bound = BoundGraph::build_with_occurrences(
        manifest,
        expected,
        nodes.clone(),
        edges.clone(),
        coverage.clone(),
        occurrences,
    )?;
    nodes.sort_by(|a, b| a.id.cmp(&b.id));
    edges.sort_by(|a, b| a.id.cmp(&b.id));
    coverage.sort_by(|a, b| {
        (&a.node, a.relation, a.direction, &a.producer).cmp(&(
            &b.node,
            b.relation,
            b.direction,
            &b.producer,
        ))
    });
    let mut bytes = Vec::new();
    append(&mut bytes, Record::Manifest(bound.manifest().clone()))?;
    for node in nodes {
        append(&mut bytes, Record::Node(node))?;
    }
    for edge in edges {
        append(&mut bytes, Record::Edge(edge))?;
    }
    for entry in coverage {
        append(&mut bytes, Record::Coverage(entry))?;
    }
    for occurrence in bound.occurrences().records() {
        append(&mut bytes, Record::Occurrence(occurrence.clone()))?;
    }
    let id = canonical::hash("harness.ri.snapshot.v1", &bytes);
    Ok(Artifact { bytes, id })
}

/// Verify exact artifact identity, record encoding/order and expected source.
///
/// # Errors
/// Rejects changed bytes, torn records, noncanonical/schema-invalid JSON, misplaced
/// records and invalid graph/provenance. It never rebuilds source evidence.
pub fn read(bytes: &[u8], expected_id: &str, expected: &Source) -> Result<BoundGraph, String> {
    if bytes.is_empty() || bytes.len() > MAX_SNAPSHOT_BYTES || !bytes.ends_with(b"\n") {
        return Err("invalid snapshot byte bound or torn tail".into());
    }
    if canonical::hash("harness.ri.snapshot.v1", bytes) != expected_id {
        return Err("snapshot hash mismatch".into());
    }
    let mut manifest = None;
    let (mut nodes, mut edges, mut coverage) = (Vec::new(), Vec::new(), Vec::new());
    let mut occurrences = Vec::new();
    let mut phase = 0;
    for line in bytes[..bytes.len() - 1].split(|&b| b == b'\n') {
        match canonical::decode::<Record>(line)? {
            Record::Manifest(value) if phase == 0 => {
                manifest = Some(value);
                phase = 1;
            }
            Record::Node(value) if phase == 1 => nodes.push(value),
            Record::Edge(value) if phase == 1 || phase == 2 => {
                edges.push(value);
                phase = 2;
            }
            Record::Coverage(value) if (1..=3).contains(&phase) => {
                coverage.push(value);
                phase = 3;
            }
            Record::Occurrence(value) if (1..=4).contains(&phase) => {
                occurrences.push(value);
                phase = 4;
            }
            _ => return Err("invalid snapshot record phase".into()),
        }
        if nodes.len() > 100_000
            || edges.len() > 1_000_000
            || coverage.len() > 1_000_000
            || occurrences.len() > 100_000
        {
            return Err("snapshot record count exceeds bound".into());
        }
    }
    let manifest = manifest.ok_or("snapshot manifest missing")?;
    if nodes.windows(2).any(|pair| pair[0].id >= pair[1].id)
        || occurrences.windows(2).any(|pair| pair[0].id >= pair[1].id)
        || edges.windows(2).any(|pair| pair[0].id >= pair[1].id)
        || coverage.windows(2).any(|pair| {
            let a = &pair[0];
            let b = &pair[1];
            (&a.node, a.relation, a.direction, &a.producer)
                >= (&b.node, b.relation, b.direction, &b.producer)
        })
        || manifest
            .producers
            .windows(2)
            .any(|pair| pair[0].id >= pair[1].id)
        || manifest
            .producers
            .iter()
            .any(|p| p.inputs.windows(2).any(|pair| pair[0].name >= pair[1].name))
    {
        return Err("snapshot record order is not canonical".into());
    }
    BoundGraph::build_with_occurrences(manifest, expected, nodes, edges, coverage, occurrences)
        .map_err(str::to_owned)
}
