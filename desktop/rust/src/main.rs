#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

#[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
compile_error!("LeapMux Desktop only supports macOS, Linux, and Windows");

mod frame;
pub(crate) mod proto {
    include!(concat!(env!("OUT_DIR"), "/leapmux.desktop.v1.rs"));
}

// Test-only peak-allocation tracker; `#[global_allocator]` is wired below.
#[cfg(test)]
mod alloc_probe;

mod sidecar_ipc;

// Windows-only connect/handshake + its tests. A single module-level `cfg`
// (here) replaces the per-import `#[cfg(windows)]` attributes the inline version
// grew -- it restricts every windows-only import inside `windows_impl` at once,
// so a new one cannot compile silently on macOS and fail only on Windows CI.
#[cfg(windows)]
mod windows_impl;

// Sidecar process bootstrap/connect/handshake/shutdown -- the layer above
// `sidecar_ipc` (transport) and `frame` (codec).
mod sidecar;

// Streaming file-save subsystem (file_save_open/write/commit/abort chain).
mod file_save;

// The tray icon, the window-behaviour policy it governs, and the launch
// decision that policy feeds. Its per-platform minimize hooks carry their own
// `cfg` attributes inside the module, the way `windows_impl` does.
mod tray;

#[cfg(target_os = "linux")]
mod tabfix_linux;

use base64::Engine;
use serde::{Deserialize, Serialize};
use serde_json::json;

use crate::file_save::{
    file_save_abort, file_save_commit, file_save_open, file_save_open_dialog, file_save_write,
    SaveStreamRegistry, SAVE_HANDLE_GC_INTERVAL, SAVE_HANDLE_IDLE_TIMEOUT,
};
use crate::frame::{get_sidecar_info_request, read_frame, write_frame};
use crate::sidecar::bootstrap_sidecar;
// The Windows-only connect/handshake surface (and its windows-only imports)
// lives in `windows_impl`, behind a single module-level `#[cfg(windows)]` --
// see `mod windows_impl` below -- so the next windows-only import lands there
// and cannot rot invisibly on a macOS dev host.
// Unix-only peer-credential helpers; referenced by the unix sidecar-IPC tests.
#[cfg(all(unix, test))]
use crate::sidecar_ipc::{
    dev_sidecar_endpoint, endpoint_holder_pid, private_dev_sidecar_endpoint, require_peer_uid,
    require_same_user_peer, socket_peer_pid, socket_peer_uid,
};
// The unix connect/handshake twin lives in `sidecar`; the tests exercise it
// directly.
#[cfg(all(unix, test))]
use crate::sidecar::connect_and_handshake_dev_sidecar;
#[cfg(all(unix, test))]
use std::os::unix::net::UnixStream;
use std::{
    collections::HashMap,
    io::{self, BufReader, Read, Write},
    path::PathBuf,
    process::Child,
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        Arc, Mutex,
    },
    thread,
    time::Duration,
};
// `Command` is used only by the macOS relaunch helper (it shells out to
// `/usr/bin/open -n` via LaunchServices); restrict it to macOS so the other
// targets do not trip `-D unused-imports`.
#[cfg(target_os = "macos")]
use std::process::Command;
// `OsStr`, `Path`, and `Instant` are referenced unqualified only by the test
// module below (plus the `Path`/`OsStr`/`Instant` save code, now in
// `file_save`). `fs` is referenced unqualified only by the unix socket tests.
// Gate them so the production binary stays warning-free on every platform.
#[cfg(all(unix, test))]
use std::fs;
#[cfg(test)]
use std::{ffi::OsStr, path::Path, time::Instant};
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
/// Write one diagnostic line from the shell to standard error.
///
/// ONE prefix for the whole binary, so `grep leapmux-desktop:` returns the
/// whole log. Before this macro the same program wrote four spellings --
/// `leapmux-desktop:`, `leapmux:`, and three lines with no prefix at all -- and
/// `grep` on any one of them found a part of the log and hid the rest.
///
/// `desktop-sidecar:` in `sidecar.rs` is the deliberate exception, and it must
/// stay: it marks a line that the SIDECAR wrote, which is another program's
/// output that this shell only forwards.
///
/// A macro and not a function, because `eprintln!` takes format arguments that
/// borrow from the call site.
macro_rules! shell_log {
    ($($arg:tt)*) => {
        eprintln!("leapmux-desktop: {}", format_args!($($arg)*))
    };
}
pub(crate) use shell_log;

/// The label of the one window that `tauri.conf.json` declares.
///
/// Every lookup and every window-event filter in the shell spells it through
/// this constant, so a typo at a call site is a compile error rather than a
/// hook that never fires. Two JSON files hold the same text and no Rust
/// constant can bind either: `tauri.conf.json` declares the window, and
/// `capabilities/default.json` lists `"windows": ["main"]`, which is what gives
/// that window its `core:window:*` permissions. A rename must edit all three,
/// and a capability that matches no window denies every window command from
/// the webview at run time, with nothing to see at compile time.
///
/// It is NOT contract material. The webview reaches its own window with
/// `getCurrentWindow()` and spells no label, so the value crosses no language
/// boundary.
///
/// Not to be confused with `tray::TRAY_ID`, which carries the same text for the
/// tray icon.
pub(crate) const MAIN_WINDOW_LABEL: &str = "main";
pub(crate) const SIDECAR_PROTOCOL_VERSION: &str = "1";
pub(crate) const DEV_SIDECAR_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(30);
// CONNECT_TIMEOUT is the outer loop budget for "endpoint reachable +
// handshake succeeds"; HANDSHAKE_TIMEOUT bounds a single attempt. Keep
// CONNECT meaningfully larger so the loop can retry after a wedged probe.
pub(crate) const DEV_SIDECAR_CONNECT_TIMEOUT: Duration = Duration::from_secs(60);
pub(crate) const DEV_SIDECAR_HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(30);
const SIDECAR_INITIAL_HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(30);
/// How long `setup` waits for the cached window behaviour before it launches
/// with the built-in defaults. Short, because this call sits on the startup
/// path and every field it carries has a safe fallback: the cost of giving up
/// is a tray icon that appears once the webview reports, not a broken launch.
const CACHED_CONFIG_TIMEOUT: Duration = Duration::from_secs(5);

// Cross-language env-var contract, generated from contracts/desktop.json
// (the Go sidecar reads the same names from its generated contracts package;
// the Tauri event names live in the same generated module).
mod contracts_generated {
    include!("generated/contracts.rs");
}
pub(crate) use contracts_generated::{
    DEV_FRONTEND_URL, ENV_BINARY_HASH, ENV_DEV_ENDPOINT, ENV_DEV_FRONTEND, MAX_FRAME_SIZE_BYTES,
};

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
pub(crate) struct SidecarMetadata {
    pub(crate) endpoint: String,
    pub(crate) binary_hash: String,
    pub(crate) protocol_version: String,
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

/// NOTE: no `serde(rename_all)`. This struct goes to the webview as
/// snake_case, and `StartupInfoWire` in platformBridge.ts reads `build_info`
/// under that exact name.
#[derive(Serialize)]
struct StartupInfoResponse {
    config: DesktopConfigResponse,
    build_info: BuildInfoResponse,
    /// The state the window starts in, decided by the shell at launch and
    /// reported ONCE (see `tray::LaunchState::take`). The webview sizes the
    /// window before it is mapped, so it has to know whether to show it.
    launch_visibility: String,
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

/// Locks a mutex, recovering transparently from poisoning.
///
/// This is the single policy for the three shallow bookkeeping locks in the
/// crate -- `PendingMap`, `DesktopShell.state`, and `SaveStreamRegistry.handles`
/// -- all of which today guard nothing more than HashMap ops, field assignment,
/// or `Arc::clone`. A panic held across such a guard leaves the guarded state
/// internally consistent, so returning the (poisoned) guard is sound: the
/// alternative -- panicking on every subsequent access -- would wedge the shell
/// permanently over a HashMap allocation edge case.
///
/// The per-handle `Arc<Mutex<File>>` in `SaveStreamRegistry::write_chunk` is
/// deliberately NOT covered here: a panic mid-`write_all` corrupts the save
/// file, so that lock fails closed (returns an error so the partial is
/// discarded) rather than recovering silently. See
/// https://github.com/leapmux/leapmux/issues/277.
///
/// Recovery is otherwise silent by design (the point is to keep serving), but
/// the FIRST recovery across the process emits one stderr line via
/// `POISON_WARNED` so a degraded shell is at least observable in logs without
/// spamming on every subsequent access.
fn recover<T>(m: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| {
        if !POISON_WARNED.swap(true, Ordering::Relaxed) {
            shell_log!(
                "recovered a poisoned mutex; the desktop shell is in a degraded state (see issue #277)"
            );
        }
        e.into_inner()
    })
}

/// One-shot latch so `recover` warns about degraded-state operation exactly
/// once per process rather than on every subsequent access of a poisoned
/// mutex (which could be thousands of times per second under sustained
/// degraded operation).
static POISON_WARNED: AtomicBool = AtomicBool::new(false);

type PendingResponse = oneshot::Sender<Result<proto::Response, String>>;
// Every production lock on this map (and on DesktopShell.state /
// SaveStreamRegistry.handles below) goes through `recover()` above, which
// returns a poisoned guard instead of panicking on re-entry. Sound here because
// every critical section is a shallow map/field op. See
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
    // Locks recover from poisoning rather than wedging shell state; see the
    // `recover` helper and PendingMap comment above, and
    // https://github.com/leapmux/leapmux/issues/277.
    state: Mutex<ShellState>,
}

#[cfg(windows)]
use windows_impl::connect_and_handshake_dev_sidecar;

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
                        crate::shell_log!("sidecar frame read error: {err}");
                    }
                    recover(&pending).clear();
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
                crate::shell_log!("sidecar frame write error: {err}");
                break;
            }
        }
        // Drop in-flight callers so their oneshot receivers resolve with
        // an error instead of hanging when the peer goes away.
        recover(&pending).clear();
    });
    tx
}

impl DesktopShell {
    /// Spawn the sidecar and open the session, WITHOUT the first handshake.
    ///
    /// The handshake is `initial_handshake`, so the caller can run it beside
    /// the other launch read instead of before it. A shell this returns holds
    /// the launcher defaults until that call completes.
    fn connect(app_handle: AppHandle) -> Result<Self, String> {
        let local_app_url = if cfg!(debug_assertions) {
            crate::DEV_FRONTEND_URL.to_string()
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

        Ok(shell)
    }

    /// Learn the sidecar's state, with a limit so a wedged child cannot hang
    /// the Tauri setup thread.
    ///
    /// SEPARATE from `connect`, so the caller can issue it beside the other
    /// launch read rather than after it. The reader and writer threads start in
    /// `connect` and `send_request_async` multiplexes by id, so the session is
    /// ready before this runs -- and the Go sidecar dispatches every frame in
    /// its own goroutine, so nothing forces the two to serialize. Running them
    /// in sequence made the worst case the SUM of the two limits.
    ///
    /// A shell that has not completed this holds the launcher defaults, so
    /// `setup` must await it before it reads `runtime_state`.
    async fn initial_handshake(&self) -> Result<(), String> {
        tokio::time::timeout(
            SIDECAR_INITIAL_HANDSHAKE_TIMEOUT,
            self.refresh_state_from_sidecar(),
        )
        .await
        .map_err(|_| {
            format!(
                "initial sidecar handshake timed out after {:?}",
                SIDECAR_INITIAL_HANDSHAKE_TIMEOUT
            )
        })?
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
        let resp = check_response(self.send_request_async(get_sidecar_info_request()).await?)?;
        let info = sidecar_info_from_response(resp, "get_sidecar_info")?;
        apply_sidecar_info(&self.state, info);
        Ok(())
    }

    fn runtime_state(&self) -> RuntimeState {
        let state = recover(&self.state).clone();
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
                    mode,
                },
            ))
            .await?;
        Ok(())
    }

    /// Cache the resolved Desktop preferences on this device.
    ///
    /// `start_on_login` is not among them: the operating system's login-item
    /// registration is that setting's state. See `SetDesktopBehaviorRequest`.
    async fn save_desktop_behavior(&self, behavior: tray::WindowBehavior) -> Result<(), String> {
        let _ = self
            .send_request_async(proto::request::Method::SetDesktopBehavior(
                proto::SetDesktopBehaviorRequest {
                    tray_enabled: behavior.tray_enabled,
                    tray_on_close: behavior.tray_on_close.to_token().to_string(),
                    tray_on_minimize: behavior.tray_on_minimize.to_token().to_string(),
                    start_minimized: behavior.start_minimized.to_token().to_string(),
                },
            ))
            .await?;
        Ok(())
    }

    /// The persisted desktop config, for the launch decision.
    async fn load_desktop_config(&self) -> Result<proto::DesktopConfig, String> {
        let resp = check_response(
            self.send_request_async(proto::request::Method::GetConfig(
                proto::GetConfigRequest {},
            ))
            .await?,
        )?;
        match resp.result {
            Some(proto::response::Result::Config(cfg)) => Ok(cfg),
            _ => Err("unexpected response for get_config".to_string()),
        }
    }

    fn current_zoom(&self) -> f64 {
        f64::from_bits(self.webview_zoom.load(Ordering::Relaxed))
    }

    fn set_zoom(&self, zoom: f64) -> Result<(), String> {
        let clamped = zoom.clamp(0.5, 3.0);
        if let Some(window) = self.app_handle.get_webview_window(MAIN_WINDOW_LABEL) {
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
    recover(&sidecar.pending).insert(id, tx);

    let frame = proto::Frame {
        message: Some(proto::frame::Message::Request(proto::Request {
            id,
            method: Some(method),
        })),
    };
    if sidecar.writer_tx.send(frame).is_err() {
        recover(&sidecar.pending).remove(&id);
        return Err("desktop sidecar writer disconnected".to_string());
    }

    rx.await
        .map_err(|_| "desktop sidecar disconnected".to_string())?
}

// --- Response helpers ---

pub(crate) fn check_response(resp: proto::Response) -> Result<proto::Response, String> {
    if resp.error.is_empty() {
        Ok(resp)
    } else {
        Err(resp.error)
    }
}

pub(crate) fn sidecar_info_from_response(
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

fn apply_sidecar_info(state: &Mutex<ShellState>, info: proto::SidecarInfo) {
    let shell_mode = shell_mode_from_proto(&info);
    let mut guard = recover(state);
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
            let tx = recover(pending).remove(&id);
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
            let _ = app_handle.emit(contracts_generated::EVENT_CHANNEL_MESSAGE, b64);
        }
        proto::event::Payload::ChannelClose(close) => {
            let _ = app_handle.emit(
                contracts_generated::EVENT_CHANNEL_CLOSE,
                json!({ "code": close.code, "reason": close.reason, "wasClean": close.was_clean }),
            );
        }
        proto::event::Payload::UserEventsMessage(msg) => {
            // Forward the hub's length-prefixed WatchUserEvent frame
            // verbatim to the webview. The frontend's `useUserEvents`
            // hook decodes identically to native WS frames.
            let b64 = base64::engine::general_purpose::STANDARD.encode(&msg.data);
            let _ = app_handle.emit(contracts_generated::EVENT_USER_EVENTS_MESSAGE, b64);
        }
        proto::event::Payload::UserEventsClose(close) => {
            let _ = app_handle.emit(
                contracts_generated::EVENT_USER_EVENTS_CLOSE,
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
            let _ = app_handle.emit(contracts_generated::EVENT_SIDECAR_LOG, payload);
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
    launch: State<'_, Arc<tray::LaunchState>>,
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
                    window_mode: cfg.window_mode,
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
                launch_visibility: launch.take().as_str().to_string(),
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
async fn open_userevents_relay(
    shell: State<'_, Arc<DesktopShell>>,
    relay_id: u64,
    workspace_ids: Vec<String>,
    resume_hlc: Option<String>,
    resume_epoch: Option<String>,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::OpenUserEventsRelay(
                proto::OpenUserEventsRelayRequest {
                    relay_id,
                    workspace_ids,
                    resume_hlc,
                    resume_epoch,
                },
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn close_userevents_relay(
    shell: State<'_, Arc<DesktopShell>>,
    relay_id: u64,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::CloseUserEventsRelay(
                proto::CloseUserEventsRelayRequest { relay_id },
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

/// One application the sidecar detected, as the webview reads it.
///
/// `kind` rides through as the raw enum number the sidecar sent. The webview
/// compares it against the generated proto enum, so the app menu can group
/// editors apart from the file manager without any side testing an id literal.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ExternalAppPayload {
    id: String,
    display_name: String,
    kind: i32,
}

#[tauri::command]
async fn list_external_apps(
    shell: State<'_, Arc<DesktopShell>>,
    refresh: Option<bool>,
) -> Result<Vec<ExternalAppPayload>, String> {
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::ListExternalApps(
                proto::ListExternalAppsRequest {
                    refresh: refresh.unwrap_or(false),
                },
            ))
            .await?,
    )?;
    match resp.result {
        Some(proto::response::Result::ListExternalApps(r)) => Ok(r
            .apps
            .into_iter()
            .map(|a| ExternalAppPayload {
                id: a.id,
                display_name: a.display_name,
                kind: a.kind,
            })
            .collect()),
        _ => Err("unexpected response for list_external_apps".to_string()),
    }
}

#[tauri::command]
async fn open_in_external_app(
    shell: State<'_, Arc<DesktopShell>>,
    app_id: String,
    path: String,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::OpenInExternalApp(
                proto::OpenInExternalAppRequest { app_id, path },
            ))
            .await?,
    )?;
    Ok(())
}

pub(crate) fn decode_b64(b64: &str) -> Result<Vec<u8>, String> {
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

    let local_app_url = recover(&shell.state).local_app_url.clone();
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
//
// The relaunch passes NO arguments, so the new process never sees
// `--autostart` and always opens an ordinary window. That is correct and must
// stay: `start_minimized` governs the LOGIN launch alone, and a user who just
// granted Full Disk Access waits for the window to come back. Forwarding argv
// here would hide it into the tray instead.
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

/// Apply the resolved Desktop preferences the webview just pushed.
///
/// Every step runs regardless of what the one before it did, so one broken
/// piece never leaves the rest of the policy unapplied. Two failures are worth
/// a message the user reads: an operating system that refuses a login item, and
/// a Linux desktop with no status-icon library. Both look like "LeapMux ignores
/// my settings" if they stay silent.
///
/// EVERY refusal is reported, each addressed to the row that owns it. The two
/// failures are independent -- a Linux desktop with no status-icon library can
/// also be one whose operating system declines a login item -- so a channel
/// that carried one would leave the other toggle reading "on" with nothing
/// behind it and no message anywhere.
///
/// The whole body runs under `push_lock`. Tauri gives each invocation its own
/// task, and two that overlap can otherwise reach the sidecar in the opposite
/// order to the one the user chose, which leaves the device cache holding the
/// older set for the next launch to decide from.
#[tauri::command]
async fn set_desktop_behavior(
    app: AppHandle,
    shell: State<'_, Arc<DesktopShell>>,
    state: State<'_, Arc<tray::TrayState>>,
    launch: State<'_, Arc<tray::LaunchState>>,
    behavior: tray::DesktopBehavior,
) -> Result<(), Vec<tray::BehaviorRefusal>> {
    let _serialized = state.push_lock.lock().await;
    let mut refusals: Vec<tray::BehaviorRefusal> = Vec::new();
    let mut record = |refusal: tray::BehaviorRefusal| {
        shell_log!(
            "the system refused {}: {}",
            refusal.setting, refusal.message
        );
        refusals.push(refusal);
    };

    // 1. The tray itself. `apply` records what was ACHIEVED, so a build that
    //    fails downgrades the policy instead of leaving it lying.
    if let Err(err) = state.apply(&app, &behavior.window()) {
        record(tray::BehaviorRefusal::tray(err));
    }

    // 2. Bring the window back if the user is now stranded, or if the launch
    //    left it out of the way on a cache the account contradicts. A PREDICATE
    //    over the current state rather than a transition, so it also repairs a
    //    tray that failed to build and a window left hidden by an earlier
    //    session.
    // A probe that fails reads as NOT visible, so the reveal happens. Getting
    // this backwards costs a focus on a window that was already up; getting it
    // the other way round leaves a user with no tray and no window, which is
    // the exact state this step exists to prevent.
    let visible = app
        .get_webview_window(MAIN_WINDOW_LABEL)
        .and_then(|window| window.is_visible().ok())
        .unwrap_or(false);
    let launch_was_wrong = launch.launch_was_wrong(behavior.start_minimized, state.is_enabled());
    if tray::must_reveal_window(state.is_enabled(), visible, launch_was_wrong) {
        tray::show_main_window(&app);
    }

    // 3. The login item. `enable()` runs unconditionally while the preference
    //    is on, because rewriting the entry is how a stale path is repaired
    //    after the application moves (an AppImage rename, an MSI reinstall).
    if let Err(err) = apply_login_item(&app, behavior.start_on_login) {
        record(tray::BehaviorRefusal::start_on_login(err));
    }

    // 4. The device cache, so the next launch can decide before the webview
    //    exists. A failure here is logged, not reported: the live policy
    //    already applies and the only loss is the next launch's head start.
    //
    //    Pushed on EVERY call. The sidecar owns the config and skips a write
    //    that changes nothing (`App.updateConfig`), so the shell keeps no
    //    second copy of the set to compare against -- and with no copy, there
    //    is nothing that can go stale against the file, and no failed RPC that
    //    can leave one stale.
    //
    //    `window()`, so the login item cannot reach the file even by accident:
    //    the cached type does not carry that field at all.
    if let Err(err) = shell.save_desktop_behavior(behavior.window()).await {
        crate::shell_log!("cache the window behaviour: {err}");
    }

    if refusals.is_empty() {
        Ok(())
    } else {
        Err(refusals)
    }
}

/// Register or deregister the operating system's login item.
///
/// A build that may not touch the login items REPORTS that, in both
/// directions. Answering `Ok` instead tells the webview that the system
/// accepted the choice, and the row then reads "on" with nothing behind it --
/// the exact silence the refusal channel exists to remove. The disable
/// direction matters as much: a release build and a development build resolve
/// the same login-item entry, so a silent skip leaves an entry the user just
/// asked to remove.
fn apply_login_item(app: &AppHandle, start_on_login: bool) -> Result<(), String> {
    use tauri_plugin_autostart::ManagerExt;

    if !autostart_allowed() {
        return Err(
            "This build of LeapMux does not change your login items. Set \
             LEAPMUX_ALLOW_DEV_AUTOSTART to test this setting."
                .to_string(),
        );
    }
    let manager = app.autolaunch();
    if start_on_login {
        return manager.enable().map_err(|err| {
            format!("LeapMux could not add itself to your login items: {err}")
        });
    }
    // `is_enabled` is only a guard against a spurious error when nothing is
    // registered. It must never block the ENABLE path: it compares the stored
    // path against the current one, which is exactly what goes stale.
    match manager.is_enabled() {
        Ok(true) => manager.disable().map_err(|err| {
            format!("LeapMux could not remove itself from your login items: {err}")
        }),
        _ => Ok(()),
    }
}

/// Whether this build may touch the operating system's login items.
///
/// A debug build refuses, because `current_exe()` under `tauri dev` is a target
/// directory artefact: a developer who opens the Desktop preferences would
/// otherwise acquire a login item pointing at a binary that the next build
/// overwrites and `cargo clean` deletes. Set LEAPMUX_ALLOW_DEV_AUTOSTART to
/// test the feature itself.
fn autostart_allowed() -> bool {
    !cfg!(debug_assertions) || std::env::var_os("LEAPMUX_ALLOW_DEV_AUTOSTART").is_some()
}

/// Start the ordinary shutdown: drain the sidecar, then exit.
///
/// Shared by the `quit_app` command and the tray menu's Quit item so the two
/// spellings cannot drift. It never touches the window, so it cannot be
/// diverted into the hide-to-tray branch of `CloseRequested`.
pub(crate) fn request_app_exit(app: &AppHandle) {
    if let Some(shell) = app.try_state::<Arc<DesktopShell>>() {
        handle_app_exit(shell.inner().clone());
    } else {
        app.exit(0);
    }
}

#[tauri::command]
fn quit_app(app: AppHandle) {
    request_app_exit(&app);
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

fn open_main_web_inspector(app: &AppHandle) {
    if let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) {
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

    // Drop every open save handle and remove its partial file BEFORE the
    // sidecar goes away. The CAS above runs this exactly once.
    //
    // It lives here rather than in the `ExitRequested` arm because that arm is
    // skipped once `exit_in_progress` is latched -- and `quit_app` latches it
    // first, so the menu's Quit used to leave the partials on disk. The tray's
    // Quit takes the same route, which is what made the gap worth closing.
    if let Some(registry) = shell.app_handle.try_state::<Arc<SaveStreamRegistry>>() {
        registry.cleanup_all();
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
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            // Launching a second copy is the other way a user expects a window
            // back out of the tray, so this goes through the same restore the
            // tray's Show item uses.
            tray::show_main_window(app);
        }))
        // LaunchAgent, not AppleScript: the AppleScript login-item path
        // registers a bundle path with NO arguments, so `--autostart` would
        // never reach argv and a macOS login launch could not be told from a
        // hand launch.
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            Some(vec![tray::AUTOSTART_ARG]),
        ));

    // Linux and Windows render the app menu as a hamburger dropdown inside
    // the custom titlebar (`CustomTitlebar.tsx`). Only macOS uses a native
    // Tauri menu (the system-wide Apple menu bar).
    #[cfg(target_os = "macos")]
    let builder = builder.menu(build_app_menu);

    builder
        .on_menu_event(|app, event| {
            #[cfg(target_os = "macos")]
            if event.id() == SHOW_ABOUT_MENU_ID {
                let _ = app.emit(contracts_generated::EVENT_MENU_SHOW_ABOUT, ());
            } else if event.id() == SHOW_PREFERENCES_MENU_ID {
                let _ = app.emit(contracts_generated::EVENT_MENU_SHOW_PREFERENCES, ());
            } else if event.id() == OPEN_WEB_INSPECTOR_MENU_ID {
                open_main_web_inspector(app);
            }
            #[cfg(not(target_os = "macos"))]
            let _ = (app, event);
        })
        .on_window_event(|window, event| {
            if window.label() != MAIN_WINDOW_LABEL {
                return;
            }

            match event {
                WindowEvent::CloseRequested { api, .. } => {
                    let app = window.app_handle();
                    // The latch is read FIRST. `handle_main_window_close`
                    // re-issues `window.close()`, which arrives here a second
                    // time, and diverting THAT into the tray would strand a
                    // quit already under way.
                    let closing = app
                        .try_state::<Arc<DesktopShell>>()
                        .is_some_and(|shell| shell.close_in_progress.load(Ordering::SeqCst));
                    if closing {
                        return;
                    }
                    if let Some(state) = app.try_state::<Arc<tray::TrayState>>() {
                        if state.window_action(tray::WindowIntent::CloseRequested)
                            == tray::WindowAction::HideWindow
                        {
                            // HIDE, never close: closing destroys the webview
                            // and every open tab's client state with it.
                            api.prevent_close();
                            // Recorded, so the startup safety net does not read
                            // this window as a frontend that never ran and pull
                            // it back five seconds in.
                            state.record_hide_to_tray();
                            let _ = window.hide();
                            return;
                        }
                    }
                    if let Some(shell) = app.try_state::<Arc<DesktopShell>>() {
                        api.prevent_close();
                        handle_main_window_close(shell.inner().clone(), window.clone());
                    }
                }
                // Windows reports a minimize as a resize; see
                // `tray::minimize_windows`.
                #[cfg(windows)]
                WindowEvent::Resized(_) => tray::minimize_windows::on_resized(window),
                _ => {}
            }
        })
        .setup(|app| {
            // titleBarStyle "Overlay" is a macOS-only option. On Linux and
            // Windows the native decorations are left in place by default,
            // which causes the OS title bar to render alongside our custom
            // one. Drop the native decorations so the frontend can draw its
            // own drag region and window controls end-to-end.
            #[cfg(any(target_os = "linux", target_os = "windows"))]
            if let Some(w) = app.get_webview_window(MAIN_WINDOW_LABEL) {
                let _ = w.set_decorations(false);
            }

            // Work around WebKitGTK's GTK-level Tab focus traversal so
            // ProseMirror can receive Tab/Shift+Tab keydowns. See
            // `tabfix_linux.rs` for the rationale.
            #[cfg(target_os = "linux")]
            if let Some(w) = app.get_webview_window(MAIN_WINDOW_LABEL) {
                tabfix_linux::install(&w);
            }

            // `start_minimized` governs the login launch alone, so the flag
            // the autostart plugin registered is what distinguishes it.
            //
            // Read FIRST, because it needs no I/O and the safety net below is
            // armed from it before anything can block.
            //
            // `args_os` and not `args`: the latter PANICS on an argument that
            // is not valid Unicode, and this runs on the startup path for
            // whatever a desktop launcher chose to pass.
            let autostart_launch = tray::is_autostart_launch(
                std::env::args_os().map(|arg| arg.to_string_lossy().into_owned()),
            );

            // Safety net: if the frontend does not show the window within
            // tray::STARTUP_REVEAL_DEADLINE (a JS error, say), show it anyway
            // rather than leave an invisible app.
            //
            // Armed HERE, at the top of setup, because everything below it can
            // block: the sidecar handshake waits up to
            // SIDECAR_INITIAL_HANDSHAKE_TIMEOUT and the cached-config read up
            // to CACHED_CONFIG_TIMEOUT. A net armed after them promises five
            // seconds and delivers forty.
            tray::spawn_startup_safety_net(app.handle().clone(), autostart_launch);

            let shell = Arc::new(DesktopShell::connect(app.handle().clone())?);

            // The handshake and the device cache of the Desktop preferences,
            // read TOGETHER. The shell must decide the tray and the initial
            // window state before the webview exists, and neither read depends
            // on the other: the session multiplexes by id and the sidecar
            // dispatches each frame in its own goroutine. In sequence the worst
            // case was the SUM of the two limits.
            //
            // A timeout limits the config read, the same as the handshake: a
            // wedged sidecar must not hang the launch, and every field it
            // carries has a safe default (tray off, ordinary window). The
            // handshake has no such fallback, so its failure ends setup.
            let (handshake, loaded) = tauri::async_runtime::block_on(async {
                tokio::join!(
                    shell.initial_handshake(),
                    tokio::time::timeout(CACHED_CONFIG_TIMEOUT, shell.load_desktop_config()),
                )
            });
            handshake?;
            let cached = loaded.ok().and_then(Result::ok).unwrap_or_default();

            // The login item is NOT reconciled here. Its state is the
            // operating system's registration, which the cache deliberately
            // does not mirror, so the shell has nothing authoritative to
            // reconcile against until the first push arrives with the real
            // preference. That push rewrites the entry, which is what repairs
            // a path gone stale because the application moved.

            let launch_state = tray::install(app.handle(), &cached, autostart_launch);

            let runtime_state = shell.runtime_state();
            if runtime_state.connected && runtime_state.shell_mode == ShellMode::Distributed {
                if let Some(window) = app.get_webview_window(MAIN_WINDOW_LABEL) {
                    let target_url = Url::parse(&runtime_state.hub_url)
                        .map_err(|err| format!("parse reattach hub url: {err}"))?;
                    window
                        .navigate(target_url)
                        .map_err(|err| format!("navigate to reattached hub: {err}"))?;
                }
                // The shell reveals the window on THIS route, because
                // `LauncherView` -- the only caller of `restoreWindowGeometry`
                // -- never mounts once the webview navigates to the hub. It
                // also carries the saved geometry for the same reason: nothing
                // else sizes the window here, so without it every reattach
                // launch opens at the built-in default.
                //
                // IMMEDIATELY, and not once the hub page loads. `navigate` only
                // requests the navigation, so the window maps before the first
                // byte arrives and the user sees an empty window for the length
                // of the page load. That is the accepted trade: the load is
                // over a NETWORK and can be slow, can serve an error, or can
                // never finish, and a hub that is unreachable would then leave
                // no window at all until the safety net fires five seconds in.
                // A window the user can act on -- switch mode, quit -- beats a
                // painted one that may never come.
                //
                // Through `take()`, so the decision is CONSUMED here: a later
                // "Switch mode..." navigates back to the launcher, which mounts
                // and asks `get_startup_info` for the launch state. Leaving the
                // latch unread would answer `hidden` and hide the launcher the
                // user just asked for.
                tray::apply_launch_visibility(app.handle(), launch_state.take(), Some(&cached));
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
            open_userevents_relay,
            close_userevents_relay,
            create_tunnel,
            delete_tunnel,
            reset_tunnels,
            list_tunnels,
            list_external_apps,
            open_in_external_app,
            file_save_open,
            file_save_open_dialog,
            file_save_write,
            file_save_commit,
            file_save_abort,
            switch_mode,
            #[cfg(target_os = "macos")]
            restart_app,
            save_window_geometry,
            set_desktop_behavior,
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
            match event {
                RunEvent::ExitRequested { api, .. } => {
                    if let Some(shell) = app.try_state::<Arc<DesktopShell>>() {
                        if !shell.exit_in_progress.load(Ordering::SeqCst) {
                            // The save handles are dropped inside
                            // `handle_app_exit`, which every exit route reaches
                            // -- including the ones that latch
                            // `exit_in_progress` before this arm runs.
                            api.prevent_exit();
                            handle_app_exit(shell.inner().clone());
                        }
                    }
                }
                // macOS: a click on the Dock icon of an application with no
                // visible window. LeapMux hides its window for a close and for
                // a minimize, and it registers no `LSUIElement`, so the Dock
                // icon stays. AppKit's own default does nothing for a window
                // that `orderOut:` took off the screen list, which leaves the
                // icon looking broken and the tray icon as the only route back.
                //
                // `show_main_window` is the same entry point the tray click,
                // the tray menu item and the single-instance callback share, so
                // every request that the operating system makes for this
                // application reveals the window by one rule.
                //
                // A login launch that asked to start hidden keeps its window
                // hidden. AppKit sends this for a RE-open and not for the first
                // launch, which a probe confirmed: a delegate that implements
                // `applicationShouldHandleReopen:hasVisibleWindows:` receives
                // nothing while the application starts with no window.
                #[cfg(target_os = "macos")]
                RunEvent::Reopen {
                    has_visible_windows,
                    ..
                } if !has_visible_windows => tray::show_main_window(app),
                _ => {}
            }
        });
}

/// A test-only global allocator that records the largest single allocation made
/// on each thread.
///
/// It exists for `frame::tests::read_frame_async_rejects_oversize_varint_before_allocating`
/// (which reaches it via `crate::alloc_probe::peak_alloc_of`). That test's
/// contract -- reject an oversize length prefix *without allocating the
/// payload* -- cannot be pinned by asserting on the returned error: the very
/// same "frame too large" surfaces whether the `MAX_FRAME_SIZE` check runs
/// before the payload `vec!` or after it. Only measuring the allocation tells
/// the two apart, and the difference is the whole point of the check: a peer
/// that sends a bogus varint must not be able to make us allocate gigabytes.
#[cfg(test)]
#[global_allocator]
static PEAK_TRACKING_ALLOCATOR: alloc_probe::PeakTracking = alloc_probe::PeakTracking;

#[cfg(test)]
mod tests {
    use super::*;
    use crate::file_save::{
        defuse_final_path, is_partial_name, open_unique_tmp, resolve_save_as_final, tmp_path_for,
        SaveStreamRegistry, SAVE_DEFUSE_SUFFIX, SAVE_TMP_SUFFIX,
    };
    use std::io;
    use std::sync::atomic::AtomicU64;

    #[cfg(unix)]
    use std::os::unix::net::UnixListener;
    // SystemTime backs the unix unique_test_socket_path helper; the windows twin
    // moved to windows_impl, so this import is unix-only now.
    #[cfg(unix)]
    use std::time::SystemTime;

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

    pub(super) fn sidecar_info(
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

    // The window mode is a CONTRACT TOKEN on every hop, so there is no bridge
    // to round-trip and nothing that can rewrite one value into another. The
    // shell's own tokens must be the generated ones, or the config it writes
    // would not be the config the webview reads.
    #[test]
    fn the_window_mode_tokens_come_from_the_contract() {
        assert_eq!(contracts_generated::WINDOW_MODE_NORMAL, "normal");
        assert_eq!(contracts_generated::WINDOW_MODE_MAXIMIZED, "maximized");
        assert_eq!(contracts_generated::WINDOW_MODE_FULLSCREEN, "fullscreen");

        let tokens = [
            contracts_generated::WINDOW_MODE_NORMAL,
            contracts_generated::WINDOW_MODE_MAXIMIZED,
            contracts_generated::WINDOW_MODE_FULLSCREEN,
        ];
        let unique: std::collections::HashSet<_> = tokens.iter().collect();
        assert_eq!(unique.len(), tokens.len(), "one setting, so all three differ");
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
                    send_sidecar_request(&s, get_sidecar_info_request()).await
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
        assert!(
            !tmp_path.exists(),
            "the partial tmp is discarded, not left behind"
        );
        assert!(
            !final_path.exists(),
            "no corrupt file is renamed into place"
        );
        drop(in_flight_clone);
    }

    // `recover` is the policy for every shallow bookkeeping lock: a panic held
    // across the guard must NOT permanently wedge the lock. Poison it once, then
    // confirm the next `recover` returns a usable guard instead of panicking --
    // which is the whole reason the helper exists over plain `.lock().unwrap()`.
    #[test]
    fn recover_returns_a_usable_guard_after_poisoning() {
        use std::panic;
        let m = Mutex::new(0i32);
        // Poison the mutex: a panic while holding the guard marks it poisoned.
        let _ = panic::catch_unwind(panic::AssertUnwindSafe(|| {
            let _g = m.lock().unwrap();
            panic!("intentional test panic to poison the mutex");
        }));
        assert!(m.is_poisoned(), "precondition: the mutex is poisoned");
        // After poisoning, `.lock().unwrap()` would itself panic; `recover` must not.
        let guard = recover(&m);
        assert_eq!(*guard, 0, "the poisoned guard is still usable");
    }

    // The FIRST recovery of a poisoned mutex across the process latches
    // `POISON_WARNED` so the shell's degraded state is observable without
    // spamming stderr on every subsequent access (which a sustained-degraded
    // shell would otherwise hit thousands of times per second). Resetting the
    // latch before poisoning makes this test's assertions deterministic
    // against whatever other tests have done; the latch is monotonic, so a
    // racing parallel test setting it does not change the post-condition.
    #[test]
    fn recover_warns_about_degraded_state_at_most_once() {
        use std::panic;
        POISON_WARNED.store(false, Ordering::Relaxed);
        let m = Mutex::new(0i32);
        let _ = panic::catch_unwind(panic::AssertUnwindSafe(|| {
            let _g = m.lock().unwrap();
            panic!("intentional test panic to poison the mutex");
        }));
        // First recovery latches the warning. Hold the guard so clippy's
        // `let_underscore_lock` lint does not flag an immediate drop (and so
        // the recovery's effect -- driving `recover`'s body -- clearly happens).
        let _guard1 = recover(&m);
        assert!(
            POISON_WARNED.load(Ordering::Relaxed),
            "the first recovery of a poisoned mutex latches the one-shot warning"
        );
        // A second recovery must NOT reset or re-fire -- the latch stays set.
        drop(_guard1);
        let _guard2 = recover(&m);
        assert!(
            POISON_WARNED.load(Ordering::Relaxed),
            "the warning latch is one-shot; a second recovery does not reset it"
        );
    }

    // The per-handle file lock in `write_chunk` is the ONE lock that does NOT
    // recover: a panic mid-`write_all` corrupts the file, so the second write
    // must fail loudly AND discard the partial -- rather than silently continue
    // writing, and rather than leaving a corrupted partial for `commit` to
    // rename onto the final path. This test pins BOTH invariants: the write
    // errors, the handle is discarded so a later `commit` finds "unknown save
    // handle", and neither the tmp nor an empty final is left behind.
    #[test]
    fn write_chunk_fails_loudly_after_the_file_lock_is_poisoned() {
        let registry = SaveStreamRegistry::new();
        let (tmp_path, final_path) = save_test_paths();
        // Panic-safe cleanup: drop the registry's open File BEFORE remove_file
        // (Windows refuses remove while a handle is open -- the same invariant
        // `discard_stream` and `commit` enforce in production), and reclaim the
        // tmp even if an assertion below panics and skips a trailing remove.
        // `registry` is dropped first by being declared before `tmp`; the
        // explicit `drop(registry)` makes the ordering load-bearing rather than
        // accidental.
        let file = std::fs::File::create(&tmp_path).expect("create tmp");
        let handle = registry.insert(file, tmp_path.clone(), final_path.clone());

        // Poison the handle's per-file mutex the only way std allows: panic
        // while holding its guard, in a scope whose guard unwinds and DROPS the
        // clone. This reproduces the production hazard exactly -- after a
        // `write_all` panic the panicking frame's `Arc` clone is unwound away,
        // leaving the registry's clone as sole owner -- so the `commit` refusal
        // asserted below exercises the real `try_unwrap`-succeeds-after-unwind
        // path, not the in-flight-clone path the sibling test covers.
        let poisoned = poison_handle_file_lock(&registry, handle.id);
        assert!(
            poisoned,
            "precondition: the per-handle file lock is poisoned"
        );

        let err = registry
            .write_chunk(handle.id, b"more")
            .expect_err("a poisoned file lock must fail the write, not recover");
        assert!(
            err.contains("a prior write panicked"),
            "unexpected error: {err}"
        );

        // The fail-closed path discards the handle: the tmp is gone, a retry
        // finds the handle unknown, and -- the load-bearing assertion -- a
        // `commit` on the poisoned id refuses (with "unknown save handle")
        // instead of renaming the corrupted/empty partial onto the final path.
        // Before the discard-on-fail-closed fix this would `Ok(())` and
        // atomically replace `final_path` with the truncated tmp.
        assert!(
            !tmp_path.exists(),
            "the corrupted tmp is discarded, not left"
        );
        let retry = registry.write_chunk(handle.id, b"more");
        assert!(
            retry
                .as_ref()
                .is_err_and(|err| err.contains("unknown save handle")),
            "after the fail-closed discard the handle is gone; got: {retry:?}"
        );
        let commit = registry.commit(handle.id);
        assert!(
            commit
                .as_ref()
                .is_err_and(|err| err.contains("unknown save handle")),
            "commit must refuse a poisoned id instead of renaming the partial; got: {commit:?}"
        );
        assert!(
            !final_path.exists(),
            "no corrupted/empty partial is renamed to the final path"
        );
        drop(registry);
        let _ = std::fs::remove_file(&tmp_path);
        let _ = std::fs::remove_file(&final_path);
    }

    /// Poison a handle's per-handle `Arc<Mutex<File>>` by panicking while
    /// holding its guard in a scope that drops the cloned `Arc` on unwind.
    /// Returns `true` if the mutex is poisoned after the call. Shared between
    /// the write-fail-closed tests so the poison mechanism stays identical.
    fn poison_handle_file_lock(registry: &SaveStreamRegistry, id: u64) -> bool {
        use std::panic;
        let poisoned = {
            let file_mutex = {
                let guard = registry.handles.lock().unwrap();
                guard.get(&id).unwrap().file.clone()
            };
            let _ = panic::catch_unwind(panic::AssertUnwindSafe(|| {
                let _g = file_mutex.lock().unwrap();
                panic!("intentional test panic to poison the file lock");
            }));
            // `file_mutex` drops here as the scope closes -- after the panic,
            // mirroring `write_chunk`'s local clone unwinding away.
            file_mutex.is_poisoned()
        };
        poisoned
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
