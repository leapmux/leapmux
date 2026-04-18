#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

#[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
compile_error!("LeapMux Desktop only supports macOS, Linux, and Windows");

mod proto {
    include!(concat!(env!("OUT_DIR"), "/leapmux.desktop.v1.rs"));
}

use base64::Engine;
use prost::Message;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
#[cfg(unix)]
use std::os::unix::{fs::PermissionsExt, net::UnixStream};
use std::{
    collections::HashMap,
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
use tokio::sync::oneshot;

#[cfg(target_os = "macos")]
const APP_SUBMENU_ID: &str = "leapmux-app-menu";
#[cfg(target_os = "macos")]
const SHOW_ABOUT_MENU_ID: &str = "show-about";
#[cfg(target_os = "macos")]
const SHOW_PREFERENCES_MENU_ID: &str = "show-preferences";
#[cfg(target_os = "macos")]
const OPEN_WEB_INSPECTOR_MENU_ID: &str = "open-web-inspector";
const MAX_FRAME_SIZE: u64 = 16 * 1024 * 1024; // 16 MB
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

#[derive(Serialize)]
struct SidecarMetadata {
    endpoint: String,
    pid: u32,
    binary_hash: String,
    protocol_version: String,
}

// --- Frame read/write utilities ---

fn write_frame(w: &mut impl Write, frame: &proto::Frame) -> io::Result<()> {
    let mut buf = Vec::with_capacity(frame.encoded_len() + 10);
    frame.encode_length_delimited(&mut buf).map_err(|err| {
        io::Error::new(io::ErrorKind::InvalidData, format!("encode frame: {err}"))
    })?;
    w.write_all(&buf)?;
    w.flush()
}

// Note: prost's `decode_length_delimited` requires an in-memory `Buf`, not
// an `io::Read` stream. For streaming stdio reads we must manually decode the
// varint length prefix, then `read_exact` the payload before decoding.
fn read_frame(r: &mut impl Read) -> io::Result<proto::Frame> {
    let size = read_varint(r)?;
    if size > MAX_FRAME_SIZE {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("frame too large: {size} bytes (max {MAX_FRAME_SIZE})"),
        ));
    }
    let mut data = vec![0u8; size as usize];
    r.read_exact(&mut data)?;
    proto::Frame::decode(data.as_slice())
        .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, format!("decode frame: {err}")))
}

fn read_varint(r: &mut impl Read) -> io::Result<u64> {
    let mut x: u64 = 0;
    let mut s: u32 = 0;
    let mut buf = [0u8; 1];
    for _ in 0..10 {
        r.read_exact(&mut buf)?;
        let b = buf[0];
        if b < 0x80 {
            return Ok(x | (b as u64) << s);
        }
        x |= ((b & 0x7f) as u64) << s;
        s += 7;
    }
    Err(io::Error::new(
        io::ErrorKind::InvalidData,
        "varint overflow",
    ))
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

#[derive(Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
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
    window_maximized: bool,
}

#[derive(Serialize)]
struct BuildInfoResponse {
    version: String,
    commit_hash: String,
    commit_time: String,
    build_time: String,
}

#[derive(Serialize)]
struct ProxyHttpResponsePayload {
    status: i32,
    headers: HashMap<String, String>,
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
    #[serde(rename = "hubURL")]
    hub_url: String,
    user_id: String,
}

// --- Sidecar process ---

type PendingResponse = oneshot::Sender<Result<proto::Response, String>>;
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
    writer: Mutex<Box<dyn Write + Send>>,
    pending: PendingMap,
    next_id: AtomicU64,
}

struct DesktopShell {
    app_handle: AppHandle,
    sidecar: SidecarProcess,
    close_in_progress: AtomicBool,
    exit_in_progress: AtomicBool,
    webview_zoom: AtomicU64,
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
    let endpoint = dev_sidecar_endpoint();
    #[cfg(windows)]
    let endpoint = dev_sidecar_endpoint()?;
    let metadata_path = dev_sidecar_metadata_path();
    let binary_hash = hash_sidecar_binary(sidecar_path)?;

    if let Ok(Some((reader, writer, info))) = try_connect_dev_sidecar(&endpoint) {
        if info.protocol_version == SIDECAR_PROTOCOL_VERSION && info.binary_hash == binary_hash {
            write_sidecar_metadata(&metadata_path, &endpoint, info.pid as u32, &binary_hash)?;
            return Ok(SidecarBootstrap {
                child: None,
                reader,
                writer,
            });
        }
        request_sidecar_shutdown(&endpoint, info.pid as u32)?;
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
                write_sidecar_metadata(&metadata_path, &endpoint, info.pid as u32, &binary_hash)?;
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
type SidecarStream = UnixStream;
#[cfg(windows)]
type SidecarStream = PipeHandle;

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

fn connect_and_handshake_dev_sidecar(
    endpoint: &str,
) -> Result<Option<(SidecarStream, SidecarStream, proto::SidecarInfo)>, String> {
    let (mut reader, mut writer) = match connect_sidecar_endpoint(endpoint)? {
        Some(pair) => pair,
        None => return Ok(None),
    };
    // Unix streams carry per-op timeouts from connect; Windows named pipes
    // don't, so arm a watchdog that cancels the handshake's synchronous I/O.
    #[cfg(windows)]
    let _watchdog = HandshakeWatchdog::arm(DEV_SIDECAR_HANDSHAKE_TIMEOUT)?;
    let info = fetch_sidecar_info(&mut reader, &mut writer)?;
    finalize_sidecar_streams(&reader, &writer)?;
    Ok(Some((reader, writer, info)))
}

fn request_sidecar_shutdown(endpoint: &str, pid: u32) -> Result<(), String> {
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
            return Ok(());
        }
        thread::sleep(Duration::from_millis(100));
    }

    force_kill_sidecar(pid)?;
    Ok(())
}

#[cfg(unix)]
fn connect_sidecar_endpoint(
    endpoint: &str,
) -> Result<Option<(SidecarStream, SidecarStream)>, String> {
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
fn finalize_sidecar_streams(reader: &SidecarStream, writer: &SidecarStream) -> Result<(), String> {
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
    if !resp.error.is_empty() {
        return Err(resp.error);
    }
    match resp.result {
        Some(proto::response::Result::SidecarInfo(info)) => Ok(info),
        _ => Err("unexpected response for get_sidecar_info".to_string()),
    }
}

#[cfg(unix)]
fn force_kill_sidecar(pid: u32) -> Result<(), String> {
    let status = Command::new("kill")
        .args(["-TERM", &pid.to_string()])
        .status()
        .map_err(|err| format!("terminate stale sidecar: {err}"))?;
    if !status.success() {
        let kill_status = Command::new("kill")
            .args(["-KILL", &pid.to_string()])
            .status()
            .map_err(|err| format!("kill stale sidecar: {err}"))?;
        if !kill_status.success() {
            return Err(format!("failed to kill stale sidecar process {pid}"));
        }
    }
    Ok(())
}

#[cfg(unix)]
fn cleanup_dev_sidecar_artifacts(endpoint: &str, metadata_path: &Path) {
    let _ = fs::remove_file(endpoint);
    let _ = fs::remove_file(metadata_path);
}

fn write_sidecar_metadata(
    metadata_path: &Path,
    endpoint: &str,
    pid: u32,
    binary_hash: &str,
) -> Result<(), String> {
    let metadata = SidecarMetadata {
        endpoint: endpoint.to_string(),
        pid,
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
    let file =
        fs::File::open(sidecar_path).map_err(|err| format!("read desktop sidecar binary: {err}"))?;
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

#[cfg(windows)]
use std::os::windows::ffi::OsStrExt;
#[cfg(windows)]
use windows_sys::Win32::{
    Foundation::{
        CloseHandle, DuplicateHandle, GetLastError, LocalFree, DUPLICATE_SAME_ACCESS,
        ERROR_FILE_NOT_FOUND, ERROR_PIPE_BUSY, HANDLE, HLOCAL, INVALID_HANDLE_VALUE,
    },
    Security::{
        Authorization::ConvertSidToStringSidW, GetTokenInformation, TokenUser, TOKEN_QUERY,
        TOKEN_USER,
    },
    Storage::FileSystem::{
        CreateFileW, ReadFile, WriteFile, FILE_ATTRIBUTE_NORMAL, FILE_GENERIC_READ,
        FILE_GENERIC_WRITE, OPEN_EXISTING,
    },
    System::{
        Pipes::WaitNamedPipeW,
        Threading::{
            GetCurrentProcess, GetCurrentThread, OpenProcess, OpenProcessToken, TerminateProcess,
            PROCESS_TERMINATE,
        },
        IO::CancelSynchronousIo,
    },
};

#[cfg(windows)]
fn finalize_sidecar_streams(_: &SidecarStream, _: &SidecarStream) -> Result<(), String> {
    Ok(())
}

#[cfg(windows)]
fn is_sidecar_gone(pipe_name: &str) -> bool {
    matches!(connect_sidecar_endpoint(pipe_name), Ok(None))
}

#[cfg(windows)]
fn connect_sidecar_endpoint(
    pipe_name: &str,
) -> Result<Option<(SidecarStream, SidecarStream)>, String> {
    const MAX_BUSY_RETRIES: u32 = 3;
    let wide = wide_cstring(pipe_name);
    for _ in 0..=MAX_BUSY_RETRIES {
        let handle = unsafe {
            CreateFileW(
                wide.as_ptr(),
                FILE_GENERIC_READ | FILE_GENERIC_WRITE,
                0,
                std::ptr::null(),
                OPEN_EXISTING,
                FILE_ATTRIBUTE_NORMAL,
                std::ptr::null_mut(),
            )
        };
        if handle == INVALID_HANDLE_VALUE {
            let err = unsafe { GetLastError() };
            if err == ERROR_FILE_NOT_FOUND {
                return Ok(None);
            }
            if err == ERROR_PIPE_BUSY {
                let waited = unsafe { WaitNamedPipeW(wide.as_ptr(), 5_000) };
                if waited == 0 {
                    // Still busy or pipe closed between retries; let the caller
                    // decide whether to keep trying.
                    return Ok(None);
                }
                continue;
            }
            return Err(format!("open named pipe {pipe_name}: error {err}"));
        }

        let dup = match duplicate_handle(handle) {
            Ok(dup) => dup,
            Err(err) => {
                unsafe {
                    CloseHandle(handle);
                }
                return Err(format!("duplicate pipe handle: error {err}"));
            }
        };
        return Ok(Some((PipeHandle(handle), PipeHandle(dup))));
    }
    Ok(None)
}

#[cfg(windows)]
fn force_kill_sidecar(pid: u32) -> Result<(), String> {
    unsafe {
        let handle = OpenProcess(PROCESS_TERMINATE, 0, pid);
        if handle.is_null() {
            return Err(format!(
                "open sidecar process {pid}: error {}",
                GetLastError()
            ));
        }
        let ok = TerminateProcess(handle, 1);
        let err = if ok == 0 { GetLastError() } else { 0 };
        CloseHandle(handle);
        if ok == 0 {
            return Err(format!("terminate sidecar process {pid}: error {err}"));
        }
    }
    Ok(())
}

#[cfg(windows)]
fn cleanup_dev_sidecar_artifacts(_endpoint: &str, metadata_path: &Path) {
    // Named pipes release themselves when the server closes the listener;
    // only the metadata file persists on disk.
    let _ = fs::remove_file(metadata_path);
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
    base.join("leapmux-desktop").join("sidecar.json")
}

#[cfg(windows)]
fn sidecar_identity() -> Result<String, String> {
    use std::sync::OnceLock;
    static CACHED: OnceLock<Result<String, String>> = OnceLock::new();
    CACHED
        .get_or_init(|| {
            current_user_sid().map(|raw| {
                raw.chars()
                    .map(|c| {
                        if c.is_ascii_alphanumeric() || c == '-' {
                            c
                        } else {
                            '_'
                        }
                    })
                    .collect()
            })
        })
        .clone()
}

#[cfg(windows)]
fn current_user_sid() -> Result<String, String> {
    unsafe {
        let mut token: HANDLE = std::ptr::null_mut();
        if OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) == 0 {
            return Err(format!("open process token: error {}", GetLastError()));
        }
        let mut needed: u32 = 0;
        GetTokenInformation(token, TokenUser, std::ptr::null_mut(), 0, &mut needed);
        let mut buffer = vec![0u8; needed as usize];
        let ok = GetTokenInformation(
            token,
            TokenUser,
            buffer.as_mut_ptr() as *mut _,
            needed,
            &mut needed,
        );
        let token_err = if ok == 0 { GetLastError() } else { 0 };
        CloseHandle(token);
        if ok == 0 {
            return Err(format!("get token user info: error {token_err}"));
        }
        let user_info = &*(buffer.as_ptr() as *const TOKEN_USER);
        let mut sid_string_ptr: *mut u16 = std::ptr::null_mut();
        if ConvertSidToStringSidW(user_info.User.Sid, &mut sid_string_ptr) == 0 {
            return Err(format!("convert sid to string: error {}", GetLastError()));
        }
        let mut len = 0;
        while *sid_string_ptr.add(len) != 0 {
            len += 1;
        }
        let slice = std::slice::from_raw_parts(sid_string_ptr, len);
        let sid = String::from_utf16_lossy(slice);
        LocalFree(sid_string_ptr as HLOCAL);
        Ok(sid)
    }
}

#[cfg(windows)]
fn duplicate_handle(source: HANDLE) -> Result<HANDLE, u32> {
    unsafe {
        let mut dup: HANDLE = std::ptr::null_mut();
        let proc = GetCurrentProcess();
        if DuplicateHandle(proc, source, proc, &mut dup, 0, 0, DUPLICATE_SAME_ACCESS) == 0 {
            return Err(GetLastError());
        }
        Ok(dup)
    }
}

#[cfg(windows)]
fn wide_cstring(s: &str) -> Vec<u16> {
    std::ffi::OsStr::new(s)
        .encode_wide()
        .chain(std::iter::once(0))
        .collect()
}

// Invariant: .0 is an owned, non-aliased HANDLE — Drop closes it, so
// constructors (only reachable within this module) must ensure exclusive
// ownership. Aliased handles would cause double-close and break Send.
#[cfg(windows)]
struct PipeHandle(HANDLE);

#[cfg(windows)]
impl Drop for PipeHandle {
    fn drop(&mut self) {
        unsafe {
            CloseHandle(self.0);
        }
    }
}

// Safe given the ownership invariant on PipeHandle.0 above.
#[cfg(windows)]
unsafe impl Send for PipeHandle {}

#[cfg(windows)]
impl Read for PipeHandle {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        const ERROR_BROKEN_PIPE: u32 = 109;
        const ERROR_PIPE_NOT_CONNECTED: u32 = 233;
        let mut read: u32 = 0;
        let ok = unsafe {
            ReadFile(
                self.0,
                buf.as_mut_ptr() as *mut _,
                buf.len() as u32,
                &mut read,
                std::ptr::null_mut(),
            )
        };
        if ok == 0 {
            let err = unsafe { GetLastError() };
            if err == ERROR_BROKEN_PIPE || err == ERROR_PIPE_NOT_CONNECTED {
                return Ok(0);
            }
            return Err(io::Error::from_raw_os_error(err as i32));
        }
        Ok(read as usize)
    }
}

#[cfg(windows)]
impl Write for PipeHandle {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        let mut written: u32 = 0;
        let ok = unsafe {
            WriteFile(
                self.0,
                buf.as_ptr() as *const _,
                buf.len() as u32,
                &mut written,
                std::ptr::null_mut(),
            )
        };
        if ok == 0 {
            return Err(io::Error::from_raw_os_error(unsafe { GetLastError() } as i32));
        }
        Ok(written as usize)
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

// ThreadHandle wraps a duplicated thread HANDLE and closes it on drop. The
// raw HANDLE is !Send; this newtype asserts the invariant that callers hand
// ownership to exactly one thread.
#[cfg(windows)]
struct ThreadHandle(HANDLE);

#[cfg(windows)]
impl Drop for ThreadHandle {
    fn drop(&mut self) {
        unsafe { CloseHandle(self.0) };
    }
}

// Safe: HANDLE is a kernel-object reference; ownership has been duplicated
// explicitly via DuplicateHandle and is not shared elsewhere.
#[cfg(windows)]
unsafe impl Send for ThreadHandle {}

// HandshakeWatchdog bounds a blocking I/O sequence on the arming thread with
// a deadline. On drop (or deadline), the watchdog thread exits; if the
// deadline elapses before drop, it calls CancelSynchronousIo on the arming
// thread so the in-flight ReadFile/WriteFile returns with an error.
#[cfg(windows)]
struct HandshakeWatchdog {
    done_tx: std::sync::mpsc::Sender<()>,
}

#[cfg(windows)]
impl HandshakeWatchdog {
    fn arm(timeout: Duration) -> Result<Self, String> {
        let target = duplicate_current_thread_handle()?;
        let (done_tx, done_rx) = std::sync::mpsc::channel::<()>();
        thread::spawn(move || {
            if done_rx.recv_timeout(timeout).is_err() {
                unsafe { CancelSynchronousIo(target.0) };
            }
            drop(target);
        });
        Ok(HandshakeWatchdog { done_tx })
    }
}

#[cfg(windows)]
impl Drop for HandshakeWatchdog {
    fn drop(&mut self) {
        let _ = self.done_tx.send(());
    }
}

#[cfg(windows)]
fn duplicate_current_thread_handle() -> Result<ThreadHandle, String> {
    duplicate_handle(unsafe { GetCurrentThread() })
        .map(ThreadHandle)
        .map_err(|err| format!("duplicate current thread handle: error {err}"))
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

        let shell = Self {
            app_handle,
            sidecar: SidecarProcess {
                _child: bootstrap.child,
                writer: Mutex::new(bootstrap.writer),
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
        let shutdown = self.send_request_async(proto::request::Method::Shutdown(
            proto::ShutdownRequest {},
        ));
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
        let info = match resp.result {
            Some(proto::response::Result::SidecarInfo(info)) => info,
            _ => return Err("unexpected response for get_sidecar_info".to_string()),
        };
        let shell_mode = match info.shell_mode() {
            proto::SidecarShellMode::Solo => ShellMode::Solo,
            proto::SidecarShellMode::Distributed => ShellMode::Distributed,
            _ => ShellMode::Launcher,
        };
        let mut state = self.state.lock().unwrap();
        state.shell_mode = shell_mode;
        state.connected = info.connected;
        state.hub_url = info.hub_url;
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

    async fn save_window_size(
        &self,
        width: u32,
        height: u32,
        maximized: bool,
    ) -> Result<(), String> {
        let _ = self
            .send_request_async(proto::request::Method::SetWindowSize(
                proto::SetWindowSizeRequest {
                    width: width as i32,
                    height: height as i32,
                    maximized,
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
    {
        let mut writer = sidecar.writer.lock().unwrap();
        if let Err(err) = write_frame(&mut *writer, &frame) {
            sidecar.pending.lock().unwrap().remove(&id);
            return Err(format!("write request: {err}"));
        }
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
        proto::event::Payload::ChannelClose(_) => {
            let _ = app_handle.emit("channel:close", Value::Null);
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
    let exe = std::env::current_exe()
        .map_err(|err| format!("resolve current exe: {err}"))?;
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
                    mode: cfg.mode,
                    hub_url: cfg.hub_url,
                    window_width: cfg.window_width,
                    window_height: cfg.window_height,
                    window_maximized: cfg.window_maximized,
                },
                build_info: BuildInfoResponse {
                    version: build.version,
                    commit_hash: build.commit_hash,
                    commit_time: build.commit_time,
                    build_time: build.build_time,
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
    check_response(
        shell
            .send_request_async(proto::request::Method::ConnectSolo(
                proto::ConnectSoloRequest {},
            ))
            .await?,
    )?;
    let mut state = shell.state.lock().unwrap();
    state.shell_mode = ShellMode::Solo;
    state.connected = true;
    state.hub_url.clear();
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

    let normalized_hub_url = match resp.result {
        Some(proto::response::Result::ConnectDistributed(r)) => r.hub_url,
        _ => return Err("unexpected response for connect_distributed".to_string()),
    };

    {
        let mut state = shell.state.lock().unwrap();
        state.shell_mode = ShellMode::Distributed;
        state.connected = true;
        state.hub_url = normalized_hub_url.clone();
    }

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
        base64::engine::general_purpose::STANDARD
            .decode(&payload.body_base64)
            .map_err(|err| format!("decode request body: {err}"))?
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
            headers: r.headers,
            body: base64::engine::general_purpose::STANDARD.encode(&r.body),
        }),
        _ => Err("unexpected response for proxy_http".to_string()),
    }
}

#[tauri::command]
async fn open_channel_relay(shell: State<'_, Arc<DesktopShell>>) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::OpenChannelRelay(
                proto::OpenChannelRelayRequest {},
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
    let data = base64::engine::general_purpose::STANDARD
        .decode(&b64_data)
        .map_err(|err| format!("decode channel message: {err}"))?;

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
async fn close_channel_relay(shell: State<'_, Arc<DesktopShell>>) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::CloseChannelRelay(
                proto::CloseChannelRelayRequest {},
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
        hub_url: config.hub_url,
        user_id: config.user_id,
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

#[tauri::command]
async fn switch_mode(
    shell: State<'_, Arc<DesktopShell>>,
    window: WebviewWindow,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::SwitchMode(
                proto::SwitchModeRequest {},
            ))
            .await?,
    )?;

    let local_app_url = {
        let mut state = shell.state.lock().unwrap();
        state.shell_mode = ShellMode::Launcher;
        state.connected = false;
        state.hub_url.clear();
        state.local_app_url.clone()
    };

    let target_url =
        Url::parse(&local_app_url).map_err(|err| format!("parse launcher url: {err}"))?;
    window
        .navigate(target_url)
        .map_err(|err| format!("navigate to launcher: {err}"))?;
    Ok(())
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
    maximized: bool,
) -> Result<(), String> {
    shell.save_window_size(width, height, maximized).await
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
        let menu = app.menu().ok_or_else(|| "app menu is not available".to_string())?;
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

    let help_menu = Submenu::with_id_and_items(
        app,
        HELP_SUBMENU_ID,
        "Help",
        true,
        &[&open_web_inspector],
    )?;

    Menu::with_items(
        app,
        &[
            &app_menu,
            &edit_menu,
            &view_menu,
            &window_menu,
            &help_menu,
        ],
    )
}

fn main() {
    // Work around known WebKitGTK issues on Linux:
    // - DMA-BUF renderer fails with "Failed to create GBM buffer"
    // - Hardware compositing can trigger Wayland protocol errors
    //   (tauri-apps/tauri#8541)
    // Disabling both avoids GPU buffer management issues while
    // keeping native Wayland support.
    #[cfg(target_os = "linux")]
    {
        std::env::set_var("WEBKIT_DISABLE_DMABUF_RENDERER", "1");
        std::env::set_var("WEBKIT_DISABLE_COMPOSITING_MODE", "1");
    }

    let builder = tauri::Builder::default()
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
            open_channel_relay,
            send_channel_message,
            close_channel_relay,
            create_tunnel,
            delete_tunnel,
            list_tunnels,
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
                        api.prevent_exit();
                        handle_app_exit(shell.inner().clone());
                    }
                }
            }
        });
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io;
    use std::sync::atomic::AtomicU64;
    use std::time::SystemTime;

    #[cfg(unix)]
    use std::os::unix::net::UnixListener;

    #[cfg(windows)]
    use windows_sys::Win32::Storage::FileSystem::PIPE_ACCESS_DUPLEX;
    #[cfg(windows)]
    use windows_sys::Win32::System::Pipes::{
        ConnectNamedPipe, CreateNamedPipeW, PIPE_READMODE_BYTE, PIPE_TYPE_BYTE, PIPE_WAIT,
    };

    static TEST_COUNTER: AtomicU64 = AtomicU64::new(0);

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
            let response = proto::Frame {
                message: Some(proto::frame::Message::Response(proto::Response {
                    id: 1,
                    error: String::new(),
                    result: Some(proto::response::Result::SidecarInfo(proto::SidecarInfo {
                        protocol_version: SIDECAR_PROTOCOL_VERSION.to_string(),
                        binary_hash: "test-hash".to_string(),
                        pid: std::process::id() as i64,
                        shell_mode: 0,
                        connected: false,
                        hub_url: String::new(),
                    })),
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

    #[cfg(windows)]
    fn spawn_fake_sidecar_pipe(pipe_name: String) -> thread::JoinHandle<()> {
        // Create the pipe synchronously so the client can connect as soon as
        // this function returns, regardless of when the accept thread runs.
        let wide = wide_cstring(&pipe_name);
        let handle = unsafe {
            CreateNamedPipeW(
                wide.as_ptr(),
                PIPE_ACCESS_DUPLEX,
                PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT,
                1,
                65536,
                65536,
                0,
                std::ptr::null(),
            )
        };
        assert!(
            handle != INVALID_HANDLE_VALUE,
            "CreateNamedPipeW failed: error {}",
            unsafe { GetLastError() },
        );
        let server = PipeHandle(handle);
        thread::spawn(move || {
            let mut stream = server;
            let connected = unsafe { ConnectNamedPipe(stream.0, std::ptr::null_mut()) };
            if connected == 0 {
                let err = unsafe { GetLastError() };
                const ERROR_PIPE_CONNECTED: u32 = 535;
                assert_eq!(
                    err, ERROR_PIPE_CONNECTED,
                    "ConnectNamedPipe failed: error {err}"
                );
            }
            let _ = read_frame(&mut stream).expect("read handshake request");
            let response = proto::Frame {
                message: Some(proto::frame::Message::Response(proto::Response {
                    id: 1,
                    error: String::new(),
                    result: Some(proto::response::Result::SidecarInfo(proto::SidecarInfo {
                        protocol_version: SIDECAR_PROTOCOL_VERSION.to_string(),
                        binary_hash: "test-hash".to_string(),
                        pid: std::process::id() as i64,
                        shell_mode: 0,
                        connected: false,
                        hub_url: String::new(),
                    })),
                })),
            };
            write_frame(&mut stream, &response).expect("write handshake response");
            let _ = stream.read(&mut [0u8; 1]);
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
    fn handshake_watchdog_cancels_wedged_read() {
        // Spawn a server that accepts the connection but never writes back.
        // With the watchdog armed, the ReadFile should return an error after
        // the deadline instead of blocking indefinitely.
        let pipe_name = unique_test_pipe_name();
        let wide = wide_cstring(&pipe_name);
        let server_handle = unsafe {
            CreateNamedPipeW(
                wide.as_ptr(),
                PIPE_ACCESS_DUPLEX,
                PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT,
                1,
                65536,
                65536,
                0,
                std::ptr::null(),
            )
        };
        assert!(server_handle != INVALID_HANDLE_VALUE);
        let server = PipeHandle(server_handle);
        let server_thread = thread::spawn(move || {
            let _stream = server;
            unsafe { ConnectNamedPipe(_stream.0, std::ptr::null_mut()) };
            thread::sleep(Duration::from_secs(3));
        });

        let (mut reader, _writer) = connect_sidecar_endpoint(&pipe_name)
            .expect("connect ok")
            .expect("server reachable");

        let start = Instant::now();
        let _watchdog = HandshakeWatchdog::arm(Duration::from_millis(200)).expect("arm");
        let mut buf = [0u8; 16];
        let err = reader.read(&mut buf).expect_err("read should fail");
        let elapsed = start.elapsed();

        assert!(
            elapsed < Duration::from_secs(2),
            "read should have been cancelled, elapsed {:?}",
            elapsed
        );
        const ERROR_OPERATION_ABORTED: i32 = 995;
        assert_eq!(
            err.raw_os_error(),
            Some(ERROR_OPERATION_ABORTED),
            "unexpected error: {err}"
        );

        drop(_watchdog);
        let _ = server_thread.join();
    }

    #[cfg(windows)]
    #[test]
    fn sidecar_identity_returns_valid_sid_form() {
        let sid = sidecar_identity().expect("sidecar_identity");
        assert!(
            sid.starts_with("S-1-"),
            "expected SID to start with S-1-, got: {sid}"
        );
        assert!(
            sid.chars().all(|c| c.is_ascii_alphanumeric() || c == '-'),
            "sanitized SID must contain only alphanumerics and hyphens, got: {sid}"
        );
    }

    #[cfg(windows)]
    #[test]
    fn dev_sidecar_endpoint_has_expected_shape() {
        let name = dev_sidecar_endpoint().expect("endpoint");
        assert!(
            name.starts_with("\\\\.\\pipe\\leapmux-desktop-"),
            "unexpected prefix in pipe name: {name}"
        );
        assert!(
            name.ends_with("-sidecar"),
            "unexpected suffix in pipe name: {name}"
        );
    }

    #[cfg(windows)]
    #[test]
    fn dev_sidecar_metadata_path_ends_with_sidecar_json() {
        let path = dev_sidecar_metadata_path();
        let suffix = PathBuf::from("leapmux-desktop").join("sidecar.json");
        assert!(
            path.ends_with(&suffix),
            "expected path to end with {}, got: {}",
            suffix.display(),
            path.display()
        );
    }

    #[cfg(windows)]
    #[test]
    fn write_sidecar_metadata_roundtrips_json() {
        let counter = TEST_COUNTER.fetch_add(1, Ordering::SeqCst);
        let path = std::env::temp_dir().join(format!("leapmux-test-metadata-{counter}.json"));
        let _ = fs::remove_file(&path);

        write_sidecar_metadata(&path, "\\\\.\\pipe\\test", 4242, "hash-abc")
            .expect("write metadata");
        let data = fs::read_to_string(&path).expect("read metadata");
        assert!(data.contains("\\\\\\\\.\\\\pipe\\\\test"));
        assert!(data.contains("\"pid\": 4242"));
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
        let sidecar = SidecarProcess {
            _child: None,
            writer: Mutex::new(Box::new(writer)),
            pending: pending.clone(),
            next_id: AtomicU64::new(1),
        };

        let responder = thread::spawn(move || {
            loop {
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
                thread::sleep(Duration::from_millis(10));
            }
        });

        let resp = tauri::async_runtime::block_on(send_sidecar_request(
            &sidecar,
            proto::request::Method::Shutdown(proto::ShutdownRequest {}),
        ))
        .expect("send shutdown request");
        responder.join().expect("responder join");

        assert_eq!(resp.id, 1);

        let mut cursor = io::Cursor::new(buffer.snapshot());
        let frame = read_frame(&mut cursor).expect("read request frame");
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
}
