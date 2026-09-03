//! Sidecar process bootstrap, connect, handshake, and shutdown.
//!
//! Distinct from `sidecar_ipc` (the kernel transport: unix socket / named pipe)
//! and from `frame` (the wire codec): this module is the layer ABOVE both -- it
//! spawns the sidecar process (or reuses a live dev one), drives the
//! GetSidecarInfo handshake over the transport, writes the dev-sidecar metadata
//! record, and asks a stale peer to shut down. The live runtime handle
//! (`SidecarProcess` + reader/writer threads) lives in `main.rs`'s `DesktopShell`;
//! this module only produces the `SidecarBootstrap` (child + reader/writer halves)
//! the shell installs.
//!
//! Extracted from `main.rs`.

use std::fs;
use std::io::{BufRead, BufReader, BufWriter, Read, Write};
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

use sha2::{Digest, Sha256};

use crate::frame::{read_frame, write_frame};
use crate::proto;
use crate::sidecar_ipc::{
    cleanup_dev_sidecar_artifacts, connect_sidecar_endpoint, dev_sidecar_endpoint,
    dev_sidecar_metadata_path, endpoint_holder_pid, is_sidecar_gone, private_dev_sidecar_endpoint,
    restrict_dir_permissions, restrict_file_permissions,
};
#[cfg(unix)]
use crate::sidecar_ipc::{finalize_sidecar_streams, SidecarReader, SidecarWriter};
use crate::{
    SidecarMetadata, DEV_SIDECAR_CONNECT_TIMEOUT, DEV_SIDECAR_SHUTDOWN_TIMEOUT, ENV_BINARY_HASH,
    ENV_DEV_ENDPOINT, SIDECAR_PROTOCOL_VERSION,
};
// `get_sidecar_info_request` / `check_response` / `sidecar_info_from_response`
// back the unix `fetch_sidecar_info` handshake; the Windows twin lives in
// `windows_impl`, so these are unix-only here.
#[cfg(unix)]
use crate::{check_response, frame::get_sidecar_info_request, sidecar_info_from_response};
// The Windows handshake twin (overlapped-I/O named pipe, raced against a timeout)
// lives in `windows_impl`; `main.rs` re-exports it at the crate root for every
// caller including this module.
#[cfg(windows)]
use crate::connect_and_handshake_dev_sidecar;

/// The freshly-spawned (or reused) sidecar a `DesktopShell` installs: the child
/// process (owned, when we spawned it) plus the reader/writer halves of the
/// transport the reader/writer threads will own.
pub(crate) struct SidecarBootstrap {
    pub(crate) child: Option<Child>,
    pub(crate) reader: Box<dyn Read + Send>,
    pub(crate) writer: Box<dyn Write + Send>,
}

/// Spawn (dev) or spawn-via-stdio (release) the sidecar, returning its bootstrap.
pub(crate) fn bootstrap_sidecar(sidecar_path: &Path) -> Result<SidecarBootstrap, String> {
    #[cfg(any(unix, windows))]
    if cfg!(debug_assertions) {
        return bootstrap_dev_sidecar(sidecar_path);
    }
    spawn_stdio_sidecar(sidecar_path)
}

// bootstrap_dev_sidecar tries to reuse a live dev sidecar at the well-known
// endpoint (unix socket on Unix, named pipe on Windows) and falls back to
// spawning a fresh one when the endpoint is stale, incompatible, or missing.
#[cfg(any(unix, windows))]
fn bootstrap_dev_sidecar(sidecar_path: &Path) -> Result<SidecarBootstrap, String> {
    #[cfg(unix)]
    let (endpoint, private_endpoint) = (dev_sidecar_endpoint(), private_dev_sidecar_endpoint());
    #[cfg(windows)]
    let (endpoint, private_endpoint) = (dev_sidecar_endpoint()?, private_dev_sidecar_endpoint()?);
    let metadata_path = dev_sidecar_metadata_path();
    let binary_hash = hash_sidecar_binary(sidecar_path)?;

    // Whatever is already on the endpoint decides how we proceed. The one thing we
    // never do is kill it: on this path the peer is by definition NOT our child, so
    // the only PID available would be the one it reports about itself -- the
    // arbitrary-process-kill primitive `force_kill_sidecar` used to be. When we cannot
    // reclaim the endpoint we move to a private one instead, so a stale or foreign
    // holder is routed around rather than blocking the launch.
    let mut endpoint = endpoint;
    match try_connect_dev_sidecar(&endpoint) {
        Ok(Some((reader, writer, info)))
            if info.protocol_version == SIDECAR_PROTOCOL_VERSION
                && info.binary_hash == binary_hash =>
        {
            write_sidecar_metadata(&metadata_path, &endpoint, &binary_hash)?;
            return Ok(SidecarBootstrap {
                child: None,
                reader,
                writer,
            });
        }
        // Ours, but a stale build or protocol. Ask it to go; if it ignores us, leave
        // it holding the path.
        Ok(Some(_)) => {
            if !request_sidecar_shutdown(&endpoint) {
                endpoint = private_endpoint;
            }
        }
        // Nothing is listening: the endpoint is ours to take.
        Ok(None) => {}
        // Unreachable or answered by another user (see require_same_user_peer). Their
        // socket is not ours to unlink -- /tmp is sticky -- so binding here would just
        // fail. Take a private endpoint.
        Err(err) => {
            crate::shell_log!("cannot use dev sidecar endpoint {endpoint}: {err}");
            endpoint = private_endpoint;
        }
    }
    cleanup_dev_sidecar_artifacts(&endpoint, &metadata_path);

    let mut command = Command::new(sidecar_path);
    command
        .env(ENV_DEV_ENDPOINT, &endpoint)
        .env(ENV_BINARY_HASH, &binary_hash)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::inherit());
    let child = command
        .spawn()
        .map_err(|err| format!("spawn desktop sidecar: {err}"))?;

    let start = Instant::now();
    loop {
        match try_connect_dev_sidecar(&endpoint) {
            Ok(Some((reader, writer, info))) => {
                if info.protocol_version != *SIDECAR_PROTOCOL_VERSION {
                    return Err(format!(
                        "unexpected sidecar protocol version: {}",
                        info.protocol_version,
                    ));
                }
                if info.binary_hash != binary_hash {
                    return Err("spawned sidecar reported an unexpected binary hash".to_string());
                }
                write_sidecar_metadata(&metadata_path, &endpoint, &binary_hash)?;
                return Ok(SidecarBootstrap {
                    child: Some(child),
                    reader,
                    writer,
                });
            }
            Ok(None) => {}
            Err(err) => return Err(err),
        }

        if start.elapsed() > DEV_SIDECAR_CONNECT_TIMEOUT {
            return Err("timed out waiting for desktop sidecar endpoint".to_string());
        }
        thread::sleep(Duration::from_millis(100));
    }
}

fn spawn_stdio_sidecar(sidecar_path: &Path) -> Result<SidecarBootstrap, String> {
    let mut command = Command::new(sidecar_path);
    command
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = command
        .spawn()
        .map_err(|err| format!("spawn desktop sidecar: {err}"))?;
    let stdin = child
        .stdin
        .take()
        .ok_or_else(|| "desktop sidecar stdin unavailable".to_string())?;
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| "desktop sidecar stdout unavailable".to_string())?;
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| "desktop sidecar stderr unavailable".to_string())?;
    start_sidecar_stderr_thread(stderr);
    Ok(SidecarBootstrap {
        child: Some(child),
        reader: Box::new(stdout),
        writer: Box::new(BufWriter::new(stdin)),
    })
}

fn start_sidecar_stderr_thread(stderr: impl Read + Send + 'static) {
    thread::spawn(move || {
        let reader = BufReader::new(stderr);
        for line in reader.lines().map_while(Result::ok) {
            // The one `eprintln!` that `shell_log!` must not replace. This
            // forwards what the SIDECAR wrote, so the prefix marks another
            // program's output and reading it as a shell line would misattribute
            // every sidecar error.
            eprintln!("desktop-sidecar: {line}");
        }
    });
}

type DevSidecarConnection = (
    Box<dyn Read + Send>,
    Box<dyn Write + Send>,
    proto::SidecarInfo,
);

fn try_connect_dev_sidecar(endpoint: &str) -> Result<Option<DevSidecarConnection>, String> {
    match connect_and_handshake_dev_sidecar(endpoint)? {
        Some((reader, writer, info)) => Ok(Some((
            Box::new(reader),
            Box::new(BufWriter::new(writer)),
            info,
        ))),
        None => Ok(None),
    }
}

// The unix handshake runs the GetSidecarInfo exchange synchronously over the
// connected socket. The Windows twin (overlapped-I/O named pipe, raced against
// a handshake timeout) lives in `windows_impl` and is re-exported into the
// crate root for this module to call.
#[cfg(unix)]
pub(crate) fn connect_and_handshake_dev_sidecar(
    endpoint: &str,
) -> Result<Option<(SidecarReader, SidecarWriter, proto::SidecarInfo)>, String> {
    let (mut reader, mut writer) = match connect_sidecar_endpoint(endpoint)? {
        Some(pair) => pair,
        None => return Ok(None),
    };
    let info = fetch_sidecar_info(&mut reader, &mut writer)?;
    finalize_sidecar_streams(&reader, &writer)?;
    Ok(Some((reader, writer, info)))
}

/// Asks whatever is listening on `endpoint` to shut down, and reports whether it
/// actually went away.
///
/// It deliberately does NOT force-kill. This runs on the path where the peer
/// reported a protocol/binary mismatch, which means it is emphatically *not* this
/// shell's child -- so the only PID available is the one the peer reported over the
/// socket about itself. Trusting that made this an arbitrary-process-kill
/// primitive running at the developer's uid: anything able to answer on the
/// endpoint (the dev socket lives at a predictable path) could name any PID it
/// liked and have the shell SIGTERM then SIGKILL it. A stale sidecar that ignores
/// a cooperative shutdown is a dev-box annoyance; killing an arbitrary process is
/// not an acceptable way to resolve it.
fn request_sidecar_shutdown(endpoint: &str) -> bool {
    if let Ok(Some((mut reader, mut writer))) = connect_sidecar_endpoint(endpoint) {
        let frame = proto::Frame {
            message: Some(proto::frame::Message::Request(proto::Request {
                id: 1,
                method: Some(proto::request::Method::Shutdown(proto::ShutdownRequest {})),
            })),
        };
        let _ = write_frame(&mut writer, &frame);
        let _ = read_frame(&mut reader);
    }

    let deadline = Instant::now() + DEV_SIDECAR_SHUTDOWN_TIMEOUT;
    while Instant::now() < deadline {
        if is_sidecar_gone(endpoint) {
            return true;
        }
        thread::sleep(Duration::from_millis(100));
    }

    // Name the KERNEL-verified holder pid so the developer can stop it by hand.
    // We deliberately do NOT kill it: adopting/killing on a self-reported
    // identity is the arbitrary-process-kill primitive this shell gave up (see
    // above). The kernel pid is trustworthy (unlike a wire-reported one) but is
    // used only to make this message actionable.
    let holder = match endpoint_holder_pid(endpoint) {
        Some(pid) => format!("process {pid}"),
        None => "an unidentified process".to_string(),
    };
    crate::shell_log!(
        "a sidecar ({holder}) is holding {endpoint} and did not shut down \
         when asked; starting on a private endpoint instead. Stop it manually if it \
         should not be running -- in SOLO mode it also holds the shared DB's runtime \
         lease, so ConnectSolo will keep failing until it is gone (a launcher-mode \
         orphan only costs the sidecar-reuse optimisation)."
    );
    false
}

#[cfg(unix)]
fn fetch_sidecar_info(
    reader: &mut impl Read,
    writer: &mut impl Write,
) -> Result<proto::SidecarInfo, String> {
    let frame = proto::Frame {
        message: Some(proto::frame::Message::Request(proto::Request {
            id: 1,
            method: Some(get_sidecar_info_request()),
        })),
    };
    write_frame(writer, &frame).map_err(|err| format!("request sidecar info: {err}"))?;
    let frame = read_frame(reader).map_err(|err| format!("read sidecar info: {err}"))?;
    let resp = match frame.message {
        Some(proto::frame::Message::Response(resp)) => resp,
        _ => return Err("unexpected frame while reading sidecar info".to_string()),
    };
    sidecar_info_from_response(check_response(resp)?, "get_sidecar_info")
}

pub(crate) fn write_sidecar_metadata(
    metadata_path: &Path,
    endpoint: &str,
    binary_hash: &str,
) -> Result<(), String> {
    let metadata = SidecarMetadata {
        endpoint: endpoint.to_string(),
        binary_hash: binary_hash.to_string(),
        protocol_version: SIDECAR_PROTOCOL_VERSION.to_string(),
    };
    if let Some(parent) = metadata_path.parent() {
        fs::create_dir_all(parent).map_err(|err| format!("create sidecar metadata dir: {err}"))?;
        restrict_dir_permissions(parent)?;
    }
    let data = serde_json::to_vec_pretty(&metadata)
        .map_err(|err| format!("serialize sidecar metadata: {err}"))?;
    fs::write(metadata_path, data).map_err(|err| format!("write sidecar metadata: {err}"))?;
    restrict_file_permissions(metadata_path)?;
    Ok(())
}

fn hash_sidecar_binary(sidecar_path: &Path) -> Result<String, String> {
    let file = fs::File::open(sidecar_path)
        .map_err(|err| format!("read desktop sidecar binary: {err}"))?;
    let mut reader = BufReader::new(file);
    let mut hasher = Sha256::new();
    let mut buf = [0u8; 64 * 1024];
    loop {
        let n = reader
            .read(&mut buf)
            .map_err(|err| format!("read desktop sidecar binary: {err}"))?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    let digest = hasher.finalize();
    Ok(digest.iter().map(|b| format!("{:02x}", b)).collect())
}
