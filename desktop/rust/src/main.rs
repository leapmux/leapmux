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
use tauri::{
    menu::{Menu, MenuItem, PredefinedMenuItem, Submenu, HELP_SUBMENU_ID},
    AppHandle, Emitter, Manager, RunEvent, State, Url, WebviewWindow, Window, WindowEvent,
};
use tokio::sync::oneshot;

const SHOW_ABOUT_MENU_ID: &str = "show-about";
const OPEN_WEB_INSPECTOR_MENU_ID: &str = "open-web-inspector";
#[cfg(any(target_os = "linux", target_os = "windows"))]
const QUIT_MENU_ID: &str = "quit";
#[cfg(any(target_os = "linux", target_os = "windows"))]
const MINIMIZE_MENU_ID: &str = "minimize";
#[cfg(any(target_os = "linux", target_os = "windows"))]
const MAXIMIZE_MENU_ID: &str = "maximize";
const MAX_FRAME_SIZE: u64 = 16 * 1024 * 1024; // 16 MB
const SIDECAR_PROTOCOL_VERSION: &str = "1";
const DEV_SIDECAR_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(5);
#[cfg(unix)]
const DEV_SIDECAR_CONNECT_TIMEOUT: Duration = Duration::from_secs(2);

#[derive(Serialize, Deserialize)]
struct SidecarMetadata {
    socket_path: String,
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
    #[cfg(unix)]
    if cfg!(debug_assertions) {
        return bootstrap_dev_socket_sidecar(sidecar_path);
    }
    spawn_stdio_sidecar(sidecar_path)
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

#[cfg(unix)]
fn bootstrap_dev_socket_sidecar(sidecar_path: &Path) -> Result<SidecarBootstrap, String> {
    let socket_path = dev_sidecar_socket_path();
    let metadata_path = dev_sidecar_metadata_path();
    let binary_hash = hash_sidecar_binary(sidecar_path)?;

    if let Ok(Some((reader, writer, info))) = try_connect_dev_sidecar(&socket_path) {
        if info.protocol_version == SIDECAR_PROTOCOL_VERSION && info.binary_hash == binary_hash {
            write_sidecar_metadata(&metadata_path, &socket_path, info.pid as u32, &binary_hash)?;
            return Ok(SidecarBootstrap {
                child: None,
                reader,
                writer,
            });
        }
        request_sidecar_shutdown(&socket_path, info.pid as u32)?;
        cleanup_dev_sidecar_artifacts(&socket_path, &metadata_path);
    } else {
        cleanup_dev_sidecar_artifacts(&socket_path, &metadata_path);
    }

    let mut command = Command::new(sidecar_path);
    command
        .env("LEAPMUX_DESKTOP_DEV_SOCKET", &socket_path)
        .env("LEAPMUX_DESKTOP_BINARY_HASH", &binary_hash)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::inherit());
    let child = command
        .spawn()
        .map_err(|err| format!("spawn desktop sidecar: {err}"))?;
    let start = Instant::now();
    loop {
        match try_connect_dev_sidecar(&socket_path) {
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
                write_sidecar_metadata(
                    &metadata_path,
                    &socket_path,
                    info.pid as u32,
                    &binary_hash,
                )?;
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
            return Err("timed out waiting for desktop sidecar socket".to_string());
        }
        thread::sleep(Duration::from_millis(100));
    }
}

#[cfg(unix)]
fn try_connect_dev_sidecar(
    socket_path: &Path,
) -> Result<
    Option<(
        Box<dyn Read + Send>,
        Box<dyn Write + Send>,
        proto::SidecarInfo,
    )>,
    String,
> {
    if !socket_path.exists() {
        return Ok(None);
    }

    let stream = match UnixStream::connect(socket_path) {
        Ok(stream) => stream,
        Err(err)
            if err.kind() == io::ErrorKind::NotFound
                || err.kind() == io::ErrorKind::ConnectionRefused =>
        {
            return Ok(None);
        }
        Err(err) => return Err(format!("connect desktop sidecar socket: {err}")),
    };
    stream
        .set_read_timeout(Some(DEV_SIDECAR_CONNECT_TIMEOUT))
        .map_err(|err| format!("set sidecar socket read timeout: {err}"))?;
    stream
        .set_write_timeout(Some(DEV_SIDECAR_CONNECT_TIMEOUT))
        .map_err(|err| format!("set sidecar socket write timeout: {err}"))?;
    let reader = stream
        .try_clone()
        .map_err(|err| format!("clone sidecar socket: {err}"))?;
    let writer = stream;
    let mut info_reader = reader
        .try_clone()
        .map_err(|err| format!("clone sidecar socket: {err}"))?;
    let mut info_writer = writer
        .try_clone()
        .map_err(|err| format!("clone sidecar socket: {err}"))?;
    let info = fetch_sidecar_info(&mut info_reader, &mut info_writer)?;
    Ok(Some((
        Box::new(reader),
        Box::new(BufWriter::new(writer)),
        info,
    )))
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
    if !resp.error.is_empty() {
        return Err(resp.error);
    }
    match resp.result {
        Some(proto::response::Result::SidecarInfo(info)) => Ok(info),
        _ => Err("unexpected response for get_sidecar_info".to_string()),
    }
}

#[cfg(unix)]
fn request_sidecar_shutdown(socket_path: &Path, pid: u32) -> Result<(), String> {
    if let Ok(stream) = UnixStream::connect(socket_path) {
        let reader = stream
            .try_clone()
            .map_err(|err| format!("clone sidecar socket: {err}"))?;
        let mut writer = stream;
        writer
            .set_write_timeout(Some(DEV_SIDECAR_CONNECT_TIMEOUT))
            .map_err(|err| format!("set sidecar socket write timeout: {err}"))?;
        let mut reader = reader;
        reader
            .set_read_timeout(Some(DEV_SIDECAR_CONNECT_TIMEOUT))
            .map_err(|err| format!("set sidecar socket read timeout: {err}"))?;
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
        if !socket_path.exists() {
            return Ok(());
        }
        thread::sleep(Duration::from_millis(100));
    }

    force_kill_sidecar(pid)?;
    Ok(())
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
fn cleanup_dev_sidecar_artifacts(socket_path: &Path, metadata_path: &Path) {
    let _ = fs::remove_file(socket_path);
    let _ = fs::remove_file(metadata_path);
}

#[cfg(unix)]
fn write_sidecar_metadata(
    metadata_path: &Path,
    socket_path: &Path,
    pid: u32,
    binary_hash: &str,
) -> Result<(), String> {
    let metadata = SidecarMetadata {
        socket_path: socket_path.display().to_string(),
        pid,
        binary_hash: binary_hash.to_string(),
        protocol_version: SIDECAR_PROTOCOL_VERSION.to_string(),
    };
    if let Some(parent) = metadata_path.parent() {
        fs::create_dir_all(parent).map_err(|err| format!("create sidecar metadata dir: {err}"))?;
        #[cfg(unix)]
        fs::set_permissions(parent, fs::Permissions::from_mode(0o700))
            .map_err(|err| format!("set sidecar metadata dir permissions: {err}"))?;
    }
    let data = serde_json::to_vec_pretty(&metadata)
        .map_err(|err| format!("serialize sidecar metadata: {err}"))?;
    fs::write(metadata_path, data).map_err(|err| format!("write sidecar metadata: {err}"))?;
    #[cfg(unix)]
    fs::set_permissions(metadata_path, fs::Permissions::from_mode(0o600))
        .map_err(|err| format!("set sidecar metadata permissions: {err}"))?;
    Ok(())
}

#[cfg(unix)]
fn dev_sidecar_socket_path() -> PathBuf {
    dev_sidecar_runtime_dir().join(format!("{}-sidecar.sock", sidecar_identity()))
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
    std::env::var("USER")
        .or_else(|_| std::env::var("USERNAME"))
        .unwrap_or_else(|_| "default".to_string())
        .chars()
        .map(|ch| if ch.is_ascii_alphanumeric() { ch } else { '_' })
        .collect()
}

fn hash_sidecar_binary(sidecar_path: &Path) -> Result<String, String> {
    let data =
        fs::read(sidecar_path).map_err(|err| format!("read desktop sidecar binary: {err}"))?;
    let digest = Sha256::digest(&data);
    Ok(format!("{:x}", digest))
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

        tauri::async_runtime::block_on(shell.refresh_state_from_sidecar())?;
        Ok(shell)
    }

    async fn send_request_async(
        &self,
        method: proto::request::Method,
    ) -> Result<proto::Response, String> {
        let id = self.sidecar.next_id.fetch_add(1, Ordering::Relaxed);
        let (tx, rx) = oneshot::channel();
        self.sidecar.pending.lock().unwrap().insert(id, tx);

        let frame = proto::Frame {
            message: Some(proto::frame::Message::Request(proto::Request {
                id,
                method: Some(method),
            })),
        };
        {
            let mut writer = self.sidecar.writer.lock().unwrap();
            if let Err(err) = write_frame(&mut *writer, &frame) {
                self.sidecar.pending.lock().unwrap().remove(&id);
                return Err(format!("write request: {err}"));
            }
        }

        rx.await
            .map_err(|_| "desktop sidecar disconnected".to_string())?
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
        let shell_mode = match info.shell_mode.as_str() {
            "solo" => ShellMode::Solo,
            "distributed" => ShellMode::Distributed,
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

    // Prefer the dev-mode path next to the Cargo project; fall back to the
    // bundled resource directory used in release builds.
    let dev_path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("_up_")
        .join("go")
        .join("bin")
        .join(&sidecar_name);
    if dev_path.exists() {
        return Ok(dev_path);
    }

    let resource_dir = app_handle
        .path()
        .resource_dir()
        .map_err(|err| format!("resolve resource dir: {err}"))?;
    Ok(resource_dir
        .join("_up_")
        .join("go")
        .join("bin")
        .join(&sidecar_name))
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

#[tauri::command]
async fn restart_app(
    shell: State<'_, Arc<DesktopShell>>,
    _window: WebviewWindow,
) -> Result<(), String> {
    let current_exe =
        std::env::current_exe().map_err(|err| format!("resolve current exe: {err}"))?;
    Command::new(current_exe)
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
    app.exit(0);
}

#[tauri::command]
fn hide_menu_bar(app: AppHandle) {
    #[cfg(any(target_os = "linux", target_os = "windows"))]
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.hide_menu();
    }
    let _ = app;
}

#[tauri::command]
fn toggle_menu_bar(app: AppHandle) {
    #[cfg(any(target_os = "linux", target_os = "windows"))]
    if let Some(w) = app.get_webview_window("main") {
        if w.is_menu_visible().unwrap_or(false) {
            let _ = w.hide_menu();
        } else {
            let _ = w.show_menu();
        }
    }
    let _ = app;
}

#[tauri::command]
fn open_web_inspector(app: AppHandle) {
    open_main_web_inspector(&app);
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
        shell.app_handle.exit(0);
    });
}

fn build_app_menu(app: &AppHandle) -> tauri::Result<Menu<tauri::Wry>> {
    let show_about = MenuItem::with_id(
        app,
        SHOW_ABOUT_MENU_ID,
        "About LeapMux Desktop...",
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

    #[cfg(target_os = "macos")]
    let app_menu = Submenu::with_items(
        app,
        "LeapMux Desktop",
        true,
        &[
            &show_about,
            &PredefinedMenuItem::separator(app)?,
            &PredefinedMenuItem::services(app, None)?,
            &PredefinedMenuItem::separator(app)?,
            &PredefinedMenuItem::hide(app, None)?,
            &PredefinedMenuItem::hide_others(app, None)?,
            &PredefinedMenuItem::separator(app)?,
            &PredefinedMenuItem::quit(app, None)?,
        ],
    )?;

    let file_menu = Submenu::with_items(
        app,
        "File",
        true,
        &[
            #[cfg(any(target_os = "macos", target_os = "windows"))]
            &PredefinedMenuItem::close_window(app, None)?,
            #[cfg(any(target_os = "linux", target_os = "windows"))]
            &MenuItem::with_id(app, QUIT_MENU_ID, "Quit", true, Some("Ctrl+Q"))?,
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

    #[cfg(target_os = "macos")]
    let view_menu = Submenu::with_items(
        app,
        "View",
        true,
        &[&PredefinedMenuItem::fullscreen(app, None)?],
    )?;

    #[cfg(target_os = "macos")]
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

    #[cfg(any(target_os = "linux", target_os = "windows"))]
    let window_menu = Submenu::with_items(
        app,
        "Window",
        true,
        &[
            &MenuItem::with_id(app, MINIMIZE_MENU_ID, "Minimize", true, None::<&str>)?,
            &MenuItem::with_id(app, MAXIMIZE_MENU_ID, "Maximize", true, None::<&str>)?,
        ],
    )?;

    let help_menu = Submenu::with_id_and_items(
        app,
        HELP_SUBMENU_ID,
        "Help",
        true,
        &[
            #[cfg(any(target_os = "linux", target_os = "windows"))]
            &show_about,
            &open_web_inspector,
        ],
    )?;

    Menu::with_items(
        app,
        &[
            #[cfg(target_os = "macos")]
            &app_menu,
            &file_menu,
            &edit_menu,
            #[cfg(target_os = "macos")]
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

    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            focus_main_window(app);
        }))
        .menu(build_app_menu)
        .on_menu_event(|app, event| {
            if event.id() == SHOW_ABOUT_MENU_ID {
                let _ = app.emit("menu:show-about", ());
            } else if event.id() == OPEN_WEB_INSPECTOR_MENU_ID {
                open_main_web_inspector(app);
            }
            #[cfg(any(target_os = "linux", target_os = "windows"))]
            {
                if event.id() == QUIT_MENU_ID {
                    app.exit(0);
                } else if event.id() == MINIMIZE_MENU_ID {
                    if let Some(w) = app.get_webview_window("main") {
                        let _ = w.minimize();
                    }
                } else if event.id() == MAXIMIZE_MENU_ID {
                    if let Some(w) = app.get_webview_window("main") {
                        let is_max = w.is_maximized().unwrap_or(false);
                        if is_max {
                            let _ = w.unmaximize();
                        } else {
                            let _ = w.maximize();
                        }
                    }
                }
            }
            // Re-hide the menu bar after a menu item is selected (Linux/Windows).
            #[cfg(any(target_os = "linux", target_os = "windows"))]
            if let Some(w) = app.get_webview_window("main") {
                let _ = w.hide_menu();
            }
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
            // On Linux, titleBarStyle "Overlay" is ignored, so remove
            // native decorations entirely — the frontend renders its own
            // titlebar with custom window controls.
            #[cfg(target_os = "linux")]
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
            restart_app,
            save_window_geometry,
            quit_app,
            hide_menu_bar,
            toggle_menu_bar,
            open_web_inspector,
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
