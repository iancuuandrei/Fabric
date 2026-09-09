//! Synthetic ring-graph measurement separating build, verified load and warm query.

use engorch_ri::{
    graph::{Direction, Edge, Node, NodeKind, Quality, Relation},
    manifest::{Input, Manifest, Producer, Source},
    query::{Query, Snapshot},
    snapshot,
};
use serde_json::json;
use std::{hint::black_box, time::Instant};

fn measure(nodes: usize) -> Result<serde_json::Value, String> {
    let source = Source {
        repository_id: "a".repeat(64),
        object_format: "sha1".into(),
        commit: "b".repeat(40),
        tree: "c".repeat(40),
    };
    let manifest = Manifest {
        format: 1,
        source: source.clone(),
        producers: vec![Producer {
            id: "synthetic-ring".into(),
            name: "synthetic-ring".into(),
            version: "1".into(),
            artifact_sha256: "d".repeat(64),
            inputs: vec![Input {
                name: "synthetic".into(),
                sha256: "e".repeat(64),
            }],
        }],
    };
    let vertices: Vec<Node> = (0..nodes)
        .map(|i| Node {
            id: format!("n{i:06}"),
            kind: NodeKind::Symbol,
            path: None,
            scip: None,
        })
        .collect();
    let edges: Vec<Edge> = (0..nodes)
        .map(|i| Edge {
            id: format!("e{i:06}"),
            from: format!("n{i:06}"),
            to: format!("n{:06}", (i + 1) % nodes),
            relation: Relation::References,
            producer: "synthetic-ring".into(),
            quality: Quality::Observed,
        })
        .collect();
    let mut builds = Vec::new();
    let mut loads = Vec::new();
    let mut queries = Vec::new();
    let mut snapshot_id = String::new();
    let mut bytes = 0;
    for sample in 0..6 {
        // Input copying is outside snapshot construction timing.
        let inputs = (manifest.clone(), vertices.clone(), edges.clone());
        let started = Instant::now();
        let artifact = snapshot::build(inputs.0, &source, inputs.1, inputs.2, vec![])?;
        let build_ns = started.elapsed().as_nanos();
        if !snapshot_id.is_empty() && snapshot_id != artifact.id {
            return Err("nondeterministic snapshot".into());
        }
        snapshot_id.clone_from(&artifact.id);
        bytes = artifact.bytes.len();
        let started = Instant::now();
        let loaded = Snapshot::read(&artifact.bytes, &artifact.id, &source)?;
        let load_ns = started.elapsed().as_nanos();
        let query = Query {
            node: "n000000".into(),
            relation: Relation::References,
            direction: Direction::Outgoing,
            producer: "synthetic-ring".into(),
            limit: 16,
        };
        let started = Instant::now();
        for _ in 0..1000 {
            let page = loaded.neighbors(black_box(&query), None)?;
            if page.evidence.edges.len() != 1 {
                return Err("wrong ring adjacency".into());
            }
            black_box(page);
        }
        let query_batch_ns = started.elapsed().as_nanos();
        if sample != 0 {
            builds.push(build_ns);
            loads.push(load_ns);
            queries.push(query_batch_ns);
        }
    }
    Ok(
        json!({"dataset":"synthetic-ring-v1","nodes":nodes,"edges":nodes,
        "snapshot_id":snapshot_id,"snapshot_bytes":bytes,"warmup_samples":1,
        "sample_count":5,"build_samples_ns":builds,"verified_load_samples_ns":loads,
        "warm_query_batch_samples_ns":queries,"queries_per_batch":1000,
        "query_out_degree":1,"peak_memory_bytes":null,
        "limitations":["synthetic identifiers are not repository evidence",
        "warm in-memory bytes; no disk read or compiler process",
        "build timing excludes input cloning; query timing includes identity and output allocation"]}),
    )
}

fn main() -> Result<(), String> {
    let results = [1000, 10000]
        .into_iter()
        .map(measure)
        .collect::<Result<Vec<_>, _>>()?;
    println!(
        "{}",
        serde_json::to_string_pretty(&results).map_err(|e| e.to_string())?
    );
    Ok(())
}
