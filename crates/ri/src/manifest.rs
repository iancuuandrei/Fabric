//! Explicit snapshot provenance, validated without filesystem or network effects.
//!
//! Validation establishes shape and graph/producer bindings, not authenticity of
//! claimed digests. The artifact reader must verify content hashes separately.

use std::collections::BTreeSet;

use crate::graph::{Coverage, Edge, Graph, Node};

/// Source authority copied from the controller's immutable repository observation.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Source {
    /// Canonical controller repository identity SHA-256.
    pub repository_id: String,
    /// Git object format: sha1 or sha256.
    pub object_format: String,
    /// Exact commit object ID.
    pub commit: String,
    /// Exact tree object ID.
    pub tree: String,
}

/// One immutable producer input; names distinguish source/configuration artifacts.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Input {
    /// Stable logical input name, not an ambient filesystem lookup.
    pub name: String,
    /// SHA-256 of the exact input bytes.
    pub sha256: String,
}

/// Exact producer identity and all declared inputs required to reproduce its work.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Producer {
    /// Stable identity referenced by graph edges and coverage records.
    pub id: String,
    /// Human-readable producer implementation name.
    pub name: String,
    /// Exact producer version, not an unversioned alias.
    pub version: String,
    /// SHA-256 of the producer executable or source artifact.
    pub artifact_sha256: String,
    /// Explicit input artifact identities, sorted by name after admission.
    pub inputs: Vec<Input>,
}

/// Versioned snapshot provenance. It contains no mutable latest pointer.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Manifest {
    /// Snapshot format version, independent of the CLI/package version.
    pub format: u32,
    /// Exact source identity for the entire graph.
    pub source: Source,
    /// Producer registry, sorted by ID after admission.
    pub producers: Vec<Producer>,
}

fn digest(value: &str, length: usize) -> bool {
    value.len() == length
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn label(value: &str) -> bool {
    !value.trim().is_empty() && value.len() <= 256 && !value.chars().any(char::is_control)
}

impl Source {
    /// Validate repository and Git object identifier shapes without claiming
    /// that the named objects exist or that supplied source bytes match them.
    pub fn validate(&self) -> Result<(), &'static str> {
        let length = match self.object_format.as_str() {
            "sha1" => 40,
            "sha256" => 64,
            _ => return Err("unsupported source object format"),
        };
        if !digest(&self.repository_id, 64)
            || !digest(&self.commit, length)
            || !digest(&self.tree, length)
        {
            return Err("invalid source identity digest");
        }
        Ok(())
    }
}

impl Manifest {
    /// Validate the exact expected source and normalize producer/input order.
    ///
    /// # Errors
    /// Rejects unknown format, source substitution, malformed IDs, empty producer
    /// registries, duplicate producer/input identities or more than 64 producers
    /// and 4,096 inputs per producer. This does not verify claimed artifact bytes.
    pub fn validate(mut self, expected: &Source) -> Result<Self, &'static str> {
        if self.format != 1 || &self.source != expected {
            return Err("snapshot format or expected source mismatch");
        }
        self.source.validate()?;
        if self.producers.is_empty() || self.producers.len() > 64 {
            return Err("invalid producer registry size");
        }
        self.producers.sort_by(|a, b| a.id.cmp(&b.id));
        let mut seen = BTreeSet::new();
        for producer in &mut self.producers {
            if !label(&producer.id)
                || !label(&producer.name)
                || !label(&producer.version)
                || !digest(&producer.artifact_sha256, 64)
                || !seen.insert(producer.id.clone())
            {
                return Err("invalid or duplicate producer identity");
            }
            if producer.inputs.is_empty() || producer.inputs.len() > 4096 {
                return Err("invalid producer input count");
            }
            producer.inputs.sort_by(|a, b| a.name.cmp(&b.name));
            let mut previous = None;
            for input in &producer.inputs {
                if input.name.trim().is_empty()
                    || input.name.len() > 4103
                    || input.name.chars().any(char::is_control)
                    || !digest(&input.sha256, 64)
                    || previous == Some(input.name.as_str())
                {
                    return Err("invalid or duplicate producer input");
                }
                previous = Some(input.name.as_str());
            }
        }
        Ok(self)
    }
}

/// A graph whose relationships and coverage all refer to registered producers.
/// This is an in-memory construction result, not a verified on-disk snapshot.
pub struct BoundGraph {
    manifest: Manifest,
    graph: Graph,
    occurrences: crate::occurrence::Index,
}

impl BoundGraph {
    /// Bind graph evidence to the expected source and a validated producer registry.
    ///
    /// # Errors
    /// Propagates manifest/graph validation failures and rejects unregistered
    /// producers, including producer names appearing only in coverage claims.
    pub fn build(
        manifest: Manifest,
        expected: &Source,
        nodes: Vec<Node>,
        edges: Vec<Edge>,
        coverage: Vec<Coverage>,
    ) -> Result<Self, &'static str> {
        Self::build_with_occurrences(manifest, expected, nodes, edges, coverage, Vec::new())
    }

    /// Bind graph and occurrence evidence to the expected source and producers.
    ///
    /// # Errors
    /// Rejects graph/manifest errors and any occurrence without an exact registered
    /// file input or valid resolved symbol target.
    pub fn build_with_occurrences(
        manifest: Manifest,
        expected: &Source,
        nodes: Vec<Node>,
        edges: Vec<Edge>,
        coverage: Vec<Coverage>,
        occurrences: Vec<crate::occurrence::Occurrence>,
    ) -> Result<Self, &'static str> {
        let manifest = manifest.validate(expected)?;
        let producers: BTreeSet<_> = manifest.producers.iter().map(|p| p.id.as_str()).collect();
        for node in &nodes {
            if let Some(name) = &node.scip {
                if node.kind != crate::graph::NodeKind::Symbol
                    || !producers.contains(name.producer.as_str())
                {
                    return Err("invalid SCIP node kind or producer");
                }
                let identity = crate::scip::SymbolIdentity::new(
                    &name.producer,
                    &name.symbol,
                    node.path.as_deref(),
                )
                .map_err(|_| "invalid SCIP node symbol")?;
                if identity.id != node.id || identity.document != node.path {
                    return Err("SCIP node identity or document scope mismatch");
                }
            }
        }
        if edges
            .iter()
            .any(|e| !producers.contains(e.producer.as_str()))
            || coverage
                .iter()
                .any(|c| !producers.contains(c.producer.as_str()))
        {
            return Err("graph evidence references an unregistered producer");
        }
        let mut bound = Self {
            manifest,
            graph: Graph::build(nodes, edges, coverage)?,
            occurrences: crate::occurrence::Index::empty(),
        };
        bound.occurrences = crate::occurrence::Index::build(&bound, occurrences)?;
        Ok(bound)
    }

    /// Read normalized immutable provenance.
    pub fn manifest(&self) -> &Manifest {
        &self.manifest
    }

    /// Query the validated immutable graph without changing provenance.
    pub fn graph(&self) -> &Graph {
        &self.graph
    }

    /// Access admitted immutable source occurrences and their lookup indexes.
    pub fn occurrences(&self) -> &crate::occurrence::Index {
        &self.occurrences
    }
}
