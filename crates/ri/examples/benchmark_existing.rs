//! Measure verified loading and occurrence queries for an explicitly bound snapshot.

use engorch_ri::{
    canonical,
    manifest::Source,
    query::{OccurrenceQuery, Snapshot},
    snapshot::MAX_SNAPSHOT_BYTES,
};
use serde::{Deserialize, Serialize};
use serde_json::json;
use std::{fs::File, hint::black_box, io::Read, time::Instant};

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct Request {
    snapshot_path: String,
    snapshot_id: String,
    source: Source,
    query: OccurrenceQuery,
}

fn bounded(path: &str, limit: usize) -> Result<Vec<u8>, String> {
    let mut bytes = Vec::new();
    File::open(path)
        .map_err(|e| e.to_string())?
        .take(limit as u64 + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| e.to_string())?;
    if bytes.len() > limit {
        return Err("benchmark input exceeds bound".into());
    }
    Ok(bytes)
}

fn main() -> Result<(), String> {
    let args: Vec<_> = std::env::args().collect();
    if args.len() != 2 {
        return Err("usage: benchmark_existing REQUEST_JSON".into());
    }
    let request: Request = canonical::decode(&bounded(&args[1], 1 << 20)?)?;
    let bytes = bounded(&request.snapshot_path, MAX_SNAPSHOT_BYTES)?;
    let mut loads = Vec::new();
    let mut queries = Vec::new();
    let mut result_id = String::new();
    let mut result_count = 0;
    for sample in 0..6 {
        let start = Instant::now();
        let loaded = Snapshot::read(&bytes, &request.snapshot_id, &request.source)?;
        let load_ns = start.elapsed().as_nanos();
        let result = loaded.occurrences(&request.query, None)?;
        result_count = result.occurrences.len();
        if result_count == 0 {
            return Err("benchmark requires a nonempty occurrence query".into());
        }
        let encoded =
            canonical::encode(&serde_json::to_value(&result).map_err(|e| e.to_string())?)?;
        let hash = canonical::hash("harness.ri.benchmark-result.v1", &encoded);
        if !result_id.is_empty() && result_id != hash {
            return Err("query result changed".into());
        }
        result_id = hash;
        let start = Instant::now();
        for _ in 0..1000 {
            let page = loaded.occurrences(black_box(&request.query), None)?;
            if page.occurrences.len() != result_count {
                return Err("query count changed".into());
            }
            black_box(page);
        }
        let query_ns = start.elapsed().as_nanos();
        if sample != 0 {
            loads.push(load_ns);
            queries.push(query_ns);
        }
    }
    if bytes != bounded(&request.snapshot_path, MAX_SNAPSHOT_BYTES)? {
        return Err("snapshot changed during measurement".into());
    }
    println!(
        "{}",
        serde_json::to_string_pretty(&json!({
            "dataset":"supplied-snapshot", "source":request.source,
            "snapshot_id":request.snapshot_id,"snapshot_bytes":bytes.len(),
            "query":request.query,"result_hash":result_id,"result_count":result_count,
            "sample_count":5,"warmup_samples":1,"queries_per_batch":1000,
            "verified_load_samples_ns":loads,"warm_query_batch_samples_ns":queries,
            "limitations":["in-memory load excludes disk and producer time",
            "query batch includes validation and allocation, not result serialization",
            "supplied artifact identity alone does not attest historical production"]
        }))
        .map_err(|e| e.to_string())?
    );
    Ok(())
}
