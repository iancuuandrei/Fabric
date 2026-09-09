//! Repository intelligence reports source evidence; it does not choose task context.
//!
//! This crate owns graph indexes, coverage reasoning and identifier navigation.
//! The Go controller retains workflow and effect authority.

pub mod canonical;
pub mod changed;
pub mod graph;
pub mod identifiers;
pub mod import;
pub mod lexical;
pub mod lexical_disk;
pub mod lexical_index;
pub mod lexical_io;
pub mod lexical_manifest;
pub mod manifest;
pub mod occurrence;
pub mod query;
pub mod scip;
pub mod snapshot;
pub mod structural;
pub mod transport;
