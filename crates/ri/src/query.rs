//! Finite query pages with exact snapshot/query-bound continuation.

use crate::{
    canonical,
    graph::{Direction, Neighbors, Relation},
    manifest::{BoundGraph, Source},
    snapshot,
};
use serde::{Deserialize, Serialize};

/// Exact adjacency query; producer and direction cannot be inferred from context.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Query {
    /// Existing graph node identity.
    pub node: String,
    /// Requested relationship.
    pub relation: Relation,
    /// Requested direction.
    pub direction: Direction,
    /// Registered producer identity.
    pub producer: String,
    /// Maximum edges returned, between 1 and 128.
    pub limit: usize,
}

/// One registered producer's exact-scope coverage declaration.
#[derive(Serialize)]
pub struct CoverageDeclaration<'a> {
    /// Registered producer identity, linkable to the snapshot manifest.
    pub producer: &'a str,
    /// Declared completeness, or null when the producer made no assertion.
    pub completeness: Option<crate::graph::Completeness>,
}

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Cursor {
    snapshot: String,
    query: String,
    after: String,
}

/// Direct semantic occurrence query; relationship expansion is separate.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct OccurrenceQuery {
    /// Existing resolved symbol node ID.
    pub symbol: String,
    /// Exact registered producer scope.
    pub producer: String,
    /// True for definitions, false for references; unknown roles are excluded.
    pub definitions: bool,
    /// Maximum records, between 1 and 128.
    pub limit: usize,
}

/// Exact byte-position lookup retaining overlapping source observations.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct LocateQuery {
    /// Existing committed file path.
    pub path: String,
    /// Zero-based UTF-8 byte offset; half-open intervals exclude their end.
    pub offset: usize,
    /// Registered producer identity.
    pub producer: String,
    /// Maximum returned observations, from 1 through 128.
    pub limit: usize,
}

/// Bounded direct occurrence results without completeness inference.
#[derive(Serialize)]
pub struct OccurrencePage<'a> {
    /// Source-bound semantic observations in stable ID order.
    pub occurrences: Vec<&'a crate::occurrence::Occurrence>,
    /// Exact artifact identity.
    pub snapshot_id: &'a str,
    /// All query parameters bound to this response.
    pub query_id: String,
    /// Next-page token or null on exhaustion.
    pub next_after: Option<String>,
    /// Always false: current occurrence coverage cannot prove absence.
    pub absence_proven: bool,
}

/// Page evidence and optional canonical continuation token.
#[derive(Serialize)]
pub struct Page<'a> {
    /// Exact-scope graph evidence; empty pages alone do not prove absence.
    pub evidence: Neighbors<'a>,
    /// Exact snapshot identity.
    pub snapshot_id: &'a str,
    /// Domain-separated identity of all query parameters, including page limit.
    pub query_id: String,
    /// Canonical cursor for the next page, or none at exhaustion.
    pub next_after: Option<String>,
}

/// A query handle obtainable only by verifying snapshot bytes and expected source.
pub struct Snapshot {
    id: String,
    bound: BoundGraph,
}

impl Snapshot {
    /// Return a finite change page with continuation bound to both artifacts.
    ///
    /// # Errors
    /// Rejects invalid page limits, foreign cursors and incompatible manifests.
    pub fn changed_page(
        &self,
        after: &Snapshot,
        limit: usize,
        cursor: Option<&str>,
    ) -> Result<serde_json::Value, String> {
        #[derive(Serialize, Deserialize)]
        #[serde(deny_unknown_fields)]
        struct ChangeCursor {
            before: String,
            after: String,
            limit: usize,
            position: crate::changed::Position,
        }
        let cursor = cursor
            .map(|text| canonical::decode::<ChangeCursor>(text.as_bytes()))
            .transpose()?;
        if cursor
            .as_ref()
            .is_some_and(|c| c.before != self.id || c.after != after.id || c.limit != limit)
        {
            return Err("change cursor identity mismatch".into());
        }
        let changes = self.changes_to(after)?;
        let page = changes.page(limit, cursor.as_ref().map(|c| &c.position))?;
        let next_after = page
            .next
            .as_ref()
            .map(|position| -> Result<String, String> {
                let value = serde_json::to_value(ChangeCursor {
                    before: self.id.clone(),
                    after: after.id.clone(),
                    limit,
                    position: position.clone(),
                })
                .map_err(|e| e.to_string())?;
                String::from_utf8(canonical::encode(&value)?).map_err(|e| e.to_string())
            })
            .transpose()?;
        Ok(
            serde_json::json!({"before_id":self.id,"after_id":after.id,"limit":limit,
            "source_identity_changed":page.source_identity_changed,"changed_producers":page.changed_producers,
            "sources":page.sources,"next_after":next_after}),
        )
    }
    /// Compare exact input declarations between two independently verified snapshots.
    /// Common repository authority is supplied by the caller, not inferred here.
    ///
    /// # Errors
    /// Propagates incompatible source format or manifest validation errors.
    pub fn changes_to(&self, after: &Snapshot) -> Result<crate::changed::Changes, String> {
        crate::changed::compare(self.bound.manifest(), after.bound.manifest())
    }
    /// Locate observations at an exact file byte offset without semantic ranking.
    /// Empty spans match their exact offset; nonempty spans are half-open.
    ///
    /// # Errors
    /// Rejects unknown files/producers, invalid bounds and foreign cursors.
    pub fn locate(
        &self,
        query: &LocateQuery,
        after: Option<&str>,
    ) -> Result<OccurrencePage<'_>, String> {
        if query.path.len() > 4096
            || query.offset > 64 << 20
            || !(1..=128).contains(&query.limit)
            || !self
                .bound
                .graph()
                .at_path(&query.path)
                .iter()
                .any(|node| node.kind == crate::graph::NodeKind::File)
            || !self
                .bound
                .manifest()
                .producers
                .iter()
                .any(|p| p.id == query.producer)
        {
            return Err("invalid locate file/producer/bounds".into());
        }
        let query_id = canonical::hash(
            "harness.ri.locate-query.v1",
            &canonical::encode(&serde_json::to_value(query).map_err(|e| e.to_string())?)?,
        );
        let cursor = after
            .map(|text| canonical::decode::<Cursor>(text.as_bytes()))
            .transpose()?;
        if let Some(cursor) = &cursor
            && (cursor.snapshot != self.id
                || cursor.query != query_id
                || cursor.after.is_empty()
                || cursor.after.len() > 256)
        {
            return Err("locate cursor identity mismatch".into());
        }
        let after_id = cursor.as_ref().map_or("", |c| c.after.as_str());
        let mut records: Vec<_> = self
            .bound
            .occurrences()
            .iter_path(&query.path)
            .filter(|o| {
                o.id.as_str() > after_id
                    && o.producer == query.producer
                    && o.span.start <= query.offset
                    && (query.offset < o.span.end
                        || o.span.start == o.span.end && query.offset == o.span.start)
            })
            .take(query.limit + 1)
            .collect();
        let next_after = if records.len() > query.limit {
            records.pop();
            let cursor = Cursor {
                snapshot: self.id.clone(),
                query: query_id.clone(),
                after: records
                    .last()
                    .ok_or("missing locate continuation")?
                    .id
                    .clone(),
            };
            Some(
                String::from_utf8(canonical::encode(
                    &serde_json::to_value(cursor).map_err(|e| e.to_string())?,
                )?)
                .map_err(|e| e.to_string())?,
            )
        } else {
            None
        };
        Ok(OccurrencePage {
            occurrences: records,
            snapshot_id: &self.id,
            query_id,
            next_after,
            absence_proven: false,
        })
    }
    /// Report each registered producer's declaration for one exact scope.
    /// The registry bounds this result to at most 64 entries. Missing declarations
    /// remain null; declarations are not promoted into an absence claim.
    ///
    /// # Errors
    /// Rejects an unknown node.
    pub fn coverage(
        &self,
        node: &str,
        relation: Relation,
        direction: Direction,
    ) -> Result<Vec<CoverageDeclaration<'_>>, String> {
        self.bound
            .manifest()
            .producers
            .iter()
            .map(|producer| {
                Ok(CoverageDeclaration {
                    producer: &producer.id,
                    completeness: self.bound.graph().coverage(
                        node,
                        relation,
                        direction,
                        &producer.id,
                    )?,
                })
            })
            .collect()
    }
    /// Find a bounded observed path within a registered producer's graph.
    ///
    /// # Errors
    /// Rejects unknown producers and propagates graph query validation errors.
    pub fn path(
        &self,
        query: &crate::graph::PathQuery,
    ) -> Result<crate::graph::PathResult<'_>, String> {
        if !self
            .bound
            .manifest()
            .producers
            .iter()
            .any(|p| p.id == query.producer)
        {
            return Err("unknown path query producer".into());
        }
        self.bound.graph().path(query).map_err(str::to_owned)
    }
    /// Resolve an exact producer SCIP spelling with optional local document scope.
    /// This uses the deterministic identity index, not fuzzy name matching.
    /// A missing result makes no absence or coverage claim.
    ///
    /// # Errors
    /// Rejects unknown producers, invalid local identities and metadata mismatch.
    pub fn scip_symbol(
        &self,
        producer: &str,
        symbol: &str,
        document: Option<&str>,
    ) -> Result<Option<&crate::graph::Node>, String> {
        if !self
            .bound
            .manifest()
            .producers
            .iter()
            .any(|p| p.id == producer)
        {
            return Err("unknown symbol query producer".into());
        }
        let identity = crate::scip::SymbolIdentity::new(producer, symbol, document)?;
        let node = self.bound.graph().node(&identity.id);
        if let Some(node) = node
            && !node
                .scip
                .as_ref()
                .is_some_and(|name| name.producer == producer && name.symbol == symbol)
        {
            return Err("symbol lookup metadata mismatch".into());
        }
        Ok(node)
    }
    /// Page direct definitions or references from an immutable symbol index.
    ///
    /// # Errors
    /// Rejects unknown symbols/producers, invalid limits and foreign cursors.
    pub fn occurrences(
        &self,
        query: &OccurrenceQuery,
        after: Option<&str>,
    ) -> Result<OccurrencePage<'_>, String> {
        if query.symbol.len() > 256
            || query.producer.len() > 256
            || !(1..=128).contains(&query.limit)
            || !self
                .bound
                .graph()
                .node(&query.symbol)
                .is_some_and(|node| node.kind == crate::graph::NodeKind::Symbol)
            || !self
                .bound
                .manifest()
                .producers
                .iter()
                .any(|p| p.id == query.producer)
        {
            return Err("invalid occurrence query symbol/producer/bounds".into());
        }
        let query_id = canonical::hash(
            "harness.ri.occurrence-query.v1",
            &canonical::encode(&serde_json::to_value(query).map_err(|e| e.to_string())?)?,
        );
        let cursor = after
            .map(|text| canonical::decode::<Cursor>(text.as_bytes()))
            .transpose()?;
        if let Some(cursor) = &cursor
            && (cursor.snapshot != self.id
                || cursor.query != query_id
                || cursor.after.is_empty()
                || cursor.after.len() > 256)
        {
            return Err("occurrence cursor identity mismatch".into());
        }
        let after_id = cursor.as_ref().map_or("", |cursor| cursor.after.as_str());
        let mut records: Vec<_> = self
            .bound
            .occurrences()
            .iter_symbol(&query.symbol)
            .filter(|record| {
                record.id.as_str() > after_id
                    && record.producer == query.producer
                    && record
                        .roles
                        .is_some_and(|roles| roles.is_definition() == query.definitions)
            })
            .take(query.limit + 1)
            .collect();
        let next_after = if records.len() > query.limit {
            records.pop();
            let cursor = Cursor {
                snapshot: self.id.clone(),
                query: query_id.clone(),
                after: records
                    .last()
                    .ok_or("missing occurrence continuation")?
                    .id
                    .clone(),
            };
            Some(
                String::from_utf8(canonical::encode(
                    &serde_json::to_value(cursor).map_err(|e| e.to_string())?,
                )?)
                .map_err(|e| e.to_string())?,
            )
        } else {
            None
        };
        Ok(OccurrencePage {
            occurrences: records,
            snapshot_id: &self.id,
            query_id,
            next_after,
            absence_proven: false,
        })
    }
    /// Return the number of source occurrences admitted from snapshot records.
    pub fn occurrence_count(&self) -> usize {
        self.bound.occurrences().records().len()
    }
    /// Return immutable source provenance for a verified snapshot.
    pub fn source(&self) -> &Source {
        &self.bound.manifest().source
    }

    /// Return sorted producer identities admitted from the snapshot manifest.
    pub fn producer_ids(&self) -> Vec<&str> {
        self.bound
            .manifest()
            .producers
            .iter()
            .map(|p| p.id.as_str())
            .collect()
    }

    /// Return node, edge and coverage counts without scanning the graph.
    pub fn counts(&self) -> (usize, usize, usize) {
        self.bound.graph().counts()
    }
    /// Admit immutable bytes with an externally supplied identity and source.
    ///
    /// # Errors
    /// Propagates all snapshot identity, encoding and graph validation errors.
    pub fn read(bytes: &[u8], id: &str, source: &Source) -> Result<Self, String> {
        Ok(Self {
            id: id.to_owned(),
            bound: snapshot::read(bytes, id, source)?,
        })
    }

    /// Execute a bounded adjacency query, rejecting foreign continuation tokens.
    ///
    /// # Errors
    /// Rejects unknown producers/nodes, invalid bounds and cursors whose exact
    /// snapshot or query differs. Tokens are integrity bindings, not credentials.
    pub fn neighbors(&self, query: &Query, after: Option<&str>) -> Result<Page<'_>, String> {
        if query.node.len() > 256 || query.producer.len() > 256 || !(1..=128).contains(&query.limit)
        {
            return Err("invalid query bounds".into());
        }
        if !self
            .bound
            .manifest()
            .producers
            .iter()
            .any(|p| p.id == query.producer)
        {
            return Err("unknown query producer".into());
        }
        let bytes = canonical::encode(&serde_json::to_value(query).map_err(|e| e.to_string())?)?;
        let query_id = canonical::hash("harness.ri.query.v1", &bytes);
        let cursor = after
            .map(|text| canonical::decode::<Cursor>(text.as_bytes()))
            .transpose()?;
        if let Some(cursor) = &cursor
            && (cursor.snapshot != self.id
                || cursor.query != query_id
                || cursor.after.is_empty()
                || cursor.after.len() > 256)
        {
            return Err("cursor identity mismatch".into());
        }
        let (evidence, more) = self.bound.graph().neighbor_page(
            &query.node,
            query.relation,
            query.direction,
            &query.producer,
            cursor.as_ref().map_or("", |c| c.after.as_str()),
            query.limit,
        )?;
        let next_after = if more {
            let last = evidence
                .edges
                .last()
                .ok_or("page continuation lacks edge")?;
            let value = serde_json::to_value(Cursor {
                snapshot: self.id.clone(),
                query: query_id.clone(),
                after: last.id.clone(),
            })
            .map_err(|e| e.to_string())?;
            Some(String::from_utf8(canonical::encode(&value)?).map_err(|e| e.to_string())?)
        } else {
            None
        };
        Ok(Page {
            evidence,
            snapshot_id: &self.id,
            query_id,
            next_after,
        })
    }
}
