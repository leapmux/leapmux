#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

#[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
compile_error!("LeapMux Desktop only supports macOS, Linux, and Windows");

mod proto {
    include!(concat!(env!("OUT_DIR"), "/leapmux.desktop.v1.rs"));
}

#[cfg(target_os = "linux")]
mod tabfix_linux;

use base64::Engine;
use prost::Message;
use serde::{Deserialize, Serialize};
use serde_json::json;
use sha2::{Digest, Sha256};
#[cfg(unix)]
use std::os::unix::{fs::PermissionsExt, net::UnixStream};
use std::{
    collections::{HashMap, HashSet},
    ffi::OsStr,
    fs,
    io::{self, BufReader, BufWriter, Read, Write},
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        Arc, Mutex,
    },
    thread,
    time::{Duration, Instant},
};
#[cfg(target_os = "macos")]
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem, Submenu, HELP_SUBMENU_ID};
use tauri::{
    AppHandle, Emitter, Manager, RunEvent, State, Url, WebviewWindow, Window, WindowEvent,
};
use tokio::sync::{mpsc, oneshot};

#[cfg(target_os = "macos")]
const APP_SUBMENU_ID: &str = "leapmux-app-menu";
#[cfg(target_os = "macos")]
const SHOW_ABOUT_MENU_ID: &str = "show-about";
#[cfg(target_os = "macos")]
const SHOW_PREFERENCES_MENU_ID: &str = "show-preferences";
#[cfg(target_os = "macos")]
const OPEN_WEB_INSPECTOR_MENU_ID: &str = "open-web-inspector";
// Must stay in sync with maxFrameSize in desktop/go/frame.go: it must exceed the
// 16 MiB org-events read limit plus its Frame/Event envelope so a full-size
// OrgMaterialized bootstrap is not rejected on read.
const MAX_FRAME_SIZE: u64 = 20 * 1024 * 1024; // 20 MiB
                                              // A base-128 varint carries 7 bits per byte, so a u64 length prefix needs at
                                              // most 10 bytes. A reader that has consumed this many without seeing a
                                              // terminating byte is being fed a malformed (or malicious) prefix.
const MAX_VARINT_BYTES: usize = 10;
const SIDECAR_PROTOCOL_VERSION: &str = "1";
const DEV_SIDECAR_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(30);
// CONNECT_TIMEOUT is the outer loop budget for "endpoint reachable +
// handshake succeeds"; HANDSHAKE_TIMEOUT bounds a single attempt. Keep
// CONNECT meaningfully larger so the loop can retry after a wedged probe.
const DEV_SIDECAR_CONNECT_TIMEOUT: Duration = Duration::from_secs(60);
const DEV_SIDECAR_HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(30);
const SIDECAR_INITIAL_HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(30);

// Cross-language env-var contract; see desktop/go/main.go for semantics.
const ENV_DEV_ENDPOINT: &str = "LEAPMUX_DESKTOP_DEV_ENDPOINT";
const ENV_BINARY_HASH: &str = "LEAPMUX_DESKTOP_BINARY_HASH";

/// The shell's own record of the dev sidecar it last bootstrapped, written for
/// human/debug inspection and read back by nothing.
///
/// It deliberately carries NO pid. The only code that ever read one was
/// `force_kill_sidecar`, which this shell no longer has: on the adopt path the pid
/// could only ever be the one the PEER reported about itself over a predictable
/// socket, which made "kill the pid in the metadata" an arbitrary-process-kill
/// primitive at the developer's uid. The adopt path also has no child to ask, so
/// there is no honest value to record there -- and a field nothing reads cannot be
/// misused, whereas one that merely LOOKS authoritative invites the next reader to
/// trust it.
#[derive(Serialize)]
struct SidecarMetadata {
    endpoint: String,
    binary_hash: String,
    protocol_version: String,
}

// --- Frame read/write utilities ---
//
// The sync and async halves below are two transports over ONE wire format: the
// sync half drives stdio (and, on Windows, the named pipe via SyncPipeReader/
// SyncPipeWriter), the async half drives tokio streams. Every decision that is
// part of the FORMAT -- the encoding, the size cap, the decode error mapping,
// the varint state machine -- lives in the shared helpers here and is called by
// both, so the twins can only ever differ in their I/O loop. See the
// `#[cfg(any(windows, test))]` gates on the async half: `test` is what makes
// them compile and run off Windows, so drift fails CI in front of its author
// instead of on the one OS nobody built.
//
// This section is already self-delimited by this banner and has no dependency
// beyond `mod proto` and the two consts above -- the proposed first extraction
// out of this 4000+-line file into its own frame.rs module. See
// https://github.com/leapmux/leapmux/issues/282.

/// Encodes `frame` into a length-delimited buffer ready to hand to a writer.
fn encode_frame(frame: &proto::Frame) -> io::Result<Vec<u8>> {
    let mut buf = Vec::with_capacity(frame.encoded_len() + MAX_VARINT_BYTES);
    frame.encode_length_delimited(&mut buf).map_err(|err| {
        io::Error::new(io::ErrorKind::InvalidData, format!("encode frame: {err}"))
    })?;
    Ok(buf)
}

/// Checks a decoded length prefix against `MAX_FRAME_SIZE` and narrows it to a
/// payload length.
///
/// Callers MUST call this BEFORE allocating the payload buffer: rejecting the
/// size is what stops a peer from making us allocate gigabytes off a bogus
/// varint, and a check that runs after the allocation protects nothing. The cap
/// is also what makes the `as usize` narrowing lossless on every target we
/// build for.
fn frame_len(size: u64) -> io::Result<usize> {
    if size > MAX_FRAME_SIZE {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("frame too large: {size} bytes (max {MAX_FRAME_SIZE})"),
        ));
    }
    Ok(size as usize)
}

/// Decodes a frame body -- the bytes AFTER the length prefix, exactly
/// `frame_len` of them.
fn decode_frame(data: &[u8]) -> io::Result<proto::Frame> {
    proto::Frame::decode(data)
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, format!("decode frame: {err}")))
}

/// Folds one wire byte into an in-progress varint decode.
///
/// Returns `Some(value)` when `b` terminates the varint (high bit clear), and
/// `None` when more bytes are needed -- so a reader keeps only its own read
/// loop and shares the state machine.
fn varint_step(x: &mut u64, s: &mut u32, b: u8) -> Option<u64> {
    if b < 0x80 {
        return Some(*x | (b as u64) << *s);
    }
    *x |= ((b & 0x7f) as u64) << *s;
    *s += 7;
    None
}

/// The error a reader returns once a varint has run past `MAX_VARINT_BYTES`
/// without terminating.
fn varint_overflow() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, "varint overflow")
}

fn write_frame(w: &mut impl Write, frame: &proto::Frame) -> io::Result<()> {
    w.write_all(&encode_frame(frame)?)?;
    w.flush()
}

// Note: prost's `decode_length_delimited` requires an in-memory `Buf`, not
// an `io::Read` stream. For streaming stdio reads we must manually decode the
// varint length prefix, then `read_exact` the payload before decoding.
fn read_frame(r: &mut impl Read) -> io::Result<proto::Frame> {
    let len = frame_len(read_varint(r)?)?;
    let mut data = vec![0u8; len];
    r.read_exact(&mut data)?;
    decode_frame(&data)
}

fn read_varint(r: &mut impl Read) -> io::Result<u64> {
    let mut x: u64 = 0;
    let mut s: u32 = 0;
    let mut buf = [0u8; 1];
    for _ in 0..MAX_VARINT_BYTES {
        r.read_exact(&mut buf)?;
        if let Some(v) = varint_step(&mut x, &mut s, buf[0]) {
            return Ok(v);
        }
    }
    Err(varint_overflow())
}

// --- Tauri types ---

#[derive(Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
enum PlatformMode {
    Web,
    TauriDesktopSolo,
    TauriDesktopDistributed,
    TauriMobileDistributed,
}

#[derive(Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
enum HubTransport {
    Direct,
    Proxy,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
enum ShellMode {
    Launcher,
    Solo,
    Distributed,
}

#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct PlatformCapabilities {
    mode: PlatformMode,
    hub_transport: HubTransport,
    tunnels: bool,
    app_control: bool,
    window_control: bool,
    system_permissions: bool,
    local_solo: bool,
}

#[derive(Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct RuntimeState {
    shell_mode: ShellMode,
    connected: bool,
    hub_url: String,
    capabilities: PlatformCapabilities,
}

#[derive(Serialize)]
struct StartupInfoResponse {
    config: DesktopConfigResponse,
    build_info: BuildInfoResponse,
}

#[derive(Serialize)]
struct DesktopConfigResponse {
    mode: String,
    hub_url: String,
    window_width: i32,
    window_height: i32,
    window_mode: String,
}

#[derive(Serialize)]
struct BuildInfoResponse {
    version: String,
    commit_hash: String,
    commit_time: String,
    build_time: String,
    branch: String,
}

#[derive(Serialize)]
struct ProxyHttpResponsePayload {
    status: i32,
    headers: HashMap<String, Vec<String>>,
    body: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct TunnelInfoResponse {
    id: String,
    worker_id: String,
    r#type: String,
    bind_addr: String,
    bind_port: i32,
    target_addr: String,
    target_port: i32,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct TunnelConfigInput {
    worker_id: String,
    r#type: String,
    target_addr: String,
    target_port: i32,
    bind_addr: String,
    bind_port: i32,
}

// --- Sidecar process ---

type PendingResponse = oneshot::Sender<Result<proto::Response, String>>;
// Every `.lock().unwrap()` on this map (and on DesktopShell.state /
// SaveStreamRegistry.handles below) panics on every subsequent access once a
// panic-while-holding-the-guard poisons it -- there is no PoisonError handling
// or parking_lot in this crate. Currently low-severity (all critical sections
// here are shallow map/field ops), but worth one consistent policy across all
// production sites rather than an ad-hoc fix per site. See
// https://github.com/leapmux/leapmux/issues/277.
type PendingMap = Arc<Mutex<HashMap<u64, PendingResponse>>>;

#[derive(Clone)]
struct ShellState {
    shell_mode: ShellMode,
    connected: bool,
    hub_url: String,
    local_app_url: String,
}

struct SidecarProcess {
    _child: Option<Child>,
    writer_tx: mpsc::UnboundedSender<proto::Frame>,
    pending: PendingMap,
    next_id: AtomicU64,
}

struct DesktopShell {
    app_handle: AppHandle,
    sidecar: SidecarProcess,
    close_in_progress: AtomicBool,
    exit_in_progress: AtomicBool,
    webview_zoom: AtomicU64,
    // Poisoning wedges every command touching shell state permanently; see the
    // PendingMap comment above and https://github.com/leapmux/leapmux/issues/277.
    state: Mutex<ShellState>,
}

struct SidecarBootstrap {
    child: Option<Child>,
    reader: Box<dyn Read + Send>,
    writer: Box<dyn Write + Send>,
}

fn start_sidecar_reader_thread(
    app_handle: AppHandle,
    pending: PendingMap,
    reader: Box<dyn Read + Send>,
) {
    thread::spawn(move || {
        let mut reader = BufReader::new(reader);
        loop {
            match read_frame(&mut reader) {
                Ok(frame) => handle_sidecar_frame(&app_handle, &pending, frame),
                Err(err) => {
                    if err.kind() != io::ErrorKind::UnexpectedEof {
                        eprintln!("sidecar frame read error: {err}");
                    }
                    pending.lock().unwrap().clear();
                    break;
                }
            }
        }
    });
}

// Owns the writer end of the sidecar stream on a dedicated OS thread so
// async invoke handlers never block a Tokio worker on pipe I/O, and so
// concurrent senders serialize implicitly through the channel rather than
// contending on a Mutex.
//
// The channel is unbounded. Practical depth is bounded by the number of
// in-flight RPCs — each caller holds a pending oneshot while awaiting a
// response — and the writer drains a local pipe much faster than callers
// enqueue frames, so the channel stays near-empty in the steady state.
fn start_sidecar_writer_thread(
    writer: Box<dyn Write + Send>,
    pending: PendingMap,
) -> mpsc::UnboundedSender<proto::Frame> {
    let (tx, mut rx) = mpsc::unbounded_channel::<proto::Frame>();
    thread::spawn(move || {
        let mut writer = writer;
        while let Some(frame) = rx.blocking_recv() {
            if let Err(err) = write_frame(&mut writer, &frame) {
                eprintln!("sidecar frame write error: {err}");
                break;
            }
        }
        // Drop in-flight callers so their oneshot receivers resolve with
        // an error instead of hanging when the peer goes away.
        pending.lock().unwrap().clear();
    });
    tx
}

fn bootstrap_sidecar(sidecar_path: &Path) -> Result<SidecarBootstrap, String> {
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
            eprintln!("leapmux: cannot use dev sidecar endpoint {endpoint}: {err}");
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
                if info.protocol_version != SIDECAR_PROTOCOL_VERSION {
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
        use io::BufRead;
        for line in reader.lines().map_while(Result::ok) {
            eprintln!("desktop-sidecar: {line}");
        }
    });
}

type DevSidecarConnection = (
    Box<dyn Read + Send>,
    Box<dyn Write + Send>,
    proto::SidecarInfo,
);

#[cfg(unix)]
type SidecarReader = UnixStream;
#[cfg(unix)]
type SidecarWriter = UnixStream;

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

#[cfg(unix)]
fn connect_and_handshake_dev_sidecar(
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

#[cfg(windows)]
fn connect_and_handshake_dev_sidecar(
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

#[cfg(windows)]
async fn windows_handshake_async(
    pipe_name: &str,
) -> Result<Option<(NamedPipeClient, proto::SidecarInfo)>, String> {
    let mut client = match open_named_pipe_client(pipe_name).await? {
        PipeConnect::Connected(c) => c,
        PipeConnect::NotFound => return Ok(None),
        PipeConnect::Busy => {
            return Err(format!("named pipe {pipe_name} is busy (sidecar alive)"));
        }
    };
    let request = proto::Frame {
        message: Some(proto::frame::Message::Request(proto::Request {
            id: 1,
            method: Some(proto::request::Method::GetSidecarInfo(
                proto::GetSidecarInfoRequest {},
            )),
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

#[cfg(any(windows, test))]
async fn write_frame_async<W: tokio::io::AsyncWrite + Unpin>(
    w: &mut W,
    frame: &proto::Frame,
) -> io::Result<()> {
    w.write_all(&encode_frame(frame)?).await?;
    w.flush().await
}

#[cfg(any(windows, test))]
async fn read_frame_async<R: tokio::io::AsyncRead + Unpin>(r: &mut R) -> io::Result<proto::Frame> {
    // frame_len rejects an oversize prefix before the vec! below allocates.
    let len = frame_len(read_varint_async(r).await?)?;
    let mut data = vec![0u8; len];
    r.read_exact(&mut data).await?;
    decode_frame(&data)
}

#[cfg(any(windows, test))]
async fn read_varint_async<R: tokio::io::AsyncRead + Unpin>(r: &mut R) -> io::Result<u64> {
    let mut x: u64 = 0;
    let mut s: u32 = 0;
    let mut buf = [0u8; 1];
    for _ in 0..MAX_VARINT_BYTES {
        r.read_exact(&mut buf).await?;
        if let Some(v) = varint_step(&mut x, &mut s, buf[0]) {
            return Ok(v);
        }
    }
    Err(varint_overflow())
}

/// Asks whatever is listening on `endpoint` to shut down, and reports whether it
/// actually went away.
///
/// It deliberately does NOT force-kill. This runs on the path where the peer
/// reported a protocol/binary mismatch, which means it is emphatically *not* this
/// shell's child -- so the only PID available is the one the peer reported over
/// the socket about itself. Trusting that made this an arbitrary-process-kill
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
    eprintln!(
        "leapmux: a sidecar ({holder}) is holding {endpoint} and did not shut down \
         when asked; starting on a private endpoint instead. Stop it manually if it \
         should not be running -- in SOLO mode it also holds the shared DB's runtime \
         lease, so ConnectSolo will keep failing until it is gone (a launcher-mode \
         orphan only costs the sidecar-reuse optimisation)."
    );
    false
}

/// A dev sidecar endpoint private to THIS shell process.
///
/// The shared per-user endpoint is what lets a dev reload reuse a running sidecar, so
/// it is the default. But it is only a cache: when it cannot be reclaimed -- a wedged
/// leftover that ignores a cooperative shutdown, or another user's socket -- the
/// launch must still succeed. Suffixing our own PID gives a path nothing else holds,
/// so a fresh sidecar always starts. In LAUNCHER mode a single unkillable orphan then
/// costs only the reuse optimisation. In SOLO mode it costs more: the orphan also
/// holds the shared user-data DB's runtime lease, so the fresh sidecar's ConnectSolo
/// fails against the locked DB until the orphan is stopped by hand (surfaced by the
/// request_sidecar_shutdown diagnostic, which names the kernel-verified holder pid).
///
/// The previous behaviour, aborting the launch, made one leftover from a SIGKILLed
/// `task test-e2e` run block every subsequent `task dev` until the dev hunted it down
/// by hand.
#[cfg(unix)]
fn private_dev_sidecar_endpoint() -> String {
    dev_sidecar_runtime_dir()
        .join(format!(
            "{}-sidecar-{}.sock",
            sidecar_identity(),
            std::process::id()
        ))
        .display()
        .to_string()
}

#[cfg(unix)]
/// Refuses a dev sidecar socket answered by anyone but this user.
///
/// Everything downstream of the connect trusts the peer on its own word: the shell
/// adopts whatever answers here as its sidecar if it self-reports a matching protocol
/// version and binary hash — and a hash is exactly as forgeable as the PID that
/// `force_kill_sidecar` used to trust before it was deleted for that reason. The
/// endpoint sits at a predictable path (the shell derives it from
/// `std::env::temp_dir()`), so "whoever bound it first" is not an authorization.
///
/// The Go side hardens the *bind* (see `requirePrivateDir` in
/// desktop/go/socket_unix.go, which refuses a socket dir it does not own): this is
/// the same boundary from the connect side, and it must be checked here too, because
/// an honest sidecar refusing to bind a squatted directory does nothing to stop this
/// shell from connecting to whatever a squatter bound instead.
///
/// `peer_cred` reads the credentials the KERNEL recorded for the peer, so unlike the
/// hash it is not something the peer can assert. Dev-only, like the endpoint itself:
/// a bundled build spawns its own child over stdio pipes and never comes here.
#[cfg(unix)]
fn require_same_user_peer(stream: &UnixStream, endpoint: &str) -> Result<(), String> {
    require_peer_uid(
        socket_peer_uid(stream)?,
        unsafe { libc::getuid() },
        endpoint,
    )
}

/// The refusal decision itself, split from the socket so it can be tested against a
/// foreign uid — binding a socket as another user needs root, so the branch that
/// actually matters here is otherwise reachable only in production.
#[cfg(unix)]
fn require_peer_uid(peer_uid: u32, our_uid: u32, endpoint: &str) -> Result<(), String> {
    if peer_uid != our_uid {
        return Err(format!(
            "refusing sidecar at {endpoint}: it is answered by uid {peer_uid}, not {our_uid}; \
             something else is holding this endpoint"
        ));
    }
    Ok(())
}

/// Reads the peer's uid from a connected Unix socket.
///
/// std's `UnixStream::peer_cred` is still unstable, so this goes to libc. The two
/// families expose the same fact through different calls: Linux via the `SO_PEERCRED`
/// socket option, macOS/BSD via `getpeereid(3)`.
#[cfg(all(unix, target_os = "linux"))]
fn socket_peer_uid(stream: &UnixStream) -> Result<u32, String> {
    use std::os::fd::AsRawFd;

    let mut cred = libc::ucred {
        pid: 0,
        uid: 0,
        gid: 0,
    };
    let mut len = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
    // SAFETY: `cred` and `len` are live, correctly sized, and only written by the
    // kernel on success; the fd is owned by `stream` for the duration of the call.
    let rc = unsafe {
        libc::getsockopt(
            stream.as_raw_fd(),
            libc::SOL_SOCKET,
            libc::SO_PEERCRED,
            std::ptr::from_mut(&mut cred).cast::<libc::c_void>(),
            &mut len,
        )
    };
    if rc != 0 {
        return Err(format!(
            "read sidecar socket peer credentials: {}",
            io::Error::last_os_error()
        ));
    }
    Ok(cred.uid)
}

#[cfg(all(unix, not(target_os = "linux")))]
fn socket_peer_uid(stream: &UnixStream) -> Result<u32, String> {
    use std::os::fd::AsRawFd;

    let mut uid: libc::uid_t = 0;
    let mut gid: libc::gid_t = 0;
    // SAFETY: both out-params are live for the call and only written on success; the
    // fd is owned by `stream` for the duration.
    let rc = unsafe { libc::getpeereid(stream.as_raw_fd(), &mut uid, &mut gid) };
    if rc != 0 {
        return Err(format!(
            "read sidecar socket peer credentials: {}",
            io::Error::last_os_error()
        ));
    }
    Ok(uid)
}

/// The KERNEL-recorded pid of the socket peer -- the process actually on the
/// other end, not a pid it reported about itself over the wire. Used only to
/// make the "an orphan is holding the endpoint" diagnostic actionable
/// (`request_sidecar_shutdown`); it is NOT an authorization signal and nothing
/// is killed by it. Linux reads it from the `SO_PEERCRED` ucred whose uid we
/// already consult; macOS/BSD from `LOCAL_PEERPID`. Returns None on any error
/// rather than failing the caller: a missing pid only weakens a log line.
#[cfg(all(unix, target_os = "linux"))]
fn socket_peer_pid(stream: &UnixStream) -> Option<u32> {
    use std::os::fd::AsRawFd;

    let mut cred = libc::ucred {
        pid: 0,
        uid: 0,
        gid: 0,
    };
    let mut len = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
    // SAFETY: as in socket_peer_uid -- kernel writes `cred`/`len` on success; the
    // fd is owned by `stream` for the call.
    let rc = unsafe {
        libc::getsockopt(
            stream.as_raw_fd(),
            libc::SOL_SOCKET,
            libc::SO_PEERCRED,
            std::ptr::from_mut(&mut cred).cast::<libc::c_void>(),
            &mut len,
        )
    };
    if rc != 0 || cred.pid <= 0 {
        return None;
    }
    Some(cred.pid as u32)
}

#[cfg(all(unix, not(target_os = "linux")))]
fn socket_peer_pid(stream: &UnixStream) -> Option<u32> {
    use std::os::fd::AsRawFd;

    let mut pid: libc::pid_t = 0;
    let mut len = std::mem::size_of::<libc::pid_t>() as libc::socklen_t;
    // SAFETY: `pid`/`len` are live for the call and only written on success; the fd
    // is owned by `stream`. LOCAL_PEERPID reports the kernel-recorded peer pid.
    let rc = unsafe {
        libc::getsockopt(
            stream.as_raw_fd(),
            libc::SOL_LOCAL,
            libc::LOCAL_PEERPID,
            std::ptr::from_mut(&mut pid).cast::<libc::c_void>(),
            &mut len,
        )
    };
    if rc != 0 || pid <= 0 {
        return None;
    }
    Some(pid as u32)
}

/// The kernel-verified pid holding `endpoint`, for diagnostics only (see
/// socket_peer_pid). On unix it opens a throwaway connection and reads the
/// peer pid; on Windows it does the same through `GetNamedPipeServerProcessId`
/// on a freshly opened client handle.
#[cfg(unix)]
fn endpoint_holder_pid(endpoint: &str) -> Option<u32> {
    let stream = UnixStream::connect(endpoint).ok()?;
    socket_peer_pid(&stream)
}

#[cfg(windows)]
fn endpoint_holder_pid(endpoint: &str) -> Option<u32> {
    use std::os::windows::io::AsRawHandle;

    let client = pipe_runtime().block_on(open_named_pipe_client(endpoint)).ok()?;
    let client = match client {
        PipeConnect::Connected(client) => client,
        // No listener (or every instance busy, which we cannot query) means
        // no pid to report -- the diagnostic falls back to naming no holder.
        _ => return None,
    };
    let mut pid: u32 = 0;
    let rc = unsafe { GetNamedPipeServerProcessId(client.as_raw_handle() as HANDLE, &mut pid) };
    if rc == 0 || pid == 0 {
        None
    } else {
        Some(pid)
    }
}

// The sidecar-IPC layer exists as per-platform twins (unix socket vs Windows
// named pipe) scattered through this file: connect_sidecar_endpoint,
// is_sidecar_gone, sidecar_identity, the *_dev_sidecar_endpoint pair, the
// peer-credential helpers, and cleanup_dev_sidecar_artifacts. Grouping the twins
// into sidecar_ipc_unix.rs / sidecar_ipc_windows.rs so the two OS
// implementations are diffable side-by-side is tracked in
// https://github.com/leapmux/leapmux/issues/296 (distinct from the frame-codec
// extraction in #282).
#[cfg(unix)]
fn connect_sidecar_endpoint(
    endpoint: &str,
) -> Result<Option<(SidecarReader, SidecarWriter)>, String> {
    let stream = match UnixStream::connect(endpoint) {
        Ok(stream) => stream,
        Err(err)
            if err.kind() == io::ErrorKind::NotFound
                || err.kind() == io::ErrorKind::ConnectionRefused =>
        {
            return Ok(None);
        }
        Err(err) => return Err(format!("connect desktop sidecar socket: {err}")),
    };
    require_same_user_peer(&stream, endpoint)?;
    let reader = stream
        .try_clone()
        .map_err(|err| format!("clone sidecar socket: {err}"))?;
    let writer = stream;
    writer
        .set_write_timeout(Some(DEV_SIDECAR_HANDSHAKE_TIMEOUT))
        .map_err(|err| format!("set sidecar socket write timeout: {err}"))?;
    reader
        .set_read_timeout(Some(DEV_SIDECAR_HANDSHAKE_TIMEOUT))
        .map_err(|err| format!("set sidecar socket read timeout: {err}"))?;
    Ok(Some((reader, writer)))
}

// Handshake timeouts must be cleared before streams are handed to the
// long-lived reader thread; otherwise reads fail with EAGAIN after a few
// seconds of idle and tear the connection down.
#[cfg(unix)]
fn finalize_sidecar_streams(reader: &SidecarReader, writer: &SidecarWriter) -> Result<(), String> {
    reader
        .set_read_timeout(None)
        .map_err(|err| format!("clear sidecar socket read timeout: {err}"))?;
    writer
        .set_write_timeout(None)
        .map_err(|err| format!("clear sidecar socket write timeout: {err}"))?;
    Ok(())
}

#[cfg(unix)]
fn is_sidecar_gone(endpoint: &str) -> bool {
    !Path::new(endpoint).exists()
}

#[cfg(unix)]
fn fetch_sidecar_info(
    reader: &mut impl Read,
    writer: &mut impl Write,
) -> Result<proto::SidecarInfo, String> {
    let frame = proto::Frame {
        message: Some(proto::frame::Message::Request(proto::Request {
            id: 1,
            method: Some(proto::request::Method::GetSidecarInfo(
                proto::GetSidecarInfoRequest {},
            )),
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

#[cfg(unix)]
fn cleanup_dev_sidecar_artifacts(endpoint: &str, metadata_path: &Path) {
    let _ = fs::remove_file(endpoint);
    let _ = fs::remove_file(metadata_path);
}

fn write_sidecar_metadata(
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

#[cfg(unix)]
fn restrict_dir_permissions(path: &Path) -> Result<(), String> {
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))
        .map_err(|err| format!("set sidecar metadata dir permissions: {err}"))
}

#[cfg(unix)]
fn restrict_file_permissions(path: &Path) -> Result<(), String> {
    fs::set_permissions(path, fs::Permissions::from_mode(0o600))
        .map_err(|err| format!("set sidecar metadata permissions: {err}"))
}

#[cfg(windows)]
fn restrict_dir_permissions(_: &Path) -> Result<(), String> {
    Ok(())
}

#[cfg(windows)]
fn restrict_file_permissions(_: &Path) -> Result<(), String> {
    Ok(())
}

#[cfg(unix)]
fn dev_sidecar_endpoint() -> String {
    dev_sidecar_runtime_dir()
        .join(format!("{}-sidecar.sock", sidecar_identity()))
        .display()
        .to_string()
}

#[cfg(unix)]
fn dev_sidecar_metadata_path() -> PathBuf {
    dev_sidecar_runtime_dir().join(format!("{}-sidecar.json", sidecar_identity()))
}

#[cfg(unix)]
fn dev_sidecar_runtime_dir() -> PathBuf {
    std::env::temp_dir().join("leapmux-desktop")
}

#[cfg(unix)]
fn sidecar_identity() -> String {
    use std::sync::OnceLock;
    static CACHED: OnceLock<String> = OnceLock::new();
    CACHED
        .get_or_init(|| {
            std::env::var("USER")
                .or_else(|_| std::env::var("USERNAME"))
                .unwrap_or_else(|_| "default".to_string())
                .chars()
                .map(|ch| if ch.is_ascii_alphanumeric() { ch } else { '_' })
                .collect()
        })
        .clone()
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

// --- Windows named-pipe dev-mode sidecar reconnect ---
//
// Why tokio's overlapped-I/O client and not a raw CreateFileW/ReadFile/
// WriteFile wrapper: a named-pipe handle opened without FILE_FLAG_OVERLAPPED
// serializes all I/O through the FILE_OBJECT lock, even across duplicated
// handles. A blocked long-lived ReadFile would prevent any concurrent
// WriteFile from making progress and deadlock the reader/writer threads.

#[cfg(any(windows, test))]
use tokio::io::{AsyncReadExt, AsyncWriteExt};
#[cfg(windows)]
use tokio::net::windows::named_pipe::{ClientOptions, NamedPipeClient};
#[cfg(windows)]
use windows_sys::Win32::{
    Foundation::{
        CloseHandle, GetLastError, LocalFree, ERROR_FILE_NOT_FOUND, ERROR_INSUFFICIENT_BUFFER,
        ERROR_PIPE_BUSY, HANDLE, HLOCAL,
    },
    Security::{
        Authorization::ConvertSidToStringSidW, EqualSid, GetLengthSid, GetTokenInformation,
        TokenUser, PSID, TOKEN_QUERY, TOKEN_USER,
    },
    System::{Pipes::GetNamedPipeServerProcessId, Threading::{
        GetCurrentProcess, OpenProcess, OpenProcessToken, PROCESS_QUERY_LIMITED_INFORMATION,
    }},
};

#[cfg(windows)]
type SidecarReader = SyncPipeReader;
#[cfg(windows)]
type SidecarWriter = SyncPipeWriter;

// `new_multi_thread` with one worker is deliberate: the reader and writer
// threads both call `block_on` on this runtime, and `current_thread` would
// serialize them through the runtime mutex (defeating the parallelism the
// FILE_OBJECT-lock fix exists to enable).
#[cfg(any(windows, test))]
fn pipe_runtime() -> &'static tokio::runtime::Runtime {
    static RUNTIME: std::sync::OnceLock<tokio::runtime::Runtime> = std::sync::OnceLock::new();
    RUNTIME.get_or_init(|| {
        tokio::runtime::Builder::new_multi_thread()
            .worker_threads(1)
            .enable_io()
            .enable_time()
            .thread_name("leapmux-named-pipe")
            .build()
            .expect("build named-pipe runtime")
    })
}

#[cfg(windows)]
pub struct SyncPipeReader {
    inner: tokio::io::ReadHalf<NamedPipeClient>,
}

#[cfg(windows)]
impl Read for SyncPipeReader {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        pipe_runtime().block_on(self.inner.read(buf))
    }
}

#[cfg(windows)]
pub struct SyncPipeWriter {
    inner: tokio::io::WriteHalf<NamedPipeClient>,
}

#[cfg(windows)]
impl Write for SyncPipeWriter {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        pipe_runtime().block_on(self.inner.write(buf))
    }

    fn flush(&mut self) -> io::Result<()> {
        pipe_runtime().block_on(self.inner.flush())
    }
}

/// Outcome of a named-pipe connect attempt. The THREE states must stay
/// distinct: a pipe that does not exist (`NotFound`) and a pipe whose every
/// server instance is momentarily busy (`Busy`) are opposite facts about the
/// sidecar's liveness -- NotFound means it is gone, Busy means it is alive and
/// serving -- and collapsing both to "no client" (the old `Ok(None)`) let a
/// live-but-busy sidecar read as gone during the shutdown poll, so the shell
/// double-spawned onto an endpoint a zombie still held.
#[cfg(windows)]
enum PipeConnect {
    Connected(NamedPipeClient),
    NotFound,
    Busy,
}

// ERROR_PIPE_BUSY gets a short retry loop; if every instance stays busy across
// the retries the pipe is alive but saturated, reported as Busy (NOT NotFound).
// ERROR_FILE_NOT_FOUND is NotFound; any other error is fatal.
#[cfg(windows)]
async fn open_named_pipe_client(pipe_name: &str) -> Result<PipeConnect, String> {
    const MAX_BUSY_RETRIES: u32 = 3;
    for _ in 0..=MAX_BUSY_RETRIES {
        match ClientOptions::new().open(pipe_name) {
            Ok(client) => return Ok(PipeConnect::Connected(client)),
            Err(err) if err.raw_os_error() == Some(ERROR_FILE_NOT_FOUND as i32) => {
                return Ok(PipeConnect::NotFound);
            }
            Err(err) if err.raw_os_error() == Some(ERROR_PIPE_BUSY as i32) => {
                tokio::time::sleep(Duration::from_millis(50)).await;
                continue;
            }
            Err(err) => return Err(format!("open named pipe {pipe_name}: {err}")),
        }
    }
    // Retries exhausted while every instance was busy: alive, not gone.
    Ok(PipeConnect::Busy)
}

#[cfg(windows)]
fn is_sidecar_gone(pipe_name: &str) -> bool {
    // ONLY NotFound means gone. A Busy pipe is a live sidecar whose instances
    // are all in use -- reporting it gone here is exactly what let the shell
    // abandon a healthy-but-busy sidecar mid-shutdown-poll and double-spawn.
    pipe_runtime().block_on(async {
        matches!(open_named_pipe_client(pipe_name).await, Ok(PipeConnect::NotFound))
    })
}

/// Refuses a dev sidecar named pipe answered by anyone but this user, the
/// Windows counterpart of the unix `connect_sidecar_endpoint`'s
/// `require_same_user_peer`. See `require_same_user_pipe_peer` for the
/// mechanism and the bind-side pair (`userOnlySDDL` in
/// `backend/locallisten/locallisten_windows.go`) that hardens the *listener*
/// the same way Unix's `requirePrivateDir` does.
#[cfg(windows)]
fn connect_sidecar_endpoint(
    pipe_name: &str,
) -> Result<Option<(SidecarReader, SidecarWriter)>, String> {
    let client = match pipe_runtime().block_on(open_named_pipe_client(pipe_name))? {
        PipeConnect::Connected(client) => client,
        // Gone: the caller's "try again later / endpoint is free" signal.
        PipeConnect::NotFound => return Ok(None),
        // Alive but saturated. NOT free to take -- surfaced as an error so the
        // bootstrap routes around it onto a private endpoint rather than
        // colliding on an endpoint a live sidecar still holds.
        PipeConnect::Busy => {
            return Err(format!("named pipe {pipe_name} is busy (sidecar alive)"));
        }
    };
    require_same_user_pipe_peer(&client, pipe_name)?;
    let (r, w) = tokio::io::split(client);
    Ok(Some((
        SyncPipeReader { inner: r },
        SyncPipeWriter { inner: w },
    )))
}

#[cfg(windows)]
fn cleanup_dev_sidecar_artifacts(_endpoint: &str, metadata_path: &Path) {
    // Named pipes release themselves when the server closes the listener;
    // only the metadata file persists on disk.
    let _ = fs::remove_file(metadata_path);
}

/// Windows counterpart of the unix private endpoint (see there for why it exists).
/// Named pipes carry no filesystem path, so the PID goes in the pipe name.
#[cfg(windows)]
fn private_dev_sidecar_endpoint() -> Result<String, String> {
    Ok(format!(
        "\\\\.\\pipe\\leapmux-desktop-{}-sidecar-{}",
        sidecar_identity()?,
        std::process::id()
    ))
}

#[cfg(windows)]
fn dev_sidecar_endpoint() -> Result<String, String> {
    Ok(format!(
        "\\\\.\\pipe\\leapmux-desktop-{}-sidecar",
        sidecar_identity()?
    ))
}

#[cfg(windows)]
fn dev_sidecar_metadata_path() -> PathBuf {
    let base = std::env::var_os("LOCALAPPDATA")
        .map(PathBuf::from)
        .unwrap_or_else(std::env::temp_dir);
    dev_sidecar_metadata_path_in(&base)
}

#[cfg(windows)]
fn dev_sidecar_metadata_path_in(base: &Path) -> PathBuf {
    base.join("leapmux-desktop").join("sidecar.json")
}

#[cfg(windows)]
fn sanitize_sid_for_pipe(raw: &str) -> String {
    raw.chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || c == '-' {
                c
            } else {
                '_'
            }
        })
        .collect()
}

#[cfg(windows)]
fn sidecar_identity() -> Result<String, String> {
    use std::sync::OnceLock;
    static CACHED: OnceLock<Result<String, String>> = OnceLock::new();
    CACHED
        .get_or_init(|| {
            current_user_sid()
                .and_then(|bytes| sid_to_string(&bytes))
                .map(|raw| sanitize_sid_for_pipe(&raw))
        })
        .clone()
}

/// The current process's user SID, copied into an owned buffer so callers can
/// hold it past the token handle. Used both to name the per-user dev pipe
/// (via `sidecar_identity`) and as the comparison point for the connect-side
/// identity check (`require_same_user_pipe_peer`) -- the two callers that need
/// to agree on what "us" means go through one place to find out.
#[cfg(windows)]
fn current_user_sid() -> Result<Vec<u8>, String> {
    let mut token: HANDLE = std::ptr::null_mut();
    if unsafe { OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) } == 0 {
        return Err(format!("open process token: error {}", unsafe { GetLastError() }));
    }
    let sid = token_user_sid(token);
    unsafe { CloseHandle(token) };
    sid
}

/// Renders a SID (as raw bytes) into the `S-1-5-...` string form. Wraps the
/// `ConvertSidToStringSidW` + `LocalFree` pair so neither caller has to.
#[cfg(windows)]
fn sid_to_string(sid: &[u8]) -> Result<String, String> {
    let mut sid_string_ptr: *mut u16 = std::ptr::null_mut();
    if unsafe { ConvertSidToStringSidW(sid.as_ptr() as PSID, &mut sid_string_ptr) } == 0 {
        return Err(format!("convert sid to string: error {}", unsafe { GetLastError() }));
    }
    let mut len = 0;
    while unsafe { *sid_string_ptr.add(len) } != 0 {
        len += 1;
    }
    let slice = unsafe { std::slice::from_raw_parts(sid_string_ptr, len) };
    let sid = String::from_utf16_lossy(slice);
    unsafe { LocalFree(sid_string_ptr as HLOCAL) };
    Ok(sid)
}

/// Queries `TokenUser` out of a token handle and returns the SID it points to,
/// copied into an owned buffer.
///
/// `GetTokenInformation` writes a `TOKEN_USER` whose `User.Sid` points *into*
/// the same buffer, so the SID borrows from it; copying into a fresh `Vec<u8>`
/// hands the caller a SID whose lifetime is independent of the token query.
/// `GetLengthSid` reports the kernel's byte count for a valid SID, so the copy
/// is exactly sized.
///
/// The size-probe call is EXPECTED to fail with ERROR_INSUFFICIENT_BUFFER (that
/// is how it reports the required size in `needed`). Checking its return
/// distinguishes that expected failure from a real one: without the check, any
/// other failure leaves `needed` at 0, the second call runs against an empty
/// buffer, and the error reported below is the SECOND call's -- masking the
/// true cause (a bad token handle, a denied query).
#[cfg(windows)]
fn token_user_sid(token: HANDLE) -> Result<Vec<u8>, String> {
    let mut needed: u32 = 0;
    if unsafe { GetTokenInformation(token, TokenUser, std::ptr::null_mut(), 0, &mut needed) } == 0 {
        let probe_err = unsafe { GetLastError() };
        if probe_err != ERROR_INSUFFICIENT_BUFFER {
            return Err(format!("probe token user info size: error {probe_err}"));
        }
    }
    let mut buffer = vec![0u8; needed as usize];
    let ok = unsafe {
        GetTokenInformation(
            token,
            TokenUser,
            buffer.as_mut_ptr() as *mut _,
            needed,
            &mut needed,
        )
    };
    let token_err = if ok == 0 { unsafe { GetLastError() } } else { 0 };
    if ok == 0 {
        return Err(format!("get token user info: error {token_err}"));
    }
    let user_info = unsafe { &*(buffer.as_ptr() as *const TOKEN_USER) };
    let sid_ptr = user_info.User.Sid;
    let len = unsafe { GetLengthSid(sid_ptr) } as usize;
    if len == 0 {
        return Err("token user sid: GetLengthSid returned 0".to_string());
    }
    let mut sid = vec![0u8; len];
    unsafe { std::ptr::copy_nonoverlapping(sid_ptr as *const u8, sid.as_mut_ptr(), len) };
    Ok(sid)
}

/// Reads the server process's user SID from a connected named-pipe client
/// handle. The PID is the kernel's report about which process bound this pipe
/// instance (not a value the peer asserts over the wire), and the SID is read
/// from that PID's primary token -- so the same chain of trust as Unix's
/// `SO_PEERCRED` / `getpeereid`: a fact the peer cannot forge.
///
/// `PROCESS_QUERY_LIMITED_INFORMATION` is the least-privilege access right
/// that lets us read another process's token's user; same-user processes hold
/// it by default, and any failure here fails closed in the caller.
#[cfg(windows)]
fn pipe_peer_sid(client: &NamedPipeClient) -> Result<Vec<u8>, String> {
    use std::os::windows::io::AsRawHandle;

    let mut server_pid: u32 = 0;
    if unsafe { GetNamedPipeServerProcessId(client.as_raw_handle() as HANDLE, &mut server_pid) }
        == 0
    {
        return Err(format!(
            "query named pipe server pid: error {}",
            unsafe { GetLastError() }
        ));
    }
    let process = unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, server_pid) };
    if process.is_null() {
        return Err(format!(
            "open server process {server_pid}: error {}",
            unsafe { GetLastError() }
        ));
    }
    let mut token: HANDLE = std::ptr::null_mut();
    let token_rc = unsafe { OpenProcessToken(process, TOKEN_QUERY, &mut token) };
    unsafe { CloseHandle(process) };
    if token_rc == 0 {
        return Err(format!(
            "open server process token: error {}",
            unsafe { GetLastError() }
        ));
    }
    let sid = token_user_sid(token);
    unsafe { CloseHandle(token) };
    sid
}

/// The refusal decision itself, split from the pipe so it can be tested
/// against a foreign SID -- binding a pipe as another user needs admin, so the
/// branch that actually matters here is otherwise reachable only in production.
/// Mirrors `require_peer_uid` on Unix.
#[cfg(windows)]
fn require_peer_sid(peer: &[u8], ours: &[u8], pipe_name: &str) -> Result<(), String> {
    // EqualSid is the canonical Win32 SID comparison; it returns FALSE for
    // unequal SIDs (different lengths or different bytes), so a peer whose SID
    // differs in any way is a clean refusal rather than a panic.
    let equal = unsafe { EqualSid(peer.as_ptr() as PSID, ours.as_ptr() as PSID) } != 0;
    if !equal {
        return Err(format!(
            "refusing sidecar at {pipe_name}: it is answered by a different user; \
             something else is holding this endpoint"
        ));
    }
    Ok(())
}

/// Refuses a dev sidecar named pipe answered by anyone but this user. Windows
/// counterpart of Unix's `require_same_user_peer`; see that function for why
/// the connect side must check this even though the Go side already restricts
/// the bind to our SID (via `userOnlySDDL` in locallisten_windows.go).
#[cfg(windows)]
fn require_same_user_pipe_peer(client: &NamedPipeClient, pipe_name: &str) -> Result<(), String> {
    let peer_sid = pipe_peer_sid(client)?;
    let our_sid = current_user_sid()?;
    require_peer_sid(&peer_sid, &our_sid, pipe_name)
}

impl DesktopShell {
    fn new(app_handle: AppHandle) -> Result<Self, String> {
        let local_app_url = if cfg!(debug_assertions) {
            "http://localhost:4328".to_string()
        } else {
            "tauri://localhost".to_string()
        };
        let sidecar_path = resolve_sidecar_path(&app_handle)?;
        let bootstrap = bootstrap_sidecar(&sidecar_path)?;

        let pending: PendingMap = Arc::new(Mutex::new(HashMap::new()));
        start_sidecar_reader_thread(app_handle.clone(), pending.clone(), bootstrap.reader);
        let writer_tx = start_sidecar_writer_thread(bootstrap.writer, pending.clone());

        let shell = Self {
            app_handle,
            sidecar: SidecarProcess {
                _child: bootstrap.child,
                writer_tx,
                pending,
                next_id: AtomicU64::new(1),
            },
            close_in_progress: AtomicBool::new(false),
            exit_in_progress: AtomicBool::new(false),
            webview_zoom: AtomicU64::new(1.0f64.to_bits()),
            state: Mutex::new(ShellState {
                shell_mode: ShellMode::Launcher,
                connected: false,
                hub_url: String::new(),
                local_app_url,
            }),
        };

        // Bound the initial sidecar handshake so a wedged child can't hang
        // the Tauri setup thread indefinitely.
        tauri::async_runtime::block_on(async {
            tokio::time::timeout(
                SIDECAR_INITIAL_HANDSHAKE_TIMEOUT,
                shell.refresh_state_from_sidecar(),
            )
            .await
            .map_err(|_| {
                format!(
                    "initial sidecar handshake timed out after {:?}",
                    SIDECAR_INITIAL_HANDSHAKE_TIMEOUT
                )
            })?
        })?;
        Ok(shell)
    }

    async fn send_request_async(
        &self,
        method: proto::request::Method,
    ) -> Result<proto::Response, String> {
        send_sidecar_request(&self.sidecar, method).await
    }

    async fn request_shutdown_async(&self) {
        let shutdown =
            self.send_request_async(proto::request::Method::Shutdown(proto::ShutdownRequest {}));
        let _ = tokio::time::timeout(Duration::from_secs(5), shutdown).await;
        tokio::time::sleep(Duration::from_millis(250)).await;
    }

    async fn refresh_state_from_sidecar(&self) -> Result<(), String> {
        let resp = check_response(
            self.send_request_async(proto::request::Method::GetSidecarInfo(
                proto::GetSidecarInfoRequest {},
            ))
            .await?,
        )?;
        let info = sidecar_info_from_response(resp, "get_sidecar_info")?;
        apply_sidecar_info(&self.state, info);
        Ok(())
    }

    fn runtime_state(&self) -> RuntimeState {
        let state = self.state.lock().unwrap().clone();
        RuntimeState {
            shell_mode: state.shell_mode,
            connected: state.connected,
            hub_url: state.hub_url.clone(),
            capabilities: capabilities_for(&state),
        }
    }

    async fn save_window_size(&self, width: u32, height: u32, mode: String) -> Result<(), String> {
        let _ = self
            .send_request_async(proto::request::Method::SetWindowSize(
                proto::SetWindowSizeRequest {
                    width: width as i32,
                    height: height as i32,
                    mode: window_mode_to_proto(&mode) as i32,
                },
            ))
            .await?;
        Ok(())
    }

    fn current_zoom(&self) -> f64 {
        f64::from_bits(self.webview_zoom.load(Ordering::Relaxed))
    }

    fn set_zoom(&self, zoom: f64) -> Result<(), String> {
        let clamped = zoom.clamp(0.5, 3.0);
        if let Some(window) = self.app_handle.get_webview_window("main") {
            window
                .set_zoom(clamped)
                .map_err(|err| format!("set webview zoom: {err}"))?;
            self.webview_zoom
                .store(clamped.to_bits(), Ordering::Relaxed);
        }
        Ok(())
    }
}

// send_sidecar_request awaits the response with NO per-request timeout, and
// that is deliberate -- do not add one. The transport is a local pipe/socket to
// a child (or same-user dev) process on this machine: there is no network to
// time out, and the reader/writer threads already fail every pending request
// the moment the transport itself errors. The remaining unbounded case is a
// sidecar that is CONNECTED but wedged (deadlocked, not reading), and we treat
// a hanging sidecar as a hanging application: a synthetic timeout would only
// convert that hang into per-command errors against a process that still holds
// the solo Hub's DB lease, inviting a doomed reconnect loop. The bounded
// exceptions are the initial handshake (SIDECAR_INITIAL_HANDSHAKE_TIMEOUT,
// where the peer has not yet proven live) and Shutdown (request_shutdown_async,
// where the caller is about to exit regardless). `proxy_http` in particular
// must stay unbounded here: it carries Hub RPCs whose own server-side budgets
// (agent startup, worktree creation) are the real timeouts.
async fn send_sidecar_request(
    sidecar: &SidecarProcess,
    method: proto::request::Method,
) -> Result<proto::Response, String> {
    let id = sidecar.next_id.fetch_add(1, Ordering::Relaxed);
    let (tx, rx) = oneshot::channel();
    sidecar.pending.lock().unwrap().insert(id, tx);

    let frame = proto::Frame {
        message: Some(proto::frame::Message::Request(proto::Request {
            id,
            method: Some(method),
        })),
    };
    if sidecar.writer_tx.send(frame).is_err() {
        sidecar.pending.lock().unwrap().remove(&id);
        return Err("desktop sidecar writer disconnected".to_string());
    }

    rx.await
        .map_err(|_| "desktop sidecar disconnected".to_string())?
}

// --- Response helpers ---

fn check_response(resp: proto::Response) -> Result<proto::Response, String> {
    if resp.error.is_empty() {
        Ok(resp)
    } else {
        Err(resp.error)
    }
}

fn sidecar_info_from_response(
    resp: proto::Response,
    context: &str,
) -> Result<proto::SidecarInfo, String> {
    match resp.result {
        Some(proto::response::Result::SidecarInfo(info)) => Ok(info),
        _ => Err(format!("unexpected response for {context}")),
    }
}

fn lifecycle_from_response(
    resp: proto::Response,
) -> Result<(proto::SidecarInfo, Vec<String>), String> {
    let resp = check_response(resp)?;
    match resp.result {
        Some(proto::response::Result::Lifecycle(result)) => result
            .sidecar_info
            .map(|info| (info, result.cleanup_errors))
            .ok_or_else(|| "lifecycle response missing sidecar info".to_string()),
        _ => Err("unexpected lifecycle response".to_string()),
    }
}

fn shell_mode_from_proto(info: &proto::SidecarInfo) -> ShellMode {
    match info.shell_mode() {
        proto::SidecarShellMode::Solo => ShellMode::Solo,
        proto::SidecarShellMode::Distributed => ShellMode::Distributed,
        _ => ShellMode::Launcher,
    }
}

// The JSON string spellings of each window display mode, shared with the
// frontend and the persisted config. Mirrors the Go constants in
// desktop/go/config.go so the vocabulary is single-sourced within each binary.
const WINDOW_MODE_NORMAL: &str = "normal";
const WINDOW_MODE_MAXIMIZED: &str = "maximized";
const WINDOW_MODE_FULLSCREEN: &str = "fullscreen";

// Bridge the persisted window mode between the JSON string used with the
// frontend and the proto enum on the sidecar wire. Empty/unknown -> normal.
fn window_mode_to_proto(mode: &str) -> proto::WindowMode {
    match mode {
        WINDOW_MODE_MAXIMIZED => proto::WindowMode::Maximized,
        WINDOW_MODE_FULLSCREEN => proto::WindowMode::Fullscreen,
        _ => proto::WindowMode::Normal,
    }
}

fn window_mode_from_proto(mode: proto::WindowMode) -> String {
    match mode {
        proto::WindowMode::Maximized => WINDOW_MODE_MAXIMIZED,
        proto::WindowMode::Fullscreen => WINDOW_MODE_FULLSCREEN,
        _ => WINDOW_MODE_NORMAL,
    }
    .to_string()
}

fn apply_sidecar_info(state: &Mutex<ShellState>, info: proto::SidecarInfo) {
    let shell_mode = shell_mode_from_proto(&info);
    let mut guard = state.lock().unwrap();
    guard.shell_mode = shell_mode;
    guard.connected = info.connected;
    guard.hub_url = info.hub_url;
}

// --- Sidecar message handling ---

fn handle_sidecar_frame(app_handle: &AppHandle, pending: &PendingMap, frame: proto::Frame) {
    let Some(message) = frame.message else { return };

    match message {
        proto::frame::Message::Response(resp) => {
            let id = resp.id;
            let tx = pending.lock().unwrap().remove(&id);
            if let Some(tx) = tx {
                if resp.error.is_empty() {
                    let _ = tx.send(Ok(resp));
                } else {
                    let _ = tx.send(Err(resp.error));
                }
            }
        }
        proto::frame::Message::Event(event) => {
            handle_sidecar_event(app_handle, event);
        }
        proto::frame::Message::Request(_) => {
            // Sidecar should never send requests to Rust.
        }
    }
}

fn handle_sidecar_event(app_handle: &AppHandle, event: proto::Event) {
    let Some(payload) = event.payload else { return };

    match payload {
        proto::event::Payload::ChannelMessage(msg) => {
            let b64 = base64::engine::general_purpose::STANDARD.encode(&msg.data);
            let _ = app_handle.emit("channel:message", b64);
        }
        proto::event::Payload::ChannelClose(close) => {
            let _ = app_handle.emit(
                "channel:close",
                json!({ "code": close.code, "reason": close.reason, "wasClean": close.was_clean }),
            );
        }
        proto::event::Payload::OrgEventsMessage(msg) => {
            // Forward the hub's length-prefixed WatchOrgEvent frame
            // verbatim to the webview. The frontend's `useOrgEvents`
            // hook decodes identically to native WS frames.
            let b64 = base64::engine::general_purpose::STANDARD.encode(&msg.data);
            let _ = app_handle.emit("orgevents:message", b64);
        }
        proto::event::Payload::OrgEventsClose(close) => {
            let _ = app_handle.emit(
                "orgevents:close",
                json!({ "code": close.code, "reason": close.reason, "wasClean": close.was_clean }),
            );
        }
        proto::event::Payload::SidecarLog(log) => {
            let payload = json!({
              "level": log.level,
              "time": log.time,
              "message": log.message,
              "attrs": log.attrs,
            });
            let _ = app_handle.emit("sidecar:log", payload);
        }
    }
}

// --- Static helpers ---

fn capabilities_for(state: &ShellState) -> PlatformCapabilities {
    match state.shell_mode {
        ShellMode::Solo | ShellMode::Launcher => PlatformCapabilities {
            mode: PlatformMode::TauriDesktopSolo,
            hub_transport: HubTransport::Proxy,
            tunnels: true,
            app_control: true,
            window_control: true,
            system_permissions: true,
            local_solo: true,
        },
        ShellMode::Distributed => PlatformCapabilities {
            mode: PlatformMode::TauriDesktopDistributed,
            hub_transport: HubTransport::Direct,
            tunnels: true,
            app_control: true,
            window_control: true,
            system_permissions: true,
            local_solo: false,
        },
    }
}

fn resolve_sidecar_path(app_handle: &AppHandle) -> Result<PathBuf, String> {
    let sidecar_name = sidecar_binary_name();

    // Dev mode: sidecar built into the Go source tree at desktop/go/bin/.
    if let Some(parent) = PathBuf::from(env!("CARGO_MANIFEST_DIR")).parent() {
        let dev_path = parent.join("go").join("bin").join(&sidecar_name);
        if dev_path.exists() {
            return Ok(dev_path);
        }
    }

    // Next to the main executable. Covers macOS bundled apps (where the
    // sidecar is placed in Contents/MacOS/) and Linux unbundled runs where
    // the sidecar has been copied beside leapmux-desktop.
    let exe = std::env::current_exe().map_err(|err| format!("resolve current exe: {err}"))?;
    if let Some(dir) = exe.parent() {
        let path = dir.join(&sidecar_name);
        if path.exists() {
            return Ok(path);
        }
    }

    // Bundled resource directory.
    let resource_dir = app_handle
        .path()
        .resource_dir()
        .map_err(|err| format!("resolve resource dir: {err}"))?;

    #[cfg(target_os = "windows")]
    {
        Ok(resource_dir
            .join("_up_")
            .join("go")
            .join("bin")
            .join(&sidecar_name))
    }
    #[cfg(not(target_os = "windows"))]
    {
        Ok(resource_dir.join(&sidecar_name))
    }
}

fn sidecar_binary_name() -> String {
    #[cfg(target_os = "macos")]
    let os = "apple-darwin";
    #[cfg(target_os = "linux")]
    let os = "unknown-linux-gnu";
    #[cfg(target_os = "windows")]
    let os = "pc-windows-msvc";

    #[cfg(target_arch = "aarch64")]
    let arch = "aarch64";
    #[cfg(target_arch = "x86_64")]
    let arch = "x86_64";

    let name = format!("leapmux-desktop-service-{arch}-{os}");
    #[cfg(target_os = "windows")]
    {
        format!("{name}.exe")
    }
    #[cfg(any(target_os = "macos", target_os = "linux"))]
    {
        name
    }
}

// --- Tauri commands ---

#[tauri::command]
fn get_runtime_state(shell: State<'_, Arc<DesktopShell>>) -> RuntimeState {
    shell.runtime_state()
}

#[tauri::command]
async fn get_startup_info(
    shell: State<'_, Arc<DesktopShell>>,
) -> Result<StartupInfoResponse, String> {
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::GetStartupInfo(
                proto::GetStartupInfoRequest {},
            ))
            .await?,
    )?;
    match resp.result {
        Some(proto::response::Result::StartupInfo(info)) => {
            let cfg = info.config.unwrap_or_default();
            let build = info.build_info.unwrap_or_default();
            Ok(StartupInfoResponse {
                config: DesktopConfigResponse {
                    window_mode: window_mode_from_proto(cfg.window_mode()),
                    mode: cfg.mode,
                    hub_url: cfg.hub_url,
                    window_width: cfg.window_width,
                    window_height: cfg.window_height,
                },
                build_info: BuildInfoResponse {
                    version: build.version,
                    commit_hash: build.commit_hash,
                    commit_time: build.commit_time,
                    build_time: build.build_time,
                    branch: build.branch,
                },
            })
        }
        _ => Err("unexpected response for get_startup_info".to_string()),
    }
}

#[tauri::command]
async fn check_full_disk_access(shell: State<'_, Arc<DesktopShell>>) -> Result<bool, String> {
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::CheckFullDiskAccess(
                proto::CheckFullDiskAccessRequest {},
            ))
            .await?,
    )?;
    match resp.result {
        Some(proto::response::Result::BoolValue(v)) => Ok(v.value),
        _ => Err("unexpected response for check_full_disk_access".to_string()),
    }
}

#[tauri::command]
async fn open_full_disk_access_settings(shell: State<'_, Arc<DesktopShell>>) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::OpenFullDiskAccessSettings(
                proto::OpenFullDiskAccessSettingsRequest {},
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn connect_solo(shell: State<'_, Arc<DesktopShell>>) -> Result<(), String> {
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::ConnectSolo(
                proto::ConnectSoloRequest {},
            ))
            .await?,
    )?;
    let info = sidecar_info_from_response(resp, "connect_solo")?;
    apply_sidecar_info(&shell.state, info);
    Ok(())
}

#[tauri::command]
async fn connect_distributed(
    shell: State<'_, Arc<DesktopShell>>,
    window: WebviewWindow,
    hub_url: String,
) -> Result<(), String> {
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::ConnectDistributed(
                proto::ConnectDistributedRequest { hub_url },
            ))
            .await?,
    )?;
    let info = sidecar_info_from_response(resp, "connect_distributed")?;
    let normalized_hub_url = info.hub_url.clone();
    apply_sidecar_info(&shell.state, info);

    let target_url =
        Url::parse(&normalized_hub_url).map_err(|err| format!("parse hub url: {err}"))?;
    window
        .navigate(target_url)
        .map_err(|err| format!("navigate to hub: {err}"))?;
    Ok(())
}

#[derive(Deserialize)]
struct ProxyHttpPayload {
    method: String,
    path: String,
    headers: HashMap<String, String>,
    #[serde(rename = "bodyBase64")]
    body_base64: String,
}

#[tauri::command]
async fn proxy_http(
    shell: State<'_, Arc<DesktopShell>>,
    payload: ProxyHttpPayload,
) -> Result<ProxyHttpResponsePayload, String> {
    let body = if payload.body_base64.is_empty() {
        Vec::new()
    } else {
        decode_b64(&payload.body_base64).map_err(|err| format!("decode request body: {err}"))?
    };

    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::ProxyHttp(proto::ProxyHttpRequest {
                method: payload.method,
                path: payload.path,
                headers: payload.headers,
                body,
            }))
            .await?,
    )?;

    match resp.result {
        Some(proto::response::Result::ProxyHttp(r)) => Ok(ProxyHttpResponsePayload {
            status: r.status,
            headers: r
                .headers
                .into_iter()
                .map(|(name, values)| (name, values.values))
                .collect(),
            body: base64::engine::general_purpose::STANDARD.encode(&r.body),
        }),
        _ => Err("unexpected response for proxy_http".to_string()),
    }
}

// --- CLI PATH integration (macOS only at the sidecar level) ---

#[derive(Serialize)]
struct CliPathStatusPayload {
    state: i32,
    bundled: String,
    resolved: String,
    target: String,
    #[serde(rename = "targetKind")]
    target_kind: i32,
}

#[tauri::command]
async fn cli_path_status(
    shell: State<'_, Arc<DesktopShell>>,
) -> Result<CliPathStatusPayload, String> {
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::CliPathStatus(
                proto::CliPathStatusRequest {},
            ))
            .await?,
    )?;

    match resp.result {
        Some(proto::response::Result::CliPathStatus(r)) => Ok(CliPathStatusPayload {
            state: r.state,
            bundled: r.bundled,
            resolved: r.resolved,
            target: r.target,
            target_kind: r.target_kind,
        }),
        _ => Err("unexpected response for cli_path_status".to_string()),
    }
}

#[derive(Serialize)]
struct CliInstallSymlinkPayload {
    result: i32,
    command: String,
    path: String,
    message: String,
}

#[tauri::command]
async fn cli_install_symlink(
    shell: State<'_, Arc<DesktopShell>>,
    force: bool,
) -> Result<CliInstallSymlinkPayload, String> {
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::CliInstallSymlink(
                proto::CliInstallSymlinkRequest { force },
            ))
            .await?,
    )?;

    match resp.result {
        Some(proto::response::Result::CliInstallSymlink(r)) => Ok(CliInstallSymlinkPayload {
            result: r.result,
            command: r.command,
            path: r.path,
            message: r.message,
        }),
        _ => Err("unexpected response for cli_install_symlink".to_string()),
    }
}

// relay_id names which frontend relay wrapper is asking, so the sidecar can ignore
// a close that a later open has already superseded. See the proto's comment on
// OpenChannelRelayRequest.
#[tauri::command]
async fn open_channel_relay(
    shell: State<'_, Arc<DesktopShell>>,
    relay_id: u64,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::OpenChannelRelay(
                proto::OpenChannelRelayRequest { relay_id },
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn send_channel_message(
    shell: State<'_, Arc<DesktopShell>>,
    b64_data: String,
) -> Result<(), String> {
    let data = decode_b64(&b64_data).map_err(|err| format!("decode channel message: {err}"))?;

    check_response(
        shell
            .send_request_async(proto::request::Method::SendChannelMessage(
                proto::SendChannelMessageRequest { data },
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn close_channel_relay(
    shell: State<'_, Arc<DesktopShell>>,
    relay_id: u64,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::CloseChannelRelay(
                proto::CloseChannelRelayRequest { relay_id },
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn open_orgevents_relay(
    shell: State<'_, Arc<DesktopShell>>,
    relay_id: u64,
    org_id: String,
    workspace_ids: Vec<String>,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::OpenOrgEventsRelay(
                proto::OpenOrgEventsRelayRequest {
                    relay_id,
                    org_id,
                    workspace_ids,
                },
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn close_orgevents_relay(
    shell: State<'_, Arc<DesktopShell>>,
    relay_id: u64,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::CloseOrgEventsRelay(
                proto::CloseOrgEventsRelayRequest { relay_id },
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn create_tunnel(
    shell: State<'_, Arc<DesktopShell>>,
    config: TunnelConfigInput,
) -> Result<TunnelInfoResponse, String> {
    let cfg = proto::TunnelConfig {
        worker_id: config.worker_id,
        r#type: config.r#type,
        target_addr: config.target_addr,
        target_port: config.target_port,
        bind_addr: config.bind_addr,
        bind_port: config.bind_port,
    };

    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::CreateTunnel(
                proto::CreateTunnelRequest { config: Some(cfg) },
            ))
            .await?,
    )?;

    match resp.result {
        Some(proto::response::Result::CreateTunnel(r)) => {
            if let Some(info) = r.info {
                Ok(proto_to_tunnel_info(&info))
            } else {
                Err("missing tunnel info in response".to_string())
            }
        }
        _ => Err("unexpected response for create_tunnel".to_string()),
    }
}

#[tauri::command]
async fn delete_tunnel(
    shell: State<'_, Arc<DesktopShell>>,
    tunnel_id: String,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::DeleteTunnel(
                proto::DeleteTunnelRequest { tunnel_id },
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn reset_tunnels(shell: State<'_, Arc<DesktopShell>>) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::ResetTunnels(
                proto::ResetTunnelsRequest {},
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn list_tunnels(
    shell: State<'_, Arc<DesktopShell>>,
) -> Result<Vec<TunnelInfoResponse>, String> {
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::ListTunnels(
                proto::ListTunnelsRequest {},
            ))
            .await?,
    )?;
    match resp.result {
        Some(proto::response::Result::ListTunnels(r)) => {
            Ok(r.tunnels.iter().map(proto_to_tunnel_info).collect())
        }
        _ => Err("unexpected response for list_tunnels".to_string()),
    }
}

fn proto_to_tunnel_info(info: &proto::TunnelInfo) -> TunnelInfoResponse {
    TunnelInfoResponse {
        id: info.id.clone(),
        worker_id: info.worker_id.clone(),
        r#type: info.r#type.clone(),
        bind_addr: info.bind_addr.clone(),
        bind_port: info.bind_port,
        target_addr: info.target_addr.clone(),
        target_port: info.target_port,
    }
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct DetectedEditorPayload {
    id: String,
    display_name: String,
}

#[tauri::command]
async fn list_editors(
    shell: State<'_, Arc<DesktopShell>>,
    refresh: Option<bool>,
) -> Result<Vec<DetectedEditorPayload>, String> {
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::ListEditors(
                proto::ListEditorsRequest {
                    refresh: refresh.unwrap_or(false),
                },
            ))
            .await?,
    )?;
    match resp.result {
        Some(proto::response::Result::ListEditors(r)) => Ok(r
            .editors
            .into_iter()
            .map(|e| DetectedEditorPayload {
                id: e.id,
                display_name: e.display_name,
            })
            .collect()),
        _ => Err("unexpected response for list_editors".to_string()),
    }
}

#[tauri::command]
async fn open_in_editor(
    shell: State<'_, Arc<DesktopShell>>,
    editor_id: String,
    path: String,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::OpenInEditor(
                proto::OpenInEditorRequest { editor_id, path },
            ))
            .await?,
    )?;
    Ok(())
}

fn decode_b64(b64: &str) -> Result<Vec<u8>, String> {
    base64::engine::general_purpose::STANDARD
        .decode(b64)
        .map_err(|e| e.to_string())
}

// File-save commands used by the frontend's download flow.
//
// Bytes traverse the Tauri IPC as the raw request body (`InvokeBody::Raw`),
// not base64 — for multi-MB downloads the encode/decode round-trip plus
// the ~33% wire bloat was the dominant cost. The filename rides along
// in a custom header, base64-encoded so HTTP-style ASCII restrictions
// don't mangle Unicode names.
//
// Saves are streamed through a `file_save_open[_dialog] → file_save_write* →
// file_save_commit | file_save_abort` chain. The Rust side keeps the
// destination `File` open in a registry between calls, so the JS caller
// can pipe each 1 MiB worker chunk straight through `file_save_write`
// without ever materializing the whole file. This bounds the peak
// transient memory (per chunk × ~3 copies across the IPC boundary) to
// a few MiB even for multi-hundred-MB downloads.
//
// Writes go to a sibling temp file (`<final>.leapmux.tmp`); on commit we
// atomic-rename it onto the final path, and on abort we delete it.
// Consequence: the final name never appears on disk until the save is
// complete, and (for Save as...) the user's existing file at the chosen
// path is preserved if the save fails. The Downloads variant iterates
// candidate names ("foo.ext",
// "foo (1).ext", ...) skipping any whose final path already exists and
// claiming each candidate's `<name>.leapmux.tmp` with `create_new` — that
// single open both reserves the iteration spot against concurrent
// LeapMux saves of the same basename and provides the file the bytes
// stream into.

fn read_header_str<'a>(
    request: &'a tauri::ipc::Request<'_>,
    name: &str,
) -> Result<&'a str, String> {
    request
        .headers()
        .get(name)
        .ok_or_else(|| format!("missing {name} header"))?
        .to_str()
        .map_err(|err| format!("invalid {name} header: {err}"))
}

fn read_b64_header(request: &tauri::ipc::Request<'_>, name: &str) -> Result<String, String> {
    let bytes = decode_b64(read_header_str(request, name)?)?;
    String::from_utf8(bytes).map_err(|err| format!("invalid {name} utf-8: {err}"))
}

/// Parse the decimal `handle-id` header shared by `file_save_write`,
/// `file_save_commit`, and `file_save_abort`.
fn read_handle_id(request: &tauri::ipc::Request<'_>) -> Result<u64, String> {
    read_header_str(request, "handle-id")?
        .parse()
        .map_err(|err| format!("invalid handle-id: {err}"))
}

/// Run a blocking closure on the dedicated blocking-thread pool and
/// return its result, surfacing a join failure as an error string. Used
/// for save-stream operations that touch the disk and shouldn't tie up
/// the async executor thread servicing other Tauri commands.
async fn run_blocking<F, T>(f: F) -> Result<T, String>
where
    F: FnOnce() -> Result<T, String> + Send + 'static,
    T: Send + 'static,
{
    tokio::task::spawn_blocking(f)
        .await
        .map_err(|err| format!("spawn_blocking join: {err}"))?
}

/// Cap on collision-dedup attempts. With "foo (N).ext" picking from
/// `1..MAX`, this bounds the directory scan and the suffix the user
/// sees ("foo (1023).ext" is well past the point where a different
/// filename is more useful than another increment).
const MAX_SAVE_COLLISION_ATTEMPTS: u32 = 1024;

/// Suffix appended to the final path to form the streaming partial.
/// Shared by the producer (`tmp_path_for`), the matcher (`is_partial_name`,
/// used by `sweep_orphan_tmps`), and the defuser (`defuse_final_path`) so
/// the three cannot drift.
///
/// Two invariants load-bearing for #285:
/// - Distinctive (`.leapmux.tmp`, not a bare `.tmp`): distinctive enough
///   that the startup sweep never matches a generic `*.tmp` some other
///   tool left in Downloads. This is a naming convention, not a hard
///   guarantee against a foreign file that deliberately reuses the
///   suffix; what makes the sweep safe for *our* files is that
///   `defuse_final_path` keeps every LeapMux final clear of the suffix,
///   so a match is a LeapMux partial by construction.
/// - Deterministic (no PID/randomness): `create_new` on this fixed
///   sibling name is what reserves a collision slot against concurrent
///   same-name saves.
const SAVE_TMP_SUFFIX: &str = ".leapmux.tmp";

/// Appended to a chosen final whose own name would match `is_partial_name`,
/// so a committed final can never be mistaken for a partial by the sweep.
/// The single source of truth for the defuse marker, shared by both defuse
/// sites (`defuse_final_path` and the inline defuse in `open_unique_tmp`) so
/// the two cannot drift — the same role `SAVE_TMP_SUFFIX` plays for the
/// partial suffix. Must not itself end in `SAVE_TMP_SUFFIX`.
const SAVE_DEFUSE_SUFFIX: &str = ".download";

/// How often the idle-handle GC scans the registry for handles whose
/// JS pump appears to have died. 60s keeps the scan cost negligible
/// while still bounding orphan-disk-junk lifetime to roughly
/// `SAVE_HANDLE_GC_INTERVAL + SAVE_HANDLE_IDLE_TIMEOUT`.
const SAVE_HANDLE_GC_INTERVAL: Duration = Duration::from_secs(60);

/// How long a handle can sit without a `write_chunk` (or `close`)
/// before the GC discards it. An active save touches `last_write_at`
/// per chunk, so the gap can only widen if the JS pump is wedged or
/// the renderer process died. 5 min is well above any realistic
/// per-chunk latency (1 MiB chunks rarely take more than seconds) but
/// short enough that an orphan partial is gone before the user notices.
const SAVE_HANDLE_IDLE_TIMEOUT: Duration = Duration::from_secs(300);

/// Registry entry for a save in progress: the open file plus the
/// paths needed to finalize or discard. Distinct from
/// `SaveStreamHandle`, which is the id+path token JS holds; this
/// struct is Rust-only and never crosses the IPC boundary.
struct OpenSaveStream {
    /// `Arc<Mutex<File>>` rather than `Mutex<File>` so `write_chunk` can
    /// short-lock the registry to clone the Arc, drop the registry
    /// lock, and then take the per-file lock — writes to different
    /// streams run in parallel instead of serializing on a registry-
    /// wide mutex.
    file: Arc<Mutex<std::fs::File>>,
    /// Sibling `<final>.leapmux.tmp` path that bytes stream into.
    tmp_path: PathBuf,
    /// Final destination — the partial is atomic-renamed onto this on success.
    final_path: PathBuf,
    /// Updated on insert and on every `write_chunk`. The idle-handle
    /// GC compares this against `SAVE_HANDLE_IDLE_TIMEOUT` to detect
    /// JS pumps that died without calling `file_save_commit` or
    /// `file_save_abort`. Lives under the registry `Mutex<HashMap>`
    /// lock so it shares the existing critical section instead of
    /// needing its own atomic.
    last_write_at: Instant,
}

/// Open destination files keyed by a monotonic u64 id. The JS caller
/// receives the id from `file_save_open[_dialog]` and submits it back
/// with each `file_save_write` and the final `file_save_commit` or
/// `file_save_abort`.
struct SaveStreamRegistry {
    /// Starts at 1 so a freshly constructed registry never hands out 0 —
    /// keeps "0 == sentinel" assumptions on the JS side safe.
    next_id: AtomicU64,
    // Poisoning makes every future download panic, and kills the 60s gc_idle
    // task permanently on its next tick; see the PendingMap comment above and
    // https://github.com/leapmux/leapmux/issues/277.
    handles: Mutex<HashMap<u64, OpenSaveStream>>,
}

impl SaveStreamRegistry {
    fn new() -> Self {
        Self {
            next_id: AtomicU64::new(1),
            handles: Mutex::new(HashMap::new()),
        }
    }

    /// Insert a freshly-opened file into the registry and return the
    /// JS-facing handle (id + the final path as a UTF-8 string).
    /// Both `file_save_open` and `file_save_open_dialog` end with this,
    /// so it lives here to keep the id/path packaging in one spot.
    fn insert(
        &self,
        file: std::fs::File,
        tmp_path: PathBuf,
        final_path: PathBuf,
    ) -> SaveStreamHandle {
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let path = final_path.to_string_lossy().into_owned();
        self.handles.lock().unwrap().insert(
            id,
            OpenSaveStream {
                file: Arc::new(Mutex::new(file)),
                tmp_path,
                final_path,
                last_write_at: Instant::now(),
            },
        );
        SaveStreamHandle { id, path }
    }

    fn take(&self, id: u64) -> Option<OpenSaveStream> {
        self.handles.lock().unwrap().remove(&id)
    }

    /// Finalize the save: take the handle, ensure no write is still in flight,
    /// then atomic-rename the partial onto the final path. On any failure the
    /// partial is discarded.
    ///
    /// The in-flight-write check is load-bearing. `write_chunk` clones the
    /// per-handle `Arc<Mutex<File>>` and writes with the registry lock
    /// released, so a duplicated/overlapping `file_save_write` (a buggy or
    /// retrying JS pump that does not await the previous write) could still be
    /// holding a clone when commit runs. Committing anyway would, on Windows,
    /// fail the rename with an opaque "used by another process" error, and on
    /// Unix SUCCEED while the in-flight write appends to the just-renamed file
    /// -- silent corruption. So after `take` (which removes the registry's own
    /// clone, and after which `write_chunk` can create no new one -- its
    /// `get_mut` returns None), `Arc::try_unwrap` is the reliable
    /// sole-ownership test: if it fails, a write clone is live, and commit
    /// fails loudly and discards the partial rather than racing it.
    fn commit(&self, id: u64) -> Result<(), String> {
        let Some(stream) = self.take(id) else {
            return Err(format!("unknown save handle {id}"));
        };
        let OpenSaveStream {
            file,
            tmp_path,
            final_path,
            last_write_at: _,
        } = stream;
        // Sole-ownership check: see the method doc. Arc::try_unwrap returns the
        // inner Mutex only when this is the last reference.
        let file = match Arc::try_unwrap(file) {
            Ok(file) => file,
            Err(_) => {
                discard_partials(&tmp_path);
                return Err(format!(
                    "save handle {id} has a write still in progress; commit refused"
                ));
            }
        };
        // No `sync_all` before the rename -- intentional. A `sync_all` on a
        // multi-hundred-MB save blocks for seconds while the OS flushes the
        // page cache, and neither flow has a contract that needs it: the
        // Downloads variant only ever writes to a path that was empty when we
        // picked the name (so a power-loss window between rename and OS flush
        // just loses the new file, it can't corrupt anything else), and the
        // Save-as variant's overwrite is already non-atomic at the
        // user-content level (the user picked a path knowing it would be
        // replaced, and can re-save on crash). Don't add a sync here without
        // matching the latency cost to a concrete guarantee we actually need to
        // make.
        //
        // Drop the File before the rename: Windows refuses `rename`/
        // `remove_file` while the handle is open. try_unwrap above proved this
        // is the sole owner, so this drop releases the underlying File.
        drop(file);
        // `std::fs::rename` replaces the destination on both Unix and Windows.
        // For save-as that overwrites the user's prior content (the path they
        // chose). For the Downloads flow the final path was empty when we
        // picked the name (`open_unique_tmp` skips candidates whose final
        // already exists); a file appearing there mid-stream is a TOCTOU race
        // we accept.
        let result = std::fs::rename(&tmp_path, &final_path).map_err(|err| format!("rename: {err}"));
        if result.is_err() {
            discard_partials(&tmp_path);
        }
        result
    }

    fn write_chunk(&self, id: u64, bytes: &[u8]) -> Result<(), String> {
        // Lock the registry only long enough to refresh the idle
        // timestamp and clone the per-handle Arc, then drop the
        // registry lock before acquiring the file lock. Concurrent
        // writes targeting different handles can then proceed in
        // parallel.
        let file = {
            let mut guard = self.handles.lock().unwrap();
            let handle = guard
                .get_mut(&id)
                .ok_or_else(|| format!("unknown save handle {id}"))?;
            handle.last_write_at = Instant::now();
            handle.file.clone()
        };
        let mut guard = file.lock().unwrap();
        guard
            .write_all(bytes)
            .map_err(|err| format!("write: {err}"))
    }

    /// Drop all open handles and remove any partial files. Called from
    /// the app exit path so an interrupted save doesn't leave junk on
    /// disk.
    fn cleanup_all(&self) {
        let drained: Vec<_> = self.handles.lock().unwrap().drain().collect();
        for (_, stream) in drained {
            discard_stream(stream);
        }
    }

    /// Discard handles whose `last_write_at` is older than `max_idle`.
    /// Two-phase: snapshot stale ids under a brief lock, then `take`
    /// each individually so the per-discard `remove_file` syscalls
    /// never run under the registry lock. A handle being actively
    /// written to during the scan window is fine — a racing
    /// `write_chunk` refreshes `last_write_at`, and the take in phase
    /// 2 re-checks against `max_idle` and skips it.
    ///
    /// This cleanup is in-memory only: it (and `cleanup_all` on graceful
    /// exit) covers a dead JS pump or a normal quit, but a hard process
    /// death (SIGKILL, crash, power loss) takes this registry down with it
    /// and strands the partial on disk. `sweep_orphan_tmps` reclaims those
    /// left in Downloads at the next startup (a Save-as partial stranded
    /// elsewhere is inert, not swept -- see `open_tmp_for_write`); until
    /// then a stale partial is indistinguishable from a live reservation
    /// and forces a spurious "(N)" suffix (see `open_unique_tmp`). See
    /// https://github.com/leapmux/leapmux/issues/285.
    fn gc_idle(&self, max_idle: Duration) {
        let now = Instant::now();
        let stale_ids: Vec<u64> = {
            let guard = self.handles.lock().unwrap();
            guard
                .iter()
                .filter(|(_, h)| now.duration_since(h.last_write_at) >= max_idle)
                .map(|(id, _)| *id)
                .collect()
        };
        for id in stale_ids {
            // Re-check under lock: a `write_chunk` racing between
            // snapshot and take may have refreshed the timestamp. If
            // it has, leave the stream alone.
            let stream = {
                let mut guard = self.handles.lock().unwrap();
                match guard.get(&id) {
                    Some(h) if now.duration_since(h.last_write_at) >= max_idle => guard.remove(&id),
                    _ => None,
                }
            };
            if let Some(stream) = stream {
                discard_stream(stream);
            }
        }
    }

    /// Delete orphaned save partials under `dir` left by a prior hard
    /// process death. Safe at the sole (startup) call site because three
    /// legs hold together there:
    ///
    /// 1. Distinctive suffix (`is_partial_name`) — a match is a LeapMux
    ///    save partial by construction: `defuse_final_path` keeps every
    ///    LeapMux *final* clear of `SAVE_TMP_SUFFIX`, and a generic `*.tmp`
    ///    from another tool never matches.
    /// 2. `tauri-plugin-single-instance` — no other LeapMux process can
    ///    be mid-save when this runs at startup.
    /// 3. The registry is empty and not yet `manage`d at the call site, so
    ///    none of our own saves is in flight.
    ///
    /// The live-`tmp_path` cross-check below spares any partial already in
    /// the registry, but it is NOT enough on its own to make a *periodic*
    /// call safe: `live` is snapshotted before `read_dir`, so a save that
    /// starts afterward is on disk yet absent from the snapshot, and the
    /// comparison is byte-exact on uncanonicalized paths. A future periodic
    /// caller must first close that race (snapshot under the same lock that
    /// guards insert, or re-check membership immediately before each
    /// remove) and pass the identical `dirs::download_dir()` value
    /// `file_save_open` uses. See https://github.com/leapmux/leapmux/issues/285.
    fn sweep_orphan_tmps(&self, dir: &Path) {
        let live: HashSet<PathBuf> = {
            let guard = self.handles.lock().unwrap();
            guard.values().map(|h| h.tmp_path.clone()).collect()
        };
        let entries = match std::fs::read_dir(dir) {
            Ok(entries) => entries,
            // A missing Downloads dir is benign (fresh machine, or a
            // relocated/unmounted volume) -- return quietly. Any other
            // failure (permissions, transient I/O) leaves orphans behind,
            // so log it rather than no-op invisibly, matching the per-entry
            // branches below.
            Err(err) if err.kind() == io::ErrorKind::NotFound => return,
            Err(err) => {
                eprintln!("leapmux: sweep read dir {}: {err}", dir.display());
                return;
            }
        };
        for entry in entries {
            // Log and skip a per-entry read error rather than silently
            // dropping it (as `entries.flatten()` would): an orphan behind a
            // transient error self-heals on a later launch, but the failure
            // should not be invisible. Mirrors the remove_file branch below.
            let entry = match entry {
                Ok(entry) => entry,
                Err(err) => {
                    eprintln!("leapmux: sweep read dir entry in {}: {err}", dir.display());
                    continue;
                }
            };
            if !is_partial_name(&entry.file_name()) {
                continue;
            }
            // Don't follow symlinks: dirs/symlinks named like partials
            // are spared. Log a stat failure rather than skip it silently,
            // as the read-dir and remove_file branches do.
            let ft = match entry.file_type() {
                Ok(ft) => ft,
                Err(err) => {
                    eprintln!("leapmux: sweep file type {}: {err}", entry.path().display());
                    continue;
                }
            };
            if !ft.is_file() {
                continue;
            }
            let path = entry.path();
            if live.contains(&path) {
                continue;
            }
            if let Err(err) = std::fs::remove_file(&path) {
                eprintln!(
                    "leapmux: sweep orphan save partial {}: {err}",
                    path.display()
                );
            }
        }
    }
}

/// Append `SAVE_TMP_SUFFIX` to `path` while preserving the existing
/// OsString (handles non-UTF-8 paths cleanly).
fn tmp_path_for(final_path: &Path) -> PathBuf {
    let mut name = final_path.as_os_str().to_owned();
    name.push(SAVE_TMP_SUFFIX);
    PathBuf::from(name)
}

/// Whether `name` is a save partial produced by `tmp_path_for`: it ends
/// in `SAVE_TMP_SUFFIX` and is *strictly* longer than the bare suffix. A
/// real final name is never empty, so a partial's name always exceeds the
/// suffix — a file named exactly `.leapmux.tmp` is therefore not ours and
/// is spared. This is the inverse of `tmp_path_for`, kept beside it so the
/// two can't drift, and the exact predicate `sweep_orphan_tmps` deletes
/// on and `defuse_final_path` protects finals against. Byte-wise, so
/// non-UTF-8 names are handled like `tmp_path_for`.
fn is_partial_name(name: &OsStr) -> bool {
    let bytes = name.as_encoded_bytes();
    bytes.len() > SAVE_TMP_SUFFIX.len() && bytes.ends_with(SAVE_TMP_SUFFIX.as_bytes())
}

/// Rewrite a chosen final `path` whose file name would be swept as an
/// orphan partial (`is_partial_name`) by appending `.download`, so a
/// committed final can never be mistaken for — and silently deleted as —
/// a partial by `sweep_orphan_tmps`. The OsString is preserved for
/// non-UTF-8 names. Both save entry points route their final through the
/// same reserved-suffix defuse: the Downloads auto-name flow inside
/// `open_unique_tmp`, and the Save-as dialog via this helper. Without it a
/// server-supplied name like `report.leapmux.tmp` (or a Save-as target
/// typed into Downloads) would commit a real final the next startup sweep
/// removes. See https://github.com/leapmux/leapmux/issues/285.
fn defuse_final_path(path: PathBuf) -> PathBuf {
    if path.file_name().is_some_and(is_partial_name) {
        let mut name = path.into_os_string();
        name.push(SAVE_DEFUSE_SUFFIX);
        PathBuf::from(name)
    } else {
        path
    }
}

/// Resolve a Save-as chosen path to its final write target, applying the
/// reserved-suffix defuse (`defuse_final_path`) and refusing when that defuse
/// redirects the write onto a pre-existing `.download` file the native dialog
/// never confirmed. The dialog's overwrite prompt only covers the name the
/// user picked; when that name ends in `SAVE_TMP_SUFFIX` the bytes actually
/// land on `<name>.download`, so silently replacing an existing file there
/// would destroy data the user was never asked about. The check runs only
/// when the defuse actually rewrote the path (the rare case), so a normal
/// dialog-confirmed overwrite is untouched. A residual TOCTOU window before
/// the commit rename degrades to the prior silent replace -- strictly better
/// than replacing unconditionally. See
/// https://github.com/leapmux/leapmux/issues/285.
fn resolve_save_as_final(chosen: PathBuf) -> Result<PathBuf, String> {
    let final_path = defuse_final_path(chosen.clone());
    if final_path != chosen {
        match final_path.try_exists() {
            Ok(true) => {
                return Err(format!(
                    "cannot save: {} already exists (a name ending in \
                     {SAVE_TMP_SUFFIX} was redirected to avoid the startup sweep)",
                    final_path.display()
                ))
            }
            Ok(false) => {}
            Err(err) => return Err(format!("stat {}: {err}", final_path.display())),
        }
    }
    Ok(final_path)
}

/// Open (or create+truncate) the temp sibling of `final_path` for
/// streaming writes. Used by the Save-as dialog flow
/// (`file_save_open_dialog`); the Downloads flow reserves and opens its
/// partial with `create_new` inside `open_unique_tmp` instead.
///
/// A hard death here strands the partial in the user's chosen directory.
/// `sweep_orphan_tmps` only reclaims Downloads, so a Save-as partial
/// elsewhere lingers until the user deletes it -- but it is inert: the
/// truncating open takes no collision reservation, so unlike a Downloads
/// orphan it forces no "(N)" suffix on later saves (#285).
fn open_tmp_for_write(final_path: &Path) -> Result<(std::fs::File, PathBuf), String> {
    let tmp_path = tmp_path_for(final_path);
    let tmp_file = std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .open(&tmp_path)
        .map_err(|err| format!("open tmp file: {err}"))?;
    Ok((tmp_file, tmp_path))
}

/// Remove the partial temp file. Used by both `discard_stream` and the
/// failure branches of `file_save_commit`, which have already dropped
/// the `File` themselves. The final path is never ours to remove —
/// nothing was ever created there.
fn discard_partials(tmp_path: &Path) {
    let _ = std::fs::remove_file(tmp_path);
}

/// Drop the file handle and remove the partial temp file.
fn discard_stream(stream: OpenSaveStream) {
    let OpenSaveStream { file, tmp_path, .. } = stream;
    // Drop before remove: Windows refuses `remove_file` while the
    // file handle is open.
    drop(file);
    discard_partials(&tmp_path);
}

/// JS-facing handle to an open save stream. Returned by
/// `file_save_open[_dialog]` and submitted back (as `id`) with each
/// `file_save_write` and the final `file_save_commit` /
/// `file_save_abort`. Mirrors the `SaveStreamHandle` interface in
/// `platformBridge.ts`.
#[derive(Serialize)]
struct SaveStreamHandle {
    id: u64,
    path: String,
}

/// Pick a non-colliding "foo (N).ext" candidate under `dir` and open
/// its `<candidate>.leapmux.tmp` sibling with `create_new`. The partial
/// open serves double duty: it both reserves the iteration spot against
/// concurrent LeapMux saves of the same basename and provides the file
/// the bytes stream into. The candidate itself is skipped if it already
/// exists, preserving the "don't silently overwrite a user file in
/// Downloads" behavior.
///
/// A stale orphaned partial left by a hard process crash is
/// indistinguishable from a live reservation, so it forces "(N)"
/// suffixes on later downloads of the same name until the next launch's
/// `sweep_orphan_tmps` reclaims it (#285). Filenames that themselves end
/// in `SAVE_TMP_SUFFIX` are defused by appending `.download` before the
/// collision loop — otherwise a committed final ending in the suffix
/// would be deleted by the next startup sweep.
fn open_unique_tmp(
    dir: PathBuf,
    filename: String,
) -> Result<(std::fs::File, PathBuf, PathBuf), String> {
    // Defuse reserved-suffix finals: a server-supplied name like
    // `report.leapmux.tmp` would otherwise commit a final the next
    // startup sweep would delete. Collision candidates built from the
    // defused name (`evil.leapmux.tmp (1).download`) never end in the
    // suffix. `file_save_open_dialog` applies the same defuse
    // (`defuse_final_path`) to Save-as targets.
    let filename = if is_partial_name(OsStr::new(&filename)) {
        format!("{filename}{SAVE_DEFUSE_SUFFIX}")
    } else {
        filename
    };
    let as_path = std::path::Path::new(&filename);
    let stem = as_path
        .file_stem()
        .map(|s| s.to_string_lossy().into_owned())
        .unwrap_or_default();
    let ext = as_path
        .extension()
        .map(|s| s.to_string_lossy().into_owned());
    for i in 0..MAX_SAVE_COLLISION_ATTEMPTS {
        let candidate_name = if i == 0 {
            filename.clone()
        } else {
            match &ext {
                Some(e) if !e.is_empty() => format!("{stem} ({i}).{e}"),
                _ => format!("{stem} ({i})"),
            }
        };
        let final_path = dir.join(&candidate_name);
        match final_path.try_exists() {
            Ok(true) => continue,
            Ok(false) => {}
            Err(err) => return Err(format!("stat {candidate_name}: {err}")),
        }
        let tmp_path = tmp_path_for(&final_path);
        match std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&tmp_path)
        {
            Ok(f) => return Ok((f, tmp_path, final_path)),
            Err(err) if err.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(err) => return Err(format!("create tmp file: {err}")),
        }
    }
    Err(format!(
        "too many collisions for {filename} (gave up after {MAX_SAVE_COLLISION_ATTEMPTS})"
    ))
}

/// Open a destination in the OS Downloads directory and return a
/// streaming handle. `filename` (from the `filename-b64` header) is
/// sanitized to its basename and collision-dedupped with " (N)".
#[tauri::command]
async fn file_save_open(
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<SaveStreamHandle, String> {
    let filename = read_b64_header(&request, "filename-b64")?;
    let downloads = dirs::download_dir().ok_or_else(|| "no downloads directory".to_string())?;
    // Disallow separators in the supplied filename so callers can't
    // escape the Downloads directory.
    let safe_name = std::path::Path::new(&filename)
        .file_name()
        .ok_or_else(|| "invalid filename".to_string())?
        .to_string_lossy()
        .into_owned();
    let registry = registry.inner().clone();
    let (file, tmp_path, final_path) =
        run_blocking(move || open_unique_tmp(downloads, safe_name)).await?;
    Ok(registry.insert(file, tmp_path, final_path))
}

/// Show a native save-as dialog and return a streaming handle for the
/// chosen path. Returns `None` when the user cancels — JS callers
/// should short-circuit before any worker fetch so a cancellation
/// costs nothing.
#[tauri::command]
async fn file_save_open_dialog(
    app: tauri::AppHandle,
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<Option<SaveStreamHandle>, String> {
    use tauri_plugin_dialog::DialogExt;

    let default_name = read_b64_header(&request, "default-name-b64")?;
    let (tx, rx) = oneshot::channel();
    app.dialog()
        .file()
        .set_file_name(&default_name)
        .save_file(move |path| {
            let _ = tx.send(path);
        });
    let path_opt = rx.await.map_err(|e| e.to_string())?;
    let Some(file_path) = path_opt else {
        return Ok(None);
    };
    // Defuse a Save-as target whose name ends in the reserved partial
    // suffix so the next startup sweep can't mistake the committed final
    // for an orphan and delete it (#285) -- the same guard `open_unique_tmp`
    // applies to the Downloads flow. This runs after the native dialog's
    // own overwrite prompt, so for such a name the `.download` variant is
    // what actually gets written; it is unconditional (not scoped to
    // Downloads) so no reserved-suffix final ever reaches disk to be swept,
    // whatever the directory's path spelling. Only a name literally ending
    // in `.leapmux.tmp` is affected, which a real download never produces --
    // and if that redirect would land on an existing `.download` the dialog
    // never confirmed, `resolve_save_as_final` errors rather than clobber it.
    let final_path = resolve_save_as_final(file_path.into_path().map_err(|e| e.to_string())?)?;
    let registry = registry.inner().clone();
    let (file, tmp_path) = run_blocking({
        let final_path = final_path.clone();
        move || open_tmp_for_write(&final_path)
    })
    .await?;
    Ok(Some(registry.insert(file, tmp_path, final_path)))
}

/// Append the request body bytes to the open file identified by the
/// decimal `handle-id` header. Uses `block_in_place` rather than
/// `spawn_blocking` so the body slice can be borrowed directly from
/// the request without a per-chunk clone — for a 100 MB save that
/// avoids ~100 MiB of memcpy traffic.
#[tauri::command]
async fn file_save_write(
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<(), String> {
    let handle_id = read_handle_id(&request)?;
    let bytes = match request.body() {
        tauri::ipc::InvokeBody::Raw(b) => b.as_slice(),
        _ => return Err("expected raw bytes body".to_string()),
    };
    // Tauri's command executor runs on a multi-thread tokio runtime,
    // so `block_in_place` is safe here: it parks the current worker
    // for the duration of the write and lets the runtime steal other
    // tasks. The write is bounded to one chunk (~1 MiB). The debug
    // assertion makes a future runtime-config regression (e.g. switching
    // to `current_thread`) fail with a clear message instead of tokio's
    // generic "can call `blocking` only from a `MultiThread`" panic.
    debug_assert_eq!(
        tokio::runtime::Handle::current().runtime_flavor(),
        tokio::runtime::RuntimeFlavor::MultiThread,
        "file_save_write uses block_in_place; requires a multi-thread runtime",
    );
    tokio::task::block_in_place(|| registry.write_chunk(handle_id, bytes))
}

/// Finalize the save identified by `handle-id`: sync bytes to disk and
/// atomic-rename the partial onto the final path. Discards partials on
/// failure so a partial sync doesn't leave a junk file under the
/// chosen name.
#[tauri::command]
async fn file_save_commit(
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<(), String> {
    let handle_id = read_handle_id(&request)?;
    let registry = registry.inner().clone();
    run_blocking(move || registry.commit(handle_id)).await
}

/// Discard the save identified by `handle-id`: drop the open file and
/// remove the partial. Idempotent against an already-removed
/// handle (e.g. the idle GC raced the JS pump) so the failure path on
/// the JS side stays simple.
#[tauri::command]
async fn file_save_abort(
    registry: State<'_, Arc<SaveStreamRegistry>>,
    request: tauri::ipc::Request<'_>,
) -> Result<(), String> {
    let handle_id = read_handle_id(&request)?;
    let registry = registry.inner().clone();
    run_blocking(move || {
        if let Some(stream) = registry.take(handle_id) {
            discard_stream(stream);
        }
        Ok(())
    })
    .await
}

#[tauri::command]
async fn switch_mode(
    shell: State<'_, Arc<DesktopShell>>,
    window: WebviewWindow,
) -> Result<(), String> {
    let response = shell
        .send_request_async(proto::request::Method::SwitchMode(
            proto::SwitchModeRequest {},
        ))
        .await?;
    let (info, cleanup_errors) = lifecycle_from_response(response)?;
    apply_sidecar_info(&shell.state, info);

    let local_app_url = shell.state.lock().unwrap().local_app_url.clone();
    let (target_url, cleanup_message) = launcher_url(&local_app_url, &cleanup_errors)?;
    if let Err(err) = window.navigate(target_url) {
        if cleanup_message.is_empty() {
            return Err(format!("navigate to launcher: {err}"));
        }
        return Err(format!(
            "navigate to launcher: {err}; cleanup also failed: {cleanup_message}"
        ));
    }
    Ok(())
}

fn launcher_url(local_app_url: &str, cleanup_errors: &[String]) -> Result<(Url, String), String> {
    let mut target_url =
        Url::parse(local_app_url).map_err(|err| format!("parse launcher url: {err}"))?;
    let cleanup_message = cleanup_errors.join("\n");
    if !cleanup_message.is_empty() {
        target_url
            .query_pairs_mut()
            .append_pair("cleanup_error", &cleanup_message);
    }
    Ok((target_url, cleanup_message))
}

// restart_app is macOS-only: only the Full Disk Access flow needs the app
// to relaunch itself, and FDA is macOS-only.
#[cfg(target_os = "macos")]
#[tauri::command]
async fn restart_app(
    shell: State<'_, Arc<DesktopShell>>,
    _window: WebviewWindow,
) -> Result<(), String> {
    let current_exe =
        std::env::current_exe().map_err(|err| format!("resolve current exe: {err}"))?;
    let app_bundle = current_exe
        .ancestors()
        .find(|p| p.extension().is_some_and(|e| e == "app"))
        .unwrap_or(&current_exe)
        .to_path_buf();

    // The single-instance plugin kills any second instance that starts while
    // the primary is still alive, so the relaunch helper polls for the parent
    // PID to disappear before invoking the new instance via LaunchServices.
    let parent_pid = std::process::id();
    Command::new("/bin/sh")
        .arg("-c")
        .arg(format!(
            "while kill -0 {pid} 2>/dev/null; do sleep 0.1; done; \
             exec /usr/bin/open -n {bundle:?}",
            pid = parent_pid,
            bundle = app_bundle,
        ))
        .spawn()
        .map_err(|err| format!("restart app: {err}"))?;
    shell.app_handle.exit(0);
    Ok(())
}

#[tauri::command]
async fn save_window_geometry(
    shell: State<'_, Arc<DesktopShell>>,
    width: u32,
    height: u32,
    mode: String,
) -> Result<(), String> {
    shell.save_window_size(width, height, mode).await
}

#[tauri::command]
fn quit_app(app: AppHandle) {
    if let Some(shell) = app.try_state::<Arc<DesktopShell>>() {
        handle_app_exit(shell.inner().clone());
    } else {
        app.exit(0);
    }
}

#[tauri::command]
fn open_web_inspector(app: AppHandle) {
    open_main_web_inspector(&app);
}

#[tauri::command]
fn set_menu_item_accelerator(
    app: AppHandle,
    item_id: String,
    accelerator: Option<String>,
) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        let menu = app
            .menu()
            .ok_or_else(|| "app menu is not available".to_string())?;
        let app_menu = menu
            .get(APP_SUBMENU_ID)
            .and_then(|item| item.as_submenu().cloned());
        let help_menu = menu
            .get(HELP_SUBMENU_ID)
            .and_then(|item| item.as_submenu().cloned());
        let item = app_menu
            .as_ref()
            .and_then(|submenu| submenu.get(&item_id))
            .or_else(|| help_menu.as_ref().and_then(|submenu| submenu.get(&item_id)))
            .ok_or_else(|| format!("menu item not found: {item_id}"))?;
        let menu_item = item
            .as_menuitem()
            .ok_or_else(|| format!("menu item is not a standard menu item: {item_id}"))?;
        menu_item
            .set_accelerator(accelerator.as_deref())
            .map_err(|err| format!("set accelerator for {item_id}: {err}"))?;
    }

    #[cfg(not(target_os = "macos"))]
    let _ = (app, item_id, accelerator);

    Ok(())
}

#[tauri::command]
fn zoom_in_webview(shell: State<'_, Arc<DesktopShell>>) -> Result<(), String> {
    shell.set_zoom(shell.current_zoom() + 0.1)
}

#[tauri::command]
fn zoom_out_webview(shell: State<'_, Arc<DesktopShell>>) -> Result<(), String> {
    shell.set_zoom(shell.current_zoom() - 0.1)
}

#[tauri::command]
fn reset_webview_zoom(shell: State<'_, Arc<DesktopShell>>) -> Result<(), String> {
    shell.set_zoom(1.0)
}

// --- Window/app helpers ---

fn focus_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
    }
}

fn open_main_web_inspector(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        window.open_devtools();
    }
}

fn handle_main_window_close(shell: Arc<DesktopShell>, window: Window) {
    if shell
        .close_in_progress
        .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
        .is_err()
    {
        return;
    }

    tauri::async_runtime::spawn(async move {
        let _ = window.close();
    });
}

fn handle_app_exit(shell: Arc<DesktopShell>) {
    if shell
        .exit_in_progress
        .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
        .is_err()
    {
        return;
    }

    tauri::async_runtime::spawn(async move {
        shell.request_shutdown_async().await;
        shell.app_handle.exit(0);
    });
}

#[cfg(target_os = "macos")]
fn build_app_menu(app: &AppHandle) -> tauri::Result<Menu<tauri::Wry>> {
    let show_about = MenuItem::with_id(
        app,
        SHOW_ABOUT_MENU_ID,
        "About LeapMux Desktop...",
        true,
        None::<&str>,
    )?;

    let show_preferences = MenuItem::with_id(
        app,
        SHOW_PREFERENCES_MENU_ID,
        "Preferences...",
        true,
        None::<&str>,
    )?;

    let open_web_inspector = MenuItem::with_id(
        app,
        OPEN_WEB_INSPECTOR_MENU_ID,
        "Open Web Inspector",
        true,
        None::<&str>,
    )?;

    let app_menu = Submenu::with_id_and_items(
        app,
        APP_SUBMENU_ID,
        "LeapMux Desktop",
        true,
        &[
            &show_about,
            &show_preferences,
            &PredefinedMenuItem::separator(app)?,
            &PredefinedMenuItem::services(app, None)?,
            &PredefinedMenuItem::separator(app)?,
            &PredefinedMenuItem::hide(app, None)?,
            &PredefinedMenuItem::hide_others(app, None)?,
            &PredefinedMenuItem::separator(app)?,
            &PredefinedMenuItem::quit(app, None)?,
        ],
    )?;

    let edit_menu = Submenu::with_items(
        app,
        "Edit",
        true,
        &[
            &PredefinedMenuItem::undo(app, None)?,
            &PredefinedMenuItem::redo(app, None)?,
            &PredefinedMenuItem::separator(app)?,
            &PredefinedMenuItem::cut(app, None)?,
            &PredefinedMenuItem::copy(app, None)?,
            &PredefinedMenuItem::paste(app, None)?,
            &PredefinedMenuItem::select_all(app, None)?,
        ],
    )?;

    let view_menu = Submenu::with_items(
        app,
        "View",
        true,
        &[&PredefinedMenuItem::fullscreen(app, None)?],
    )?;

    let window_menu = Submenu::with_id_and_items(
        app,
        tauri::menu::WINDOW_SUBMENU_ID,
        "Window",
        true,
        &[
            &PredefinedMenuItem::minimize(app, None)?,
            &PredefinedMenuItem::maximize(app, None)?,
            &PredefinedMenuItem::separator(app)?,
            &PredefinedMenuItem::close_window(app, None)?,
        ],
    )?;

    let help_menu =
        Submenu::with_id_and_items(app, HELP_SUBMENU_ID, "Help", true, &[&open_web_inspector])?;

    Menu::with_items(
        app,
        &[&app_menu, &edit_menu, &view_menu, &window_menu, &help_menu],
    )
}

fn main() {
    // Work around known WebKitGTK DMA-BUF renderer issues on Linux:
    // - DMA-BUF renderer fails with "Failed to create GBM buffer"
    // Disabling DMA-BUF avoids GPU buffer management issues while
    // keeping native Wayland support.
    #[cfg(target_os = "linux")]
    {
        std::env::set_var("WEBKIT_DISABLE_DMABUF_RENDERER", "1");

        // Pin GStreamer's registry cache to a stable per-user file so
        // the plugin scan survives across launches and doesn't collide
        // with any system-wide GStreamer registry the user may have.
        // Per the XDG Base Directory Spec, XDG_CACHE_HOME must be an
        // absolute path; treat empty or relative values as unset and
        // fall back to $HOME/.cache. Skip pinning entirely if neither
        // resolves to an absolute path — a relative GST_REGISTRY would
        // be resolved against the process working directory.
        let cache_root = std::env::var_os("XDG_CACHE_HOME")
            .map(PathBuf::from)
            .filter(|p| p.is_absolute())
            .or_else(|| {
                std::env::var_os("HOME")
                    .map(PathBuf::from)
                    .filter(|p| p.is_absolute())
                    .map(|home| home.join(".cache"))
            });
        if let Some(dir) = cache_root.map(|root| root.join("leapmux")) {
            if std::fs::create_dir_all(&dir).is_ok() {
                std::env::set_var("GST_REGISTRY", dir.join("gstreamer-registry.bin"));
            }
        }
    }

    let builder = tauri::Builder::default()
        .plugin(tauri_plugin_clipboard_manager::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            focus_main_window(app);
        }));

    // Linux and Windows render the app menu as a hamburger dropdown inside
    // the custom titlebar (`CustomTitlebar.tsx`). Only macOS uses a native
    // Tauri menu (the system-wide Apple menu bar).
    #[cfg(target_os = "macos")]
    let builder = builder.menu(build_app_menu);

    builder
        .on_menu_event(|app, event| {
            #[cfg(target_os = "macos")]
            if event.id() == SHOW_ABOUT_MENU_ID {
                let _ = app.emit("menu:show-about", ());
            } else if event.id() == SHOW_PREFERENCES_MENU_ID {
                let _ = app.emit("menu:show-preferences", ());
            } else if event.id() == OPEN_WEB_INSPECTOR_MENU_ID {
                open_main_web_inspector(app);
            }
            #[cfg(not(target_os = "macos"))]
            let _ = (app, event);
        })
        .on_window_event(|window, event| {
            if window.label() != "main" {
                return;
            }

            if let WindowEvent::CloseRequested { api, .. } = event {
                if let Some(shell) = window.app_handle().try_state::<Arc<DesktopShell>>() {
                    if !shell.close_in_progress.load(Ordering::SeqCst) {
                        api.prevent_close();
                        handle_main_window_close(shell.inner().clone(), window.clone());
                    }
                }
            }
        })
        .setup(|app| {
            // titleBarStyle "Overlay" is a macOS-only option. On Linux and
            // Windows the native decorations are left in place by default,
            // which causes the OS title bar to render alongside our custom
            // one. Drop the native decorations so the frontend can draw its
            // own drag region and window controls end-to-end.
            #[cfg(any(target_os = "linux", target_os = "windows"))]
            if let Some(w) = app.get_webview_window("main") {
                let _ = w.set_decorations(false);
            }

            // Work around WebKitGTK's GTK-level Tab focus traversal so
            // ProseMirror can receive Tab/Shift+Tab keydowns. See
            // `tabfix_linux.rs` for the rationale.
            #[cfg(target_os = "linux")]
            if let Some(w) = app.get_webview_window("main") {
                tabfix_linux::install(&w);
            }

            // Safety net: if the frontend doesn't show the window within 5s
            // (e.g. JS error), show it anyway to avoid an invisible app.
            let handle = app.handle().clone();
            std::thread::spawn(move || {
                std::thread::sleep(std::time::Duration::from_secs(5));
                if let Some(w) = handle.get_webview_window("main") {
                    let _ = w.show();
                }
            });

            let shell = Arc::new(DesktopShell::new(app.handle().clone())?);
            let runtime_state = shell.runtime_state();
            if runtime_state.connected && runtime_state.shell_mode == ShellMode::Distributed {
                if let Some(window) = app.get_webview_window("main") {
                    let target_url = Url::parse(&runtime_state.hub_url)
                        .map_err(|err| format!("parse reattach hub url: {err}"))?;
                    window
                        .navigate(target_url)
                        .map_err(|err| format!("navigate to reattached hub: {err}"))?;
                }
            }
            app.manage(shell);
            let save_registry = Arc::new(SaveStreamRegistry::new());
            // Reclaim orphaned save partials left by a prior hard death
            // (#285). Synchronous and pre-`manage`: every save command
            // resolves the registry via managed `State`, so no save can
            // be in flight yet; a spawned sweep could race
            // `file_save_open` between `create_new` and `registry.insert`.
            // Single-instance + distinctive suffix make every matching
            // on-disk file at this point definitionally an orphan.
            if let Some(downloads) = dirs::download_dir() {
                save_registry.sweep_orphan_tmps(&downloads);
            }
            // Background GC for orphan save handles: when the renderer
            // dies mid-stream (page reload, crash) the JS pump never
            // calls `file_save_commit` or `file_save_abort`, leaving
            // the handle + its partial file alive until `cleanup_all`
            // at app exit. The GC bounds that lifetime to roughly
            // `IDLE_TIMEOUT + GC_INTERVAL`.
            let gc_registry = save_registry.clone();
            tauri::async_runtime::spawn(async move {
                let mut interval = tokio::time::interval(SAVE_HANDLE_GC_INTERVAL);
                loop {
                    interval.tick().await;
                    gc_registry.gc_idle(SAVE_HANDLE_IDLE_TIMEOUT);
                }
            });
            app.manage(save_registry);
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            get_runtime_state,
            get_startup_info,
            check_full_disk_access,
            open_full_disk_access_settings,
            connect_solo,
            connect_distributed,
            proxy_http,
            cli_path_status,
            cli_install_symlink,
            open_channel_relay,
            send_channel_message,
            close_channel_relay,
            open_orgevents_relay,
            close_orgevents_relay,
            create_tunnel,
            delete_tunnel,
            reset_tunnels,
            list_tunnels,
            list_editors,
            open_in_editor,
            file_save_open,
            file_save_open_dialog,
            file_save_write,
            file_save_commit,
            file_save_abort,
            switch_mode,
            #[cfg(target_os = "macos")]
            restart_app,
            save_window_geometry,
            quit_app,
            open_web_inspector,
            set_menu_item_accelerator,
            zoom_in_webview,
            zoom_out_webview,
            reset_webview_zoom,
        ])
        .build(tauri::generate_context!())
        .expect("error while building LeapMux desktop")
        .run(|app, event| {
            if let RunEvent::ExitRequested { api, .. } = event {
                if let Some(shell) = app.try_state::<Arc<DesktopShell>>() {
                    if !shell.exit_in_progress.load(Ordering::SeqCst) {
                        // Drop any open save handles and remove their
                        // partial files before shutting the sidecar
                        // down. The CAS inside `handle_app_exit`
                        // guarantees we run this exactly once.
                        if let Some(registry) = app.try_state::<Arc<SaveStreamRegistry>>() {
                            registry.cleanup_all();
                        }
                        api.prevent_exit();
                        handle_app_exit(shell.inner().clone());
                    }
                }
            }
        });
}

/// A test-only global allocator that records the largest single allocation made
/// on each thread.
///
/// It exists for `read_frame_async_rejects_oversize_varint_before_allocating`.
/// That test's contract -- reject an oversize length prefix *without allocating
/// the payload* -- cannot be pinned by asserting on the returned error: the very
/// same "frame too large" surfaces whether the `MAX_FRAME_SIZE` check runs
/// before the payload `vec!` or after it. Only measuring the allocation tells
/// the two apart, and the difference is the whole point of the check: a peer
/// that sends a bogus varint must not be able to make us allocate gigabytes.
#[cfg(test)]
mod alloc_probe {
    use std::alloc::{GlobalAlloc, Layout, System};
    use std::cell::Cell;

    thread_local! {
        // Deliberately thread-local rather than a global counter: the test
        // harness runs tests in parallel threads, and a shared counter would
        // make one test's allocations visible to another. `const` init keeps
        // this free of a destructor, so the allocator can't re-enter TLS setup.
        static PEAK: Cell<usize> = const { Cell::new(0) };
    }

    fn record(size: usize) {
        // `try_with` (not `with`): the allocator stays live during TLS
        // teardown, after PEAK has been destroyed. Nothing to record then.
        let _ = PEAK.try_with(|peak| peak.set(peak.get().max(size)));
    }

    pub struct PeakTracking;

    // SAFETY: every method forwards to `System` with the same arguments it was
    // given; the only added work is recording the requested size.
    unsafe impl GlobalAlloc for PeakTracking {
        unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
            record(layout.size());
            unsafe { System.alloc(layout) }
        }

        // `vec![0u8; n]` lands here, not in `alloc`, via the zeroing
        // specialization -- which is exactly the allocation under test.
        unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
            record(layout.size());
            unsafe { System.alloc_zeroed(layout) }
        }

        unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
            record(new_size);
            unsafe { System.realloc(ptr, layout, new_size) }
        }

        unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
            unsafe { System.dealloc(ptr, layout) }
        }
    }

    /// Runs `f` on the current thread, returning its value alongside the
    /// largest single allocation it requested.
    ///
    /// Only allocations on the calling thread are counted, so `f` must do the
    /// work itself rather than hand it to another thread. `Runtime::block_on`
    /// qualifies: it drives the future on the caller.
    pub fn peak_alloc_of<T>(f: impl FnOnce() -> T) -> (T, usize) {
        PEAK.with(|peak| peak.set(0));
        let out = f();
        (out, PEAK.with(|peak| peak.get()))
    }
}

#[cfg(test)]
#[global_allocator]
static PEAK_TRACKING_ALLOCATOR: alloc_probe::PeakTracking = alloc_probe::PeakTracking;

#[cfg(test)]
mod tests {
    use super::*;
    use std::io;
    use std::sync::atomic::AtomicU64;
    use std::time::SystemTime;

    #[cfg(unix)]
    use std::os::unix::net::UnixListener;

    #[cfg(windows)]
    use tokio::net::windows::named_pipe::{NamedPipeServer, ServerOptions};

    static TEST_COUNTER: AtomicU64 = AtomicU64::new(0);

    // A private endpoint must be distinct from the shared one, and stable within a
    // process.
    //
    // The shared per-user endpoint is a reuse CACHE, not a requirement: when it cannot
    // be reclaimed -- a wedged leftover that ignores a cooperative shutdown, or another
    // user's socket -- the launch falls back here rather than aborting. Aborting is
    // what one SIGKILLed `task test-e2e` leftover used to do to every later `task dev`,
    // and the alternative (killing it) is the arbitrary-process-kill primitive this
    // shell deliberately gave up.
    #[cfg(unix)]
    #[test]
    fn private_dev_sidecar_endpoint_is_distinct_and_stable() {
        let shared = dev_sidecar_endpoint();
        let private = private_dev_sidecar_endpoint();
        assert_ne!(
            private, shared,
            "the fallback must not collide with the squatted path"
        );
        assert_eq!(
            private,
            private_dev_sidecar_endpoint(),
            "stable within a process"
        );
        assert!(
            private.contains(&std::process::id().to_string()),
            "the fallback is keyed on OUR pid, so nothing else holds it: {private}"
        );
        assert!(
            private.ends_with(".sock"),
            "still a unix socket path: {private}"
        );
    }

    // The dev sidecar socket sits at a predictable path, and everything downstream of
    // the connect trusts the peer's self-reported protocol version and binary hash --
    // a hash being exactly as forgeable as the PID force_kill_sidecar used to trust.
    // The peer's uid is the one fact it cannot assert, so the connect must check it.
    // The REFUSAL is the branch that matters, and binding a socket as another user
    // needs root -- so it is driven through require_peer_uid directly. Without this the
    // only coverage is the accept path, which would pass just as well if the check
    // always returned Ok.
    #[cfg(unix)]
    #[test]
    fn require_peer_uid_refuses_a_foreign_owner() {
        let us = unsafe { libc::getuid() };
        let err = require_peer_uid(us + 1, us, "/tmp/leapmux-desktop/x.sock")
            .expect_err("a socket answered by another uid must be refused");
        assert!(err.contains("refusing sidecar"), "{err}");
        assert!(
            err.contains(&(us + 1).to_string()),
            "names the squatter's uid: {err}"
        );

        // root answering is still not us.
        require_peer_uid(0, us, "/tmp/leapmux-desktop/x.sock")
            .expect_err("uid 0 is not this user either");
    }

    #[cfg(unix)]
    #[test]
    fn require_peer_uid_accepts_our_own_uid() {
        let us = unsafe { libc::getuid() };
        require_peer_uid(us, us, "/tmp/leapmux-desktop/x.sock").expect("our own uid is accepted");
    }

    #[cfg(unix)]
    #[test]
    fn require_same_user_peer_accepts_our_own_socket() {
        let counter = TEST_COUNTER.fetch_add(1, Ordering::SeqCst);
        let path = std::env::temp_dir().join(format!("leapmux-peercred-{counter}.sock"));
        let _ = fs::remove_file(&path);
        let listener = UnixListener::bind(&path).expect("bind");
        let accepted = thread::spawn(move || listener.accept().map(|(s, _)| s));

        let stream = UnixStream::connect(&path).expect("connect");
        // We bound the listener ourselves, so the peer IS us.
        require_same_user_peer(&stream, path.to_str().unwrap())
            .expect("our own socket is accepted");

        drop(accepted.join().expect("accept thread").expect("accept"));
        let _ = fs::remove_file(&path);
    }

    // ...and the uid it reports is the kernel's, not anything the peer chose: it must
    // match this process, since that is what the check compares against.
    #[cfg(unix)]
    #[test]
    fn socket_peer_uid_reports_the_kernel_recorded_owner() {
        let counter = TEST_COUNTER.fetch_add(1, Ordering::SeqCst);
        let path = std::env::temp_dir().join(format!("leapmux-peeruid-{counter}.sock"));
        let _ = fs::remove_file(&path);
        let listener = UnixListener::bind(&path).expect("bind");
        let accepted = thread::spawn(move || listener.accept().map(|(s, _)| s));

        let stream = UnixStream::connect(&path).expect("connect");
        let uid = socket_peer_uid(&stream).expect("peer uid");
        assert_eq!(
            uid,
            unsafe { libc::getuid() },
            "the peer of our own socket is us"
        );

        drop(accepted.join().expect("accept thread").expect("accept"));
        let _ = fs::remove_file(&path);
    }

    // socket_peer_pid / endpoint_holder_pid report the KERNEL-recorded peer pid,
    // used only to make the "an orphan holds the endpoint" diagnostic actionable.
    // The peer of a socket we connect to ourselves is this process, so both must
    // report our own pid.
    #[cfg(unix)]
    #[test]
    fn endpoint_holder_pid_reports_the_kernel_recorded_peer() {
        let counter = TEST_COUNTER.fetch_add(1, Ordering::SeqCst);
        let path = std::env::temp_dir().join(format!("leapmux-peerpid-{counter}.sock"));
        let _ = fs::remove_file(&path);
        let listener = UnixListener::bind(&path).expect("bind");
        // Accept exactly the two connections this test makes: the direct
        // socket_peer_pid read below, and endpoint_holder_pid's own throwaway
        // connection. The accepted streams are held until the thread ends so the
        // peers stay connected while their pids are read.
        let acceptor = thread::spawn(move || {
            let mut held = Vec::new();
            for _ in 0..2 {
                if let Ok((stream, _)) = listener.accept() {
                    held.push(stream);
                }
            }
            held
        });

        let stream = UnixStream::connect(&path).expect("connect");
        assert_eq!(
            socket_peer_pid(&stream),
            Some(std::process::id()),
            "the peer of our own socket is this process"
        );
        assert_eq!(
            endpoint_holder_pid(path.to_str().expect("utf8 path")),
            Some(std::process::id()),
            "endpoint_holder_pid names the kernel-recorded holder"
        );

        drop(stream);
        let _ = acceptor.join();
        let _ = fs::remove_file(&path);
    }

    // A path nothing is listening on has no holder pid (rather than erroring).
    #[cfg(unix)]
    #[test]
    fn endpoint_holder_pid_is_none_for_an_absent_endpoint() {
        let counter = TEST_COUNTER.fetch_add(1, Ordering::SeqCst);
        let path = std::env::temp_dir().join(format!("leapmux-peerpid-absent-{counter}.sock"));
        let _ = fs::remove_file(&path);
        assert_eq!(endpoint_holder_pid(path.to_str().expect("utf8 path")), None);
    }

    // ---- Unix-specific helpers and tests ----

    #[cfg(unix)]
    fn unique_test_socket_path() -> PathBuf {
        let counter = TEST_COUNTER.fetch_add(1, Ordering::SeqCst);
        let nanos = SystemTime::now()
            .duration_since(SystemTime::UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let pid = std::process::id();
        std::env::temp_dir().join(format!("leapmux-test-{pid}-{nanos}-{counter}.sock"))
    }

    #[cfg(unix)]
    fn spawn_fake_sidecar(socket_path: PathBuf) -> thread::JoinHandle<()> {
        let listener = UnixListener::bind(&socket_path).expect("bind fake sidecar");
        thread::spawn(move || {
            let (mut stream, _) = listener.accept().expect("accept fake sidecar");
            // Consume the GetSidecarInfo request so the handshake completes.
            let _ = read_frame(&mut stream).expect("read handshake request");
            let mut info = sidecar_info(proto::SidecarShellMode::Unspecified, false, "");
            info.pid = std::process::id() as i64;
            let response = proto::Frame {
                message: Some(proto::frame::Message::Response(proto::Response {
                    id: 1,
                    error: String::new(),
                    result: Some(proto::response::Result::SidecarInfo(info)),
                })),
            };
            write_frame(&mut stream, &response).expect("write handshake response");
            // Hold the connection open so the client can inspect its stream.
            // The test drops its reader to signal completion.
            let _ = stream.read(&mut [0u8; 1]);
        })
    }

    #[cfg(unix)]
    #[test]
    fn connect_and_handshake_clears_stream_timeouts() {
        let socket_path = unique_test_socket_path();
        let server = spawn_fake_sidecar(socket_path.clone());

        let endpoint = socket_path.to_str().expect("socket path is utf-8");
        let (reader, writer, info) = connect_and_handshake_dev_sidecar(endpoint)
            .expect("handshake ok")
            .expect("server present");

        assert_eq!(info.protocol_version, SIDECAR_PROTOCOL_VERSION);
        // Without the fix these return `Some(DEV_SIDECAR_HANDSHAKE_TIMEOUT)`,
        // which causes the long-lived reader thread to see EAGAIN
        // ("Resource temporarily unavailable (os error 35)") after the
        // handshake timeout of idle and tear the sidecar connection down.
        assert_eq!(reader.read_timeout().expect("read_timeout"), None);
        assert_eq!(writer.write_timeout().expect("write_timeout"), None);

        drop(reader);
        drop(writer);
        server.join().expect("fake sidecar thread");
        let _ = fs::remove_file(&socket_path);
    }

    // ---- Windows-specific helpers and tests ----

    #[cfg(windows)]
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
    #[cfg(windows)]
    fn start_test_pipe_server(pipe_name: &str) -> NamedPipeServer {
        pipe_runtime().block_on(async {
            ServerOptions::new()
                .first_pipe_instance(true)
                .create(pipe_name)
                .expect("create named pipe server")
        })
    }

    #[cfg(windows)]
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

    #[cfg(windows)]
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

    #[cfg(windows)]
    #[test]
    fn connect_returns_none_when_pipe_absent() {
        let pipe_name = unique_test_pipe_name();
        let result = connect_sidecar_endpoint(&pipe_name).expect("no error");
        assert!(
            result.is_none(),
            "expected None for nonexistent pipe, got Some"
        );
    }

    #[cfg(windows)]
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
                tokio::time::sleep(Duration::from_secs(3)).await;
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
    #[cfg(windows)]
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
                method: Some(proto::request::Method::GetSidecarInfo(
                    proto::GetSidecarInfoRequest {},
                )),
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

    #[cfg(windows)]
    #[test]
    fn is_sidecar_gone_reports_true_for_absent_pipe() {
        let pipe_name = unique_test_pipe_name();
        assert!(is_sidecar_gone(&pipe_name));
    }

    #[cfg(windows)]
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
    #[cfg(windows)]
    const FOREIGN_SID_A: [u8; 12] = [
        0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x20, 0x00, 0x00, 0x00,
    ];
    #[cfg(windows)]
    const FOREIGN_SID_B: [u8; 12] = [
        0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x2a, 0x00, 0x00, 0x00,
    ];

    #[cfg(windows)]
    #[test]
    fn require_peer_sid_accepts_same_sid() {
        require_peer_sid(&FOREIGN_SID_A, &FOREIGN_SID_A, "\\\\?\\pipe\\test")
            .expect("same SID is accepted");
    }

    #[cfg(windows)]
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
    #[cfg(windows)]
    #[test]
    fn require_same_user_pipe_peer_accepts_our_own_pipe() {
        let pipe_name = unique_test_pipe_name();
        let _server = start_test_pipe_server(&pipe_name);
        let client = pipe_runtime().block_on(async {
            ClientOptions::new()
                .open(&pipe_name)
                .expect("open named pipe client")
        });
        require_same_user_pipe_peer(&client, &pipe_name)
            .expect("our own pipe is accepted");
    }

    // endpoint_holder_pid now reports the kernel-recorded server pid on Windows
    // (GetNamedPipeServerProcessId), retiring the previous unconditional None.
    #[cfg(windows)]
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

    #[cfg(windows)]
    #[test]
    fn endpoint_holder_pid_is_none_for_an_absent_endpoint() {
        let pipe_name = unique_test_pipe_name();
        assert_eq!(endpoint_holder_pid(&pipe_name), None);
    }

    #[cfg(any(windows, test))]
    #[test]
    fn read_frame_async_roundtrips_multibyte_varint_frame() {
        // A frame whose encoded body exceeds 127 bytes forces the
        // length-delimited prefix into a multi-byte varint, exercising the
        // loop in read_varint_async.
        pipe_runtime().block_on(async {
            let (mut writer, mut reader) = tokio::io::duplex(64 * 1024);
            let mut info = sidecar_info(
                proto::SidecarShellMode::Distributed,
                true,
                "https://example.invalid/path/to/hub",
            );
            info.binary_hash = "x".repeat(200);
            let frame = proto::Frame {
                message: Some(proto::frame::Message::Response(proto::Response {
                    id: 7,
                    error: String::new(),
                    result: Some(proto::response::Result::SidecarInfo(info)),
                })),
            };
            assert!(
                frame.encoded_len() > 127,
                "test precondition: frame must exceed 1-byte varint range, got {}",
                frame.encoded_len()
            );

            write_frame_async(&mut writer, &frame).await.expect("write");
            drop(writer);
            let received = read_frame_async(&mut reader).await.expect("read");
            assert_eq!(received.encoded_len(), frame.encoded_len());
            match received.message {
                Some(proto::frame::Message::Response(r)) => assert_eq!(r.id, 7),
                other => panic!("unexpected message: {other:?}"),
            }
        });
    }

    // frame_len and varint_step are the two decisions the sync and async readers
    // SHARE. The reader tests above reach them only through a socket, and only at
    // sizes a real frame happens to take -- so the boundary itself (exactly at the
    // cap vs. one byte over) is asserted here, where it can be stated exactly.
    #[test]
    fn frame_len_admits_the_cap_and_refuses_one_byte_past_it() {
        assert_eq!(frame_len(0).expect("an empty frame is a frame"), 0);
        // Exactly at the cap is legal: the check is `>`, not `>=`, and a frame of
        // precisely MAX_FRAME_SIZE must still be readable.
        assert_eq!(
            frame_len(MAX_FRAME_SIZE).expect("a frame at the cap is legal"),
            MAX_FRAME_SIZE as usize
        );

        let err = frame_len(MAX_FRAME_SIZE + 1).expect_err("one byte past the cap is refused");
        assert_eq!(err.kind(), io::ErrorKind::InvalidData);
        assert!(
            err.to_string().contains("frame too large"),
            "operators and the sync reader's tests key on this text: {err}"
        );

        // A bogus varint is the case the cap exists for: u64::MAX must be refused as
        // a size rather than narrowed by `as usize` into a plausible allocation.
        frame_len(u64::MAX).expect_err("a bogus length prefix is refused, not truncated");
    }

    #[test]
    fn varint_step_terminates_on_a_clear_high_bit_and_accumulates_otherwise() {
        // Single byte, high bit clear: terminates immediately with its own value.
        let (mut x, mut s) = (0u64, 0u32);
        assert_eq!(varint_step(&mut x, &mut s, 0x01), Some(1));

        // Two bytes: the first only accumulates (returns None and advances the
        // shift), the second terminates and contributes its bits at that shift.
        // 0xAC 0x02 is protobuf's canonical varint for 300.
        let (mut x, mut s) = (0u64, 0u32);
        assert_eq!(varint_step(&mut x, &mut s, 0xAC), None, "high bit set: more bytes needed");
        assert_eq!(s, 7, "each continuation byte carries 7 payload bits");
        assert_eq!(varint_step(&mut x, &mut s, 0x02), Some(300));
    }

    #[cfg(any(windows, test))]
    #[test]
    fn read_frame_async_rejects_oversize_varint_before_allocating() {
        // A length prefix exceeding MAX_FRAME_SIZE must be rejected without
        // attempting to allocate the payload, so a peer can't make us
        // allocate gigabytes by sending a bogus varint.
        //
        // The error assertions below are necessary but NOT sufficient: the same
        // error surfaces even if the check runs after the `vec!`. So measure the
        // allocation too -- that, and only that, pins the ordering that gives
        // the check its value. `pipe_runtime()` is resolved outside the probe so
        // one-time runtime construction isn't attributed to the read.
        let runtime = pipe_runtime();
        let (_, peak) = alloc_probe::peak_alloc_of(|| {
            runtime.block_on(async {
                let (mut writer, mut reader) = tokio::io::duplex(64);
                let mut buf = Vec::new();
                let mut v: u64 = MAX_FRAME_SIZE + 1;
                loop {
                    let byte = (v & 0x7f) as u8;
                    v >>= 7;
                    if v == 0 {
                        buf.push(byte);
                        break;
                    }
                    buf.push(byte | 0x80);
                }
                tokio::io::AsyncWriteExt::write_all(&mut writer, &buf)
                    .await
                    .expect("write varint");
                drop(writer);

                let err = read_frame_async(&mut reader)
                    .await
                    .expect_err("oversize frame must error");
                assert_eq!(err.kind(), io::ErrorKind::InvalidData);
                assert!(
                    err.to_string().contains("frame too large"),
                    "unexpected error message: {err}"
                );
            })
        });
        assert!(
            (peak as u64) < MAX_FRAME_SIZE,
            "read_frame_async allocated {peak} bytes for a rejected {} byte prefix; \
             the MAX_FRAME_SIZE check must run BEFORE the payload is allocated",
            MAX_FRAME_SIZE + 1
        );
    }

    #[cfg(any(windows, test))]
    #[test]
    fn read_frame_async_returns_eof_when_peer_closes() {
        // The reader thread distinguishes UnexpectedEof from real errors so
        // a clean peer-close doesn't log a noisy error line. Pin that
        // contract here.
        pipe_runtime().block_on(async {
            let (writer, mut reader) = tokio::io::duplex(64);
            drop(writer);
            let err = read_frame_async(&mut reader)
                .await
                .expect_err("eof must be reported");
            assert_eq!(err.kind(), io::ErrorKind::UnexpectedEof);
        });
    }

    #[cfg(windows)]
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

    #[cfg(windows)]
    #[test]
    fn dev_sidecar_endpoint_uses_full_pipe_format() {
        // Pin the full endpoint string, including the variable identity, so a
        // regression in prefix/suffix or identity placement is caught.
        let identity = sidecar_identity().expect("sidecar_identity");
        let expected = format!("\\\\.\\pipe\\leapmux-desktop-{identity}-sidecar");
        let actual = dev_sidecar_endpoint().expect("endpoint");
        assert_eq!(actual, expected);
    }

    #[cfg(windows)]
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

    #[cfg(windows)]
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

    #[derive(Clone, Default)]
    struct SharedBuffer(Arc<Mutex<Vec<u8>>>);

    impl SharedBuffer {
        fn snapshot(&self) -> Vec<u8> {
            self.0.lock().unwrap().clone()
        }
    }

    impl Write for SharedBuffer {
        fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(buf);
            Ok(buf.len())
        }

        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    #[test]
    fn send_sidecar_request_writes_shutdown_frame() {
        let writer = SharedBuffer::default();
        let buffer = writer.clone();
        let pending: PendingMap = Arc::new(Mutex::new(HashMap::new()));
        let writer_tx = start_sidecar_writer_thread(Box::new(writer), pending.clone());
        let sidecar = SidecarProcess {
            _child: None,
            writer_tx,
            pending: pending.clone(),
            next_id: AtomicU64::new(1),
        };

        let responder = thread::spawn(move || loop {
            if let Some(tx) = pending.lock().unwrap().remove(&1) {
                let _ = tx.send(Ok(proto::Response {
                    id: 1,
                    error: String::new(),
                    result: Some(proto::response::Result::BoolValue(proto::BoolValue {
                        value: true,
                    })),
                }));
                break;
            }
            thread::sleep(Duration::from_millis(100));
        });

        let resp = tauri::async_runtime::block_on(send_sidecar_request(
            &sidecar,
            proto::request::Method::Shutdown(proto::ShutdownRequest {}),
        ))
        .expect("send shutdown request");
        responder.join().expect("responder join");

        assert_eq!(resp.id, 1);

        assert!(
            wait_until(
                || read_frame(&mut io::Cursor::new(buffer.snapshot())).is_ok(),
                Duration::from_secs(1),
            ),
            "writer thread never flushed the shutdown frame"
        );
        let mut cursor = io::Cursor::new(buffer.snapshot());
        let frame = read_frame(&mut cursor).expect("decode flushed frame");
        let request = match frame.message {
            Some(proto::frame::Message::Request(req)) => req,
            other => panic!("unexpected frame: {other:?}"),
        };
        assert_eq!(request.id, 1);
        assert!(matches!(
            request.method,
            Some(proto::request::Method::Shutdown(_))
        ));
    }

    fn sidecar_info(
        mode: proto::SidecarShellMode,
        connected: bool,
        hub_url: &str,
    ) -> proto::SidecarInfo {
        proto::SidecarInfo {
            protocol_version: SIDECAR_PROTOCOL_VERSION.to_string(),
            binary_hash: "test-hash".to_string(),
            pid: 0,
            shell_mode: mode as i32,
            connected,
            hub_url: hub_url.to_string(),
        }
    }

    fn fresh_state() -> Mutex<ShellState> {
        Mutex::new(ShellState {
            shell_mode: ShellMode::Launcher,
            connected: false,
            hub_url: String::new(),
            local_app_url: "http://localhost:4328".to_string(),
        })
    }

    #[test]
    fn shell_mode_from_proto_maps_solo() {
        let info = sidecar_info(proto::SidecarShellMode::Solo, true, "");
        assert_eq!(shell_mode_from_proto(&info), ShellMode::Solo);
    }

    #[test]
    fn shell_mode_from_proto_maps_distributed() {
        let info = sidecar_info(proto::SidecarShellMode::Distributed, true, "https://hub");
        assert_eq!(shell_mode_from_proto(&info), ShellMode::Distributed);
    }

    #[test]
    fn shell_mode_from_proto_maps_launcher() {
        let info = sidecar_info(proto::SidecarShellMode::Launcher, false, "");
        assert_eq!(shell_mode_from_proto(&info), ShellMode::Launcher);
    }

    #[test]
    fn shell_mode_from_proto_falls_back_to_launcher_on_unspecified() {
        // Untrusted sidecar state must not silently flip the shell into
        // Solo/Distributed when no valid mode was supplied.
        let info = sidecar_info(proto::SidecarShellMode::Unspecified, true, "https://hub");
        assert_eq!(shell_mode_from_proto(&info), ShellMode::Launcher);
    }

    #[test]
    fn lifecycle_response_preserves_launcher_state_and_cleanup_errors() {
        let info = sidecar_info(proto::SidecarShellMode::Launcher, false, "");
        let response = proto::Response {
            id: 1,
            error: String::new(),
            result: Some(proto::response::Result::Lifecycle(proto::LifecycleResult {
                sidecar_info: Some(info.clone()),
                cleanup_errors: vec!["lease release failed".to_string()],
            })),
        };

        let (actual_info, cleanup_errors) =
            lifecycle_from_response(response).expect("valid lifecycle response");
        assert_eq!(actual_info, info);
        assert_eq!(cleanup_errors, vec!["lease release failed"]);
    }

    #[test]
    fn launcher_url_carries_all_cleanup_errors_and_preserves_existing_query() {
        let (url, message) = launcher_url(
            "http://localhost:4328/app?source=desktop",
            &[
                "lease release failed".to_string(),
                "hub stop failed".to_string(),
            ],
        )
        .expect("valid launcher url");

        assert_eq!(message, "lease release failed\nhub stop failed");
        let query: HashMap<_, _> = url.query_pairs().into_owned().collect();
        assert_eq!(query.get("source").map(String::as_str), Some("desktop"));
        assert_eq!(
            query.get("cleanup_error").map(String::as_str),
            Some(message.as_str())
        );
    }

    #[test]
    fn launcher_url_omits_empty_cleanup_warning() {
        let (url, message) =
            launcher_url("http://localhost:4328/app", &[]).expect("valid launcher url");

        assert!(message.is_empty());
        assert!(url.query().is_none());
    }

    #[test]
    fn switch_mode_response_rejects_top_level_transition_error() {
        let response = proto::Response {
            id: 1,
            error: "save config failed".to_string(),
            result: None,
        };

        assert_eq!(
            lifecycle_from_response(response).expect_err("transition must fail"),
            "save config failed"
        );
    }

    #[test]
    fn switch_mode_response_requires_sidecar_info() {
        let response = proto::Response {
            id: 1,
            error: String::new(),
            result: Some(proto::response::Result::Lifecycle(proto::LifecycleResult {
                sidecar_info: None,
                cleanup_errors: Vec::new(),
            })),
        };

        assert_eq!(
            lifecycle_from_response(response).expect_err("sidecar info is required"),
            "lifecycle response missing sidecar info"
        );
    }

    #[test]
    fn window_mode_proto_round_trips_every_state() {
        for mode in ["normal", "maximized", "fullscreen"] {
            assert_eq!(window_mode_from_proto(window_mode_to_proto(mode)), mode);
        }
    }

    #[test]
    fn window_mode_to_proto_defaults_unknown_to_normal() {
        // The JSON string from the frontend is untrusted; empty or unexpected
        // values must land on Normal rather than a stray enum.
        assert_eq!(window_mode_to_proto(""), proto::WindowMode::Normal);
        assert_eq!(window_mode_to_proto("bogus"), proto::WindowMode::Normal);
    }

    #[test]
    fn window_mode_from_proto_maps_unspecified_to_normal() {
        // A fresh config or an older sidecar sends UNSPECIFIED; it must read
        // back as the windowed default, not an empty string.
        assert_eq!(
            window_mode_from_proto(proto::WindowMode::Unspecified),
            "normal",
        );
    }

    #[test]
    fn apply_sidecar_info_overwrites_stale_cache() {
        let state = fresh_state();
        {
            let mut guard = state.lock().unwrap();
            guard.shell_mode = ShellMode::Solo;
            guard.connected = true;
            guard.hub_url = "stale".to_string();
        }

        apply_sidecar_info(
            &state,
            sidecar_info(
                proto::SidecarShellMode::Distributed,
                true,
                "https://hub.example",
            ),
        );

        let guard = state.lock().unwrap();
        assert_eq!(guard.shell_mode, ShellMode::Distributed);
        assert!(guard.connected);
        assert_eq!(guard.hub_url, "https://hub.example");
    }

    #[test]
    fn apply_sidecar_info_clears_hub_url_on_launcher() {
        let state = fresh_state();
        {
            let mut guard = state.lock().unwrap();
            guard.shell_mode = ShellMode::Distributed;
            guard.connected = true;
            guard.hub_url = "https://hub".to_string();
        }

        apply_sidecar_info(
            &state,
            sidecar_info(proto::SidecarShellMode::Launcher, false, ""),
        );

        let guard = state.lock().unwrap();
        assert_eq!(guard.shell_mode, ShellMode::Launcher);
        assert!(!guard.connected);
        assert!(guard.hub_url.is_empty());
    }

    struct FailingWriter;

    impl Write for FailingWriter {
        fn write(&mut self, _buf: &[u8]) -> io::Result<usize> {
            Err(io::Error::new(io::ErrorKind::BrokenPipe, "forced failure"))
        }
        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    fn wait_until<F: FnMut() -> bool>(mut cond: F, timeout: Duration) -> bool {
        let deadline = Instant::now() + timeout;
        while Instant::now() < deadline {
            if cond() {
                return true;
            }
            thread::sleep(Duration::from_millis(100));
        }
        cond()
    }

    #[test]
    fn concurrent_send_sidecar_requests_produce_distinct_wellformed_frames() {
        const N: u64 = 8;
        let writer = SharedBuffer::default();
        let buffer = writer.clone();
        let pending: PendingMap = Arc::new(Mutex::new(HashMap::new()));
        let writer_tx = start_sidecar_writer_thread(Box::new(writer), pending.clone());
        let sidecar = Arc::new(SidecarProcess {
            _child: None,
            writer_tx,
            pending: pending.clone(),
            next_id: AtomicU64::new(1),
        });

        let responder_pending = pending.clone();
        let responder = thread::spawn(move || {
            let deadline = Instant::now() + Duration::from_secs(5);
            let mut answered = 0u64;
            while answered < N && Instant::now() < deadline {
                let ids: Vec<u64> = { responder_pending.lock().unwrap().keys().copied().collect() };
                for id in ids {
                    if let Some(tx) = responder_pending.lock().unwrap().remove(&id) {
                        let _ = tx.send(Ok(proto::Response {
                            id,
                            error: String::new(),
                            result: Some(proto::response::Result::BoolValue(proto::BoolValue {
                                value: true,
                            })),
                        }));
                        answered += 1;
                    }
                }
                thread::sleep(Duration::from_millis(100));
            }
        });

        let runtime = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(4)
            .enable_all()
            .build()
            .expect("multi-thread runtime");
        runtime.block_on(async {
            let mut handles = Vec::new();
            for _ in 0..N {
                let s = sidecar.clone();
                handles.push(tokio::spawn(async move {
                    send_sidecar_request(
                        &s,
                        proto::request::Method::GetSidecarInfo(proto::GetSidecarInfoRequest {}),
                    )
                    .await
                }));
            }
            for h in handles {
                h.await.expect("join").expect("request");
            }
        });
        responder.join().expect("responder join");

        assert!(
            wait_until(
                || {
                    let snap = buffer.snapshot();
                    let mut cursor = io::Cursor::new(snap);
                    let mut count = 0u64;
                    while read_frame(&mut cursor).is_ok() {
                        count += 1;
                    }
                    count == N
                },
                Duration::from_secs(2),
            ),
            "writer thread did not flush all frames"
        );

        let snapshot = buffer.snapshot();
        let mut cursor = io::Cursor::new(snapshot);
        let mut ids = std::collections::HashSet::new();
        for _ in 0..N {
            let frame = read_frame(&mut cursor).expect("decode frame");
            let request = match frame.message {
                Some(proto::frame::Message::Request(req)) => req,
                other => panic!("unexpected frame: {other:?}"),
            };
            assert!(matches!(
                request.method,
                Some(proto::request::Method::GetSidecarInfo(_))
            ));
            assert!(ids.insert(request.id), "duplicate id {}", request.id);
        }
        assert_eq!(ids.len() as u64, N);
    }

    #[test]
    fn send_sidecar_request_errors_when_writer_thread_has_exited() {
        let pending: PendingMap = Arc::new(Mutex::new(HashMap::new()));
        let writer_tx = start_sidecar_writer_thread(Box::new(FailingWriter), pending.clone());
        let sidecar = SidecarProcess {
            _child: None,
            writer_tx,
            pending: pending.clone(),
            next_id: AtomicU64::new(1),
        };

        let first = tauri::async_runtime::block_on(send_sidecar_request(
            &sidecar,
            proto::request::Method::Shutdown(proto::ShutdownRequest {}),
        ));
        assert_eq!(first, Err("desktop sidecar disconnected".to_string()));

        assert!(
            wait_until(|| sidecar.writer_tx.is_closed(), Duration::from_secs(1),),
            "writer channel never closed"
        );

        let second = tauri::async_runtime::block_on(send_sidecar_request(
            &sidecar,
            proto::request::Method::Shutdown(proto::ShutdownRequest {}),
        ));
        assert_eq!(
            second,
            Err("desktop sidecar writer disconnected".to_string())
        );
        assert!(pending.lock().unwrap().is_empty());
    }

    #[test]
    fn writer_thread_exit_clears_pending_entries() {
        let pending: PendingMap = Arc::new(Mutex::new(HashMap::new()));
        let (phantom_tx, phantom_rx) = oneshot::channel::<Result<proto::Response, String>>();
        pending.lock().unwrap().insert(42, phantom_tx);

        let writer_tx = start_sidecar_writer_thread(Box::new(FailingWriter), pending.clone());

        writer_tx
            .send(proto::Frame {
                message: Some(proto::frame::Message::Request(proto::Request {
                    id: 1,
                    method: Some(proto::request::Method::Shutdown(proto::ShutdownRequest {})),
                })),
            })
            .expect("send to writer");

        assert!(
            wait_until(
                || pending.lock().unwrap().is_empty(),
                Duration::from_secs(1),
            ),
            "writer thread never cleared pending on exit"
        );

        // The phantom receiver must observe the sender drop — the signal a
        // real in-flight send_sidecar_request relies on to unblock.
        let dropped = tauri::async_runtime::block_on(phantom_rx);
        assert!(dropped.is_err(), "phantom oneshot should be dropped");
    }

    /// A process-unique temp path `<tmpdir>/<prefix>-<pid>-<counter>` (not
    /// created). The atomic `fetch_add` guarantees intra-process uniqueness
    /// (any ordering suffices for a bare uniqueness counter); the pid keeps
    /// concurrent test binaries apart. The socket/pipe helpers deliberately
    /// add a `{nanos}` component instead, because a reused pid could clash
    /// with a stale *bound* endpoint from a prior run -- a hazard a
    /// freshly-created file does not have.
    fn unique_temp_path(prefix: &str) -> PathBuf {
        let counter = TEST_COUNTER.fetch_add(1, Ordering::Relaxed);
        std::env::temp_dir().join(format!("{prefix}-{}-{counter}", std::process::id()))
    }

    fn save_test_paths() -> (std::path::PathBuf, std::path::PathBuf) {
        let final_path = unique_temp_path("leapmux-save").with_extension("txt");
        (tmp_path_for(&final_path), final_path)
    }

    /// A unique temp path (not created) for sweep / open_unique_tmp tests.
    fn unique_sweep_dir_path() -> PathBuf {
        unique_temp_path("leapmux-sweep")
    }

    /// A freshly-created unique temp directory for sweep / open_unique_tmp
    /// tests (the unit under test is a dir scan), removed on drop. The
    /// cleanup is panic-safe: a failing assertion mid-test still reclaims
    /// the directory, unlike a trailing `remove_dir_all` a panic would skip.
    struct SweepTestDir {
        path: PathBuf,
    }

    impl SweepTestDir {
        fn new() -> Self {
            let path = unique_sweep_dir_path();
            std::fs::create_dir_all(&path).expect("create sweep test dir");
            Self { path }
        }

        fn path(&self) -> &Path {
            &self.path
        }
    }

    impl Drop for SweepTestDir {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.path);
        }
    }

    // The happy path: a handle with no write in flight commits by atomic-rename,
    // and the final file carries what was written.
    #[test]
    fn save_stream_commit_renames_when_no_write_in_flight() {
        let registry = SaveStreamRegistry::new();
        let (tmp_path, final_path) = save_test_paths();
        let mut file = std::fs::File::create(&tmp_path).expect("create tmp");
        use std::io::Write;
        file.write_all(b"hello").expect("seed tmp");
        drop(file);
        let reopened = std::fs::OpenOptions::new()
            .write(true)
            .open(&tmp_path)
            .expect("reopen tmp");
        let handle = registry.insert(reopened, tmp_path.clone(), final_path.clone());

        registry.commit(handle.id).expect("commit must succeed");
        assert!(!tmp_path.exists(), "the tmp file is renamed away");
        assert_eq!(std::fs::read(&final_path).unwrap(), b"hello");
        let _ = std::fs::remove_file(&final_path);
    }

    // A write still in flight when commit runs (a duplicated/racing
    // file_save_write holding a clone of the handle's Arc) must make commit FAIL
    // LOUDLY and discard the partial -- not rename it. On Unix the old code
    // renamed successfully while the in-flight write corrupted the committed
    // file; try_unwrap catches the live clone before that can happen.
    #[test]
    fn save_stream_commit_refuses_when_a_write_is_in_flight() {
        let registry = SaveStreamRegistry::new();
        let (tmp_path, final_path) = save_test_paths();
        let file = std::fs::File::create(&tmp_path).expect("create tmp");
        let handle = registry.insert(file, tmp_path.clone(), final_path.clone());

        // Simulate an overlapping write_chunk: hold a clone of the handle's Arc.
        let in_flight_clone = {
            let guard = registry.handles.lock().unwrap();
            guard.get(&handle.id).unwrap().file.clone()
        };

        let err = registry
            .commit(handle.id)
            .expect_err("commit must refuse while a write clone is live");
        assert!(
            err.contains("write still in progress"),
            "unexpected error: {err}"
        );
        assert!(!tmp_path.exists(), "the partial tmp is discarded, not left behind");
        assert!(!final_path.exists(), "no corrupt file is renamed into place");
        drop(in_flight_clone);
    }

    #[test]
    fn sweep_removes_orphaned_partials() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        let foo = dir.join(format!("foo.txt{SAVE_TMP_SUFFIX}"));
        let bar = dir.join(format!("bar{SAVE_TMP_SUFFIX}"));
        std::fs::write(&foo, b"orphan").expect("seed foo");
        std::fs::write(&bar, b"orphan").expect("seed bar");

        SaveStreamRegistry::new().sweep_orphan_tmps(dir);

        assert!(!foo.exists(), "extensioned orphan must be removed");
        assert!(!bar.exists(), "extensionless orphan must be removed");
    }

    #[test]
    fn sweep_spares_files_not_matching_the_suffix() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        // Load-bearing foreign-file case: bare `.tmp` must never be swept.
        let foreign_tmp = dir.join("foo.tmp");
        let plain = dir.join("foo.txt");
        let suffix_mid = dir.join("foo.leapmux.tmp.txt");
        let exact_suffix = dir.join(SAVE_TMP_SUFFIX);
        std::fs::write(&foreign_tmp, b"x").expect("seed foreign");
        std::fs::write(&plain, b"x").expect("seed plain");
        std::fs::write(&suffix_mid, b"x").expect("seed mid");
        std::fs::write(&exact_suffix, b"x").expect("seed exact");

        SaveStreamRegistry::new().sweep_orphan_tmps(dir);

        assert!(foreign_tmp.exists(), "foreign .tmp must survive");
        assert!(plain.exists(), "plain final must survive");
        assert!(suffix_mid.exists(), "suffix mid-name must survive");
        assert!(exact_suffix.exists(), "exact-suffix name must survive");
    }

    #[test]
    fn sweep_spares_directories_named_like_partials() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        let nested = dir.join(format!("dir{SAVE_TMP_SUFFIX}"));
        std::fs::create_dir_all(&nested).expect("create nested dir");
        let inside = nested.join("keep.txt");
        std::fs::write(&inside, b"keep").expect("seed inside");

        SaveStreamRegistry::new().sweep_orphan_tmps(dir);

        assert!(
            nested.is_dir(),
            "directory named like a partial must survive"
        );
        assert!(inside.exists(), "contents of spared dir must survive");
    }

    #[test]
    fn sweep_spares_live_registry_partials() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        let live_final = dir.join("live.txt");
        let live_tmp = tmp_path_for(&live_final);
        let dead = dir.join(format!("dead.txt{SAVE_TMP_SUFFIX}"));
        std::fs::write(&live_tmp, b"live").expect("seed live");
        std::fs::write(&dead, b"dead").expect("seed dead");

        let registry = SaveStreamRegistry::new();
        let file = std::fs::OpenOptions::new()
            .write(true)
            .open(&live_tmp)
            .expect("open live");
        let _handle = registry.insert(file, live_tmp.clone(), live_final);

        registry.sweep_orphan_tmps(dir);

        assert!(live_tmp.exists(), "live registry partial must survive");
        assert!(!dead.exists(), "dead orphan must be removed");
        // Drop the open handle so Windows can unlink it before the guard's
        // Drop removes the directory.
        registry.cleanup_all();
    }

    #[test]
    fn sweep_tolerates_missing_dir() {
        let missing = unique_sweep_dir_path();
        assert!(!missing.exists());
        SaveStreamRegistry::new().sweep_orphan_tmps(&missing);
    }

    /// #285 regression: an orphaned partial forces "(1)" until swept,
    /// after which the unsuffixed name is free again.
    #[test]
    fn orphaned_partial_forces_suffix_until_swept() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        let orphan = dir.join(format!("foo.txt{SAVE_TMP_SUFFIX}"));
        std::fs::write(&orphan, b"orphan").expect("seed orphan");

        let (file, _tmp, final_path) =
            open_unique_tmp(dir.to_path_buf(), "foo.txt".into()).expect("open while orphaned");
        assert_eq!(
            final_path.file_name().and_then(|n| n.to_str()),
            Some("foo (1).txt"),
            "orphan must force the (1) collision"
        );
        // Windows can't unlink an open file; drop before sweep.
        drop(file);

        SaveStreamRegistry::new().sweep_orphan_tmps(dir);
        assert!(!orphan.exists(), "orphan must be gone after sweep");

        let (file2, _tmp2, final2) =
            open_unique_tmp(dir.to_path_buf(), "foo.txt".into()).expect("open after sweep");
        assert_eq!(
            final2.file_name().and_then(|n| n.to_str()),
            Some("foo.txt"),
            "unsuffixed name must be free after sweep"
        );
        // Drop the open handle so Windows can unlink it before the guard's
        // Drop removes the directory (the partials it leaves are inside it).
        drop(file2);
    }

    #[test]
    fn open_unique_tmp_defuses_reserved_suffix_names() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        let (file, _tmp, final_path) =
            open_unique_tmp(dir.to_path_buf(), format!("evil{SAVE_TMP_SUFFIX}"))
                .expect("open defused name");
        let final_name = final_path
            .file_name()
            .and_then(|n| n.to_str())
            .expect("utf-8 name");
        assert_eq!(final_name, format!("evil{SAVE_TMP_SUFFIX}.download"));
        assert!(
            !final_name.ends_with(SAVE_TMP_SUFFIX),
            "final must not end in the reserved suffix"
        );
        drop(file);
    }

    // An existing final (not just an existing partial) must also push the
    // candidate iteration to "(1)" — the `try_exists` skip preserves the
    // "don't silently overwrite a user file in Downloads" behavior.
    #[test]
    fn open_unique_tmp_skips_existing_finals() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        std::fs::write(dir.join("foo.txt"), b"user file").expect("seed final");

        let (file, tmp, final_path) =
            open_unique_tmp(dir.to_path_buf(), "foo.txt".into()).expect("open with final present");
        assert_eq!(
            final_path.file_name().and_then(|n| n.to_str()),
            Some("foo (1).txt"),
            "existing final must force the (1) collision"
        );
        assert!(tmp.exists(), "the (1) candidate's partial must be reserved");
        drop(file);
    }

    // The defuse invariant must survive the collision loop: even when the
    // defused name itself collides, no "(N)" candidate may end in the
    // reserved suffix, or the next startup sweep would eat the committed
    // final.
    #[test]
    fn open_unique_tmp_defused_collisions_never_end_in_suffix() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        std::fs::write(dir.join(format!("evil{SAVE_TMP_SUFFIX}.download")), b"x")
            .expect("seed defused final");

        let (file, _tmp, final_path) =
            open_unique_tmp(dir.to_path_buf(), format!("evil{SAVE_TMP_SUFFIX}"))
                .expect("open colliding defused name");
        let final_name = final_path
            .file_name()
            .and_then(|n| n.to_str())
            .expect("utf-8 name");
        assert_eq!(final_name, format!("evil{SAVE_TMP_SUFFIX} (1).download"));
        assert!(
            !final_name.ends_with(SAVE_TMP_SUFFIX),
            "collision candidates must not end in the reserved suffix"
        );
        drop(file);
    }

    #[test]
    fn tmp_path_for_appends_the_sweep_suffix() {
        let final_path = PathBuf::from("foo.txt");
        let tmp = tmp_path_for(&final_path);
        let name = tmp.file_name().expect("file name");
        assert!(
            name.as_encoded_bytes()
                .ends_with(SAVE_TMP_SUFFIX.as_bytes()),
            "tmp_path_for must append SAVE_TMP_SUFFIX; got {}",
            name.to_string_lossy()
        );
    }

    // The sweep's matcher: only names strictly longer than the suffix and
    // ending in it are partials. The `exact_suffix` and `suffix_mid` cases
    // pin the boundaries the sweep relies on to spare finals.
    #[test]
    fn is_partial_name_matches_only_strictly_longer_suffixed_names() {
        let extensioned = format!("foo.txt{SAVE_TMP_SUFFIX}");
        let extensionless = format!("bar{SAVE_TMP_SUFFIX}");
        assert!(is_partial_name(OsStr::new(&extensioned)));
        assert!(is_partial_name(OsStr::new(&extensionless)));
        // Exactly the suffix is not a partial: a real final is never empty.
        assert!(!is_partial_name(OsStr::new(SAVE_TMP_SUFFIX)));
        assert!(!is_partial_name(OsStr::new("foo.tmp")));
        assert!(!is_partial_name(OsStr::new("foo.txt")));
        assert!(!is_partial_name(OsStr::new("foo.leapmux.tmp.txt")));
    }

    #[test]
    fn defuse_final_path_appends_download_to_reserved_suffix_names() {
        let defused = defuse_final_path(PathBuf::from(format!("/x/report{SAVE_TMP_SUFFIX}")));
        let expected = format!("report{SAVE_TMP_SUFFIX}.download");
        assert_eq!(
            defused.file_name().and_then(|n| n.to_str()),
            Some(expected.as_str())
        );
        // The defused result is no longer a partial, so the sweep spares it.
        assert!(!is_partial_name(defused.file_name().expect("file name")));

        // A normal final is returned unchanged.
        let plain = PathBuf::from("/x/report.pdf");
        assert_eq!(defuse_final_path(plain.clone()), plain);

        // A path with no file name is returned unchanged (no panic).
        assert_eq!(defuse_final_path(PathBuf::from("/")), PathBuf::from("/"));
    }

    // The defuse marker must not itself end in the reserved partial suffix:
    // if it did, appending it would leave the final still matching
    // `is_partial_name`, and the next startup sweep would delete the very
    // final the defuse was meant to protect. Pins the invariant the
    // `SAVE_DEFUSE_SUFFIX` doc states.
    #[test]
    fn save_defuse_suffix_clears_the_reserved_suffix() {
        assert!(
            !SAVE_DEFUSE_SUFFIX.ends_with(SAVE_TMP_SUFFIX),
            "the defuse marker must clear the reserved suffix, not re-add it"
        );
        // Appending the marker to a reserved-suffix name yields a non-partial.
        let defused = format!("anything{SAVE_TMP_SUFFIX}{SAVE_DEFUSE_SUFFIX}");
        assert!(!is_partial_name(OsStr::new(&defused)));
    }

    #[test]
    fn unique_temp_path_yields_distinct_prefixed_paths() {
        let a = unique_temp_path("leapmux-uniqtest");
        let b = unique_temp_path("leapmux-uniqtest");
        assert_ne!(a, b, "each call must yield a distinct path");
        assert!(a.starts_with(std::env::temp_dir()));
        assert!(a
            .file_name()
            .unwrap()
            .to_string_lossy()
            .starts_with("leapmux-uniqtest-"));
    }

    // A name exactly equal to the suffix is deliberately NOT defused --
    // `is_partial_name` requires strictly-longer -- and that is safe only
    // because the sweep spares exact-suffix names for the same reason.
    // This test pins the two sides of that coupling together: the
    // committed final survives while its genuine orphan partial (which IS
    // strictly longer) is still reclaimed.
    #[test]
    fn open_unique_tmp_exact_suffix_name_stays_undefused_and_unswept() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        let (file, tmp, final_path) =
            open_unique_tmp(dir.to_path_buf(), SAVE_TMP_SUFFIX.to_string())
                .expect("open exact-suffix name");
        assert_eq!(
            final_path.file_name().and_then(|n| n.to_str()),
            Some(SAVE_TMP_SUFFIX),
            "exact-suffix name must not be defused"
        );
        // Simulate the committed final plus its orphaned partial, then
        // sweep: the final is spared, the partial is reclaimed.
        drop(file);
        std::fs::write(&final_path, b"data").expect("seed final");
        SaveStreamRegistry::new().sweep_orphan_tmps(dir);
        assert!(
            final_path.exists(),
            "exact-suffix final must survive the sweep"
        );
        assert!(!tmp.exists(), "its orphan partial must still be reclaimed");
    }

    /// #285 Save-as data-loss regression: a Save-as target whose name ends
    /// in the reserved suffix is defused, so the committed final survives
    /// the next startup sweep instead of being deleted as an orphan. The
    /// undefused half of the test demonstrates the bug the defuse closes.
    #[test]
    fn defused_save_as_final_survives_the_sweep() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        let expected_name = format!("report{SAVE_TMP_SUFFIX}.download");

        // Without defuse, a Save-as of `report.leapmux.tmp` commits this
        // exact name -- which the sweep then deletes. That is the bug.
        let undefused = dir.join(format!("report{SAVE_TMP_SUFFIX}"));
        std::fs::write(&undefused, b"user data").expect("seed undefused");
        SaveStreamRegistry::new().sweep_orphan_tmps(dir);
        assert!(
            !undefused.exists(),
            "an undefused reserved-suffix final is swept -- the data-loss bug"
        );

        // With defuse (as `file_save_open_dialog` now applies), the committed
        // final is `report.leapmux.tmp.download`, which the sweep spares.
        let committed = defuse_final_path(dir.join(format!("report{SAVE_TMP_SUFFIX}")));
        assert_eq!(
            committed.file_name().and_then(|n| n.to_str()),
            Some(expected_name.as_str())
        );
        std::fs::write(&committed, b"user data").expect("seed defused");
        SaveStreamRegistry::new().sweep_orphan_tmps(dir);
        assert!(
            committed.exists(),
            "the defused Save-as final must survive the sweep"
        );
    }

    // A Save-as target literally ending in the reserved suffix redirects the
    // write to the `.download` twin. If that twin already exists, the resolver
    // must refuse rather than let commit's rename silently clobber a file the
    // native dialog never confirmed. Fails against a resolver that only
    // defuses without the existence check (it would return Ok).
    #[test]
    fn resolve_save_as_final_refuses_clobbering_existing_download_twin() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        let chosen = dir.join(format!("report{SAVE_TMP_SUFFIX}"));
        let twin = dir.join(format!("report{SAVE_TMP_SUFFIX}.download"));
        std::fs::write(&twin, b"precious").expect("seed twin");
        let err = resolve_save_as_final(chosen).expect_err("must refuse to clobber the twin");
        assert!(err.contains("already exists"), "unexpected error: {err}");
        assert_eq!(
            std::fs::read(&twin).unwrap(),
            b"precious",
            "twin must be untouched"
        );
    }

    // The guard must not over-block: a reserved-suffix target with no existing
    // twin still defuses, and a normal dialog-confirmed target passes through
    // unchanged (its own overwrite prompt already covered it).
    #[test]
    fn resolve_save_as_final_allows_defuse_without_twin_and_passes_normal_paths() {
        let guard = SweepTestDir::new();
        let dir = guard.path();
        let chosen = dir.join(format!("report{SAVE_TMP_SUFFIX}"));
        let resolved = resolve_save_as_final(chosen).expect("no twin -> defuse ok");
        assert_eq!(
            resolved.file_name().and_then(|n| n.to_str()),
            Some(format!("report{SAVE_TMP_SUFFIX}.download").as_str())
        );

        // A normal dialog-confirmed overwrite must not be blocked, even if the
        // chosen path already exists.
        let normal = dir.join("report.pdf");
        std::fs::write(&normal, b"x").expect("seed normal");
        assert_eq!(
            resolve_save_as_final(normal.clone()).expect("normal path passes"),
            normal,
            "a dialog-confirmed normal overwrite must not be blocked"
        );
    }
}
