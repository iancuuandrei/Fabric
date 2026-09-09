//! Lexical scope identity and exact byte admission regression tests.
use engorch_ri::{
    lexical_manifest::{LexicalFile, LexicalManifest},
    manifest::Source,
};
use sha2::{Digest, Sha256};

#[test]
fn record_transport_exceeds_single_request_bound_and_rejects_torn_tail() {
    use engorch_ri::{canonical, lexical_manifest::read_manifest};
    let mut manifest = fixture();
    let file = manifest.files[0].clone();
    manifest.files = (0..6000)
        .map(|n| {
            let mut f = file.clone();
            f.path = format!("src/{n:06}.rs");
            f
        })
        .collect();
    let mut bytes =
        canonical::encode(&serde_json::json!({"kind":"source","value":manifest.source})).unwrap();
    bytes.push(b'\n');
    for file in &manifest.files {
        bytes.extend(canonical::encode(&serde_json::json!({"kind":"file","value":file})).unwrap());
        bytes.push(b'\n');
    }
    assert!(bytes.len() > canonical::MAX_BYTES);
    let id = manifest.id().unwrap();
    let decoded = read_manifest(bytes.as_slice(), &id, &manifest.source).unwrap();
    assert_eq!(decoded, manifest);
    engorch_ri::lexical_manifest::verify_manifest(bytes.as_slice(), &manifest).unwrap();
    assert!(
        engorch_ri::lexical_manifest::verify_manifest(&bytes[..bytes.len() - 1], &manifest)
            .is_err()
    );
    let mut extended = bytes.clone();
    extended.extend(b"{}\n");
    assert!(engorch_ri::lexical_manifest::verify_manifest(extended.as_slice(), &manifest).is_err());
    let mut substituted = manifest.clone();
    substituted.files[3000].bytes += 1;
    assert!(engorch_ri::lexical_manifest::verify_manifest(bytes.as_slice(), &substituted).is_err());
    assert!(read_manifest(&bytes[..bytes.len() - 1], &id, &manifest.source).is_err());
    assert!(read_manifest(bytes.as_slice(), &"0".repeat(64), &manifest.source).is_err());
    let oversized = vec![b'a'; canonical::MAX_BYTES + 2];
    assert!(read_manifest(oversized.as_slice(), &id, &manifest.source).is_err());
}

#[test]
fn blob_identity_agrees_with_real_git_in_both_object_formats() {
    use std::{
        io::Write,
        process::{Command, Stdio},
    };
    for format in ["sha1", "sha256"] {
        let root = tempfile::tempdir().unwrap();
        let git = |args: &[&str]| {
            let mut cmd = Command::new("git");
            // Prevent caller repository/index/config variables from redirecting
            // this disposable fixture into the canonical checkout.
            for (key, _) in std::env::vars_os() {
                if key
                    .to_string_lossy()
                    .to_ascii_uppercase()
                    .starts_with("GIT_")
                {
                    cmd.env_remove(key);
                }
            }
            cmd.env("GIT_CONFIG_NOSYSTEM", "1").env(
                "GIT_CONFIG_GLOBAL",
                if cfg!(windows) { "NUL" } else { "/dev/null" },
            );
            cmd.current_dir(root.path()).args(args);
            cmd
        };
        let init = git(&["init", "--quiet", &format!("--object-format={format}")])
            .output()
            .unwrap();
        assert!(init.status.success(), "fixture Git initialization failed");
        for content in [b"".as_slice(), b"foo", b"line\r\n\0\xff"] {
            let mut cmd = git(&["hash-object", "--stdin", "--no-filters"]);
            let mut child = cmd
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::piped())
                .spawn()
                .unwrap();
            child.stdin.take().unwrap().write_all(content).unwrap();
            let out = child.wait_with_output().unwrap();
            assert!(out.status.success());
            let mut file = fixture().files.remove(0);
            file.blob = String::from_utf8(out.stdout).unwrap().trim().into();
            file.sha256 = Sha256::digest(content)
                .iter()
                .map(|b| format!("{b:02x}"))
                .collect();
            file.bytes = content.len() as u64;
            file.verify_git_blob(content, format).unwrap();
            assert!(file.verify_git_blob(content, "unknown").is_err());
            file.blob = "0".repeat(file.blob.len());
            assert!(file.verify_git_blob(content, format).is_err());
        }
    }
}

fn fixture() -> LexicalManifest {
    LexicalManifest {
        version: 1,
        source: Source {
            repository_id: "a".repeat(64),
            object_format: "sha1".into(),
            commit: "b".repeat(40),
            tree: "c".repeat(40),
        },
        files: vec![LexicalFile {
            path: "src/a.rs".into(),
            blob: "d".repeat(40),
            sha256: Sha256::digest(b"foo")
                .iter()
                .map(|b| format!("{b:02x}"))
                .collect(),
            bytes: 3,
        }],
    }
}

#[test]
fn exact_bytes_and_source_are_required() {
    let m = fixture();
    m.files[0].verify_bytes(b"foo").unwrap();
    assert!(m.files[0].verify_bytes(b"bar").is_err());
    assert!(m.files[0].verify_bytes(b"foo\n").is_err());
    let mut other = m.source.clone();
    other.commit = "e".repeat(40);
    assert!(m.clone().validate(&other).is_err());
    for path in ["../a", "/a", "a//b", "C:/a", "a\\b", "a/./b", ""] {
        let mut changed = m.clone();
        changed.files[0].path = path.into();
        assert!(changed.validate(&m.source).is_err(), "{path}");
    }
}

#[test]
fn manifest_identity_is_order_independent_and_content_bound() {
    let mut m = fixture();
    let mut second = m.files[0].clone();
    second.path = "src/b.rs".into();
    m.files.push(second);
    let id = m.id().unwrap();
    m.files.reverse();
    assert_eq!(id, m.id().unwrap());
    m.files[0].blob = "e".repeat(40);
    assert_ne!(id, m.id().unwrap());
    m.files.push(m.files[0].clone());
    assert!(m.id().is_err());
}
