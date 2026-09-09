//! Canonical rejection and exact encoding tests shared with the Go wire rules.

use engorch_ri::canonical::{decode, encode, hash};
use serde_json::{Value, json};

#[test]
fn exact_unicode_controls_and_integer_encoding() {
    let value = json!({"z":9007199254740991i64,"a":"Ș\n\u{0001}<>&\u{2028}"});
    let expected = "{\"a\":\"Ș\\n\\u0001<>&\u{2028}\",\"z\":9007199254740991}";
    assert_eq!(encode(&value).unwrap(), expected.as_bytes());
    assert_eq!(decode::<Value>(expected.as_bytes()).unwrap(), value);
    assert_ne!(hash("a", b"payload"), hash("b", b"payload"));
    let fixture = include_bytes!("../../../testdata/canonical-v1.json");
    assert_eq!(encode(&value).unwrap(), fixture);
    assert_eq!(
        hash("harness.canonical-fixture.v1", fixture),
        include_str!("../../../testdata/canonical-v1.sha256")
    );
}

#[test]
fn rejects_ambiguous_and_noncanonical_inputs() {
    for input in [
        r#"{"a":1,"a":1}"#,
        r#"{"z":1,"a":2}"#,
        r#"{"a": 1}"#,
        r#"{"a":-0}"#,
        r#"{"a":1.0}"#,
        r#"{"a":9007199254740992}"#,
        r#"{"é":1}"#,
        r#"{"a":"\ud800"}"#,
        r#"{}{}"#,
    ] {
        assert!(decode::<Value>(input.as_bytes()).is_err(), "{input}");
    }
    let nested = format!("{}0{}", "[".repeat(65), "]".repeat(65));
    assert!(decode::<Value>(nested.as_bytes()).is_err());
}
