//! Persistent frame boundaries, correlated failures and actual process liveness.
use engorch_ri::{canonical, transport};
use serde_json::{Value, json};
use std::io::{Read, Write};

fn frame(id: u32, request: Value) -> Vec<u8> {
    let bytes = canonical::encode(&json!({"id":id,"request":request})).unwrap();
    let mut result = (bytes.len() as u32).to_be_bytes().to_vec();
    result.extend(bytes);
    result
}

#[test]
fn invalid_frames_never_dispatch_and_sequence_cannot_repeat() {
    for bytes in [
        vec![0],
        vec![0, 0, 0, 0],
        (canonical::MAX_BYTES as u32 + 1).to_be_bytes().to_vec(),
        frame(2, json!({})),
    ] {
        let mut called = false;
        let mut output = Vec::new();
        assert!(
            transport::serve(bytes.as_slice(), &mut output, |_| {
                called = true;
                Ok(json!({}))
            })
            .is_err()
        );
        assert!(!called);
        assert!(output.is_empty());
    }
    let mut bytes = frame(1, json!({}));
    bytes.extend(frame(1, json!({})));
    let mut calls = 0;
    assert!(
        transport::serve(bytes.as_slice(), Vec::new(), |_| {
            calls += 1;
            Ok(json!({}))
        })
        .is_err()
    );
    assert_eq!(calls, 1);
}

#[test]
fn actual_process_responds_before_eof_and_continues_after_operation_error() {
    use std::{
        process::{Command, Stdio},
        sync::mpsc,
        time::Duration,
    };
    let mut child = Command::new(env!("CARGO_BIN_EXE_engorch-ri"))
        .arg("--stdio-stream")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .spawn()
        .unwrap();
    let mut input = child.stdin.take().unwrap();
    let mut output = child.stdout.take().unwrap();
    let (send, receive) = mpsc::channel();
    let reader = std::thread::spawn(move || {
        loop {
            let mut header = [0; 4];
            if output.read_exact(&mut header).is_err() {
                break;
            }
            let size = u32::from_be_bytes(header) as usize;
            assert!(size <= canonical::MAX_BYTES);
            let mut bytes = vec![0; size];
            output.read_exact(&mut bytes).unwrap();
            let value: Value = canonical::decode(&bytes).unwrap();
            if send.send(value).is_err() {
                break;
            }
        }
    });
    let source = json!({"repository_id":"a".repeat(64),"object_format":"sha1","commit":"b".repeat(40),"tree":"c".repeat(40)});
    let request = json!({"operation":"lexical_manifest","source":source,"manifest":{"version":1,"source":source,"files":[]}});
    for id in 1..=3 {
        let current = if id == 2 {
            json!({"operation":"not_supported"})
        } else {
            request.clone()
        };
        input.write_all(&frame(id, current)).unwrap();
        input.flush().unwrap();
        let response = match receive.recv_timeout(Duration::from_secs(10)) {
            Ok(value) => value,
            Err(error) => {
                let _ = child.kill();
                let _ = child.wait();
                panic!("no response before EOF: {error}");
            }
        };
        assert_eq!(response["id"], id);
        assert_eq!(response["ok"], id != 2);
        assert!(child.try_wait().unwrap().is_none());
    }
    drop(input);
    assert!(child.wait().unwrap().success());
    reader.join().unwrap();
}
