//! Encoding-bound range conversion tests for semantic interchange.
use engorch_ri::occurrence::*;

#[test]
fn structural_occurrences_remain_unresolved_and_source_bound() {
    let bytes = b"type T = crate::X;";
    let types = engorch_ri::structural::rust_types(bytes).unwrap();
    let occurrences = types.occurrences("src/lib.rs", "structural-v1").unwrap();
    let source = SourceText::new(bytes).unwrap();
    assert_eq!(occurrences.len(), 1);
    assert!(occurrences[0].symbol.is_none());
    source.validate_occurrence(&occurrences[0]).unwrap();
    let mut changed = occurrences[0].clone();
    changed.source_sha256 = "0".repeat(64);
    assert!(source.validate_occurrence(&changed).is_err());
    let other = types.occurrences("other.rs", "structural-v1").unwrap();
    assert_ne!(occurrences[0].id, other[0].id);
    assert!(Encoding::from_scip(0).is_err());
    assert!(Encoding::from_scip(9).is_err());
    assert_eq!(Encoding::from_scip(2).unwrap(), Encoding::Utf16);
    assert!(SourceText::new(&vec![b'\n'; 1_000_000]).is_err());
}

#[test]
fn encodings_agree_only_at_valid_scalar_boundaries() {
    let source = SourceText::new("a😀ș\r\nnext\n".as_bytes()).unwrap();
    assert_eq!(source.offset(0, 5, Encoding::Utf8).unwrap(), 5);
    assert_eq!(source.offset(0, 3, Encoding::Utf16).unwrap(), 5);
    assert_eq!(source.offset(0, 2, Encoding::Utf32).unwrap(), 5);
    assert!(source.offset(0, 2, Encoding::Utf16).is_err());
    assert!(source.offset(0, 2, Encoding::Utf8).is_err());
    assert_eq!(source.offset(0, 7, Encoding::Utf8).unwrap(), 7);
    assert!(source.offset(0, 8, Encoding::Utf8).is_err());
    assert_eq!(source.offset(1, 0, Encoding::Utf8).unwrap(), 9);
    assert_eq!(source.offset(2, 0, Encoding::Utf8).unwrap(), 14);
}

#[test]
fn scip_ranges_reject_invalid_or_reversed_coordinates() {
    let source = SourceText::new("a😀b\ncd".as_bytes()).unwrap();
    assert_eq!(
        source.scip_span(&[0, 1, 3], Encoding::Utf16).unwrap(),
        Span { start: 1, end: 5 }
    );
    assert_eq!(
        source.scip_span(&[0, 1, 1, 1], Encoding::Utf16).unwrap(),
        Span { start: 1, end: 8 }
    );
    for range in [&[-1, 0, 1][..], &[0, 3, 1], &[0, 1], &[0, 1, 2], &[2, 0, 0]] {
        assert!(source.scip_span(range, Encoding::Utf16).is_err());
    }
}
