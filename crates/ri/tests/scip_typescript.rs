//! Admission tests for the explicit scip-typescript 0.4.0 UTF-16 profile.
use fabric_ri::{
    manifest::{Input, Manifest, Producer, Source},
    scip::{self, wire},
};
use prost::Message;
use std::collections::BTreeMap;

fn hash(bytes: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

const SYMBOL: &str = "scip-typescript npm tsfixture 1.0.0 `library.ts`/Greeting().";
const LINE0: &str = "/* \u{1F600} */ export function Greeting() { return \"hi\"; }";
const LINE1: &str = "export const abcdef = Greeting();";

fn source_bytes() -> Vec<u8> {
    format!("{LINE0}\n{LINE1}\n").into_bytes()
}

fn fixture() -> (wire::Index, Manifest, BTreeMap<String, Vec<u8>>, Source) {
    let source = source_bytes();
    let index = wire::Index {
        metadata: Some(wire::Metadata {
            project_root: "file:///fixture".into(),
            text_document_encoding: 1,
            tool_info: Some(wire::ToolInfo {
                name: "scip-typescript".into(),
                version: "0.4.0".into(),
                arguments: vec![],
            }),
            ..Default::default()
        }),
        documents: vec![wire::Document {
            relative_path: "library.ts".into(),
            // Observed producer output omits the field; the explicit profile maps it.
            position_encoding: 0,
            occurrences: vec![
                #[allow(deprecated)]
                wire::Occurrence {
                    range: vec![0, 25, 33],
                    symbol: SYMBOL.into(),
                    symbol_roles: 1,
                    ..Default::default()
                },
                #[allow(deprecated)]
                wire::Occurrence {
                    range: vec![1, 22, 30],
                    symbol: SYMBOL.into(),
                    symbol_roles: 0,
                    ..Default::default()
                },
            ],
            ..Default::default()
        }],
        ..Default::default()
    };
    let expected = Source {
        repository_id: "a".repeat(64),
        object_format: "sha1".into(),
        commit: "b".repeat(40),
        tree: "c".repeat(40),
    };
    let manifest = Manifest {
        format: 1,
        source: expected.clone(),
        producers: vec![Producer {
            id: "ts-semantic".into(),
            name: "scip-typescript".into(),
            version: "0.4.0".into(),
            artifact_sha256: "d".repeat(64),
            inputs: vec![
                Input {
                    name: "scip:index".into(),
                    sha256: hash(&index.encode_to_vec()),
                },
                Input {
                    name: "source:library.ts".into(),
                    sha256: hash(&source),
                },
                Input {
                    name: "scip:position-policy".into(),
                    sha256: hash(scip::SCIP_TYPESCRIPT_040_POSITION_POLICY.as_bytes()),
                },
            ],
        }],
    };
    (
        index,
        manifest,
        BTreeMap::from([("library.ts".into(), source)]),
        expected,
    )
}

#[test]
fn typescript_unspecified_maps_to_utf16_with_astral_coordinates() {
    let (index, manifest, sources, expected) = fixture();
    let bytes = index.encode_to_vec();
    // Generic strict admission still rejects the omitted encoding.
    assert!(
        scip::admit(
            &bytes,
            manifest.clone(),
            &expected,
            "ts-semantic",
            "file:///fixture",
            &sources
        )
        .is_err()
    );
    let admitted = scip::admit_scip_typescript_040(
        &bytes,
        manifest.clone(),
        &expected,
        "ts-semantic",
        "file:///fixture",
        &sources,
    )
    .unwrap();
    assert_eq!(admitted.index().documents[0].position_encoding, 2);
    let artifact = admitted.snapshot(&sources).unwrap();
    let loaded = fabric_ri::snapshot::read(&artifact.bytes, &artifact.id, &expected).unwrap();
    let records = loaded.occurrences().records();
    assert_eq!(records.len(), 2);
    // UTF-16 columns [0,25,33] cross one astral surrogate pair, so the UTF-8
    // byte start is 27 rather than 25.
    let definition = records
        .iter()
        .find(|o| o.roles.unwrap().is_definition())
        .unwrap();
    assert_eq!(definition.spelling, "Greeting");
    assert_eq!(
        definition.span,
        fabric_ri::occurrence::Span { start: 27, end: 35 }
    );
    assert_eq!(
        &sources["library.ts"][definition.span.start..definition.span.end],
        b"Greeting"
    );
    let reference = records
        .iter()
        .find(|o| !o.roles.unwrap().is_definition())
        .unwrap();
    assert_eq!(reference.spelling, "Greeting");
    let line0_len = LINE0.as_bytes().len() + 1;
    assert_eq!(
        reference.span,
        fabric_ri::occurrence::Span {
            start: line0_len + 22,
            end: line0_len + 30
        }
    );
    assert_eq!(
        &sources["library.ts"][reference.span.start..reference.span.end],
        b"Greeting"
    );
    assert_eq!(definition.source_sha256, hash(&sources["library.ts"]));
}

#[test]
fn typescript_explicit_utf16_accepts_but_utf8_utf32_conflict() {
    let (index, manifest, sources, expected) = fixture();
    for encoding in [2, 1, 3] {
        let mut index = index.clone();
        index.documents[0].position_encoding = encoding;
        let bytes = index.encode_to_vec();
        let mut manifest = manifest.clone();
        manifest.producers[0].inputs[0].sha256 = hash(&bytes);
        let result = scip::admit_scip_typescript_040(
            &bytes,
            manifest,
            &expected,
            "ts-semantic",
            "file:///fixture",
            &sources,
        );
        if encoding == 2 {
            assert!(result.is_ok(), "explicit UTF-16 must be accepted");
        } else {
            assert!(result.is_err(), "encoding {encoding} must conflict");
        }
    }
}

const FILE_SYMBOL: &str = "scip-typescript npm tsfixture 1.0.0 `library.ts`/";

fn synthetic_fixture() -> (wire::Index, Manifest, BTreeMap<String, Vec<u8>>, Source) {
    let (mut index, mut manifest, sources, expected) = fixture();
    #[allow(deprecated)]
    let marker = wire::Occurrence {
        range: vec![0, 0, 0],
        enclosing_range: vec![0, 9, 2, 0],
        symbol: FILE_SYMBOL.into(),
        symbol_roles: 1,
        ..Default::default()
    };
    index.documents[0].occurrences.insert(0, marker);
    index.documents[0].symbols = vec![wire::SymbolInformation {
        symbol: FILE_SYMBOL.into(),
        ..Default::default()
    }];
    let bytes = index.encode_to_vec();
    manifest.producers[0].inputs[0].sha256 = hash(&bytes);
    (index, manifest, sources, expected)
}

fn admit_typescript(
    index: &wire::Index,
    manifest: &Manifest,
    sources: &BTreeMap<String, Vec<u8>>,
    expected: &Source,
) -> Result<(), String> {
    let bytes = index.encode_to_vec();
    scip::admit_scip_typescript_040(
        &bytes,
        manifest.clone(),
        expected,
        "ts-semantic",
        "file:///fixture",
        sources,
    )
    .map(|_| ())
}

#[test]
#[allow(deprecated)]
fn typescript_synthetic_file_marker_drops_advisory_enclosing() {
    let (index, manifest, sources, expected) = synthetic_fixture();
    let bytes = index.encode_to_vec();
    // Strict admission still rejects the non-containing synthetic enclosing.
    assert!(
        scip::admit(
            &bytes,
            manifest.clone(),
            &expected,
            "ts-semantic",
            "file:///fixture",
            &sources
        )
        .is_err()
    );
    // Explicit UTF-16 strict admission also still rejects it.
    {
        let mut strict_index = index.clone();
        strict_index.documents[0].position_encoding = 2;
        let strict_bytes = strict_index.encode_to_vec();
        let mut strict_manifest = manifest.clone();
        strict_manifest.producers[0].inputs[0].sha256 = hash(&strict_bytes);
        assert!(
            scip::admit(
                &strict_bytes,
                strict_manifest,
                &expected,
                "ts-semantic",
                "file:///fixture",
                &sources
            )
            .is_err()
        );
    }
    let admitted = scip::admit_scip_typescript_040(
        &bytes,
        manifest.clone(),
        &expected,
        "ts-semantic",
        "file:///fixture",
        &sources,
    )
    .unwrap();
    // Raw index bytes retain the invalid enclosing; only the derived
    // projection omits it.
    let raw = &admitted.index().documents[0].occurrences[0];
    assert_eq!(raw.range, vec![0, 0, 0]);
    assert_eq!(raw.enclosing_range, vec![0, 9, 2, 0]);
    let artifact = admitted.snapshot(&sources).unwrap();
    let loaded = fabric_ri::snapshot::read(&artifact.bytes, &artifact.id, &expected).unwrap();
    let records = loaded.occurrences().records();
    assert_eq!(records.len(), 3);
    let expected_marker_id =
        scip::SymbolIdentity::new("ts-semantic", FILE_SYMBOL, Some("library.ts"))
            .unwrap()
            .id;
    let marker = records
        .iter()
        .find(|o| o.symbol.as_deref() == Some(expected_marker_id.as_str()))
        .unwrap();
    // Exact byte coordinates are untouched: zero-length anchor at file start.
    assert_eq!(
        marker.span,
        fabric_ri::occurrence::Span { start: 0, end: 0 }
    );
    assert_eq!(marker.spelling, "");
    assert_eq!(
        &sources["library.ts"][marker.span.start..marker.span.end],
        b""
    );
    assert!(marker.roles.unwrap().is_definition());
    assert_eq!(marker.source_sha256, hash(&sources["library.ts"]));
}

#[test]
fn typescript_ordinary_wrong_enclosing_still_rejects() {
    let (mut index, mut manifest, sources, expected) = synthetic_fixture();
    // Ordinary token occurrence with well-formed but non-containing enclosing.
    index.documents[0].occurrences[1].enclosing_range = vec![0, 0, 1];
    let bytes = index.encode_to_vec();
    manifest.producers[0].inputs[0].sha256 = hash(&bytes);
    assert!(admit_typescript(&index, &manifest, &sources, &expected).is_err());
}

#[test]
fn typescript_synthetic_exemptions_are_exact() {
    let (index, manifest, sources, expected) = synthetic_fixture();
    // Wrong symbol (mismatched path descriptor) is not exempt.
    {
        let mut changed = index.clone();
        changed.documents[0].occurrences[0].symbol =
            "scip-typescript npm tsfixture 1.0.0 `other.ts`/".into();
        assert!(
            admit_typescript(
                &changed,
                &{
                    let mut m = manifest.clone();
                    m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
                    m
                },
                &sources,
                &expected
            )
            .is_err()
        );
    }
    // Wrong role (reference instead of exactly definition) is not exempt.
    {
        let mut changed = index.clone();
        changed.documents[0].occurrences[0].symbol_roles = 0;
        assert!(
            admit_typescript(
                &changed,
                &{
                    let mut m = manifest.clone();
                    m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
                    m
                },
                &sources,
                &expected
            )
            .is_err()
        );
    }
    // Extra definition-adjacent flag is not exactly definition.
    {
        let mut changed = index.clone();
        changed.documents[0].occurrences[0].symbol_roles = 3;
        assert!(
            admit_typescript(
                &changed,
                &{
                    let mut m = manifest.clone();
                    m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
                    m
                },
                &sources,
                &expected
            )
            .is_err()
        );
    }
    // Nonzero anchor is not exempt.
    {
        let mut changed = index.clone();
        changed.documents[0].occurrences[0].range = vec![0, 0, 1];
        assert!(
            admit_typescript(
                &changed,
                &{
                    let mut m = manifest.clone();
                    m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
                    m
                },
                &sources,
                &expected
            )
            .is_err()
        );
    }
    // Missing SymbolInformation provenance is not exempt.
    {
        let mut changed = index.clone();
        changed.documents[0].symbols.clear();
        assert!(
            admit_typescript(
                &changed,
                &{
                    let mut m = manifest.clone();
                    m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
                    m
                },
                &sources,
                &expected
            )
            .is_err()
        );
    }
    // Local symbol with the same ranges is not the nonlocal file descriptor.
    {
        let mut changed = index.clone();
        changed.documents[0].occurrences[0].symbol = "local marker".into();
        assert!(
            admit_typescript(
                &changed,
                &{
                    let mut m = manifest.clone();
                    m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
                    m
                },
                &sources,
                &expected
            )
            .is_err()
        );
    }
}

#[test]
fn typescript_file_module_descriptor_separator_is_exact() {
    let (index, manifest, sources, expected) = synthetic_fixture();
    // Valid actual-source file-module descriptor is accepted.
    assert!(admit_typescript(&index, &manifest, &sources, &expected).is_ok());
    // Doubled separator, absent separator, and extra tokens all reject, even
    // with matching SymbolInformation provenance.
    for symbol in [
        "scip-typescript npm tsfixture 1.0.0  `library.ts`/",
        "scip-typescript npm tsfixture 1.0.0`library.ts`/",
        "scip-typescript npm tsfixture 1.0.0 extra `library.ts`/",
    ] {
        let mut changed = index.clone();
        changed.documents[0].occurrences[0].symbol = symbol.into();
        changed.documents[0].symbols[0].symbol = symbol.into();
        let mut m = manifest.clone();
        m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
        assert!(
            admit_typescript(&changed, &m, &sources, &expected).is_err(),
            "descriptor {symbol:?} must reject"
        );
    }
}

#[test]
fn typescript_synthetic_malformed_enclosing_still_rejects() {
    let (index, manifest, sources, expected) = synthetic_fixture();
    // Out-of-bounds enclosing line is malformed and must fail before omission.
    {
        let mut changed = index.clone();
        changed.documents[0].occurrences[0].enclosing_range = vec![0, 9, 5, 0];
        let mut m = manifest.clone();
        m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
        assert!(admit_typescript(&changed, &m, &sources, &expected).is_err());
    }
    // Reversed enclosing is malformed and must fail before omission.
    {
        let mut changed = index.clone();
        changed.documents[0].occurrences[0].enclosing_range = vec![2, 0, 0, 9];
        let mut m = manifest.clone();
        m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
        assert!(admit_typescript(&changed, &m, &sources, &expected).is_err());
    }
    // Negative enclosing coordinate is malformed.
    {
        let mut changed = index.clone();
        changed.documents[0].occurrences[0].enclosing_range = vec![0, -1, 2, 0];
        let mut m = manifest.clone();
        m.producers[0].inputs[0].sha256 = hash(&changed.encode_to_vec());
        assert!(admit_typescript(&changed, &m, &sources, &expected).is_err());
    }
}

#[test]
fn typescript_provenance_and_substitution_rejected() {
    let (index, manifest, sources, expected) = fixture();
    let bytes = index.encode_to_vec();
    let run = |m: Manifest, s: &BTreeMap<String, Vec<u8>>| {
        scip::admit_scip_typescript_040(&bytes, m, &expected, "ts-semantic", "file:///fixture", s)
    };
    // Wrong producer name, wrong version and wrong policy hash all fail.
    for mutation in 0..3 {
        let mut changed = manifest.clone();
        match mutation {
            0 => changed.producers[0].name = "other-indexer".into(),
            1 => changed.producers[0].version = "0.5.0".into(),
            2 => {
                changed.producers[0]
                    .inputs
                    .retain(|i| i.name != "scip:position-policy");
            }
            _ => unreachable!(),
        }
        assert!(
            run(changed, &sources).is_err(),
            "mutation {mutation} accepted"
        );
    }
    let mut wrong_hash = manifest.clone();
    for input in &mut wrong_hash.producers[0].inputs {
        if input.name == "scip:position-policy" {
            input.sha256 = "0".repeat(64);
        }
    }
    assert!(run(wrong_hash, &sources).is_err());
    // Substituted source bytes and substituted index hash both fail.
    let mut changed_sources = sources.clone();
    changed_sources.insert("library.ts".into(), b"export function Other() {}".to_vec());
    assert!(run(manifest.clone(), &changed_sources).is_err());
    let mut changed_manifest = manifest.clone();
    changed_manifest.producers[0].inputs[0].sha256 = "0".repeat(64);
    assert!(run(changed_manifest, &sources).is_err());
    // Snapshot conversion rechecks sources after admission.
    let admitted = run(manifest, &sources).unwrap();
    assert!(admitted.snapshot(&changed_sources).is_err());
}
