//! Structural Rust type occurrences from immutable bytes, not resolved symbols.
//!
//! Type walking adapts Graphify `_rust_collect_type_refs` at revision
//! c9f99018774e2e0380e9f65b3959944559a0d5f6. Retained licenses/notices and changes
//! are in third_party/graphify. No method-name blocklist is used.

use crate::graph::Completeness;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::{
    ops::ControlFlow,
    time::{Duration, Instant},
};
use tree_sitter::{Node, ParseOptions, Parser};

/// Syntactic role within a type expression; neither role implies name resolution.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum TypeRole {
    /// Main type expression.
    Type,
    /// Type expression inside generic arguments.
    GenericArgument,
}

/// An observed spelling and its exact byte range in the supplied source.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TypeReference {
    /// Full source spelling, retaining qualification such as crate::module::Type.
    pub name: String,
    /// Syntactic role, not a semantic edge kind.
    pub role: TypeRole,
    /// Inclusive UTF-8 byte offset.
    pub start_byte: usize,
    /// Exclusive UTF-8 byte offset.
    pub end_byte: usize,
}

/// Structural evidence. It cannot establish semantic reference absence.
#[derive(Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TypeReferences {
    /// SHA-256 of the exact immutable input bytes.
    pub source_sha256: String,
    /// Whether the parser observed syntax errors anywhere in the input.
    pub syntax_errors: bool,
    /// Always PARTIAL: syntax cannot resolve macros, imports or compiler semantics.
    pub semantic_coverage: Completeness,
    /// Occurrences in deterministic source order, retaining repeated spellings.
    pub references: Vec<TypeReference>,
}

impl TypeReferences {
    /// Convert structural observations to explicitly unresolved source occurrences.
    ///
    /// # Errors
    /// Rejects invalid path/producer labels or malformed source identities. This
    /// does not register occurrences in a snapshot or infer a semantic symbol.
    pub fn occurrences(
        &self,
        path: &str,
        producer: &str,
    ) -> Result<Vec<crate::occurrence::Occurrence>, String> {
        if path.is_empty()
            || path.len() > 4096
            || path.contains(['\\', ':'])
            || path.chars().any(char::is_control)
            || path
                .split('/')
                .any(|p| p.is_empty() || p == "." || p == "..")
            || producer.trim().is_empty()
            || producer.len() > 256
            || producer.chars().any(char::is_control)
        {
            return Err("invalid occurrence scope".into());
        }
        if self.source_sha256.len() != 64
            || !self
                .source_sha256
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        {
            return Err("invalid occurrence source digest".into());
        }
        self.references.iter().map(|reference|{
            let value=serde_json::json!({"path":path,"source_sha256":self.source_sha256,"producer":producer,"reference":reference});
            let id=crate::canonical::hash("harness.ri.occurrence.v1",&crate::canonical::encode(&value)?);
            Ok(crate::occurrence::Occurrence {id,path:path.into(),source_sha256:self.source_sha256.clone(),span:crate::occurrence::Span {start:reference.start_byte,end:reference.end_byte},spelling:reference.name.clone(),symbol:None,roles:None,producer:producer.into(),quality:crate::graph::Quality::Observed})
        }).collect()
    }
}

struct Budget {
    deadline: Instant,
    visited: usize,
}
impl Budget {
    fn visit(&mut self) -> Result<(), String> {
        self.visited += 1;
        if self.visited > 100_000 || Instant::now() > self.deadline {
            return Err("structural traversal budget exceeded".into());
        }
        Ok(())
    }
}

fn occurrence(
    node: Node<'_>,
    source: &[u8],
    generic: bool,
    out: &mut Vec<TypeReference>,
) -> Result<(), String> {
    let name = node.utf8_text(source).map_err(|e| e.to_string())?;
    if name.len() > 4096 || out.len() >= 50_000 {
        return Err("structural occurrence bound exceeded".into());
    }
    out.push(TypeReference {
        name: name.into(),
        role: if generic {
            TypeRole::GenericArgument
        } else {
            TypeRole::Type
        },
        start_byte: node.start_byte(),
        end_byte: node.end_byte(),
    });
    Ok(())
}

fn collect(
    root: Node<'_>,
    source: &[u8],
    out: &mut Vec<TypeReference>,
    budget: &mut Budget,
) -> Result<(), String> {
    let mut pending = vec![(root, false)];
    while let Some((node, generic)) = pending.pop() {
        budget.visit()?;
        match node.kind() {
            "primitive_type" => {}
            "type_identifier" | "scoped_type_identifier" => occurrence(node, source, generic, out)?,
            "generic_type" => {
                let mut cursor = node.walk();
                let children: Vec<_> = node.named_children(&mut cursor).collect();
                let base = node.child_by_field_name("type").or_else(|| {
                    children
                        .iter()
                        .copied()
                        .find(|c| matches!(c.kind(), "type_identifier" | "scoped_type_identifier"))
                });
                if let Some(base) = base {
                    occurrence(base, source, generic, out)?;
                }
                for child in children
                    .into_iter()
                    .rev()
                    .filter(|c| c.kind() == "type_arguments")
                {
                    let mut cursor = child.walk();
                    let arguments: Vec<_> = child.named_children(&mut cursor).collect();
                    pending.extend(arguments.into_iter().rev().map(|n| (n, true)));
                }
            }
            _ => {
                let mut cursor = node.walk();
                let children: Vec<_> = node.named_children(&mut cursor).collect();
                pending.extend(children.into_iter().rev().map(|n| (n, generic)));
            }
        }
    }
    Ok(())
}

/// Parse explicit Rust type fields without opening files or resolving symbols.
///
/// # Errors
/// Rejects non-UTF-8 or inputs above 1 MiB. Parsing/traversal has a five-second
/// cooperative deadline and explicit node/occurrence bounds; limit failures return
/// no partial success. Syntax errors are retained as a coverage limitation.
pub fn rust_types(source: &[u8]) -> Result<TypeReferences, String> {
    if source.len() > 1 << 20 || std::str::from_utf8(source).is_err() {
        return Err("unsupported structural source bytes".into());
    }
    let deadline = Instant::now() + Duration::from_secs(5);
    let mut parser = Parser::new();
    parser
        .set_language(&tree_sitter_rust::LANGUAGE.into())
        .map_err(|e| e.to_string())?;
    let mut progress = |_: &tree_sitter::ParseState| {
        if Instant::now() > deadline {
            ControlFlow::Break(())
        } else {
            ControlFlow::Continue(())
        }
    };
    let tree = parser
        .parse_with_options(
            &mut |offset, _| &source[offset.min(source.len())..],
            None,
            Some(ParseOptions::new().progress_callback(&mut progress)),
        )
        .ok_or("structural parser cancelled")?;
    let mut budget = Budget {
        deadline,
        visited: 0,
    };
    let mut references = Vec::new();
    let mut pending = vec![tree.root_node()];
    while let Some(node) = pending.pop() {
        budget.visit()?;
        let type_node = node.child_by_field_name("type");
        let return_node = node.child_by_field_name("return_type");
        let mut cursor = node.walk();
        let children: Vec<_> = node.named_children(&mut cursor).collect();
        for child in children.into_iter().rev() {
            if Some(child) == type_node || Some(child) == return_node {
                collect(child, source, &mut references, &mut budget)?;
            } else {
                pending.push(child);
            }
        }
    }
    references.sort_by_key(|r| (r.start_byte, r.end_byte));
    let source_sha256 = Sha256::digest(source)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    Ok(TypeReferences {
        source_sha256,
        syntax_errors: tree.root_node().has_error(),
        semantic_coverage: Completeness::Partial,
        references,
    })
}
