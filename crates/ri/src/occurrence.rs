//! Source positions and explicit unresolved versus resolved occurrences.

use crate::graph::Quality;
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

/// Producer-declared column unit; never inferred from language or host encoding.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum Encoding {
    /// UTF-8 byte count from line start.
    Utf8,
    /// UTF-16 code units from line start.
    Utf16,
    /// Unicode scalar values from line start.
    Utf32,
}

impl Encoding {
    /// Decode explicit SCIP PositionEncoding without choosing a default.
    ///
    /// # Errors
    /// Rejects unspecified (zero) and unknown encoding values.
    pub fn from_scip(value: i32) -> Result<Self, &'static str> {
        match value {
            1 => Ok(Self::Utf8),
            2 => Ok(Self::Utf16),
            3 => Ok(Self::Utf32),
            _ => Err("unspecified or unknown position encoding"),
        }
    }
}

/// Exact half-open source byte range, independent of producer column encoding.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Span {
    /// Inclusive source byte offset.
    pub start: usize,
    /// Exclusive source byte offset.
    pub end: usize,
}

/// One syntax/semantic observation. Missing symbol identity stays unresolved.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Occurrence {
    /// Stable occurrence identity, distinct from a symbol identity.
    pub id: String,
    /// Exact committed source path.
    pub path: String,
    /// SHA-256 of the exact source bytes containing this occurrence.
    pub source_sha256: String,
    /// Source byte interval.
    pub span: Span,
    /// Exact spelling in this interval, not a normalized lookup term.
    pub spelling: String,
    /// Resolved symbol identity supplied by a semantic producer, or null.
    pub symbol: Option<String>,
    /// SCIP role flags when explicitly supplied; null for structural observations.
    /// Zero means a producer-declared reference, distinct from unknown roles.
    pub roles: Option<crate::scip::Roles>,
    /// Producer identity to be checked against the snapshot registry.
    pub producer: String,
    /// Evidence strength; syntax observations do not imply semantic resolution.
    pub quality: Quality,
}

/// Validated immutable occurrence indexes, scoped to a bound graph's manifest.
pub struct Index {
    records: Vec<Occurrence>,
    by_path: BTreeMap<String, Vec<usize>>,
    by_symbol: BTreeMap<String, Vec<usize>>,
}

impl Index {
    /// Iterate exact-file observations in stable ID order without allocation.
    pub fn iter_path<'a>(&'a self, path: &str) -> impl Iterator<Item = &'a Occurrence> {
        self.by_path
            .get(path)
            .into_iter()
            .flatten()
            .map(|&index| &self.records[index])
    }
    /// Iterate symbol occurrences in stable ID order without allocating a result.
    pub fn iter_symbol<'a>(&'a self, symbol: &str) -> impl Iterator<Item = &'a Occurrence> {
        self.by_symbol
            .get(symbol)
            .into_iter()
            .flatten()
            .map(|&index| &self.records[index])
    }
    pub(crate) fn empty() -> Self {
        Self {
            records: Vec::new(),
            by_path: BTreeMap::new(),
            by_symbol: BTreeMap::new(),
        }
    }
    /// Admit occurrences only for registered files, producers and source inputs.
    /// Producer input names use `source:<path>` for exact per-file byte identities.
    ///
    /// # Errors
    /// Rejects duplicate IDs, invalid ranges, absent files/producers/input hashes,
    /// resolved targets that are not graph symbols, or more than 100,000 records.
    /// Source spelling must additionally be checked against bytes by the producer.
    pub fn build(
        graph: &crate::manifest::BoundGraph,
        mut records: Vec<Occurrence>,
    ) -> Result<Self, &'static str> {
        if records.len() > 100_000 {
            return Err("occurrence count exceeds bound");
        }
        records.sort_by(|a, b| a.id.cmp(&b.id));
        let mut by_path: BTreeMap<String, Vec<usize>> = BTreeMap::new();
        let mut by_symbol: BTreeMap<String, Vec<usize>> = BTreeMap::new();
        let source_inputs: BTreeMap<_, _> = graph
            .manifest()
            .producers
            .iter()
            .flat_map(|producer| {
                producer.inputs.iter().filter_map(move |input| {
                    input
                        .name
                        .strip_prefix("source:")
                        .map(|path| ((producer.id.as_str(), path), input.sha256.as_str()))
                })
            })
            .collect();
        for (index, record) in records.iter().enumerate() {
            if record.id.is_empty()
                || record.id.len() > 256
                || record.id.chars().any(char::is_control)
                || index > 0 && records[index - 1].id == record.id
                || record.span.end < record.span.start
                || record.span.end > 64 << 20
                || record.spelling.len() != record.span.end - record.span.start
            {
                return Err("invalid or duplicate occurrence identity/range");
            }
            if !by_path.contains_key(&record.path)
                && !graph
                    .graph()
                    .at_path(&record.path)
                    .iter()
                    .any(|n| n.kind == crate::graph::NodeKind::File)
            {
                return Err("occurrence file absent from graph");
            }
            if source_inputs
                .get(&(record.producer.as_str(), record.path.as_str()))
                .copied()
                != Some(record.source_sha256.as_str())
            {
                return Err("occurrence source input mismatch");
            }
            if let Some(symbol) = &record.symbol {
                if !graph
                    .graph()
                    .node(symbol)
                    .is_some_and(|n| n.kind == crate::graph::NodeKind::Symbol)
                {
                    return Err("resolved occurrence symbol missing");
                }
                by_symbol.entry(symbol.clone()).or_default().push(index);
            }
            by_path.entry(record.path.clone()).or_default().push(index);
        }
        Ok(Self {
            records,
            by_path,
            by_symbol,
        })
    }

    /// Iterate all records in deterministic identity order for snapshot encoding.
    pub fn records(&self) -> &[Occurrence] {
        &self.records
    }

    /// Find exact-path occurrences without scanning unrelated records.
    pub fn at_path(&self, path: &str) -> Vec<&Occurrence> {
        self.by_path
            .get(path)
            .into_iter()
            .flatten()
            .map(|&i| &self.records[i])
            .collect()
    }

    /// Find explicitly resolved symbol occurrences. Empty results do not prove absence.
    pub fn for_symbol(&self, symbol: &str) -> Vec<&Occurrence> {
        self.by_symbol
            .get(symbol)
            .into_iter()
            .flatten()
            .map(|&i| &self.records[i])
            .collect()
    }

    /// Producer-declared definitions for a resolved symbol. Unknown structural
    /// roles are excluded; empty results do not prove absence.
    pub fn definitions(&self, symbol: &str) -> Vec<&Occurrence> {
        self.for_symbol(symbol)
            .into_iter()
            .filter(|record| record.roles.is_some_and(|roles| roles.is_definition()))
            .collect()
    }

    /// Producer-declared references for a resolved symbol. A SCIP zero role
    /// bitset is a reference; null roles remain unknown and are excluded.
    /// Empty results do not prove absence.
    pub fn references(&self, symbol: &str) -> Vec<&Occurrence> {
        self.for_symbol(symbol)
            .into_iter()
            .filter(|record| record.roles.is_some_and(|roles| !roles.is_definition()))
            .collect()
    }
}

/// Indexed immutable UTF-8 source for repeatable coordinate conversion.
pub struct SourceText<'a> {
    text: &'a str,
    starts: Vec<usize>,
    source_sha256: String,
}

impl<'a> SourceText<'a> {
    /// Index line starts once, retaining the original bytes without normalization.
    ///
    /// # Errors
    /// Rejects non-UTF-8 or sources above 64 MiB.
    pub fn new(bytes: &'a [u8]) -> Result<Self, &'static str> {
        if bytes.len() > 64 << 20 {
            return Err("source text exceeds byte bound");
        }
        let text = std::str::from_utf8(bytes).map_err(|_| "source text is not UTF-8")?;
        let mut starts = vec![0];
        for (index, byte) in bytes.iter().enumerate() {
            if *byte == b'\n' {
                if starts.len() >= 1_000_000 {
                    return Err("source line count exceeds bound");
                }
                starts.push(index + 1);
            }
        }
        use sha2::{Digest, Sha256};
        let source_sha256 = Sha256::digest(bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        Ok(Self {
            text,
            starts,
            source_sha256,
        })
    }

    /// Convert a zero-based line and column to a byte offset.
    ///
    /// # Errors
    /// Rejects unknown lines, columns beyond line content, UTF-8 splits and UTF-16
    /// surrogate splits. CRLF is a line ending, not content available to columns.
    pub fn offset(
        &self,
        line: usize,
        column: usize,
        encoding: Encoding,
    ) -> Result<usize, &'static str> {
        let start = *self.starts.get(line).ok_or("source line out of range")?;
        let mut end = self
            .starts
            .get(line + 1)
            .copied()
            .unwrap_or(self.text.len());
        if end > start && self.text.as_bytes()[end - 1] == b'\n' {
            end -= 1;
            if end > start && self.text.as_bytes()[end - 1] == b'\r' {
                end -= 1;
            }
        }
        let text = &self.text[start..end];
        if encoding == Encoding::Utf8 {
            if column > text.len() || !text.is_char_boundary(column) {
                return Err("invalid UTF-8 column");
            }
            return Ok(start + column);
        }
        let mut units = 0;
        for (byte, ch) in text.char_indices() {
            if units == column {
                return Ok(start + byte);
            }
            units += if encoding == Encoding::Utf16 {
                ch.len_utf16()
            } else {
                1
            };
            if units > column {
                return Err("column splits a Unicode scalar");
            }
        }
        if units == column {
            Ok(end)
        } else {
            Err("source column out of range")
        }
    }

    /// Convert SCIP's three- or four-integer range without guessing encoding.
    ///
    /// # Errors
    /// Rejects negative values, invalid arity, invalid positions or reversed ranges.
    pub fn scip_span(&self, range: &[i32], encoding: Encoding) -> Result<Span, &'static str> {
        if range.iter().any(|v| *v < 0) {
            return Err("negative source coordinate");
        }
        let (line, column, end_line, end_column) = match range {
            [l, c, e] => (*l, *c, *l, *e),
            [l, c, el, ec] => (*l, *c, *el, *ec),
            _ => return Err("invalid SCIP range length"),
        };
        let start = self.offset(line as usize, column as usize, encoding)?;
        let end = self.offset(end_line as usize, end_column as usize, encoding)?;
        if end < start {
            return Err("reversed source range");
        }
        Ok(Span { start, end })
    }

    /// Validate spelling against exact source bytes without resolving its symbol.
    ///
    /// # Errors
    /// Rejects invalid ranges or source spelling substitution.
    pub fn validate_occurrence(&self, occurrence: &Occurrence) -> Result<(), &'static str> {
        if occurrence.source_sha256 != self.source_sha256
            || self.text.get(occurrence.span.start..occurrence.span.end)
                != Some(occurrence.spelling.as_str())
        {
            return Err("occurrence source spelling mismatch");
        }
        Ok(())
    }
}
