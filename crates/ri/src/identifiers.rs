//! Deterministic identifier segmentation for explicit symbol lookup.
//!
//! Adapted from CodeGraph's `splitIdentifierSegments`, MIT, copyright 2026
//! Colby Mchenry. See `third_party/codegraph/README.md` and its retained license.
//! Unlike the donor's prompt gate, no short words, numbers or later segments are
//! silently discarded. These segments are an index aid, never a relevance gate.

use std::collections::BTreeSet;

/// Maximum UTF-8 input bytes accepted by [`segments`]. Oversize input is an error.
pub const MAX_IDENTIFIER_BYTES: usize = 16 * 1024;

/// Returned when an identifier exceeds the explicit allocation bound.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct IdentifierTooLong;

impl std::fmt::Display for IdentifierTooLong {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("identifier exceeds 16384 UTF-8 bytes")
    }
}

impl std::error::Error for IdentifierTooLong {}

/// Split camelCase, acronyms and punctuation into lowercase, unique segments.
///
/// Order is first occurrence. Digits stay attached unless followed by uppercase.
/// Unicode uses Rust's alphabetic/numeric and case properties, without accent
/// stripping or normalization. Empty input returns no segments. Work and retained
/// character storage are bounded by [`MAX_IDENTIFIER_BYTES`].
///
/// # Errors
/// Returns [`IdentifierTooLong`] instead of silently dropping later segments.
///
/// # Examples
/// ```
/// use engorch_ri::identifiers::segments;
/// assert_eq!(segments("HTMLParser_base64Encode").unwrap(),
///            ["html", "parser", "base64", "encode"]);
/// ```
pub fn segments(name: &str) -> Result<Vec<String>, IdentifierTooLong> {
    if name.len() > MAX_IDENTIFIER_BYTES {
        return Err(IdentifierTooLong);
    }
    let mut seen = BTreeSet::new();
    let mut result = Vec::new();
    for run in name
        .split(|c: char| !c.is_alphanumeric())
        .filter(|s| !s.is_empty())
    {
        let chars: Vec<(usize, char)> = run.char_indices().collect();
        let mut start = 0;
        for pos in 1..=chars.len() {
            let boundary = pos == chars.len()
                || (chars[pos].1.is_uppercase()
                    && (chars[pos - 1].1.is_lowercase()
                        || chars[pos - 1].1.is_numeric()
                        || (chars[pos - 1].1.is_uppercase()
                            && chars.get(pos + 1).is_some_and(|(_, c)| c.is_lowercase()))));
            if boundary {
                let end = chars.get(pos).map_or(run.len(), |(byte, _)| *byte);
                let segment = run[start..end].to_lowercase();
                if seen.insert(segment.clone()) {
                    result.push(segment);
                }
                start = end;
            }
        }
    }
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn camel_acronym_unicode_and_repeated_words() {
        assert_eq!(
            segments("HTMLParser_base64Encode.parser").unwrap(),
            ["html", "parser", "base64", "encode"]
        );
        assert_eq!(
            segments("ȘirDeDate.ΔιαφοράHTTP").unwrap(),
            ["șir", "de", "date", "διαφορά", "http"]
        );
    }

    #[test]
    fn never_silently_discards_short_numeric_or_late_segments() {
        assert_eq!(segments("x_1_y_2_z_3_a_4_b_5_c_6_d_7").unwrap().len(), 14);
        assert_eq!(segments(&"a".repeat(128)).unwrap(), ["a".repeat(128)]);
        assert_eq!(
            segments(&"a".repeat(MAX_IDENTIFIER_BYTES + 1)),
            Err(IdentifierTooLong)
        );
        assert!(segments("---").unwrap().is_empty());
    }
}
