//! Exact byte matching with conservative candidate planning from pinned tgrep.
//!
//! Snapshot binding and index storage belong to the caller. This module does not
//! treat index candidates as matches or an empty optimization plan as absence.

use regex::bytes::{Regex, RegexBuilder};
use tgrep_core::query::{self, QueryPlan};

/// Compiled exact matcher and its optional candidate restriction.
pub struct LexicalQuery {
    id: String,
    matcher: Regex,
    plan: QueryPlan,
    path: Option<String>,
    file_type: Option<String>,
}

/// Candidate selection, before exact source matching.
#[derive(Debug, PartialEq, Eq)]
pub struct Candidates {
    /// Sorted and deduplicated file IDs in the admitted index universe.
    pub files: Vec<u32>,
    /// True when no proven restriction is used.
    pub full_scan: bool,
}

fn unrestricted(plan: &QueryPlan) -> bool {
    match plan {
        QueryPlan::MatchAll => true,
        QueryPlan::And(parts) => parts.is_empty(),
        QueryPlan::Or(parts) => parts.is_empty() || parts.iter().any(unrestricted),
    }
}

impl LexicalQuery {
    /// Compile bounded Rust-regex syntax or a literal. Unicode/case-folded and
    /// inline-flag patterns currently use full evaluation until their planner
    /// equivalence is qualified; they retain the exact matcher's semantics.
    pub fn compile(pattern: &str, fixed: bool, case_insensitive: bool) -> Result<Self, String> {
        Self::compile_filtered(pattern, fixed, case_insensitive, None, None)
    }

    /// Compile matcher options with optional repository path/subtree and file
    /// extension filters. Empty filter strings have the legacy unfiltered identity.
    pub fn compile_filtered(
        pattern: &str,
        fixed: bool,
        case_insensitive: bool,
        path: Option<&str>,
        file_type: Option<&str>,
    ) -> Result<Self, String> {
        if pattern.len() > 16 * 1024 {
            return Err("lexical pattern exceeds bound".into());
        }
        let path = path.filter(|value| !value.is_empty()).map(str::to_owned);
        if path.as_ref().is_some_and(|value| {
            value.len() > 4096
                || value
                    .chars()
                    .any(|c| c.is_control() || c == '\\' || c == ':')
                || value
                    .split('/')
                    .any(|part| part.is_empty() || part == "." || part == "..")
        }) {
            return Err("invalid lexical path filter".into());
        }
        let file_type = file_type
            .filter(|value| !value.is_empty())
            .map(str::to_owned);
        if file_type.as_ref().is_some_and(|value| {
            value.len() > 32
                || !value.bytes().enumerate().all(|(n, b)| {
                    b.is_ascii_alphanumeric() || (n > 0 && matches!(b, b'_' | b'+' | b'-'))
                })
        }) {
            return Err("invalid lexical type filter".into());
        }
        let expression = if fixed {
            regex::escape(pattern)
        } else {
            pattern.to_owned()
        };
        let matcher = RegexBuilder::new(&expression)
            .case_insensitive(case_insensitive)
            .size_limit(8 * 1024 * 1024)
            .dfa_size_limit(8 * 1024 * 1024)
            .build()
            .map_err(|_| "invalid or oversized lexical expression".to_string())?;
        let plan = if case_insensitive || !pattern.is_ascii() || (!fixed && pattern.contains("(?"))
        {
            QueryPlan::MatchAll
        } else if fixed {
            query::build_literal_plan(pattern, false)
        } else {
            query::build_query_plan(pattern, false).unwrap_or(QueryPlan::MatchAll)
        };
        let (domain, identity) = if path.is_none() && file_type.is_none() {
            (
                "harness.ri.lexical-query.v1",
                serde_json::json!({
                    "pattern": pattern, "fixed": fixed, "case_insensitive": case_insensitive,
                    "profile": "rust-regex-bytes-v1-tgrep-e2007b52"
                }),
            )
        } else {
            (
                "harness.ri.lexical-query.v2",
                serde_json::json!({
                    "pattern": pattern, "fixed": fixed, "case_insensitive": case_insensitive,
                    "path": path, "type": file_type,
                    "profile": "rust-regex-bytes-v1-tgrep-e2007b52-path-type-v1"
                }),
            )
        };
        let id = crate::canonical::hash(domain, &crate::canonical::encode(&identity)?);
        Ok(Self {
            id,
            matcher,
            plan,
            path,
            file_type,
        })
    }

    /// Identity of exact query options and the versioned matcher profile.
    pub fn id(&self) -> &str {
        &self.id
    }

    /// Whether a repository-relative path is inside the optional exact
    /// path/subtree scope and has the optional case-sensitive extension.
    pub fn includes_path(&self, candidate: &str) -> bool {
        self.path.as_ref().is_none_or(|path| {
            candidate == path
                || candidate
                    .strip_prefix(path)
                    .is_some_and(|suffix| suffix.starts_with('/'))
        }) && self.file_type.as_ref().is_none_or(|file_type| {
            candidate
                .rsplit('/')
                .next()
                .is_some_and(|name| name.ends_with(&format!(".{file_type}")))
        })
    }

    /// Select candidates using postings from the same immutable byte universe.
    /// The caller must supply every admitted file, including files without any
    /// trigrams. No filesystem reads or model reasoning occur here.
    pub fn candidates(&self, universe: &[u32], lookup: impl Fn(u32) -> Vec<u32>) -> Candidates {
        let full_scan = unrestricted(&self.plan);
        let mut files = if full_scan {
            universe.to_vec()
        } else {
            query::execute_plan(&self.plan, &lookup)
        };
        files.sort_unstable();
        files.dedup();
        Candidates { files, full_scan }
    }

    /// Run the configured exact matcher on source bytes. Offsets are byte ranges,
    /// not Unicode character positions. Caller applies output/pagination bounds.
    pub fn ranges<'a>(
        &'a self,
        bytes: &'a [u8],
    ) -> impl Iterator<Item = std::ops::Range<usize>> + 'a {
        self.matcher.find_iter(bytes).map(|m| m.range())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tgrep_core::live::LiveIndex;

    #[test]
    fn indexed_candidates_preserve_exact_matches() {
        let texts: &[&[u8]] = &[
            b"",
            b"a",
            b"RetryPolicy",
            b"foo bar",
            b"bar",
            b"foobar",
            b"FOOBAR",
            "Kelvin Σ sigma".as_bytes(),
            b"\xfffoo\x00bar",
        ];
        let mut index = LiveIndex::new();
        for (n, text) in texts.iter().enumerate() {
            index.upsert_file(&n.to_string(), text);
        }
        let universe = index.all_file_ids();
        for pattern in [
            "",
            "a",
            "foo",
            "foo.*bar",
            "foo|a",
            "(foo)?bar",
            "foo{0,2}",
            "[a-z]+",
            "(?i)foobar",
            "K",
            "\\x{212a}",
            "^bar$",
            "foo|",
            "RetryPolicy",
        ] {
            for fixed in [false, true] {
                for insensitive in [false, true] {
                    let q = LexicalQuery::compile(pattern, fixed, insensitive).unwrap();
                    let selected = q.candidates(&universe, |tri| index.lookup_trigram(tri));
                    for (n, text) in texts.iter().enumerate() {
                        if q.ranges(text).next().is_some() {
                            let id = index.file_id_for_path(&n.to_string()).unwrap();
                            assert!(
                                selected.files.contains(&id),
                                "false negative: {pattern:?} fixed={fixed} insensitive={insensitive} source={n}"
                            );
                        }
                    }
                }
            }
        }
    }

    #[test]
    fn fallback_includes_empty_and_short_files() {
        let q = LexicalQuery::compile("a|", false, false).unwrap();
        assert_eq!(
            q.candidates(&[9, 2, 9], |_| panic!("fallback must not read postings")),
            Candidates {
                files: vec![2, 9],
                full_scan: true
            }
        );
        assert!(LexicalQuery::compile("(", false, false).is_err());
    }

    #[test]
    fn path_and_type_filters_are_validated_and_identified() {
        let plain = LexicalQuery::compile("needle", true, false).unwrap();
        let empty =
            LexicalQuery::compile_filtered("needle", true, false, Some(""), Some("")).unwrap();
        assert_eq!(plain.id(), empty.id());
        let filtered =
            LexicalQuery::compile_filtered("needle", true, false, Some("src"), Some("rs")).unwrap();
        assert_ne!(plain.id(), filtered.id());
        assert!(filtered.includes_path("src/lib.rs"));
        assert!(!filtered.includes_path("src/lib.go"));
        assert!(!filtered.includes_path("other/src/lib.rs"));
        for path in ["/src", "src/", "src//lib", "src/../lib", "src\\lib"] {
            assert!(LexicalQuery::compile_filtered("x", true, false, Some(path), None).is_err());
        }
        for file_type in [".rs", "r/s", "-rs", "rust!"] {
            assert!(
                LexicalQuery::compile_filtered("x", true, false, None, Some(file_type)).is_err()
            );
        }
    }
}
