//! Canonical JSON v1 shared with the Go controller: bounded UTF-8, ASCII keys,
//! safe integers, sorted keys and no insignificant whitespace.

use serde::{Serialize, de::DeserializeOwned};
use serde_json::Value;
use sha2::{Digest, Sha256};

/// Maximum bytes in one canonical record.
pub const MAX_BYTES: usize = 1 << 20;

fn validate(value: &Value, depth: usize) -> Result<(), String> {
    if depth > 64 {
        return Err("canonical nesting exceeds 64".into());
    }
    match value {
        Value::Number(number) => {
            let valid = number
                .as_i64()
                .is_some_and(|n| (-9_007_199_254_740_991..=9_007_199_254_740_991).contains(&n));
            if !valid {
                return Err("canonical numbers must be safe integers".into());
            }
        }
        Value::Array(items) => {
            for item in items {
                validate(item, depth + 1)?;
            }
        }
        Value::Object(items) => {
            for (key, item) in items {
                if !key.is_ascii() {
                    return Err("canonical keys must be ASCII".into());
                }
                validate(item, depth + 1)?;
            }
        }
        _ => {}
    }
    Ok(())
}

/// Encode a JSON value after canonical shape validation.
///
/// # Errors
/// Rejects floating point numbers, unsafe integers, non-ASCII keys, excessive
/// depth and records above one MiB. Arrays retain their original order.
pub fn encode(value: &Value) -> Result<Vec<u8>, String> {
    validate(value, 0)?;
    let bytes = serde_json::to_vec(value).map_err(|e| e.to_string())?;
    if bytes.len() > MAX_BYTES {
        return Err("canonical record exceeds size bound".into());
    }
    Ok(bytes)
}

/// Decode only exact canonical bytes and an exact typed schema.
///
/// # Errors
/// Rejects malformed JSON, duplicate keys, whitespace/noncanonical encodings,
/// unknown or omitted fields and any typed conversion that changes the value.
/// Duplicate keys are detected because collapsing them cannot reproduce the
/// original canonical byte sequence. No permissive normalized result is returned.
pub fn decode<T: DeserializeOwned + Serialize>(bytes: &[u8]) -> Result<T, String> {
    if bytes.is_empty() || bytes.len() > MAX_BYTES {
        return Err("canonical record size invalid".into());
    }
    let value: Value = serde_json::from_slice(bytes).map_err(|e| e.to_string())?;
    if encode(&value)? != bytes {
        return Err("noncanonical or ambiguous JSON".into());
    }
    let typed: T = serde_json::from_value(value).map_err(|e| e.to_string())?;
    if encode(&serde_json::to_value(&typed).map_err(|e| e.to_string())?)? != bytes {
        return Err("typed schema changes canonical value".into());
    }
    Ok(typed)
}

/// Compute lowercase SHA-256 over domain, newline and exact bytes.
pub fn hash(domain: &str, bytes: &[u8]) -> String {
    let mut digest = Sha256::new();
    digest.update(domain.as_bytes());
    digest.update(b"\n");
    digest.update(bytes);
    let mut hex = String::with_capacity(64);
    const DIGITS: &[u8; 16] = b"0123456789abcdef";
    for byte in digest.finalize() {
        hex.push(DIGITS[(byte >> 4) as usize] as char);
        hex.push(DIGITS[(byte & 15) as usize] as char);
    }
    hex
}
