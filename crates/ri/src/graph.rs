//! Immutable typed graph with adjacency and exact-scope coverage indexes.
//!
//! Construction validates identities before publishing indexes. Queries only
//! inspect existing evidence: they do not invoke producers or mutate the graph.

use std::collections::BTreeMap;

/// Explicit bounded path exploration over one relation and producer.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PathQuery {
    /// Starting node identity.
    pub from: String,
    /// Destination node identity.
    pub to: String,
    /// Exact relation allowed on the path.
    pub relation: Relation,
    /// Direction in which edges are traversed.
    pub direction: Direction,
    /// Exact producer scope.
    pub producer: String,
    /// Maximum path length, 1 through 128.
    pub max_depth: usize,
    /// Maximum adjacency entries examined, 1 through 100,000.
    pub max_edges: usize,
}

/// Observed path evidence; lack of a path never proves semantic absence.
#[derive(serde::Serialize)]
pub struct PathResult<'a> {
    /// Whether a path was observed (including the zero-length self path).
    pub found: bool,
    /// Edges in traversal order, retaining their original direction/provenance.
    pub edges: Vec<&'a Edge>,
    /// True when an exploration bound prevented further examination.
    pub truncated: bool,
    /// Number of adjacency entries examined, including other producer scopes.
    pub examined_edges: usize,
    /// Always false; this traversal does not aggregate coverage proofs.
    pub absence_proven: bool,
}

/// Evidence taxonomy retained independently of human presentation.
#[derive(
    Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, serde::Serialize, serde::Deserialize,
)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum Quality {
    /// An authoritative producer explicitly declares this relationship.
    Declared,
    /// A producer directly observes this relationship.
    Observed,
    /// A heuristic infers this relationship; it is not a semantic guarantee.
    Inferred,
}

/// The entity represented by a graph node.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum NodeKind {
    /// A committed source file.
    File,
    /// A language symbol.
    Symbol,
    /// A module or package.
    Module,
}

/// One source entity; identity is supplied by the snapshot producer.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Node {
    /// Stable identity within the snapshot.
    pub id: String,
    /// Exact entity category.
    pub kind: NodeKind,
    /// Optional committed source path; no host filesystem paths.
    pub path: Option<String>,
    /// Exact SCIP identity metadata when supplied by a semantic producer.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub scip: Option<ScipName>,
}

/// Producer-bound opaque SCIP spelling retained for exact symbol lookup.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ScipName {
    /// Registered producer identity.
    pub producer: String,
    /// Exact original SCIP symbol string.
    pub symbol: String,
}

/// Explicit relationship category; categories never imply one another.
#[derive(
    Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, serde::Serialize, serde::Deserialize,
)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum Relation {
    /// Source contains target.
    Contains,
    /// Source defines target.
    Defines,
    /// Source references target.
    References,
    /// Source depends on target.
    DependsOn,
    /// Source calls target.
    Calls,
    /// Producer declares that reference searches should include the other symbol.
    ReferenceRelated,
    /// Source implements target, explicitly declared by a semantic producer.
    Implements,
    /// Target supplies the source symbol's type definition.
    TypeDefinition,
    /// Target supplies an alternative definition for the source symbol.
    DefinitionRelated,
}

/// One provenance-bearing relationship, preserving producer disagreements.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Edge {
    /// Stable relationship identity within the snapshot.
    pub id: String,
    /// Existing source node ID.
    pub from: String,
    /// Existing target node ID.
    pub to: String,
    /// Exact relationship category.
    pub relation: Relation,
    /// Producer identity bound by the enclosing snapshot manifest.
    pub producer: String,
    /// Strength of this individual observation.
    pub quality: Quality,
}

/// Direction is part of both query and coverage scope.
#[derive(
    Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, serde::Serialize, serde::Deserialize,
)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum Direction {
    /// Edges from the queried node.
    Outgoing,
    /// Edges to the queried node.
    Incoming,
}

/// Completeness for one exact producer, node, relation and direction.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum Completeness {
    /// Producer cannot establish coverage.
    Unknown,
    /// Producer explicitly reports incomplete coverage.
    Partial,
    /// Producer explicitly reports complete coverage of the exact scope.
    Complete,
}

/// A scoped producer assertion; absence of this record means unknown coverage.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Coverage {
    /// Exact existing node ID.
    pub node: String,
    /// Exact relationship category.
    pub relation: Relation,
    /// Incoming and outgoing scopes are independent.
    pub direction: Direction,
    /// Exact producer identity.
    pub producer: String,
    /// Producer's declared completeness.
    pub completeness: Completeness,
}

/// Finite query evidence including the narrow meaning of an empty result.
#[derive(Debug, serde::Serialize)]
pub struct Neighbors<'a> {
    /// Matching edges, sorted by stable edge ID.
    pub edges: Vec<&'a Edge>,
    /// Coverage for this exact query scope.
    pub completeness: Completeness,
    /// True only for an empty result with explicit complete exact-scope coverage.
    pub absence_proven: bool,
}

type CoverageKey = (usize, Relation, Direction, String);

/// Validated immutable graph. Routine adjacency queries scan only one node's degree.
pub struct Graph {
    nodes: Vec<Node>,
    edges: Vec<Edge>,
    by_id: BTreeMap<String, usize>,
    by_path: BTreeMap<String, Vec<usize>>,
    forward: Vec<Vec<usize>>,
    reverse: Vec<Vec<usize>>,
    coverage: BTreeMap<CoverageKey, Completeness>,
}

fn valid_id(id: &str) -> bool {
    !id.is_empty() && id.len() <= 256 && !id.chars().any(char::is_control)
}

fn valid_path(path: &str) -> bool {
    !path.is_empty()
        && path.len() <= 4096
        && !path.contains(['\\', ':'])
        && !path.chars().any(char::is_control)
        && path
            .split('/')
            .all(|part| !part.is_empty() && part != "." && part != "..")
}

impl Graph {
    /// Return node, edge and explicit coverage record counts without scanning.
    pub fn counts(&self) -> (usize, usize, usize) {
        (self.nodes.len(), self.edges.len(), self.coverage.len())
    }
    /// Return a bounded adjacency page and whether another page exists.
    /// Absence refers to the entire exact scope, never just an empty later page.
    ///
    /// # Errors
    /// Rejects unknown nodes, malformed producer IDs and limits outside 1..=128.
    pub fn neighbor_page(
        &self,
        node: &str,
        relation: Relation,
        direction: Direction,
        producer: &str,
        after: &str,
        limit: usize,
    ) -> Result<(Neighbors<'_>, bool), &'static str> {
        if !(1..=128).contains(&limit) {
            return Err("invalid query page limit");
        }
        let index = *self.by_id.get(node).ok_or("unknown query node")?;
        if !valid_id(producer) {
            return Err("invalid query producer");
        }
        let adjacency = match direction {
            Direction::Outgoing => &self.forward[index],
            Direction::Incoming => &self.reverse[index],
        };
        let mut edges = Vec::with_capacity(limit + 1);
        let mut any = false;
        for &index in adjacency {
            let edge = &self.edges[index];
            if edge.relation != relation || edge.producer != producer {
                continue;
            }
            any = true;
            if edge.id.as_str() > after {
                edges.push(edge);
            }
            if edges.len() > limit {
                break;
            }
        }
        let more = edges.len() > limit;
        edges.truncate(limit);
        let completeness = self
            .coverage
            .get(&(index, relation, direction, producer.to_owned()))
            .copied()
            .unwrap_or(Completeness::Unknown);
        Ok((
            Neighbors {
                edges,
                completeness,
                absence_proven: !any && completeness == Completeness::Complete,
            },
            more,
        ))
    }

    /// Build indexes after validating unique identities and all graph endpoints.
    ///
    /// Inputs are normalized into ID order. Duplicate coverage scopes are rejected
    /// even when equal, so producer conflicts cannot be resolved by input order.
    ///
    /// # Errors
    /// Rejects malformed identities/paths, dangling edges or coverage, duplicates,
    /// more than 100,000 nodes or more than 1,000,000 edges/coverage records.
    pub fn build(
        mut nodes: Vec<Node>,
        mut edges: Vec<Edge>,
        coverage: Vec<Coverage>,
    ) -> Result<Self, &'static str> {
        if nodes.len() > 100_000 || edges.len() > 1_000_000 || coverage.len() > 1_000_000 {
            return Err("graph exceeds construction bounds");
        }
        nodes.sort_by(|a, b| a.id.cmp(&b.id));
        edges.sort_by(|a, b| a.id.cmp(&b.id));
        let mut graph = Self {
            forward: vec![Vec::new(); nodes.len()],
            reverse: vec![Vec::new(); nodes.len()],
            nodes,
            edges,
            by_id: BTreeMap::new(),
            by_path: BTreeMap::new(),
            coverage: BTreeMap::new(),
        };
        for (index, node) in graph.nodes.iter().enumerate() {
            if !valid_id(&node.id) || graph.by_id.insert(node.id.clone(), index).is_some() {
                return Err("invalid or duplicate node ID");
            }
            if let Some(path) = &node.path {
                if !valid_path(path) {
                    return Err("invalid source path");
                }
                graph.by_path.entry(path.clone()).or_default().push(index);
            }
        }
        for (index, edge) in graph.edges.iter().enumerate() {
            if !valid_id(&edge.id)
                || !valid_id(&edge.producer)
                || index > 0 && graph.edges[index - 1].id == edge.id
            {
                return Err("invalid or duplicate edge identity");
            }
            let from = *graph.by_id.get(&edge.from).ok_or("dangling edge source")?;
            let to = *graph.by_id.get(&edge.to).ok_or("dangling edge target")?;
            graph.forward[from].push(index);
            graph.reverse[to].push(index);
        }
        for entry in coverage {
            let node = *graph
                .by_id
                .get(&entry.node)
                .ok_or("dangling coverage node")?;
            if !valid_id(&entry.producer) {
                return Err("invalid coverage producer");
            }
            if graph
                .coverage
                .insert(
                    (node, entry.relation, entry.direction, entry.producer),
                    entry.completeness,
                )
                .is_some()
            {
                return Err("duplicate coverage scope");
            }
        }
        Ok(graph)
    }

    /// Find a node by exact stable ID without scanning graph edges.
    pub fn node(&self, id: &str) -> Option<&Node> {
        self.by_id.get(id).map(|&index| &self.nodes[index])
    }

    /// Inspect an exact coverage declaration without materializing adjacency.
    /// None means no declaration, distinct from an explicit UNKNOWN assertion.
    ///
    /// # Errors
    /// Rejects unknown nodes and invalid producer identities.
    pub fn coverage(
        &self,
        node: &str,
        relation: Relation,
        direction: Direction,
        producer: &str,
    ) -> Result<Option<Completeness>, &'static str> {
        let index = *self.by_id.get(node).ok_or("unknown coverage node")?;
        if !valid_id(producer) {
            return Err("invalid coverage producer");
        }
        Ok(self
            .coverage
            .get(&(index, relation, direction, producer.to_owned()))
            .copied())
    }

    /// Breadth-first path search with deterministic edge-ID tie breaking.
    ///
    /// # Errors
    /// Rejects unknown endpoints, invalid producer identity or resource bounds.
    pub fn path(&self, query: &PathQuery) -> Result<PathResult<'_>, &'static str> {
        use std::collections::{BTreeSet, VecDeque};
        let start = *self.by_id.get(&query.from).ok_or("unknown path start")?;
        let goal = *self
            .by_id
            .get(&query.to)
            .ok_or("unknown path destination")?;
        if !valid_id(&query.producer)
            || !(1..=128).contains(&query.max_depth)
            || !(1..=100_000).contains(&query.max_edges)
        {
            return Err("invalid path query bounds");
        }
        let mut result = PathResult {
            found: start == goal,
            edges: vec![],
            truncated: false,
            examined_edges: 0,
            absence_proven: false,
        };
        if result.found {
            return Ok(result);
        }
        let mut queue = VecDeque::from([(start, 0usize)]);
        let mut visited = BTreeSet::from([start]);
        let mut parent = BTreeMap::new();
        while let Some((node, depth)) = queue.pop_front() {
            let adjacency = match query.direction {
                Direction::Outgoing => &self.forward[node],
                Direction::Incoming => &self.reverse[node],
            };
            if depth == query.max_depth {
                if !adjacency.is_empty() {
                    result.truncated = true;
                }
                continue;
            }
            for &edge_index in adjacency {
                if result.examined_edges == query.max_edges {
                    result.truncated = true;
                    return Ok(result);
                }
                result.examined_edges += 1;
                let edge = &self.edges[edge_index];
                if edge.relation != query.relation || edge.producer != query.producer {
                    continue;
                }
                let next_id = match query.direction {
                    Direction::Outgoing => &edge.to,
                    Direction::Incoming => &edge.from,
                };
                let next = self.by_id[next_id];
                if !visited.insert(next) {
                    continue;
                }
                parent.insert(next, (node, edge_index));
                if next == goal {
                    result.found = true;
                    let mut current = goal;
                    while current != start {
                        let &(previous, edge) =
                            parent.get(&current).ok_or("path parent missing")?;
                        result.edges.push(&self.edges[edge]);
                        current = previous;
                    }
                    result.edges.reverse();
                    return Ok(result);
                }
                queue.push_back((next, depth + 1));
            }
        }
        Ok(result)
    }

    /// Return nodes associated with an exact source path, in stable ID order.
    pub fn at_path(&self, path: &str) -> Vec<&Node> {
        self.by_path
            .get(path)
            .into_iter()
            .flatten()
            .map(|&index| &self.nodes[index])
            .collect()
    }

    /// Query one producer/relation/direction without scanning unrelated edges.
    /// Missing nodes are errors, not empty results. Empty incomplete results never
    /// prove absence. Returned references cannot mutate the underlying evidence.
    ///
    /// # Errors
    /// Returns an error if the queried node or producer identity is invalid.
    pub fn neighbors(
        &self,
        node: &str,
        relation: Relation,
        direction: Direction,
        producer: &str,
    ) -> Result<Neighbors<'_>, &'static str> {
        let index = *self.by_id.get(node).ok_or("unknown query node")?;
        if !valid_id(producer) {
            return Err("invalid query producer");
        }
        let adjacency = match direction {
            Direction::Outgoing => &self.forward[index],
            Direction::Incoming => &self.reverse[index],
        };
        let edges: Vec<_> = adjacency
            .iter()
            .map(|&e| &self.edges[e])
            .filter(|edge| edge.relation == relation && edge.producer == producer)
            .collect();
        let completeness = self
            .coverage
            .get(&(index, relation, direction, producer.to_owned()))
            .copied()
            .unwrap_or(Completeness::Unknown);
        Ok(Neighbors {
            absence_proven: edges.is_empty() && completeness == Completeness::Complete,
            edges,
            completeness,
        })
    }
}
