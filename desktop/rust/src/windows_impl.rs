//! Windows-only dev-sidecar connect + handshake, plus its tests.
//!
//! Extracted from `main.rs` so the windows-only imports (`NamedPipeClient`,
//! `ClientOptions`, `AsyncReadExt`, `NamedPipeServer`/`ServerOptions` in tests)
//! live next to the code that uses them. A module-level `#[cfg(windows)]` gate
//! (see `main.rs: mod windows_impl`) replaces the per-import `#[cfg]` attributes
//! the inline version needed -- the next windows-only import added here is
//! windows-gated automatically, instead of compiling silently on a macOS dev
//! host and failing on Windows CI (the trap the original extraction hit).

use tokio::net::windows::named_pipe::NamedPipeClient;

use crate::frame::{read_frame_async, write_frame_async};
use crate::proto;
use crate::sidecar_ipc::{
    connect_sidecar_endpoint_async, pipe_runtime, SidecarReader, SidecarWriter, SyncPipeReader,
    SyncPipeWriter,
};
use crate::{
    check_response, get_sidecar_info_request, sidecar_info_from_response,
    DEV_SIDECAR_HANDSHAKE_TIMEOUT,
};

/// The Windows twin of the unix `connect_and_handshake_dev_sidecar`. Runs the
/// handshake inside the named-pipe runtime so `tokio::time::timeout` can register
/// with its timer driver, then splits the client into the sync reader/writer pair
/// the bootstrap hands to its reader/writer threads.
pub(crate) fn connect_and_handshake_dev_sidecar(
    endpoint: &str,
) -> Result<Option<(SidecarReader, SidecarWriter, proto::SidecarInfo)>, String> {
    // `tokio::time::timeout(...)` must be constructed inside a runtime
    // context so its `Sleep` can register with the timer driver, so the
    // async block stays.
    let result = pipe_runtime()
        .block_on(async {
            tokio::time::timeout(
                DEV_SIDECAR_HANDSHAKE_TIMEOUT,
                windows_handshake_async(endpoint),
            )
            .await
        })
        .map_err(|_| {
            format!(
                "named-pipe handshake timed out after {:?}",
                DEV_SIDECAR_HANDSHAKE_TIMEOUT
            )
        })??;
    let (client, info) = match result {
        Some(pair) => pair,
        None => return Ok(None),
    };
    let (r, w) = tokio::io::split(client);
    Ok(Some((
        SyncPipeReader { inner: r },
        SyncPipeWriter { inner: w },
        info,
    )))
}

/// Connects `pipe_name` (through the transport layer so the same-user SID check
/// gates it -- see `connect_sidecar_endpoint_async`) and runs the GetSidecarInfo
/// exchange. Only the handshake itself is async, because it races against
/// `tokio::time::timeout`.
async fn windows_handshake_async(
    pipe_name: &str,
) -> Result<Option<(NamedPipeClient, proto::SidecarInfo)>, String> {
    let mut client = match connect_sidecar_endpoint_async(pipe_name).await? {
        Some(c) => c,
        None => return Ok(None),
    };
    let request = proto::Frame {
        message: Some(proto::frame::Message::Request(proto::Request {
            id: 1,
            method: Some(get_sidecar_info_request()),
        })),
    };
    write_frame_async(&mut client, &request)
        .await
        .map_err(|err| format!("request sidecar info: {err}"))?;
    let frame = read_frame_async(&mut client)
        .await
        .map_err(|err| format!("read sidecar info: {err}"))?;
    let resp = match frame.message {
        Some(proto::frame::Message::Response(resp)) => resp,
        _ => return Err("unexpected frame while reading sidecar info".to_string()),
    };
    let info = sidecar_info_from_response(check_response(resp)?, "get_sidecar_info")?;
    Ok(Some((client, info)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::frame::{
        get_sidecar_info_request, read_frame, read_frame_async, write_frame, write_frame_async,
    };
    use crate::sidecar::write_sidecar_metadata;
    use crate::sidecar_ipc::{
        connect_sidecar_endpoint, dev_sidecar_endpoint, dev_sidecar_metadata_path_in,
        endpoint_holder_pid, is_sidecar_gone, require_peer_sid, require_same_user_pipe_peer,
        sanitize_sid_for_pipe, sidecar_identity,
    };
    use crate::tests::sidecar_info;
    use crate::SIDECAR_PROTOCOL_VERSION;
    use std::fs;
    use std::path::PathBuf;
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::thread;
    use std::time::{Duration, Instant, SystemTime};

    use tokio::io::AsyncReadExt;
    use tokio::net::windows::named_pipe::{ClientOptions, NamedPipeServer, ServerOptions};

    static TEST_COUNTER: AtomicU64 = AtomicU64::new(0);

    fn unique_test_pipe_name() -> String {
        let counter = TEST_COUNTER.fetch_add(1, Ordering::SeqCst);
        let nanos = SystemTime::now()
            .duration_since(SystemTime::UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let pid = std::process::id();
        format!("\\\\.\\pipe\\leapmux-test-{pid}-{nanos}-{counter}")
    }

    // `ServerOptions::create` must run inside the pipe runtime so the new
    // NamedPipeServer registers with the right I/O driver.
    fn start_test_pipe_server(pipe_name: &str) -> NamedPipeServer {
        pipe_runtime().block_on(async {
            ServerOptions::new()
                .first_pipe_instance(true)
                .create(pipe_name)
                .expect("create named pipe server")
        })
    }

    fn spawn_fake_sidecar_pipe(pipe_name: String) -> thread::JoinHandle<()> {
        let server = start_test_pipe_server(&pipe_name);
        thread::spawn(move || {
            pipe_runtime().block_on(async move {
                let mut server = server;
                server.connect().await.expect("connect named pipe");
                let _ = read_frame_async(&mut server)
                    .await
                    .expect("read handshake request");
                let mut info = sidecar_info(proto::SidecarShellMode::Unspecified, false, "");
                info.pid = std::process::id() as i64;
                let response = proto::Frame {
                    message: Some(proto::frame::Message::Response(proto::Response {
                        id: 1,
                        error: String::new(),
                        result: Some(proto::response::Result::SidecarInfo(info)),
                    })),
                };
                write_frame_async(&mut server, &response)
                    .await
                    .expect("write handshake response");
                // Wait for the client to drop the connection.
                let mut scratch = [0u8; 1];
                let _ = server.read(&mut scratch).await;
            });
        })
    }

    #[test]
    fn connect_and_handshake_pipe_returns_sidecar_info() {
        let pipe_name = unique_test_pipe_name();
        let server = spawn_fake_sidecar_pipe(pipe_name.clone());

        let (reader, writer, info) = connect_and_handshake_dev_sidecar(&pipe_name)
            .expect("handshake ok")
            .expect("server present");

        assert_eq!(info.protocol_version, SIDECAR_PROTOCOL_VERSION);
        assert_eq!(info.binary_hash, "test-hash");
        assert_eq!(info.pid as u32, std::process::id());

        drop(reader);
        drop(writer);
        server.join().expect("fake sidecar thread");
    }

    #[test]
    fn connect_returns_none_when_pipe_absent() {
        let pipe_name = unique_test_pipe_name();
        let result = connect_sidecar_endpoint(&pipe_name).expect("no error");
        assert!(
            result.is_none(),
            "expected None for nonexistent pipe, got Some"
        );
    }

    #[test]
    fn handshake_timeout_surfaces_when_server_never_replies() {
        let pipe_name = unique_test_pipe_name();
        // Create the server *before* spawning the accept thread, otherwise
        // the client races and returns Ok(None) (pipe absent) before the
        // timeout fires.
        let server = start_test_pipe_server(&pipe_name);
        let server_thread = thread::spawn(move || {
            pipe_runtime().block_on(async move {
                let _ = server.connect().await;
                tokio::time::sleep(Duration::from_secs(1)).await;
            });
        });

        let start = Instant::now();
        let result = pipe_runtime().block_on(async {
            tokio::time::timeout(
                Duration::from_millis(500),
                windows_handshake_async(&pipe_name),
            )
            .await
        });
        let elapsed = start.elapsed();
        assert!(result.is_err(), "expected handshake to time out");
        assert!(
            elapsed < Duration::from_secs(2),
            "timeout should fire quickly, elapsed {:?}",
            elapsed
        );

        let _ = server_thread.join();
    }

    // Regression test for the FILE_OBJECT-lock deadlock that motivated the
    // tokio overlapped-I/O switch: under the pre-fix synchronous handles +
    // DuplicateHandle setup, the writer's WriteFile would queue behind the
    // reader's in-flight ReadFile on the shared FILE_OBJECT and hang
    // forever.
    #[test]
    fn split_halves_allow_concurrent_read_and_write() {
        let pipe_name = unique_test_pipe_name();
        let server = start_test_pipe_server(&pipe_name);
        let server_thread = thread::spawn(move || {
            pipe_runtime().block_on(async move {
                let mut server = server;
                server.connect().await.expect("server connect");
                let request = read_frame_async(&mut server)
                    .await
                    .expect("server reads request");
                let id = match request.message {
                    Some(proto::frame::Message::Request(r)) => r.id,
                    other => panic!("expected request, got {other:?}"),
                };
                let response = proto::Frame {
                    message: Some(proto::frame::Message::Response(proto::Response {
                        id,
                        error: String::new(),
                        result: Some(proto::response::Result::BoolValue(proto::BoolValue {
                            value: true,
                        })),
                    })),
                };
                write_frame_async(&mut server, &response)
                    .await
                    .expect("server writes response");
                let mut scratch = [0u8; 1];
                let _ = server.read(&mut scratch).await;
            });
        });

        let (mut reader, mut writer) = connect_sidecar_endpoint(&pipe_name)
            .expect("connect ok")
            .expect("server reachable");

        // Park the reader on a blocking read first so the write below is
        // *concurrent* with an in-flight read — the scenario that deadlocked
        // under synchronous handles.
        let (tx, rx) = std::sync::mpsc::channel();
        thread::spawn(move || {
            let result = read_frame(&mut reader);
            let _ = tx.send(result);
        });
        thread::sleep(Duration::from_millis(100));

        let request = proto::Frame {
            message: Some(proto::frame::Message::Request(proto::Request {
                id: 42,
                method: Some(get_sidecar_info_request()),
            })),
        };
        write_frame(&mut writer, &request).expect("client write");

        let response = rx
            .recv_timeout(Duration::from_secs(5))
            .expect("response not received within 5s — deadlock regression?")
            .expect("read frame");
        match response.message {
            Some(proto::frame::Message::Response(r)) => assert_eq!(r.id, 42),
            other => panic!("expected response with id=42, got {other:?}"),
        }

        drop(writer);
        server_thread.join().expect("server thread");
    }

    #[test]
    fn is_sidecar_gone_reports_true_for_absent_pipe() {
        let pipe_name = unique_test_pipe_name();
        assert!(is_sidecar_gone(&pipe_name));
    }

    #[test]
    fn is_sidecar_gone_reports_false_when_server_listening() {
        let pipe_name = unique_test_pipe_name();
        let _server = start_test_pipe_server(&pipe_name);
        assert!(!is_sidecar_gone(&pipe_name));
    }

    // ---- Windows peer-identity check (the connect-side half of the boundary
    // requirePrivateDir / userOnlySDDL defend on the bind side). Mirrors
    // require_peer_uid_refuses_a_foreign_owner on Unix. ----

    // Two minimal valid SIDs that differ only in their last sub-authority byte.
    // S-1-5-32 (BUILTIN) and S-1-5-42 -- both 12 bytes, both well-formed, so
    // EqualSid's behaviour is defined and FALSE rather than undefined.
    const FOREIGN_SID_A: [u8; 12] = [
        0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x20, 0x00, 0x00, 0x00,
    ];
    const FOREIGN_SID_B: [u8; 12] = [
        0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x2a, 0x00, 0x00, 0x00,
    ];

    #[test]
    fn require_peer_sid_accepts_same_sid() {
        require_peer_sid(&FOREIGN_SID_A, &FOREIGN_SID_A, "\\\\?\\pipe\\test")
            .expect("same SID is accepted");
    }

    #[test]
    fn require_peer_sid_refuses_a_foreign_sid() {
        let err = require_peer_sid(&FOREIGN_SID_A, &FOREIGN_SID_B, "\\\\?\\pipe\\test")
            .expect_err("a pipe answered by another SID must be refused");
        assert!(err.contains("refusing sidecar"), "{err}");
        assert!(
            err.contains("\\\\?\\pipe\\test"),
            "names the endpoint: {err}"
        );
    }

    // The pipe server is in this process, so the peer is us. Same shape as
    // require_same_user_peer_accepts_our_own_socket on Unix.
    #[test]
    fn require_same_user_pipe_peer_accepts_our_own_pipe() {
        let pipe_name = unique_test_pipe_name();
        let _server = start_test_pipe_server(&pipe_name);
        let client = pipe_runtime().block_on(async {
            ClientOptions::new()
                .open(&pipe_name)
                .expect("open named pipe client")
        });
        require_same_user_pipe_peer(&client, &pipe_name).expect("our own pipe is accepted");
    }

    // endpoint_holder_pid now reports the kernel-recorded server pid on Windows
    // (GetNamedPipeServerProcessId), retiring the previous unconditional None.
    #[test]
    fn endpoint_holder_pid_reports_the_kernel_recorded_holder() {
        let pipe_name = unique_test_pipe_name();
        let _server = start_test_pipe_server(&pipe_name);
        assert_eq!(
            endpoint_holder_pid(&pipe_name),
            Some(std::process::id()),
            "the holder of our own pipe is this process",
        );
    }

    #[test]
    fn endpoint_holder_pid_is_none_for_an_absent_endpoint() {
        let pipe_name = unique_test_pipe_name();
        assert_eq!(endpoint_holder_pid(&pipe_name), None);
    }

    #[test]
    fn sanitize_sid_for_pipe_replaces_forbidden_chars() {
        // A real Windows SID is preserved verbatim.
        assert_eq!(
            sanitize_sid_for_pipe("S-1-5-21-1234567890-1234567890-1234567890-1001"),
            "S-1-5-21-1234567890-1234567890-1234567890-1001"
        );
        // Forbidden chars become `_`.
        assert_eq!(
            sanitize_sid_for_pipe("alice/bob\\carol\\@dave"),
            "alice_bob_carol__dave"
        );
        // Whitespace, dot, and other punctuation all map to `_`.
        assert_eq!(sanitize_sid_for_pipe("a b.c:d"), "a_b_c_d");
        // Hyphen and ASCII alphanumerics are preserved.
        assert_eq!(sanitize_sid_for_pipe("Abc-123-XYZ"), "Abc-123-XYZ");
        // Non-ASCII becomes `_` (one per char).
        assert_eq!(sanitize_sid_for_pipe("zoé"), "zo_");
    }

    #[test]
    fn dev_sidecar_endpoint_uses_full_pipe_format() {
        // Pin the full endpoint string, including the variable identity, so a
        // regression in prefix/suffix or identity placement is caught.
        let identity = sidecar_identity().expect("sidecar_identity");
        let expected = format!("\\\\.\\pipe\\leapmux-desktop-{identity}-sidecar");
        let actual = dev_sidecar_endpoint().expect("endpoint");
        assert_eq!(actual, expected);
    }

    #[test]
    fn dev_sidecar_metadata_path_joins_base_with_subdir_and_file() {
        // Drive the path builder with a known base so the full result is
        // pinned, not just the trailing components.
        let base = PathBuf::from("C:\\Users\\alice\\AppData\\Local");
        let path = dev_sidecar_metadata_path_in(&base);
        assert_eq!(
            path,
            PathBuf::from("C:\\Users\\alice\\AppData\\Local")
                .join("leapmux-desktop")
                .join("sidecar.json")
        );
    }

    #[test]
    fn write_sidecar_metadata_roundtrips_json() {
        let counter = TEST_COUNTER.fetch_add(1, Ordering::SeqCst);
        let path = std::env::temp_dir().join(format!("leapmux-test-metadata-{counter}.json"));
        let _ = fs::remove_file(&path);

        write_sidecar_metadata(&path, "\\\\.\\pipe\\test", "hash-abc").expect("write metadata");
        let data = fs::read_to_string(&path).expect("read metadata");
        assert!(data.contains("\\\\\\\\.\\\\pipe\\\\test"));
        assert!(data.contains("\"binary_hash\": \"hash-abc\""));
        assert!(data.contains(&format!(
            "\"protocol_version\": \"{SIDECAR_PROTOCOL_VERSION}\""
        )));

        let _ = fs::remove_file(&path);
    }
}
