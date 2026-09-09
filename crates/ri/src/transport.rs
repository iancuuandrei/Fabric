//! Sequential bounded frames for a controller-owned persistent stdio process.
//! No retries, threads, network listeners or filesystem authority are introduced.

use crate::canonical;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::io::{Read, Write};

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    id: u32,
    request: Value,
}

/// Serve up to 10,000 sequential canonical requests. Each frame starts with a
/// four-byte big-endian payload length, bounded by canonical MAX_BYTES. IDs start
/// at one and increment exactly. EOF is clean only between complete frames.
/// Invalid framing/schema/sequence terminates the session without dispatching
/// that request. Operation failures produce correlated errors and do not retry.
/// The owner must enforce deadlines and reap the process on cancellation.
pub fn serve(
    mut input: impl Read,
    mut output: impl Write,
    mut execute: impl FnMut(Value) -> Result<Value, String>,
) -> Result<(), String> {
    let mut next = 1u32;
    loop {
        let mut header = [0u8; 4];
        match input.read_exact(&mut header[..1]) {
            Err(error) if error.kind() == std::io::ErrorKind::UnexpectedEof => return Ok(()),
            Err(error) => return Err(error.to_string()),
            Ok(()) => {}
        }
        input
            .read_exact(&mut header[1..])
            .map_err(|e| e.to_string())?;
        let length = u32::from_be_bytes(header) as usize;
        if length == 0 || length > canonical::MAX_BYTES || next > 10_000 {
            return Err("stdio frame/session bound exceeded".into());
        }
        let mut bytes = vec![0; length];
        input.read_exact(&mut bytes).map_err(|e| e.to_string())?;
        let request: Request = canonical::decode(&bytes)?;
        if request.id != next {
            return Err("stdio request sequence mismatch".into());
        }
        let response = match execute(request.request) {
            Ok(result) => json!({"version":1,"id":next,"ok":true,"result":result}),
            Err(error) => json!({"version":1,"id":next,"ok":false,"error":error}),
        };
        let bytes = canonical::encode(&response).or_else(|_| {
            canonical::encode(&json!({"version":1,"id":next,"ok":false,"error":"response exceeds protocol bounds"}))
        })?;
        output
            .write_all(&(bytes.len() as u32).to_be_bytes())
            .map_err(|e| e.to_string())?;
        output.write_all(&bytes).map_err(|e| e.to_string())?;
        output.flush().map_err(|e| e.to_string())?;
        next += 1;
    }
}
