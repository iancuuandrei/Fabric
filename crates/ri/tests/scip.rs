//! Admission tests against the captured SCIP range and role contracts.
use engorch_ri::occurrence::{Encoding, SourceText, Span};
use engorch_ri::scip::{Roles, TypedRange, span};

#[test]
#[allow(deprecated)]
fn document_locations_validate_source_and_enclosing_ranges() {
    use engorch_ri::scip::{locate_document, wire};
    let text = "a😀b";
    let mut doc = wire::Document {
        relative_path: "test.rs".into(),
        text: text.into(),
        position_encoding: 2,
        occurrences: vec![wire::Occurrence {
            range: vec![0, 1, 3],
            enclosing_range: vec![0, 0, 4],
            symbol: "local 1".into(),
            symbol_roles: 9,
            ..Default::default()
        }],
        ..Default::default()
    };
    let records = locate_document(&doc, text.as_bytes()).unwrap();
    assert_eq!(records[0].spelling, "😀");
    assert_eq!(records[0].symbol, "local 1");
    assert_eq!(records[0].roles.bits(), 9);
    assert_eq!(records[0].enclosing, Some(Span { start: 0, end: 6 }));
    assert!(locate_document(&doc, b"other").is_err());
    doc.occurrences[0].enclosing_range = vec![0, 0, 1];
    assert!(locate_document(&doc, text.as_bytes()).is_err());
    doc.occurrences[0].enclosing_range.clear();
    doc.occurrences[0].symbol_roles = 128;
    assert!(locate_document(&doc, text.as_bytes()).is_err());
    doc.occurrences[0].symbol_roles = 0;
    doc.occurrences[0].symbol.clear();
    assert_eq!(
        locate_document(&doc, text.as_bytes()).unwrap()[0].symbol,
        ""
    );
    doc.position_encoding = 0;
    assert!(locate_document(&doc, text.as_bytes()).is_err());
}

#[test]
fn binary_scip_decoding_preserves_schema_fields() {
    use engorch_ri::scip::{decode, wire};
    use prost::Message;
    let index = wire::Index {
        metadata: Some(wire::Metadata {
            project_root: "file:///fixture".into(),
            text_document_encoding: 1,
            ..Default::default()
        }),
        documents: vec![wire::Document {
            relative_path: "src/lib.rs".into(),
            position_encoding: 2,
            occurrences: vec![wire::Occurrence {
                symbol: "local 1".into(),
                symbol_roles: 9,
                typed_range: Some(wire::occurrence::TypedRange::SingleLineRange(
                    wire::SingleLineRange {
                        line: 0,
                        start_character: 1,
                        end_character: 3,
                    },
                )),
                ..Default::default()
            }],
            ..Default::default()
        }],
        ..Default::default()
    };
    let bytes = index.encode_to_vec();
    assert_eq!(decode(&bytes).unwrap(), index);
    assert!(decode(&[]).is_err());
    assert!(decode(&[0x0a, 0x80]).is_err());
    // Literal wire fixture: Index.metadata (tag 1), project_root (tag 3).
    assert_eq!(
        decode(&[10, 3, 26, 1, b'x'])
            .unwrap()
            .metadata
            .unwrap()
            .project_root,
        "x"
    );
}

#[test]
fn roles_preserve_combinations_and_reject_unknown_bits() {
    for bits in 0..=127 {
        let roles = Roles::from_scip(bits).unwrap();
        assert_eq!(i32::from(roles.bits()), bits);
        assert_eq!(roles.is_definition(), bits & 1 != 0);
    }
    for bits in [-1, i32::MIN, 128, 256, i32::MAX] {
        assert!(Roles::from_scip(bits).is_err());
    }
}

#[test]
fn dual_ranges_must_agree_in_actual_source_coordinates() {
    let source = SourceText::new("a😀b\ncd".as_bytes()).unwrap();
    let typed = Some(TypedRange::MultiLine([0, 1, 0, 3]));
    assert_eq!(
        span(&source, Encoding::Utf16, &[0, 1, 3], typed).unwrap(),
        Span { start: 1, end: 5 }
    );
    assert!(span(&source, Encoding::Utf16, &[0, 0, 3], typed).is_err());
    assert!(span(&source, Encoding::Utf16, &[0, 1], typed).is_err());
    assert!(span(&source, Encoding::Utf16, &[], None).is_err());
    assert_eq!(
        span(
            &source,
            Encoding::Utf16,
            &[],
            Some(TypedRange::SingleLine([1, 0, 0]))
        )
        .unwrap(),
        Span { start: 7, end: 7 }
    );
    assert!(
        span(
            &source,
            Encoding::Utf16,
            &[],
            Some(TypedRange::SingleLine([0, 1, 2]))
        )
        .is_err()
    );
}
