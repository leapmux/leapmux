#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

#[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
compile_error!("LeapMux Desktop only supports macOS, Linux, and Windows");

mod frame;
// The generated modules mirror the proto packages, because prost refers from
// `leapmux.desktop.v1` to the shared build message as
// `super::super::v1::BuildInfo`.
pub(crate) mod leapmux {
    pub(crate) mod v1 {
        include!(concat!(env!("OUT_DIR"), "/leapmux.v1.rs"));
    }
    pub(crate) mod desktop {
        pub(crate) mod v1 {
            include!(concat!(env!("OUT_DIR"), "/leapmux.desktop.v1.rs"));
        }
    }
}
pub(crate) use leapmux::desktop::v1 as proto;

// Test-only peak-allocation tracker; `#[global_allocator]` is wired below.
#[cfg(test)]
mod alloc_probe;

// Test-only guards for the CSP that Tauri serves with the bundled frontend.
#[cfg(test)]
mod asset_csp;

// Test-only guards for the window config the shell builds the main window
// from, the way `asset_csp` guards the served CSP.
#[cfg(test)]
mod window_config;

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

mod login_item;
mod quit_guard;
mod shutdown;

// macOS: intercept `terminate:` at `applicationShouldTerminate:` so the Dock,
// Cmd+Q, an AppleScript quit and a logout take the cooperative shutdown like
// the in-app Quit does. The module carries its own `cfg` attribute inside.
#[cfg(target_os = "macos")]
mod terminate_macos;

// Streaming file-save subsystem (file_save_open/write/commit/abort chain).
mod file_save;

// The tray icon, the window-behaviour policy it governs, and the launch
// decision that policy feeds. Its per-platform minimize hooks carry their own
// `cfg` attributes inside the module, the way `windows_impl` does.
mod tray;

// The navigation allow-list, registered as a plugin so it covers every
// webview the shell serves.
mod navigation;

#[cfg(target_os = "linux")]
mod tabfix_linux;

use base64::Engine;
use prost::Message;
use serde::{Deserialize, Serialize};
use serde_json::json;

use crate::file_save::{
    file_save_abort, file_save_commit, file_save_open, file_save_open_dialog, file_save_write,
    read_b64_header, read_header_str, SaveStreamRegistry, SAVE_HANDLE_GC_INTERVAL,
    SAVE_HANDLE_IDLE_TIMEOUT,
};
use crate::frame::{get_sidecar_info_request, read_frame, write_frame};
use crate::sidecar::bootstrap_sidecar;
// The Windows-only connect/handshake surface (and its windows-only imports)
// lives in `windows_impl`, behind a single module-level `#[cfg(windows)]` --
// see `mod windows_impl` above -- so the next windows-only import lands there
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
    collections::{HashMap, VecDeque},
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

const RAW_INVOKE_CACHE_CAPACITY: usize = 1024;
const RAW_INVOKE_CACHE_MAX_BYTES: usize = MAX_FRAME_SIZE_BYTES as usize * 2;

#[derive(Clone)]
enum RawInvokeResult {
    Proxy(Result<Vec<u8>, String>),
    Send(Result<(), String>),
}

impl RawInvokeResult {
    fn retained_bytes(&self) -> usize {
        match self {
            Self::Proxy(Ok(body)) => body.len(),
            Self::Proxy(Err(message)) | Self::Send(Err(message)) => message.len(),
            Self::Send(Ok(())) => 0,
        }
    }
}

enum RawInvokeEntry {
    InFlight(&'static str),
    Complete(RawInvokeResult),
}

struct RawInvokeCache {
    entries: HashMap<String, RawInvokeEntry>,
    completed: VecDeque<(String, usize)>,
    completed_bytes: usize,
}

struct RawInvokeRegistry {
    cache: Mutex<RawInvokeCache>,
    capacity: usize,
    max_completed_bytes: usize,
}

impl Default for RawInvokeRegistry {
    fn default() -> Self {
        Self::with_limits(RAW_INVOKE_CACHE_CAPACITY, RAW_INVOKE_CACHE_MAX_BYTES)
    }
}

impl RawInvokeRegistry {
    fn with_limits(capacity: usize, max_completed_bytes: usize) -> Self {
        Self {
            cache: Mutex::new(RawInvokeCache {
                entries: HashMap::new(),
                completed: VecDeque::new(),
                completed_bytes: 0,
            }),
            capacity,
            max_completed_bytes,
        }
    }

    fn begin(
        &self,
        request_id: &str,
        kind: &'static str,
    ) -> Result<Option<RawInvokeResult>, String> {
        if request_id.is_empty() || request_id.len() > 128 {
            return Err("raw invoke request id is invalid".to_string());
        }
        let mut cache = recover(&self.cache);
        match cache.entries.get(request_id) {
            Some(RawInvokeEntry::InFlight(existing)) => {
                return Err(format!("{existing} request is already in progress"));
            }
            Some(RawInvokeEntry::Complete(result)) => return Ok(Some(result.clone())),
            None => {}
        }
        cache
            .entries
            .insert(request_id.to_string(), RawInvokeEntry::InFlight(kind));
        Ok(None)
    }

    fn finish(&self, request_id: String, result: RawInvokeResult) {
        let retained_bytes = request_id.len() + result.retained_bytes();
        let mut cache = recover(&self.cache);
        cache
            .entries
            .insert(request_id.clone(), RawInvokeEntry::Complete(result));
        cache.completed.push_back((request_id, retained_bytes));
        cache.completed_bytes += retained_bytes;
        while cache.completed.len() > self.capacity
            || (cache.completed_bytes > self.max_completed_bytes && cache.completed.len() > 1)
        {
            if let Some((expired, expired_bytes)) = cache.completed.pop_front() {
                cache.completed_bytes -= expired_bytes;
                cache.entries.remove(&expired);
            }
        }
    }
}
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
const QUIT_APP_MENU_ID: &str = "quit-app";
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
//
// `allow(dead_code)`: the generated vocabulary serves every language, and a
// token this binary does not use must not fail its build. Relay frames use raw
// acknowledged reads, so the older event vocabulary remains unused here.
#[allow(dead_code)]
mod contracts_generated {
    include!("generated/contracts.rs");
}
pub(crate) use contracts_generated::{
    DEV_FRONTEND_URL, ENV_AGENT_HELPER, ENV_BINARY_HASH, ENV_DEV_ENDPOINT, ENV_DEV_FRONTEND,
    IPC_HEADER_HEADERS, IPC_HEADER_METHOD, IPC_HEADER_PATH, IPC_HEADER_RELAY_ID,
    IPC_HEADER_REQUEST_ID, MAX_FRAME_SIZE_BYTES,
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
#[serde(rename_all = "camelCase")]
struct TunnelKeyMismatchResponse {
    expected_fingerprint: String,
    presented_fingerprint: String,
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
    /// Set only on a stopped row: the Worker key mismatch that stopped the tunnel.
    #[serde(skip_serializing_if = "Option::is_none")]
    key_mismatch: Option<TunnelKeyMismatchResponse>,
}

/// A create's outcome: the created row, or the Worker key mismatch that
/// refused it. Mirrors the CreateTunnelResponse oneof.
#[derive(Serialize)]
#[serde(tag = "kind")]
enum CreateTunnelOutcome {
    #[serde(rename = "created")]
    Info { info: TunnelInfoResponse },
    #[serde(rename = "workerKeyMismatch")]
    KeyMismatch {
        #[serde(rename = "keyMismatch")]
        key_mismatch: TunnelKeyMismatchResponse,
    },
}

/// One tunnel listing: the rows and the channel epoch they were read under.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ListTunnelsResult {
    tunnels: Vec<TunnelInfoResponse>,
    channel_epoch: String,
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
    expected_worker_keys_hex: String,
    expected_user_id: String,
    channel_epoch: String,
    #[serde(skip)]
    hub_cookie_header: String,
    #[serde(skip)]
    hub_cookie_origin: String,
}

// --- Sidecar process ---

/// Locks a mutex, recovering transparently from poisoning.
///
/// This is the single policy for the shallow bookkeeping locks in the crate --
/// `PendingMap`, `DesktopShell.state`, `QuitGuard.state`,
/// `SaveStreamRegistry.handles`, `RawInvokeRegistry.cache`, and
/// `RelayChannelSlot.receivers` -- all of which guard nothing more than
/// HashMap ops, state transitions, field assignment, or `Arc::clone`. A panic
/// held across such a guard leaves the guarded state internally consistent, so
/// returning the (poisoned) guard is sound: the alternative -- panicking on
/// every subsequent access -- would wedge the shell permanently over a HashMap
/// allocation edge case.
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
    quit_guard: quit_guard::QuitGuard,
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
            quit_guard: quit_guard::QuitGuard::default(),
            webview_zoom: AtomicU64::new(1.0f64.to_bits()),
            state: Mutex::new(ShellState {
                shell_mode: ShellMode::Launcher,
                connected: false,
                hub_url: String::new(),
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
        if let Err(error) = shutdown::request_cleanup(shutdown).await {
            crate::shell_log!("desktop shutdown cleanup warning: {error}");
        }
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

    fn is_solo_connected(&self) -> bool {
        let state = recover(&self.state);
        is_solo_connected_state(&state)
    }

    async fn inspect_solo_busy_work_async(&self) -> quit_guard::SoloBusyWork {
        if !self.is_solo_connected() {
            return quit_guard::SoloBusyWork::default();
        }
        let response = match tokio::time::timeout(
            quit_guard::BUSY_WORK_INSPECT_TIMEOUT,
            self.send_request_async(proto::request::Method::InspectSoloBusyWork(
                proto::InspectSoloBusyWorkRequest {},
            )),
        )
        .await
        {
            Ok(result) => result,
            Err(_) => Err(format!(
                "inspect_solo_busy_work timed out after {:?}",
                quit_guard::BUSY_WORK_INSPECT_TIMEOUT
            )),
        };
        busy_work_from_response(response)
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
// where the peer has not yet proven live), Shutdown (request_shutdown_async,
// where the caller is about to exit regardless), and Solo busy-work inspection
// (inspect_solo_busy_work_async with BUSY_WORK_INSPECT_TIMEOUT, which fails
// open so a wedged sidecar cannot block quitting or switching mode).
// `proxy_http` in particular must stay unbounded here: it carries Hub RPCs
// whose own server-side budgets (agent startup, worktree creation) are the
// real timeouts.
async fn send_sidecar_request(
    sidecar: &SidecarProcess,
    method: proto::request::Method,
) -> Result<proto::Response, String> {
    struct PendingRequestGuard<'a> {
        pending: &'a PendingMap,
        id: u64,
        armed: bool,
    }

    impl Drop for PendingRequestGuard<'_> {
        fn drop(&mut self) {
            if self.armed {
                recover(self.pending).remove(&self.id);
            }
        }
    }

    let id = sidecar.next_id.fetch_add(1, Ordering::Relaxed);
    let (tx, rx) = oneshot::channel();
    recover(&sidecar.pending).insert(id, tx);
    let mut pending_guard = PendingRequestGuard {
        pending: &sidecar.pending,
        id,
        armed: true,
    };

    let frame = proto::Frame {
        message: Some(proto::frame::Message::Request(proto::Request {
            id,
            method: Some(method),
        })),
    };
    if sidecar.writer_tx.send(frame).is_err() {
        return Err("desktop sidecar writer disconnected".to_string());
    }

    let result = rx
        .await
        .map_err(|_| "desktop sidecar disconnected".to_string())?;
    pending_guard.armed = false;
    result
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
    let relay_id = event.relay_id;

    match payload {
        proto::event::Payload::ChannelMessage(msg) => {
            app_handle
                .state::<RelaySlots>()
                .channel
                .send_raw(relay_id, msg.data);
        }
        proto::event::Payload::ChannelClose(close) => {
            app_handle.state::<RelaySlots>().channel.send_close(
                relay_id,
                close.code,
                close.reason,
                close.was_clean,
            );
        }
        proto::event::Payload::UserEventsMessage(msg) => {
            // Forward the length-prefixed WatchUserEvent frame verbatim. The
            // frontend decodes it the same way as a native WebSocket frame.
            app_handle
                .state::<RelaySlots>()
                .user_events
                .send_raw(relay_id, msg.data);
        }
        proto::event::Payload::UserEventsClose(close) => {
            app_handle.state::<RelaySlots>().user_events.send_close(
                relay_id,
                close.code,
                close.reason,
                close.was_clean,
            );
        }
        proto::event::Payload::BrowserAuthResult(result) => {
            // The one result of one browser-auth ceremony, forwarded
            // verbatim: the webview resolves the ceremony's promise from it.
            let _ = app_handle.emit(
                contracts_generated::EVENT_BROWSER_AUTH_RESULT,
                json!({
                    "ceremonyId": result.ceremony_id,
                    "ok": result.ok,
                    "failure": result.failure,
                }),
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
        proto::event::Payload::TunnelsChanged(_) => {
            let (name, payload) = tunnels_changed_event_for_webview();
            let _ = app_handle.emit(name, payload);
        }
    }
}

fn tunnels_changed_event_for_webview() -> (&'static str, serde_json::Value) {
    (contracts_generated::EVENT_TUNNELS_CHANGED, json!(null))
}

// --- Static helpers ---

fn is_solo_connected_state(state: &ShellState) -> bool {
    state.shell_mode == ShellMode::Solo && state.connected
}

fn busy_work_from_response(response: Result<proto::Response, String>) -> quit_guard::SoloBusyWork {
    match response.and_then(check_response) {
        Ok(proto::Response {
            result: Some(proto::response::Result::InspectSoloBusyWork(busy)),
            ..
        }) => quit_guard::SoloBusyWork::from_proto(busy),
        Ok(_) => {
            shell_log!(
                "unexpected sidecar response for inspect_solo_busy_work; proceeding fail-open"
            );
            quit_guard::SoloBusyWork::default()
        }
        Err(err) => {
            shell_log!("inspect_solo_busy_work failed ({err}); proceeding fail-open");
            quit_guard::SoloBusyWork::default()
        }
    }
}

// No transport field on `PlatformCapabilities`: every desktop mode reaches
// the Hub through the shell's proxy, so the webview derives the transport
// from the platform it runs on instead of reading a reported value.
fn capabilities_for(state: &ShellState) -> PlatformCapabilities {
    match state.shell_mode {
        ShellMode::Solo | ShellMode::Launcher => PlatformCapabilities {
            mode: PlatformMode::TauriDesktopSolo,
            tunnels: true,
            app_control: true,
            window_control: true,
            system_permissions: true,
            local_solo: true,
        },
        ShellMode::Distributed => PlatformCapabilities {
            mode: PlatformMode::TauriDesktopDistributed,
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
        Some(proto::response::Result::StartupInfo(info)) => Ok(startup_info_response(
            info,
            launch.take().as_str().to_string(),
        )),
        _ => Err("unexpected response for get_startup_info".to_string()),
    }
}

/// Converts the sidecar's startup information to the JSON the webview reads.
/// An absent config or build reads as its defaults, so the webview always
/// receives every field.
fn startup_info_response(
    info: proto::StartupInfo,
    launch_visibility: String,
) -> StartupInfoResponse {
    let cfg = info.config.unwrap_or_default();
    let build = info.build_info.unwrap_or_default();
    StartupInfoResponse {
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
        launch_visibility,
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

/// Connect to a remote Hub. The window keeps showing the bundled frontend:
/// every mode reaches the Hub through the shell's proxy, so no navigation
/// follows the connect.
#[tauri::command]
async fn connect_distributed(
    shell: State<'_, Arc<DesktopShell>>,
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
    apply_sidecar_info(&shell.state, info);
    Ok(())
}

fn read_raw_invoke_body(request: &tauri::ipc::Request<'_>) -> Result<Vec<u8>, String> {
    match request.body() {
        tauri::ipc::InvokeBody::Raw(bytes) => Ok(bytes.clone()),
        tauri::ipc::InvokeBody::Json(serde_json::Value::Array(values)) => json_bytes(values),
        _ => Err("expected raw bytes body".to_string()),
    }
}

fn json_bytes(values: &[serde_json::Value]) -> Result<Vec<u8>, String> {
    if values.len() > MAX_FRAME_SIZE_BYTES as usize {
        return Err("raw invoke body exceeds the frame limit".to_string());
    }
    values
        .iter()
        .map(|value| {
            value
                .as_u64()
                .filter(|byte| *byte <= u8::MAX as u64)
                .map(|byte| byte as u8)
                .ok_or_else(|| "raw invoke byte array contains an invalid value".to_string())
        })
        .collect()
}

fn raw_response(bytes: Vec<u8>) -> tauri::ipc::Response {
    tauri::ipc::Response::new(tauri::ipc::InvokeResponseBody::Raw(bytes))
}

/// One proxied HTTP round trip. The request body crosses the Tauri IPC as the
/// raw invoke body (`InvokeBody::Raw`), the `file_save_write` pattern -- the
/// base64 round-trip was the dominant cost for multi-MB payloads. The method,
/// the path and the proxied headers ride InvokeOptions headers instead (the
/// `file_save_open` pattern), because the invoke-args slot holds the raw body.
/// The response comes back as the existing ProxyHttpResponse protobuf. The raw
/// Tauri body avoids base64 while the generated schema keeps Rust and the
/// webview on one framing definition.
#[tauri::command]
async fn proxy_http(
    shell: State<'_, Arc<DesktopShell>>,
    registry: State<'_, RawInvokeRegistry>,
    request: tauri::ipc::Request<'_>,
) -> Result<tauri::ipc::Response, String> {
    let request_id = read_header_str(&request, IPC_HEADER_REQUEST_ID)?.to_string();
    if let Some(cached) = registry.begin(&request_id, "HTTP proxy")? {
        return match cached {
            RawInvokeResult::Proxy(result) => result.map(raw_response),
            RawInvokeResult::Send(_) => {
                Err("raw invoke request id was reused by another command".to_string())
            }
        };
    }

    let parsed = (|| {
        let method = read_header_str(&request, IPC_HEADER_METHOD)?.to_string();
        let path = read_header_str(&request, IPC_HEADER_PATH)?.to_string();
        // The proxied headers are arbitrary Unicode key/value pairs (the JSON body
        // used to carry them as-is), and an invoke header value must stay visible
        // ASCII, so the map rides base64-encoded -- the `filename-b64` pattern.
        let headers: HashMap<String, String> =
            serde_json::from_str(&read_b64_header(&request, IPC_HEADER_HEADERS)?)
                .map_err(|err| format!("parse proxy headers: {err}"))?;
        let body = read_raw_invoke_body(&request)?;
        Ok::<_, String>((method, path, headers, body))
    })();
    let (method, path, headers, body) = match parsed {
        Ok(parsed) => parsed,
        Err(error) => {
            registry.finish(request_id, RawInvokeResult::Proxy(Err(error.clone())));
            return Err(error);
        }
    };

    let result = async {
        let resp = check_response(
            shell
                .send_request_async(proto::request::Method::ProxyHttp(proto::ProxyHttpRequest {
                    method,
                    path,
                    headers,
                    body,
                    request_id: request_id.clone(),
                }))
                .await?,
        )?;
        match resp.result {
            Some(proto::response::Result::ProxyHttp(r)) => Ok(r.encode_to_vec()),
            _ => Err("unexpected response for proxy_http".to_string()),
        }
    }
    .await;
    registry.finish(request_id, RawInvokeResult::Proxy(result.clone()));
    result.map(raw_response)
}

#[tauri::command]
async fn cancel_proxy_http(
    shell: State<'_, Arc<DesktopShell>>,
    request_id: String,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::CancelProxyHttp(
                proto::CancelProxyHttpRequest { request_id },
            ))
            .await?,
    )?;
    Ok(())
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

const NOTICE_WINDOW_LABEL: &str = "notice";

fn notice_window_url() -> tauri::WebviewUrl {
    tauri::WebviewUrl::App("NOTICE.html".into())
}

// WebView2 cannot create a webview from a synchronous command. Tauri's async
// command dispatcher runs the window builder outside the main thread.
#[tauri::command]
async fn open_notice_window(window: WebviewWindow) -> Result<(), String> {
    if window.label() != MAIN_WINDOW_LABEL {
        return Err("only the main window can open third-party licenses".to_string());
    }

    let app = window.app_handle();
    if let Some(notice) = app.get_webview_window(NOTICE_WINDOW_LABEL) {
        return notice.set_focus().map_err(|error| error.to_string());
    }

    // The initial URL must be local by construction. WebView2 can start
    // its first navigation before the navigation hook is installed.
    match tauri::WebviewWindowBuilder::new(app, NOTICE_WINDOW_LABEL, notice_window_url())
        .title("Third-party Licenses")
        .inner_size(800.0, 600.0)
        .on_new_window(|_, _| tauri::webview::NewWindowResponse::Deny)
        .build()
    {
        Ok(_) => Ok(()),
        Err(
            error @ (tauri::Error::WindowLabelAlreadyExists(_)
            | tauri::Error::WebviewLabelAlreadyExists(_)),
        ) => app
            .get_webview_window(NOTICE_WINDOW_LABEL)
            .ok_or_else(|| error.to_string())?
            .set_focus()
            .map_err(|error| error.to_string()),
        Err(error) => Err(error.to_string()),
    }
}

// --- Relay frame delivery ---

const RELAY_QUEUE_MAX_BYTES: usize = MAX_FRAME_SIZE_BYTES as usize * 2;

#[derive(Clone)]
enum RelayReadItem {
    Data(Vec<u8>),
    Close(proto::RelayClose),
}

struct QueuedRelayRead {
    cursor: u64,
    bytes: usize,
    item: RelayReadItem,
}

#[derive(Default)]
struct RelayOwnerQueue {
    next_cursor: u64,
    queued_bytes: usize,
    closed: bool,
    items: VecDeque<QueuedRelayRead>,
}

impl RelayOwnerQueue {
    fn push(&mut self, item: RelayReadItem, bytes: usize) {
        self.next_cursor += 1;
        self.queued_bytes += bytes;
        self.items.push_back(QueuedRelayRead {
            cursor: self.next_cursor,
            bytes,
            item,
        });
    }

    fn acknowledge(&mut self, cursor: u64) {
        while self.items.front().is_some_and(|item| item.cursor <= cursor) {
            let item = self.items.pop_front().expect("front item exists");
            self.queued_bytes -= item.bytes;
        }
    }
}

#[derive(Default)]
struct RelayReceivers {
    accepted: Option<u64>,
    closed_through: u64,
    owners: HashMap<u64, RelayOwnerQueue>,
}

/// The active receiver and pending candidates for one relay kind. Sidecar
/// events enqueue by exact owner. The renderer acknowledges one cursor per
/// read, so a failed invoke can repeat without losing or reordering an item.
struct RelayChannelSlot {
    receivers: Mutex<RelayReceivers>,
    changed: tokio::sync::Notify,
    max_bytes: usize,
}

impl Default for RelayChannelSlot {
    fn default() -> Self {
        Self {
            receivers: Mutex::new(RelayReceivers::default()),
            changed: tokio::sync::Notify::new(),
            max_bytes: RELAY_QUEUE_MAX_BYTES,
        }
    }
}

impl RelayChannelSlot {
    fn is_accepted(&self, relay_id: u64) -> bool {
        recover(&self.receivers).accepted == Some(relay_id)
    }

    fn register(&self, relay_id: u64) -> bool {
        let mut receivers = recover(&self.receivers);
        if relay_id == 0
            || relay_id <= receivers.closed_through
            || receivers.accepted.is_some_and(|active| active >= relay_id)
            || receivers.owners.contains_key(&relay_id)
        {
            return false;
        }
        receivers
            .owners
            .insert(relay_id, RelayOwnerQueue::default());
        true
    }

    fn accept(&self, relay_id: u64) -> bool {
        let mut receivers = recover(&self.receivers);
        if !receivers.owners.contains_key(&relay_id) {
            return false;
        }
        receivers.owners.retain(|id, _| *id >= relay_id);
        receivers.accepted = Some(relay_id);
        self.changed.notify_waiters();
        true
    }

    async fn open(
        &self,
        relay_id: u64,
        request: impl std::future::Future<Output = Result<proto::Response, String>>,
        repeat_close: impl std::future::Future<Output = Result<proto::Response, String>>,
    ) -> Result<(), String> {
        if !self.register(relay_id) {
            return Err("relay receiver could not be registered".to_string());
        }
        if let Err(error) = request.await.and_then(check_response) {
            self.close_if_owner(relay_id);
            return Err(error);
        }
        if self.accept(relay_id) {
            return Ok(());
        }
        if let Err(error) = repeat_close.await.and_then(check_response) {
            shell_log!("relay {relay_id} cleanup after open failed: {error}");
        }
        Err("relay receiver closed or superseded during open".to_string())
    }

    fn send_raw(&self, relay_id: u64, data: Vec<u8>) {
        let mut receivers = recover(&self.receivers);
        let Some(owner) = receivers.owners.get_mut(&relay_id) else {
            return;
        };
        if owner.closed {
            return;
        }
        if data.len() > self.max_bytes || owner.queued_bytes + data.len() > self.max_bytes {
            owner.items.clear();
            owner.queued_bytes = 0;
            owner.closed = true;
            owner.push(
                RelayReadItem::Close(proto::RelayClose {
                    code: 1006,
                    reason: "the renderer fell behind the native relay".to_string(),
                    was_clean: false,
                }),
                0,
            );
        } else {
            let bytes = data.len();
            owner.push(RelayReadItem::Data(data), bytes);
        }
        drop(receivers);
        self.changed.notify_waiters();
    }

    fn send_close(&self, relay_id: u64, code: u32, reason: String, was_clean: bool) {
        let mut receivers = recover(&self.receivers);
        let Some(owner) = receivers.owners.get_mut(&relay_id) else {
            return;
        };
        if owner.closed {
            return;
        }
        owner.closed = true;
        owner.push(
            RelayReadItem::Close(proto::RelayClose {
                code,
                reason,
                was_clean,
            }),
            0,
        );
        drop(receivers);
        self.changed.notify_waiters();
    }

    async fn read(
        &self,
        relay_id: u64,
        after_cursor: u64,
    ) -> Result<proto::RelayReadResponse, String> {
        loop {
            let notified = self.changed.notified();
            {
                let mut receivers = recover(&self.receivers);
                let owner = receivers
                    .owners
                    .get_mut(&relay_id)
                    .ok_or_else(|| "relay receiver is closed or superseded".to_string())?;
                owner.acknowledge(after_cursor);
                if let Some(item) = owner.items.front() {
                    let item = match &item.item {
                        RelayReadItem::Data(data) => {
                            proto::relay_read_response::Item::Data(data.clone())
                        }
                        RelayReadItem::Close(close) => {
                            proto::relay_read_response::Item::Close(close.clone())
                        }
                    };
                    return Ok(proto::RelayReadResponse {
                        cursor: owner.items.front().expect("front item exists").cursor,
                        item: Some(item),
                    });
                }
            }
            notified.await;
        }
    }

    #[cfg(test)]
    fn has_receiver(&self, relay_id: u64) -> bool {
        recover(&self.receivers).owners.contains_key(&relay_id)
    }

    #[cfg(test)]
    fn with_max_bytes(max_bytes: usize) -> Self {
        Self {
            max_bytes,
            ..Self::default()
        }
    }

    fn close_if_owner(&self, relay_id: u64) -> bool {
        let mut receivers = recover(&self.receivers);
        receivers.closed_through = receivers.closed_through.max(relay_id);
        if receivers.accepted == Some(relay_id) {
            receivers.accepted = None;
        }
        let removed = receivers.owners.remove(&relay_id).is_some();
        drop(receivers);
        self.changed.notify_waiters();
        removed
    }
}

/// Both relays' delivery slots: the channel relay and the user-events relay.
#[derive(Default)]
struct RelaySlots {
    channel: RelayChannelSlot,
    user_events: RelayChannelSlot,
}

// The sidecar uses relay_id to ignore a close after a newer open takes
// ownership. See the comment on OpenChannelRelayRequest.
#[tauri::command]
async fn open_channel_relay(
    shell: State<'_, Arc<DesktopShell>>,
    slots: State<'_, RelaySlots>,
    relay_id: u64,
) -> Result<(), String> {
    if slots.channel.is_accepted(relay_id) {
        return Ok(());
    }
    slots
        .channel
        .open(
            relay_id,
            shell.send_request_async(proto::request::Method::OpenChannelRelay(
                proto::OpenChannelRelayRequest { relay_id },
            )),
            shell.send_request_async(proto::request::Method::CloseChannelRelay(
                proto::CloseChannelRelayRequest { relay_id },
            )),
        )
        .await
}

#[tauri::command]
async fn read_channel_relay(
    slots: State<'_, RelaySlots>,
    relay_id: u64,
    after_cursor: String,
) -> Result<tauri::ipc::Response, String> {
    let cursor = after_cursor
        .parse::<u64>()
        .map_err(|error| format!("parse relay cursor: {error}"))?;
    let response = slots.channel.read(relay_id, cursor).await?;
    Ok(raw_response(response.encode_to_vec()))
}

#[tauri::command]
async fn send_channel_message(
    shell: State<'_, Arc<DesktopShell>>,
    registry: State<'_, RawInvokeRegistry>,
    request: tauri::ipc::Request<'_>,
) -> Result<(), String> {
    // The frame crosses the Tauri IPC as the raw invoke body, the
    // `file_save_write` pattern -- no base64 round-trip. Relay frames are
    // Noise ciphertext chunks (contracts/wire.json's
    // maxCiphertextForChunkBytes), which limits the size of this copy.
    let request_id = read_header_str(&request, IPC_HEADER_REQUEST_ID)?.to_string();
    if let Some(cached) = registry.begin(&request_id, "channel send")? {
        return match cached {
            RawInvokeResult::Send(result) => result,
            RawInvokeResult::Proxy(_) => {
                Err("raw invoke request id was reused by another command".to_string())
            }
        };
    }

    let parsed = (|| {
        let data = read_raw_invoke_body(&request)?;
        let relay_id = read_header_str(&request, IPC_HEADER_RELAY_ID)?
            .parse::<u64>()
            .map_err(|err| format!("parse relay id: {err}"))?;
        Ok::<_, String>((relay_id, data))
    })();
    let (relay_id, data) = match parsed {
        Ok(parsed) => parsed,
        Err(error) => {
            registry.finish(request_id, RawInvokeResult::Send(Err(error.clone())));
            return Err(error);
        }
    };

    let result = async {
        check_response(
            shell
                .send_request_async(proto::request::Method::SendChannelMessage(
                    proto::SendChannelMessageRequest { relay_id, data },
                ))
                .await?,
        )?;
        Ok(())
    }
    .await;
    registry.finish(request_id, RawInvokeResult::Send(result.clone()));
    result
}

#[tauri::command]
async fn close_channel_relay(
    shell: State<'_, Arc<DesktopShell>>,
    slots: State<'_, RelaySlots>,
    relay_id: u64,
) -> Result<(), String> {
    // Drop the owner's delivery target before waiting for the sidecar. The
    // wrapper has already closed.
    slots.channel.close_if_owner(relay_id);
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
    slots: State<'_, RelaySlots>,
    relay_id: u64,
    workspace_ids: Vec<String>,
    resume_hlc: Option<String>,
    resume_epoch: Option<String>,
) -> Result<(), String> {
    if slots.user_events.is_accepted(relay_id) {
        return Ok(());
    }
    slots
        .user_events
        .open(
            relay_id,
            shell.send_request_async(proto::request::Method::OpenUserEventsRelay(
                proto::OpenUserEventsRelayRequest {
                    relay_id,
                    workspace_ids,
                    resume_hlc,
                    resume_epoch,
                },
            )),
            shell.send_request_async(proto::request::Method::CloseUserEventsRelay(
                proto::CloseUserEventsRelayRequest { relay_id },
            )),
        )
        .await
}

#[tauri::command]
async fn read_userevents_relay(
    slots: State<'_, RelaySlots>,
    relay_id: u64,
    after_cursor: String,
) -> Result<tauri::ipc::Response, String> {
    let cursor = after_cursor
        .parse::<u64>()
        .map_err(|error| format!("parse relay cursor: {error}"))?;
    let response = slots.user_events.read(relay_id, cursor).await?;
    Ok(raw_response(response.encode_to_vec()))
}

#[tauri::command]
async fn close_userevents_relay(
    shell: State<'_, Arc<DesktopShell>>,
    slots: State<'_, RelaySlots>,
    relay_id: u64,
) -> Result<(), String> {
    slots.user_events.close_if_owner(relay_id);
    check_response(
        shell
            .send_request_async(proto::request::Method::CloseUserEventsRelay(
                proto::CloseUserEventsRelayRequest { relay_id },
            ))
            .await?,
    )?;
    Ok(())
}

fn create_tunnel_request(config: TunnelConfigInput) -> proto::request::Method {
    proto::request::Method::CreateTunnel(proto::CreateTunnelRequest {
        config: Some(proto::TunnelConfig {
            worker_id: config.worker_id,
            r#type: config.r#type,
            target_addr: config.target_addr,
            target_port: config.target_port,
            bind_addr: config.bind_addr,
            bind_port: config.bind_port,
            expected_worker_keys_hex: config.expected_worker_keys_hex,
            expected_user_id: config.expected_user_id,
            channel_epoch: config.channel_epoch,
            hub_cookie_header: config.hub_cookie_header,
            hub_cookie_origin: config.hub_cookie_origin,
        }),
    })
}

async fn distributed_tunnel_cookie_header(
    shell: &DesktopShell,
    window: &WebviewWindow,
) -> Result<(String, String), String> {
    let runtime = shell.runtime_state();
    if runtime.shell_mode != ShellMode::Distributed {
        return Ok((String::new(), String::new()));
    }
    if !runtime.connected {
        return Err("the distributed desktop is not connected".to_string());
    }
    let hub_url = Url::parse(&runtime.hub_url).map_err(|err| format!("parse Hub URL: {err}"))?;
    let page_url = window
        .url()
        .map_err(|err| format!("read webview URL: {err}"))?;
    require_same_hub_origin(&page_url, &hub_url)?;
    let hub_origin = hub_url.origin().ascii_serialization();
    let window = window.clone();
    let cookies = tokio::task::spawn_blocking(move || window.cookies_for_url(hub_url))
        .await
        .map_err(|err| format!("read Hub cookies task: {err}"))?
        .map_err(|err| format!("read Hub cookies: {err}"))?;
    let pairs = cookies
        .into_iter()
        .map(|cookie| (cookie.name().to_string(), cookie.value().to_string()))
        .collect::<Vec<_>>();
    let header = format_cookie_header(&pairs);
    if header.is_empty() {
        return Err("the authenticated Hub session cookie is missing".to_string());
    }
    Ok((header, hub_origin))
}

fn require_same_hub_origin(page_url: &Url, hub_url: &Url) -> Result<(), String> {
    if page_url.origin() != hub_url.origin() {
        return Err("the webview origin does not match the connected Hub".to_string());
    }
    Ok(())
}

fn format_cookie_header(cookies: &[(String, String)]) -> String {
    cookies
        .iter()
        .filter(|(name, value)| {
            !value.is_empty()
                && (name == contracts_generated::SESSION_COOKIE_NAME
                    || name == contracts_generated::SECURE_SESSION_COOKIE_NAME)
        })
        .map(|(name, value)| format!("{name}={value}"))
        .collect::<Vec<_>>()
        .join("; ")
}

fn delete_tunnel_request(
    tunnel_id: String,
    channel_epoch: String,
    expected_user_id: String,
) -> proto::request::Method {
    proto::request::Method::DeleteTunnel(proto::DeleteTunnelRequest {
        tunnel_id,
        channel_epoch,
        expected_user_id,
    })
}

fn reset_tunnels_request(channel_epoch: String) -> proto::request::Method {
    proto::request::Method::ResetTunnels(proto::ResetTunnelsRequest { channel_epoch })
}

fn list_tunnels_request(expected_user_id: String) -> proto::request::Method {
    proto::request::Method::ListTunnels(proto::ListTunnelsRequest { expected_user_id })
}

#[tauri::command]
async fn create_tunnel(
    shell: State<'_, Arc<DesktopShell>>,
    window: WebviewWindow,
    config: TunnelConfigInput,
) -> Result<CreateTunnelOutcome, String> {
    let mut config = config;
    let (cookie_header, cookie_origin) = distributed_tunnel_cookie_header(&shell, &window).await?;
    config.hub_cookie_header = cookie_header;
    config.hub_cookie_origin = cookie_origin;
    let resp = check_response(
        shell
            .send_request_async(create_tunnel_request(config))
            .await?,
    )?;
    create_tunnel_outcome(resp.result)
}

fn create_tunnel_outcome(
    result: Option<proto::response::Result>,
) -> Result<CreateTunnelOutcome, String> {
    match result {
        Some(proto::response::Result::CreateTunnel(r)) => match r.result {
            Some(proto::create_tunnel_response::Result::Info(info)) => {
                Ok(CreateTunnelOutcome::Info {
                    info: proto_to_tunnel_info(info),
                })
            }
            Some(proto::create_tunnel_response::Result::KeyMismatch(m)) => {
                Ok(CreateTunnelOutcome::KeyMismatch {
                    key_mismatch: proto_to_tunnel_mismatch(m),
                })
            }
            None => Err("missing result in create_tunnel response".to_string()),
        },
        _ => Err("unexpected response for create_tunnel".to_string()),
    }
}

#[tauri::command]
async fn delete_tunnel(
    shell: State<'_, Arc<DesktopShell>>,
    tunnel_id: String,
    channel_epoch: String,
    expected_user_id: String,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(delete_tunnel_request(
                tunnel_id,
                channel_epoch,
                expected_user_id,
            ))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn reset_tunnels(
    shell: State<'_, Arc<DesktopShell>>,
    channel_epoch: String,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(reset_tunnels_request(channel_epoch))
            .await?,
    )?;
    Ok(())
}

#[tauri::command]
async fn list_tunnels(
    shell: State<'_, Arc<DesktopShell>>,
    expected_user_id: String,
) -> Result<ListTunnelsResult, String> {
    let resp = check_response(
        shell
            .send_request_async(list_tunnels_request(expected_user_id))
            .await?,
    )?;
    list_tunnels_result(resp.result)
}

fn list_tunnels_result(
    result: Option<proto::response::Result>,
) -> Result<ListTunnelsResult, String> {
    match result {
        Some(proto::response::Result::ListTunnels(r)) => Ok(ListTunnelsResult {
            tunnels: r.tunnels.into_iter().map(proto_to_tunnel_info).collect(),
            channel_epoch: r.channel_epoch,
        }),
        _ => Err("unexpected response for list_tunnels".to_string()),
    }
}

fn proto_to_tunnel_info(info: proto::TunnelInfo) -> TunnelInfoResponse {
    TunnelInfoResponse {
        id: info.id,
        worker_id: info.worker_id,
        r#type: info.r#type,
        bind_addr: info.bind_addr,
        bind_port: info.bind_port,
        target_addr: info.target_addr,
        target_port: info.target_port,
        key_mismatch: info.key_mismatch.map(proto_to_tunnel_mismatch),
    }
}

fn proto_to_tunnel_mismatch(mismatch: proto::TunnelWorkerKeyMismatch) -> TunnelKeyMismatchResponse {
    TunnelKeyMismatchResponse {
        expected_fingerprint: mismatch.expected_fingerprint,
        presented_fingerprint: mismatch.presented_fingerprint,
    }
}

/// One application the sidecar detected, as the webview reads it.
///
/// The id and the name are all that cross: what the application IS comes from
/// contracts/external-apps.json, which the webview reads through its own
/// generated table rather than over the wire.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct ExternalAppPayload {
    id: String,
    display_name: String,
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

struct SwitchModeGuard {
    shell: Arc<DesktopShell>,
    switch_id: u64,
}

impl Drop for SwitchModeGuard {
    fn drop(&mut self) {
        self.shell.quit_guard.finish_switch(self.switch_id);
    }
}

struct SwitchInspectGuard {
    shell: Arc<DesktopShell>,
    inspect_id: Option<u64>,
}

impl Drop for SwitchInspectGuard {
    fn drop(&mut self) {
        if let Some(inspect_id) = self.inspect_id.take() {
            self.shell.quit_guard.cancel_switch_inspect(inspect_id);
        }
    }
}

#[tauri::command]
async fn switch_mode(
    shell: State<'_, Arc<DesktopShell>>,
    window: WebviewWindow,
    switch_id: Option<u64>,
) -> Result<(), String> {
    let active_switch_id = shell
        .quit_guard
        .enter_switch_mode(shell.is_solo_connected(), switch_id)?;
    let _switch_guard = SwitchModeGuard {
        shell: shell.inner().clone(),
        switch_id: active_switch_id,
    };

    let response = shell
        .send_request_async(proto::request::Method::SwitchMode(
            proto::SwitchModeRequest {},
        ))
        .await?;
    let (info, cleanup_errors) = lifecycle_from_response(response)?;
    apply_sidecar_info(&shell.state, info);
    shell.quit_guard.cancel_all();

    let window_url = window
        .url()
        .map_err(|err| format!("read the window url: {err}"))?;
    let base_url = launcher_base_url(&window_url);
    let (target_url, cleanup_message) = launcher_url(base_url.as_str(), &cleanup_errors)?;
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

/// The launcher's own origin, derived from the URL the window holds.
///
/// The window always shows the bundled frontend -- on the app origin in a
/// release build, on the dev server in a dev build -- so its origin IS the
/// launcher URL, and no stored copy can drift from it. The path, query and
/// fragment of the current URL carry the route the user is on, not the
/// launcher, so they are dropped here.
fn launcher_base_url(window_url: &Url) -> Url {
    let mut base = window_url.clone();
    base.set_path("/");
    base.set_query(None);
    base.set_fragment(None);
    base
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
fn restart_command(parent_pid: u32, app_bundle: &std::path::Path) -> Command {
    let mut command = Command::new("/bin/sh");
    // Filesystem paths are shell arguments, so the shell never interprets their
    // dollar signs, quotes, or command substitutions as part of its program.
    command
        .arg("-c")
        .arg("while kill -0 \"$1\" 2>/dev/null; do sleep 0.1; done; exec /usr/bin/open -n \"$2\"")
        .arg("leapmux-restart")
        .arg(parent_pid.to_string())
        .arg(app_bundle);
    command
}

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
    restart_command(parent_pid, &app_bundle)
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
            refusal.setting,
            refusal.message
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
    login_item::apply(&*app.autolaunch(), start_on_login)
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

/// Start a guarded shutdown: in connected Solo mode, inspect local busy work
/// and confirm before draining the sidecar; otherwise drain and exit at once.
///
/// Shared by the `quit_app` command, the macOS app menu Quit item, and the
/// tray menu's Quit item so all three spellings cannot drift. It never touches
/// the window, so it cannot be diverted into the hide-to-tray branch of
/// `CloseRequested`.
pub(crate) fn request_app_exit(app: &AppHandle) {
    if let Some(shell) = app.try_state::<Arc<DesktopShell>>() {
        request_guarded_exit(shell.inner().clone());
    } else {
        app.exit(0);
    }
}

#[tauri::command]
fn quit_app(app: AppHandle) {
    request_app_exit(&app);
}

#[tauri::command]
async fn begin_switch_mode(
    shell: State<'_, Arc<DesktopShell>>,
) -> Result<quit_guard::BeginSwitchModeResult, String> {
    match shell
        .quit_guard
        .begin_switch_inspect(shell.is_solo_connected())
    {
        quit_guard::SwitchInspectStart::Proceed(switch_id) => {
            Ok(quit_guard::BeginSwitchModeResult::Proceed { switch_id })
        }
        quit_guard::SwitchInspectStart::Blocked => Ok(quit_guard::BeginSwitchModeResult::Blocked),
        quit_guard::SwitchInspectStart::BlockedPrompting => {
            tray::show_main_window(&shell.app_handle);
            Ok(quit_guard::BeginSwitchModeResult::Blocked)
        }
        quit_guard::SwitchInspectStart::Inspect(inspect_id) => {
            let mut inspect_guard = SwitchInspectGuard {
                shell: shell.inner().clone(),
                inspect_id: Some(inspect_id),
            };
            let busy = shell.inspect_solo_busy_work_async().await;
            inspect_guard.inspect_id = None;
            Ok(shell.quit_guard.complete_switch_inspect(
                inspect_id,
                shell.is_solo_connected(),
                busy,
            ))
        }
    }
}

#[tauri::command]
fn resolve_switch_mode_confirmation(
    shell: State<'_, Arc<DesktopShell>>,
    prompt_id: u64,
    confirm: bool,
) -> Option<u64> {
    shell.quit_guard.resolve_switch_prompt(prompt_id, confirm)
}

#[tauri::command]
fn cancel_switch_mode(shell: State<'_, Arc<DesktopShell>>, switch_id: u64) {
    shell.quit_guard.finish_switch(switch_id);
}

#[tauri::command]
fn ack_quit_confirmation(shell: State<'_, Arc<DesktopShell>>, prompt_id: u64) -> bool {
    shell.quit_guard.acknowledge_webview(prompt_id)
}

#[tauri::command]
fn resolve_quit_confirmation(shell: State<'_, Arc<DesktopShell>>, prompt_id: u64, confirm: bool) {
    let exit_shell = shell.inner().clone();
    apply_quit_confirmation_resolution(&shell.quit_guard, prompt_id, confirm, move || {
        handle_app_exit(exit_shell);
    });
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

// --- Browser auth commands ---

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct BrowserAuthStart {
    ceremony_id: String,
    authorize_url: String,
}

/// The `kind` argument of the one start command, validated against the
/// proto enum. `Unspecified` identifies no ceremony, and an unknown value can
/// only come from a webview built against another enum. Both fail here
/// rather than reach the sidecar with a kind nobody chose.
fn parse_browser_auth_kind(kind: i32) -> Result<proto::BrowserAuthKind, String> {
    match proto::BrowserAuthKind::try_from(kind) {
        Ok(auth_kind @ (proto::BrowserAuthKind::SignIn | proto::BrowserAuthKind::StepUp)) => {
            Ok(auth_kind)
        }
        Ok(proto::BrowserAuthKind::Unspecified) | Err(_) => {
            Err(format!("invalid browser auth kind: {kind}"))
        }
    }
}

#[tauri::command]
async fn start_browser_auth(
    shell: State<'_, Arc<DesktopShell>>,
    kind: i32,
) -> Result<BrowserAuthStart, String> {
    let auth_kind = parse_browser_auth_kind(kind)?;
    let resp = check_response(
        shell
            .send_request_async(proto::request::Method::StartBrowserAuth(
                proto::StartBrowserAuthRequest {
                    kind: auth_kind as i32,
                },
            ))
            .await?,
    )?;
    match resp.result {
        Some(proto::response::Result::StartBrowserAuth(start)) => Ok(BrowserAuthStart {
            ceremony_id: start.ceremony_id,
            authorize_url: start.authorize_url,
        }),
        // The kind is in the message so a sign-in answer and a step-up
        // answer stay distinguishable.
        _ => Err(format!(
            "unexpected response for start_browser_auth({auth_kind:?})"
        )),
    }
}

#[tauri::command]
async fn cancel_browser_auth(
    shell: State<'_, Arc<DesktopShell>>,
    ceremony_id: String,
) -> Result<(), String> {
    check_response(
        shell
            .send_request_async(proto::request::Method::CancelBrowserAuth(
                proto::CancelBrowserAuthRequest { ceremony_id },
            ))
            .await?,
    )?;
    Ok(())
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

/// Start the ordinary shutdown -- claim the exit, drop the save handles, and
/// await the sidecar -- then end the exit through `finish`.
///
/// The split exists so a caller can choose how the exit ends: `handle_app_exit`
/// finishes with `AppHandle::exit(0)`, and the macOS `terminate:` hook
/// (terminate_macos) finishes with `replyToApplicationShouldTerminate:YES`,
/// which AppKit requires before it commits the quit it is holding.
///
/// Returns false when another exit already runs; the caller then answers or
/// exits nothing. It never touches the window, so it cannot be diverted into
/// the hide-to-tray branch of `CloseRequested`.
fn begin_app_exit(
    shell: Arc<DesktopShell>,
    finish: impl FnOnce(&AppHandle) + Send + 'static,
) -> bool {
    if !shell.quit_guard.claim_exit() {
        return false;
    }

    // Drop every open save handle and remove its partial file BEFORE the
    // sidecar goes away. The claim above runs this exactly once.
    //
    // It lives here rather than in the `ExitRequested` arm because that arm is
    // skipped once `QuitGuard` enters `Exiting` -- and `quit_app` claims it
    // first, so the menu's Quit used to leave the partials on disk. The tray's
    // Quit takes the same route, which is what made the gap worth closing.
    if let Some(registry) = shell.app_handle.try_state::<Arc<SaveStreamRegistry>>() {
        registry.cleanup_all();
    }

    tauri::async_runtime::spawn(async move {
        shell.request_shutdown_async().await;
        finish(&shell.app_handle);
    });
    true
}

fn handle_app_exit(shell: Arc<DesktopShell>) {
    begin_app_exit(shell, |app| app.exit(0));
}

fn apply_quit_confirmation_resolution(
    quit_guard: &quit_guard::QuitGuard,
    prompt_id: u64,
    confirm: bool,
    on_confirm: impl FnOnce(),
) {
    if quit_guard.resolve_webview(prompt_id, confirm) == quit_guard::PromptResolution::Confirmed {
        on_confirm();
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum GuardedExitAction {
    Ignore,
    ExitImmediately,
    ShowExistingPrompt,
    Inspect(u64),
}

#[derive(Debug, PartialEq, Eq)]
enum InspectOutcome {
    Stale,
    Exit,
    Present(quit_guard::SoloBusyWork),
}

fn plan_guarded_exit(
    is_solo_connected: bool,
    quit_guard: &quit_guard::QuitGuard,
) -> GuardedExitAction {
    if quit_guard.is_exiting() {
        return GuardedExitAction::Ignore;
    }
    if !is_solo_connected {
        return GuardedExitAction::ExitImmediately;
    }
    match quit_guard.begin_inspect() {
        quit_guard::InspectStart::Started(inspect_id) => GuardedExitAction::Inspect(inspect_id),
        quit_guard::InspectStart::AlreadyInspecting | quit_guard::InspectStart::AlreadyExiting => {
            GuardedExitAction::Ignore
        }
        quit_guard::InspectStart::AlreadyPrompting => GuardedExitAction::ShowExistingPrompt,
        quit_guard::InspectStart::SwitchingInProgress => GuardedExitAction::ExitImmediately,
    }
}

fn complete_guarded_inspect(
    inspect_id: u64,
    is_solo_connected: bool,
    quit_guard: &quit_guard::QuitGuard,
    busy: quit_guard::SoloBusyWork,
) -> InspectOutcome {
    if !quit_guard.is_inspecting(inspect_id) {
        return InspectOutcome::Stale;
    }
    if !is_solo_connected || busy.is_empty() {
        if quit_guard.finish_inspect_idle(inspect_id) {
            return InspectOutcome::Exit;
        }
        return InspectOutcome::Stale;
    }
    InspectOutcome::Present(busy)
}

fn request_guarded_exit_with(
    shell: Arc<DesktopShell>,
    on_exit: impl FnOnce(Arc<DesktopShell>) + Send + 'static,
) {
    match plan_guarded_exit(shell.is_solo_connected(), &shell.quit_guard) {
        GuardedExitAction::Ignore => {}
        GuardedExitAction::ExitImmediately => on_exit(shell),
        GuardedExitAction::ShowExistingPrompt => tray::show_main_window(&shell.app_handle),
        GuardedExitAction::Inspect(inspect_id) => {
            tauri::async_runtime::spawn(async move {
                let busy = shell.inspect_solo_busy_work_async().await;
                let is_solo_connected = shell.is_solo_connected();
                match complete_guarded_inspect(
                    inspect_id,
                    is_solo_connected,
                    &shell.quit_guard,
                    busy,
                ) {
                    InspectOutcome::Stale => {}
                    InspectOutcome::Exit => on_exit(shell),
                    InspectOutcome::Present(busy) => {
                        quit_guard::present_quit_confirmation(shell, inspect_id, busy);
                    }
                }
            });
        }
    }
}

fn request_guarded_exit(shell: Arc<DesktopShell>) {
    request_guarded_exit_with(shell, handle_app_exit);
}

#[cfg(target_os = "macos")]
struct MacTerminateHoldGuard<F: FnOnce()> {
    on_drop: Option<F>,
}

#[cfg(target_os = "macos")]
impl<F: FnOnce()> MacTerminateHoldGuard<F> {
    fn new(on_drop: F) -> Self {
        Self {
            on_drop: Some(on_drop),
        }
    }

    fn disarm(&mut self) {
        self.on_drop = None;
    }
}

#[cfg(target_os = "macos")]
impl<F: FnOnce()> Drop for MacTerminateHoldGuard<F> {
    fn drop(&mut self) {
        if let Some(on_drop) = self.on_drop.take() {
            on_drop();
        }
    }
}

#[cfg(target_os = "macos")]
fn dispatch_macos_inspect_outcome(
    outcome: InspectOutcome,
    begin_exit: impl FnOnce() -> bool,
    reply_terminate: impl FnOnce(bool),
    reply_false_then_present: impl FnOnce(quit_guard::SoloBusyWork),
) {
    match outcome {
        InspectOutcome::Stale => reply_terminate(false),
        InspectOutcome::Exit => {
            if !begin_exit() {
                reply_terminate(false);
            }
        }
        InspectOutcome::Present(busy) => {
            // Release AppKit's NSTerminateLater hold on the main thread BEFORE
            // presenting the confirmation dialog so a second terminate while
            // the dialog is open does not bypass applicationShouldTerminate:.
            reply_false_then_present(busy);
        }
    }
}

#[cfg(target_os = "macos")]
pub(crate) fn handle_macos_terminate(
    shell: Arc<DesktopShell>,
) -> terminate_macos::TerminateDecision {
    match plan_guarded_exit(shell.is_solo_connected(), &shell.quit_guard) {
        GuardedExitAction::Ignore => terminate_macos::TerminateDecision::Cancel,
        GuardedExitAction::ExitImmediately => {
            if begin_app_exit(shell, |app| terminate_macos::reply_to_terminate(app, true)) {
                terminate_macos::TerminateDecision::Later
            } else {
                terminate_macos::TerminateDecision::Cancel
            }
        }
        GuardedExitAction::ShowExistingPrompt => {
            tray::show_main_window(&shell.app_handle);
            terminate_macos::TerminateDecision::Cancel
        }
        GuardedExitAction::Inspect(inspect_id) => {
            tauri::async_runtime::spawn(async move {
                let guard_shell = shell.clone();
                let mut hold_guard = MacTerminateHoldGuard::new(move || {
                    guard_shell.quit_guard.finish_inspect_idle(inspect_id);
                    terminate_macos::reply_to_terminate(&guard_shell.app_handle, false);
                });
                let busy = shell.inspect_solo_busy_work_async().await;
                let is_solo_connected = shell.is_solo_connected();
                let outcome = complete_guarded_inspect(
                    inspect_id,
                    is_solo_connected,
                    &shell.quit_guard,
                    busy,
                );
                hold_guard.disarm();
                let exit_shell = shell.clone();
                let reply_app = shell.app_handle.clone();
                let present_app = shell.app_handle.clone();
                let present_shell = shell.clone();
                dispatch_macos_inspect_outcome(
                    outcome,
                    move || {
                        begin_app_exit(exit_shell, |app| {
                            terminate_macos::reply_to_terminate(app, true)
                        })
                    },
                    move |should_terminate| {
                        terminate_macos::reply_to_terminate(&reply_app, should_terminate)
                    },
                    move |busy| {
                        terminate_macos::reply_to_terminate_then(&present_app, false, move || {
                            quit_guard::present_quit_confirmation(present_shell, inspect_id, busy);
                        });
                    },
                );
            });
            terminate_macos::TerminateDecision::Later
        }
    }
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

    let quit_app_item = MenuItem::with_id(
        app,
        QUIT_APP_MENU_ID,
        "Quit LeapMux Desktop",
        true,
        Some("CmdOrCtrl+Q"),
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
            &quit_app_item,
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
        ))
        // The navigation allow-list covers every webview, not just the main
        // window; see `navigation::plugin`.
        .plugin(navigation::plugin());

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
            } else if event.id() == QUIT_APP_MENU_ID {
                request_app_exit(app);
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
                    // time, and diverting THAT into the tray or quit guard
                    // would strand a close already under way.
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
                        // Keep the window alive while the quit guard inspects
                        // and prompts; when idle or non-Solo, close the window
                        // through `handle_main_window_close` so `pagehide`
                        // flushes client storage before `ExitRequested` drains.
                        api.prevent_close();
                        let close_window = window.clone();
                        request_guarded_exit_with(shell.inner().clone(), move |s| {
                            handle_main_window_close(s, close_window);
                        });
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
            // The `main` window is declared with `"create": false`, so the
            // shell builds it here. `on_new_window` refuses every new window
            // the PAGE requests (`window.open`, a `target="_blank"` link).
            // The native command opens the bundled licenses window with the
            // same popup policy. The navigation plugin covers both windows.
            let window_config = app
                .config()
                .app
                .windows
                .iter()
                .find(|window| window.label == MAIN_WINDOW_LABEL)
                .ok_or_else(|| {
                    format!("tauri.conf.json must declare the {MAIN_WINDOW_LABEL} window")
                })?;
            tauri::WebviewWindowBuilder::from_config(app.handle(), window_config)?
                .on_new_window(|_, _| tauri::webview::NewWindowResponse::Deny)
                .on_page_load(|window, payload| {
                    if payload.event() == tauri::webview::PageLoadEvent::Started {
                        if let Some(shell) = window.app_handle().try_state::<Arc<DesktopShell>>() {
                            shell.quit_guard.cancel_webview_prompt();
                        }
                    }
                })
                .build()?;

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

            // The relay delivery slots the sidecar reader thread sends frames
            // through. Managed BEFORE `DesktopShell::connect`, whose reader
            // thread starts delivering the moment the sidecar speaks.
            app.manage(RelaySlots::default());
            app.manage(RawInvokeRegistry::default());
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
            if runtime_state.connected {
                // A launch that finds a live session enters the session
                // surface directly and never mounts `LauncherView` -- the only
                // caller of `restoreWindowGeometry` and the only reader of the
                // launch decision through `get_startup_info`. The shell
                // therefore applies the decision here, in every mode, and
                // navigates nowhere: the window already holds the bundled
                // frontend. Without it, such a launch would open at the
                // built-in default size and the visibility the decision asks
                // for would reach nothing.
                //
                // Through `take()`, so the decision is CONSUMED here: a later
                // "Switch mode..." navigates back to the launcher, which mounts
                // and asks `get_startup_info` for the launch state. Leaving the
                // latch unread would answer `hidden` and hide the launcher the
                // user just asked for.
                tray::apply_launch_visibility(app.handle(), launch_state.take(), Some(&cached));
            }
            // macOS: the Quit routes that bypass `ExitRequested` -- the menu
            // bar's PredefinedMenuItem, Cmd+Q, the Dock, AppleScript, logout
            // -- all send `terminate:`. The hook routes them into the same
            // cooperative shutdown. A refusal is logged and the shell keeps
            // its previous behaviour, which is the bug this hook exists to
            // close; silence would hide the regression.
            #[cfg(target_os = "macos")]
            if let Err(err) = terminate_macos::install(shell.clone()) {
                shell_log!("a quit cannot run the cooperative shutdown: {err}");
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
            cancel_proxy_http,
            cli_path_status,
            cli_install_symlink,
            open_notice_window,
            open_channel_relay,
            read_channel_relay,
            send_channel_message,
            close_channel_relay,
            open_userevents_relay,
            read_userevents_relay,
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
            start_browser_auth,
            cancel_browser_auth,
            quit_app,
            begin_switch_mode,
            resolve_switch_mode_confirmation,
            cancel_switch_mode,
            ack_quit_confirmation,
            resolve_quit_confirmation,
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
                        if !shell.quit_guard.is_exiting() {
                            // The save handles are dropped inside
                            // `handle_app_exit`, which every exit route reaches
                            // -- including the ones that claim `Exiting` before
                            // this arm runs.
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

    // The exit claim must admit exactly one route: a second route that
    // arrives while the first drains must read the latched Exiting state and
    // bow out, or a quit and a Dock-hide race would run two sidecar shutdowns.
    #[test]
    fn claim_exit_succeeds_once() {
        let guard = quit_guard::QuitGuard::default();

        assert!(guard.claim_exit(), "the first claim must succeed");
        assert!(
            !guard.claim_exit(),
            "the second claim must observe the latched Exiting state"
        );
    }

    // `proxy_http` sends the existing protobuf over a raw Tauri response. This
    // pins the generated wire data and its binary body through the IPC wrapper.
    #[test]
    fn proxy_response_wraps_generated_wire_data_as_raw_ipc_bytes() {
        use tauri::ipc::IpcResponse;

        let response = proto::ProxyHttpResponse {
            status: 204,
            headers: HashMap::from([(
                "x-repeated".to_string(),
                proto::HeaderValues {
                    values: vec!["one".to_string(), "two".to_string()],
                },
            )]),
            body: prost::bytes::Bytes::from_static(b"payload"),
        };
        let wire = response.encode_to_vec();
        let response = tauri::ipc::Response::new(tauri::ipc::InvokeResponseBody::Raw(wire.clone()));
        match response.body().expect("the response body") {
            tauri::ipc::InvokeResponseBody::Raw(bytes) => {
                assert_eq!(bytes, wire);
                let decoded = proto::ProxyHttpResponse::decode(bytes.as_slice())
                    .expect("decode the generated response");
                assert_eq!(decoded.status, 204);
                assert_eq!(decoded.headers["x-repeated"].values, ["one", "two"]);
                assert_eq!(decoded.body.as_ref(), b"payload");
            }
            tauri::ipc::InvokeResponseBody::Json(_) => panic!("expected a raw response body"),
        }
    }

    #[test]
    fn raw_invoke_fallback_accepts_only_byte_arrays() {
        assert_eq!(
            json_bytes(&[json!(0), json!(127), json!(255)]).expect("valid bytes"),
            [0, 127, 255]
        );
        assert!(json_bytes(&[json!(-1)]).is_err());
        assert!(json_bytes(&[json!(256)]).is_err());
        assert!(json_bytes(&[json!(1.5)]).is_err());
    }

    #[test]
    fn raw_invoke_replay_returns_the_first_result() {
        let registry = RawInvokeRegistry::default();
        assert!(registry
            .begin("request-1", "HTTP proxy")
            .expect("admit first request")
            .is_none());
        registry.finish(
            "request-1".to_string(),
            RawInvokeResult::Proxy(Ok(vec![1, 2, 3])),
        );

        match registry
            .begin("request-1", "HTTP proxy")
            .expect("replay cached request")
            .expect("cached result")
        {
            RawInvokeResult::Proxy(result) => assert_eq!(result.expect("cached reply"), [1, 2, 3]),
            RawInvokeResult::Send(_) => panic!("cached the wrong command result"),
        }
    }

    #[test]
    fn raw_invoke_replay_evicts_the_oldest_body_at_the_byte_budget() {
        let registry = RawInvokeRegistry::with_limits(10, 8);
        for request_id in ["r1", "r2"] {
            assert!(registry
                .begin(request_id, "HTTP proxy")
                .expect("admit request")
                .is_none());
            registry.finish(
                request_id.to_string(),
                RawInvokeResult::Proxy(Ok(vec![1, 2, 3])),
            );
        }

        let cache = recover(&registry.cache);
        assert!(!cache.entries.contains_key("r1"));
        assert!(cache.entries.contains_key("r2"));
        assert_eq!(cache.completed_bytes, 5);
    }

    #[test]
    fn relay_close_before_registration_refuses_that_open() {
        let slot = RelayChannelSlot::default();
        assert!(!slot.close_if_owner(7));
        assert!(!slot.register(7));
        assert!(slot.register(8));
    }

    #[tokio::test]
    async fn relay_open_retains_one_shot_bootstrap_before_the_reply() {
        let slots = RelaySlots::default();
        let slot = &slots.user_events;
        let one_shot = vec![0, 0, 0, 3, 9, 8, 7];

        slot.open(
            1,
            async {
                slot.send_raw(1, one_shot.clone());
                Ok(proto::Response::default())
            },
            async { panic!("a successful open needs no cleanup") },
        )
        .await
        .expect("the relay open should succeed");

        let delivery = slot.read(1, 0).await.expect("read bootstrap");
        assert_eq!(delivery.cursor, 1);
        assert_eq!(
            delivery.item,
            Some(proto::relay_read_response::Item::Data(one_shot))
        );
    }

    #[tokio::test]
    async fn relay_routes_frames_to_their_owner_while_a_new_open_is_pending() {
        let slots = RelaySlots::default();
        for slot in [&slots.channel, &slots.user_events] {
            assert!(slot.register(1));
            assert!(slot.accept(1));

            slot.open(
                2,
                async {
                    slot.send_raw(1, vec![10]);
                    slot.send_raw(2, vec![20]);
                    assert_eq!(
                        slot.read(1, 0).await.expect("read predecessor").item,
                        Some(proto::relay_read_response::Item::Data(vec![10]))
                    );
                    assert_eq!(
                        slot.read(2, 0).await.expect("read pending successor").item,
                        Some(proto::relay_read_response::Item::Data(vec![20]))
                    );
                    Ok(proto::Response::default())
                },
                async { panic!("a successful open needs no cleanup") },
            )
            .await
            .expect("the successor open should succeed");

            slot.send_raw(1, vec![12]);
            slot.send_raw(2, vec![22]);
            assert!(!slot.has_receiver(1));
            assert_eq!(
                slot.read(2, 1).await.expect("read successor").item,
                Some(proto::relay_read_response::Item::Data(vec![22]))
            );
        }
    }

    #[tokio::test]
    async fn relay_repeats_unacknowledged_data_then_delivers_close_in_order() {
        let slots = RelaySlots::default();
        for slot in [&slots.channel, &slots.user_events] {
            assert!(slot.register(2));
            assert!(slot.accept(2));

            let frame = vec![0xff; 1024];
            slot.send_raw(2, frame.clone());
            slot.send_close(1, 1011, "stale close".to_string(), false);
            slot.send_close(2, 1000, "done".to_string(), true);

            let first = slot.read(2, 0).await.expect("read frame");
            let repeated = slot.read(2, 0).await.expect("repeat frame");
            assert_eq!(first, repeated);
            assert_eq!(
                first.item,
                Some(proto::relay_read_response::Item::Data(frame))
            );
            let close = slot.read(2, first.cursor).await.expect("read close");
            assert_eq!(
                close.item,
                Some(proto::relay_read_response::Item::Close(proto::RelayClose {
                    code: 1000,
                    reason: "done".to_string(),
                    was_clean: true,
                }))
            );
        }
    }

    #[tokio::test]
    async fn relay_overflow_closes_only_the_slow_owner() {
        let slot = RelayChannelSlot::with_max_bytes(4);
        assert!(slot.register(1));
        assert!(slot.accept(1));
        assert!(slot.register(2));
        slot.send_raw(1, vec![1, 2, 3]);
        slot.send_raw(2, vec![9]);
        slot.send_raw(1, vec![4, 5]);

        let overflow = slot.read(1, 0).await.expect("read overflow close");
        let Some(proto::relay_read_response::Item::Close(close)) = overflow.item else {
            panic!("overflow must close the slow owner")
        };
        assert_eq!(close.code, 1006);
        assert!(!close.was_clean);
        assert_eq!(
            slot.read(2, 0).await.expect("read other owner").item,
            Some(proto::relay_read_response::Item::Data(vec![9]))
        );
    }

    #[tokio::test]
    async fn relay_preserves_the_predecessor_when_a_new_open_fails() {
        let slot = RelayChannelSlot::default();
        assert!(slot.register(1));
        assert!(slot.accept(1));

        assert_eq!(
            slot.open(2, async { Err("dial failed".to_string()) }, async {
                panic!("a rejected open needs no cleanup")
            },)
                .await,
            Err("dial failed".to_string())
        );
        assert!(!slot.has_receiver(2));
        slot.send_raw(2, vec![20]);
        slot.send_raw(1, vec![10]);
        assert_eq!(
            slot.read(1, 0).await.expect("read predecessor").item,
            Some(proto::relay_read_response::Item::Data(vec![10]))
        );
    }

    #[tokio::test]
    async fn relay_repeats_an_owner_close_that_raced_its_open() {
        let slot = RelayChannelSlot::default();
        let repeated = AtomicBool::new(false);

        assert!(slot
            .open(
                1,
                async {
                    assert!(slot.close_if_owner(1));
                    assert!(!slot.has_receiver(1));
                    Ok(proto::Response::default())
                },
                async {
                    repeated.store(true, Ordering::Relaxed);
                    Ok(proto::Response::default())
                },
            )
            .await
            .is_err());
        assert!(repeated.load(Ordering::Relaxed));
        assert!(!slot.has_receiver(1));
    }

    #[tokio::test]
    async fn relay_keeps_the_newer_receiver_when_open_replies_arrive_out_of_order() {
        let slot = RelayChannelSlot::default();
        let closed_older = AtomicBool::new(false);

        let result = slot
            .open(
                1,
                async {
                    slot.open(2, async { Ok(proto::Response::default()) }, async {
                        panic!("the newer open needs no cleanup")
                    })
                    .await
                    .expect("the newer open should succeed");
                    Ok(proto::Response::default())
                },
                async {
                    closed_older.store(true, Ordering::Relaxed);
                    Ok(proto::Response::default())
                },
            )
            .await;

        assert!(result.is_err());
        assert!(closed_older.load(Ordering::Relaxed));
        slot.send_raw(1, vec![10]);
        slot.send_raw(2, vec![20]);
        assert!(!slot.has_receiver(1));
        assert_eq!(
            slot.read(2, 0).await.expect("read newer owner").item,
            Some(proto::relay_read_response::Item::Data(vec![20]))
        );
    }

    #[tokio::test]
    async fn relay_slot_rejects_a_stale_open_and_close() {
        let slot = RelayChannelSlot::default();
        assert!(slot.register(2));
        assert!(slot.accept(2));
        assert!(!slot.register(1));
        assert!(!slot.close_if_owner(1));
        slot.send_raw(1, vec![10]);
        slot.send_raw(2, vec![20]);
        let delivered = slot.read(2, 0).await.expect("read current owner");
        assert_eq!(
            delivered.item,
            Some(proto::relay_read_response::Item::Data(vec![20]))
        );

        assert!(slot.close_if_owner(2));
        slot.send_raw(2, vec![21]);
        assert!(!slot.has_receiver(2));
    }

    // The sidecar sends its build in the shared leapmux.v1.BuildInfo message;
    // the webview still reads the snake_case `build_info` object.
    #[test]
    fn startup_info_response_reports_the_shared_build_message() {
        use prost::Message;

        let sent = proto::StartupInfo {
            config: None,
            build_info: Some(leapmux::v1::BuildInfo {
                version: "1.2.3".to_string(),
                commit_hash: "abc1234".to_string(),
                commit_time: "2026-10-01T00:00:00Z".to_string(),
                build_time: "2026-10-02T00:00:00Z".to_string(),
                branch: "main".to_string(),
            }),
        };
        // Through the wire, as the sidecar's frame arrives.
        let received = proto::StartupInfo::decode(sent.encode_to_vec().as_slice())
            .expect("decode the startup information");

        let json = serde_json::to_value(startup_info_response(received, "shown".to_string()))
            .expect("serialize the startup information");
        assert_eq!(
            json["build_info"],
            serde_json::json!({
                "version": "1.2.3",
                "commit_hash": "abc1234",
                "commit_time": "2026-10-01T00:00:00Z",
                "build_time": "2026-10-02T00:00:00Z",
                "branch": "main",
            })
        );
        assert_eq!(json["launch_visibility"], "shown");
    }

    #[test]
    fn startup_info_response_reports_an_empty_build_when_the_sidecar_sends_none() {
        let json = serde_json::to_value(startup_info_response(
            proto::StartupInfo::default(),
            "hidden".to_string(),
        ))
        .expect("serialize the startup information");
        // Every field is present, so the webview never reads an undefined one.
        assert_eq!(
            json,
            serde_json::json!({
                "config": {
                    "mode": "",
                    "hub_url": "",
                    "window_width": 0,
                    "window_height": 0,
                    "window_mode": "",
                },
                "build_info": {
                    "version": "",
                    "commit_hash": "",
                    "commit_time": "",
                    "build_time": "",
                    "branch": "",
                },
                "launch_visibility": "hidden",
            })
        );
    }

    #[test]
    fn startup_info_response_reports_the_config_the_sidecar_sends() {
        let info = proto::StartupInfo {
            config: Some(proto::DesktopConfig {
                mode: "distributed".to_string(),
                hub_url: "https://hub.example.com".to_string(),
                window_width: 1280,
                window_height: 800,
                window_mode: "maximized".to_string(),
                ..Default::default()
            }),
            build_info: None,
        };
        let json = serde_json::to_value(startup_info_response(info, "shown".to_string()))
            .expect("serialize the startup information");
        assert_eq!(
            json["config"],
            serde_json::json!({
                "mode": "distributed",
                "hub_url": "https://hub.example.com",
                "window_width": 1280,
                "window_height": 800,
                "window_mode": "maximized",
            })
        );
        assert_eq!(json["build_info"]["version"], "");
    }

    #[test]
    fn tunnel_requests_preserve_trust_and_epoch_from_the_webview() {
        let mut config: TunnelConfigInput = serde_json::from_value(json!({
            "workerId": "worker-a",
            "type": "port_forward",
            "targetAddr": "target",
            "targetPort": 3000,
            "bindAddr": "127.0.0.1",
            "bindPort": 3001,
            "expectedWorkerKeysHex": "010203",
            "expectedUserId": "user-a",
            "channelEpoch": "epoch-a",
            "hubCookieHeader": "attacker-controlled",
            "hubCookieOrigin": "https://attacker.example"
        }))
        .expect("deserialize the webview configuration");
        assert!(config.hub_cookie_header.is_empty());
        assert!(config.hub_cookie_origin.is_empty());
        config.hub_cookie_header = "leapmux-session=native".to_string();
        config.hub_cookie_origin = "https://hub.example.com".to_string();

        assert_eq!(
            create_tunnel_request(config),
            proto::request::Method::CreateTunnel(proto::CreateTunnelRequest {
                config: Some(proto::TunnelConfig {
                    worker_id: "worker-a".into(),
                    r#type: "port_forward".into(),
                    target_addr: "target".into(),
                    target_port: 3000,
                    bind_addr: "127.0.0.1".into(),
                    bind_port: 3001,
                    expected_worker_keys_hex: "010203".into(),
                    expected_user_id: "user-a".into(),
                    channel_epoch: "epoch-a".into(),
                    hub_cookie_header: "leapmux-session=native".into(),
                    hub_cookie_origin: "https://hub.example.com".into(),
                }),
            })
        );
        assert_eq!(
            list_tunnels_request("user-a".into()),
            proto::request::Method::ListTunnels(proto::ListTunnelsRequest {
                expected_user_id: "user-a".into(),
            })
        );
        assert_eq!(
            delete_tunnel_request("tunnel-a".into(), "epoch-a".into(), "user-a".into(),),
            proto::request::Method::DeleteTunnel(proto::DeleteTunnelRequest {
                tunnel_id: "tunnel-a".into(),
                channel_epoch: "epoch-a".into(),
                expected_user_id: "user-a".into(),
            })
        );
        assert_eq!(
            reset_tunnels_request("epoch-a".into()),
            proto::request::Method::ResetTunnels(proto::ResetTunnelsRequest {
                channel_epoch: "epoch-a".into(),
            })
        );
    }

    #[test]
    fn distributed_tunnel_credentials_require_the_connected_hub_origin() {
        let hub = Url::parse("https://hub.example.com:8443/base").unwrap();
        let same = Url::parse("https://hub.example.com:8443/workspace").unwrap();
        let other_port = Url::parse("https://hub.example.com/workspace").unwrap();
        let other_scheme = Url::parse("http://hub.example.com:8443/workspace").unwrap();
        require_same_hub_origin(&same, &hub).unwrap();
        assert!(require_same_hub_origin(&other_port, &hub).is_err());
        assert!(require_same_hub_origin(&other_scheme, &hub).is_err());
    }

    #[test]
    fn distributed_tunnel_cookie_header_keeps_only_nonempty_cookies() {
        assert_eq!(
            format_cookie_header(&[
                ("leapmux-session".into(), "token".into()),
                ("empty".into(), String::new()),
                ("preference".into(), "value".into()),
            ]),
            "leapmux-session=token"
        );
    }

    fn tunnel_info() -> proto::TunnelInfo {
        proto::TunnelInfo {
            id: "tunnel-a".into(),
            worker_id: "worker-a".into(),
            r#type: "port_forward".into(),
            bind_addr: "127.0.0.1".into(),
            bind_port: 3001,
            target_addr: "target".into(),
            target_port: 3000,
            key_mismatch: None,
        }
    }

    fn tunnel_mismatch() -> proto::TunnelWorkerKeyMismatch {
        proto::TunnelWorkerKeyMismatch {
            expected_fingerprint: "able-arch-bird-clay".into(),
            presented_fingerprint: "fern-golf-harp-iris".into(),
        }
    }

    fn tunnel_info_json() -> serde_json::Value {
        json!({
            "id": "tunnel-a",
            "workerId": "worker-a",
            "type": "port_forward",
            "bindAddr": "127.0.0.1",
            "bindPort": 3001,
            "targetAddr": "target",
            "targetPort": 3000
        })
    }

    fn tunnel_mismatch_json() -> serde_json::Value {
        json!({
            "expectedFingerprint": "able-arch-bird-clay",
            "presentedFingerprint": "fern-golf-harp-iris"
        })
    }

    #[test]
    fn create_tunnel_maps_the_sidecar_results_to_webview_outcomes() {
        for (result, json) in [
            (
                proto::create_tunnel_response::Result::Info(tunnel_info()),
                json!({"kind": "created", "info": tunnel_info_json()}),
            ),
            (
                proto::create_tunnel_response::Result::KeyMismatch(tunnel_mismatch()),
                json!({"kind": "workerKeyMismatch", "keyMismatch": tunnel_mismatch_json()}),
            ),
        ] {
            let result = create_tunnel_outcome(Some(proto::response::Result::CreateTunnel(
                proto::CreateTunnelResponse {
                    result: Some(result),
                },
            )))
            .expect("map the sidecar result");
            assert_eq!(
                serde_json::to_value(result).expect("serialize the webview outcome"),
                json
            );
        }
        assert!(
            create_tunnel_outcome(Some(proto::response::Result::CreateTunnel(
                proto::CreateTunnelResponse { result: None },
            )))
            .is_err()
        );
    }

    #[test]
    fn list_tunnels_maps_the_epoch_and_stopped_details() {
        let result = list_tunnels_result(Some(proto::response::Result::ListTunnels(
            proto::ListTunnelsResponse {
                channel_epoch: "epoch-a".into(),
                tunnels: vec![proto::TunnelInfo {
                    key_mismatch: Some(tunnel_mismatch()),
                    ..tunnel_info()
                }],
            },
        )))
        .expect("map the sidecar listing");
        let mut stopped = tunnel_info_json();
        stopped["keyMismatch"] = tunnel_mismatch_json();
        assert_eq!(
            serde_json::to_value(result).expect("serialize the webview listing"),
            json!({"channelEpoch": "epoch-a", "tunnels": [stopped]})
        );
        assert!(list_tunnels_result(None).is_err());
    }

    #[test]
    fn tunnel_change_events_use_the_generated_name_and_null_payload() {
        let (name, payload) = tunnels_changed_event_for_webview();
        assert_eq!(name, contracts_generated::EVENT_TUNNELS_CHANGED);
        assert_eq!(name, "tunnel:changed");
        assert_eq!(payload, serde_json::Value::Null);
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn restart_command_keeps_paths_out_of_shell_source() {
        for path in [
            "/Applications/LeapMux.app",
            "/Applications/LeapMux Desktop.app",
            "/Applications/$HOME.app",
            "/Applications/$(echo unwanted).app",
            "/Applications/`echo unwanted`.app",
            "/Applications/quotes\"'\\and\nnewlines.app",
            "",
        ] {
            let command = restart_command(42, Path::new(path));
            let args: Vec<_> = command.get_args().collect();
            assert_eq!(command.get_program(), OsStr::new("/bin/sh"));
            assert_eq!(
                args.len(),
                5,
                "the path must be a separate argument: {path:?}"
            );
            assert_eq!(args[0], "-c");
            assert_eq!(args[1], "while kill -0 \"$1\" 2>/dev/null; do sleep 0.1; done; exec /usr/bin/open -n \"$2\"");
            assert_eq!(args[2], "leapmux-restart");
            assert_eq!(args[3], "42");
            assert_eq!(args[4], OsStr::new(path));
        }
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn restart_command_preserves_non_utf8_paths() {
        use std::os::unix::ffi::OsStringExt;
        let path = PathBuf::from(std::ffi::OsString::from_vec(
            b"/Applications/LeapMux\xff.app".to_vec(),
        ));
        let command = restart_command(u32::MAX, &path);
        let args: Vec<_> = command.get_args().collect();
        assert_eq!(args.len(), 5);
        assert_eq!(args[3], u32::MAX.to_string().as_str());
        assert_eq!(args[4], path.as_os_str());
    }

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

    // `switch_mode` navigates to the launcher root of the URL the window
    // holds, so the route the user is on never survives the switch and the
    // origin -- app protocol or dev server -- needs no stored copy.
    #[test]
    fn launcher_base_url_strips_the_path_query_and_fragment() {
        let base = launcher_base_url(&Url::parse("http://tauri.localhost/app?x=1#f").expect("url"));
        assert_eq!(base.as_str(), "http://tauri.localhost/");

        let base = launcher_base_url(&Url::parse("tauri://localhost/settings").expect("url"));
        assert_eq!(base.as_str(), "tauri://localhost/");
    }

    #[test]
    fn launcher_url_on_the_launcher_base_carries_one_cleanup_message() {
        let base = launcher_base_url(&Url::parse("http://tauri.localhost/app?x=1#f").expect("url"));
        let (url, message) = launcher_url(base.as_str(), &["lease release failed".to_string()])
            .expect("valid launcher url");

        assert_eq!(message, "lease release failed");
        assert_eq!(
            url.as_str(),
            "http://tauri.localhost/?cleanup_error=lease+release+failed"
        );
    }

    // The transport is derivable from the platform -- every desktop mode
    // reaches the Hub through the shell's proxy -- so the capabilities carry
    // no transport field for the webview to read.
    #[test]
    fn capabilities_of_each_mode_carry_no_transport_field() {
        for (shell_mode, local_solo) in [
            (ShellMode::Launcher, true),
            (ShellMode::Solo, true),
            (ShellMode::Distributed, false),
        ] {
            let state = ShellState {
                shell_mode,
                connected: false,
                hub_url: String::new(),
            };
            let json =
                serde_json::to_value(capabilities_for(&state)).expect("serialize the capabilities");
            assert!(
                json.get("hubTransport").is_none(),
                "{shell_mode:?}: the transport must not be reported"
            );
            assert_eq!(
                json.get("localSolo"),
                Some(&serde_json::Value::Bool(local_solo)),
                "{shell_mode:?}: local_solo is true for solo and launcher only"
            );
        }
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

    // The one start command carries the proto enum's ordinal. Only the two
    // start kinds name a ceremony, so the unset value and a value this enum
    // does not know must fail here instead of reaching the sidecar.
    #[test]
    fn parse_browser_auth_kind_accepts_the_two_ceremony_kinds() {
        assert_eq!(
            parse_browser_auth_kind(proto::BrowserAuthKind::SignIn as i32),
            Ok(proto::BrowserAuthKind::SignIn)
        );
        assert_eq!(
            parse_browser_auth_kind(proto::BrowserAuthKind::StepUp as i32),
            Ok(proto::BrowserAuthKind::StepUp)
        );
    }

    #[test]
    fn parse_browser_auth_kind_rejects_an_unset_or_unknown_kind() {
        assert_eq!(
            parse_browser_auth_kind(proto::BrowserAuthKind::Unspecified as i32),
            Err("invalid browser auth kind: 0".to_string())
        );
        assert_eq!(
            parse_browser_auth_kind(7),
            Err("invalid browser auth kind: 7".to_string())
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
        assert_eq!(
            unique.len(),
            tokens.len(),
            "one setting, so all three differ"
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

    #[test]
    fn is_solo_connected_state_requires_solo_mode_and_connected() {
        assert!(!is_solo_connected_state(&ShellState {
            shell_mode: ShellMode::Launcher,
            connected: false,
            hub_url: String::new(),
        }));
        assert!(!is_solo_connected_state(&ShellState {
            shell_mode: ShellMode::Distributed,
            connected: true,
            hub_url: "https://remote.example.com".to_string(),
        }));
        assert!(!is_solo_connected_state(&ShellState {
            shell_mode: ShellMode::Solo,
            connected: false,
            hub_url: String::new(),
        }));
        assert!(is_solo_connected_state(&ShellState {
            shell_mode: ShellMode::Solo,
            connected: true,
            hub_url: String::new(),
        }));
    }

    #[test]
    fn busy_work_from_response_parses_matching_variant_and_fails_open() {
        let matching = busy_work_from_response(Ok(proto::Response {
            id: 1,
            error: String::new(),
            result: Some(proto::response::Result::InspectSoloBusyWork(
                proto::InspectSoloBusyWorkResponse {
                    agents: vec![proto::BusyAgentWork {
                        tab_id: "agent-1".to_string(),
                        title: "Refactor auth".to_string(),
                        active_background_tasks: vec!["Run tests".to_string()],
                        active_child_agents: vec![],
                    }],
                    terminals: vec![],
                },
            )),
        }));
        assert_eq!(matching.agents.len(), 1);
        assert_eq!(matching.agents[0].tab_id, "agent-1");

        assert!(busy_work_from_response(Ok(proto::Response {
            id: 2,
            error: String::new(),
            result: None,
        }))
        .is_empty());

        assert!(busy_work_from_response(Ok(proto::Response {
            id: 3,
            error: String::new(),
            result: Some(proto::response::Result::BoolValue(proto::BoolValue {
                value: true,
            })),
        }))
        .is_empty());

        assert!(busy_work_from_response(Err("timeout".to_string())).is_empty());
    }

    #[test]
    fn solo_busy_work_and_quit_confirm_payload_serialize_to_expected_camel_case_wire_shape() {
        let busy = quit_guard::SoloBusyWork {
            agents: vec![quit_guard::BusyAgentWork {
                tab_id: "agent-1".to_string(),
                title: "Refactor auth".to_string(),
                active_background_tasks: vec!["Run unit tests".to_string()],
                active_child_agents: vec!["Codebase reviewer".to_string()],
            }],
            terminals: vec![quit_guard::BusyTerminalWork {
                tab_id: "term-1".to_string(),
                title: "Dev server".to_string(),
                processes: vec![quit_guard::BusyTerminalProcess {
                    pid: 51234,
                    name: "node".to_string(),
                }],
                total_count: 2,
            }],
        };

        let busy_json = serde_json::to_value(&busy).expect("serialize SoloBusyWork");
        assert_eq!(
            busy_json,
            json!({
                "agents": [{
                    "tabId": "agent-1",
                    "title": "Refactor auth",
                    "activeBackgroundTasks": ["Run unit tests"],
                    "activeChildAgents": ["Codebase reviewer"],
                }],
                "terminals": [{
                    "tabId": "term-1",
                    "title": "Dev server",
                    "processes": [{ "pid": 51234, "name": "node" }],
                    "totalCount": 2,
                }],
            })
        );

        let payload = quit_guard::QuitConfirmPayload {
            prompt_id: 7,
            busy_work: busy.clone(),
        };
        let payload_json = serde_json::to_value(&payload).expect("serialize QuitConfirmPayload");
        assert_eq!(payload_json["promptId"], json!(7));
        assert_eq!(payload_json["busyWork"], busy_json);

        assert_eq!(
            serde_json::to_value(quit_guard::BeginSwitchModeResult::Proceed { switch_id: 3 })
                .expect("serialize Proceed"),
            json!({ "kind": "proceed", "switchId": 3 })
        );
        assert_eq!(
            serde_json::to_value(quit_guard::BeginSwitchModeResult::Confirm {
                prompt_id: 9,
                busy_work: busy,
            })
            .expect("serialize Confirm"),
            json!({ "kind": "confirm", "promptId": 9, "busyWork": busy_json })
        );
        assert_eq!(
            serde_json::to_value(quit_guard::BeginSwitchModeResult::Blocked)
                .expect("serialize Blocked"),
            json!({ "kind": "blocked" })
        );
    }

    fn sample_busy_work() -> quit_guard::SoloBusyWork {
        quit_guard::SoloBusyWork {
            agents: vec![quit_guard::BusyAgentWork {
                tab_id: "a1".to_string(),
                title: "Agent".to_string(),
                active_background_tasks: vec![],
                active_child_agents: vec![],
            }],
            terminals: vec![],
        }
    }

    #[test]
    fn plan_and_complete_guarded_exit_cover_all_branches_and_stale_tokens() {
        let guard = quit_guard::QuitGuard::default();

        // Non-Solo exits immediately; Solo starts a tokenized inspection.
        assert_eq!(
            plan_guarded_exit(false, &guard),
            GuardedExitAction::ExitImmediately
        );
        let GuardedExitAction::Inspect(inspect1) = plan_guarded_exit(true, &guard) else {
            panic!("expected Inspect(1)");
        };
        assert_eq!(plan_guarded_exit(true, &guard), GuardedExitAction::Ignore);

        // Stale inspection after switch_mode (cancel_all) must NOT exit or clobber a newer inspect.
        guard.cancel_all();
        assert_eq!(
            complete_guarded_inspect(inspect1, false, &guard, quit_guard::SoloBusyWork::default()),
            InspectOutcome::Stale
        );
        let GuardedExitAction::Inspect(inspect2) = plan_guarded_exit(true, &guard) else {
            panic!("expected Inspect(2)");
        };
        assert_eq!(
            complete_guarded_inspect(inspect1, true, &guard, quit_guard::SoloBusyWork::default()),
            InspectOutcome::Stale
        );
        assert!(
            guard.is_inspecting(inspect2),
            "stale inspection must not reset newer active inspection"
        );

        // Post-inspect: disconnected exits and resets guard.
        assert_eq!(
            complete_guarded_inspect(inspect2, false, &guard, sample_busy_work()),
            InspectOutcome::Exit
        );

        // Post-inspect: idle exits and resets guard.
        let GuardedExitAction::Inspect(inspect3) = plan_guarded_exit(true, &guard) else {
            panic!("expected Inspect(3)");
        };
        assert_eq!(
            complete_guarded_inspect(inspect3, true, &guard, quit_guard::SoloBusyWork::default()),
            InspectOutcome::Exit
        );

        // Post-inspect: busy returns Present and transitions to ShowExistingPrompt.
        let GuardedExitAction::Inspect(inspect4) = plan_guarded_exit(true, &guard) else {
            panic!("expected Inspect(4)");
        };
        let busy = sample_busy_work();
        assert_eq!(
            complete_guarded_inspect(inspect4, true, &guard, busy.clone()),
            InspectOutcome::Present(busy)
        );
        let _ = guard
            .transition_to_prompt(inspect4)
            .expect("busy inspect transitions to prompt");
        assert_eq!(
            plan_guarded_exit(true, &guard),
            GuardedExitAction::ShowExistingPrompt
        );

        // Quitting while an authorized switch_mode is in flight exits immediately
        // without double-prompting.
        guard.cancel_all();
        let quit_guard::SwitchInspectStart::Proceed(sw_token) = guard.begin_switch_inspect(false)
        else {
            panic!("expected switch proceed");
        };
        assert_eq!(
            plan_guarded_exit(true, &guard),
            GuardedExitAction::ExitImmediately
        );
        guard.finish_switch(sw_token);

        // Once Exiting is claimed, both plan_guarded_exit and stale/active completions ignore.
        guard.cancel_all();
        let GuardedExitAction::Inspect(inspect5) = plan_guarded_exit(true, &guard) else {
            panic!("expected Inspect(5)");
        };
        assert!(guard.claim_exit());
        assert_eq!(plan_guarded_exit(true, &guard), GuardedExitAction::Ignore);
        assert_eq!(
            complete_guarded_inspect(inspect5, true, &guard, quit_guard::SoloBusyWork::default()),
            InspectOutcome::Stale
        );
        assert_eq!(
            complete_guarded_inspect(inspect5, true, &guard, sample_busy_work()),
            InspectOutcome::Stale
        );
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn macos_inspect_dispatch_and_hold_guard_preserve_appkit_reply_order() {
        let events = std::cell::RefCell::new(Vec::new());
        let record_reply = |should_terminate: bool| {
            events.borrow_mut().push(if should_terminate {
                "reply_true"
            } else {
                "reply_false"
            });
        };

        // Stale outcome replies false without beginning exit or presenting.
        dispatch_macos_inspect_outcome(
            InspectOutcome::Stale,
            || {
                events.borrow_mut().push("begin_exit");
                true
            },
            record_reply,
            |_| events.borrow_mut().push("reply_false_then_present"),
        );
        assert_eq!(*events.borrow(), vec!["reply_false"]);

        // Exit where begin_exit succeeds lets finish callback send reply_true.
        events.borrow_mut().clear();
        dispatch_macos_inspect_outcome(
            InspectOutcome::Exit,
            || {
                events.borrow_mut().push("begin_exit_true");
                true
            },
            record_reply,
            |_| events.borrow_mut().push("reply_false_then_present"),
        );
        assert_eq!(*events.borrow(), vec!["begin_exit_true"]);

        // Exit where begin_exit returns false releases NSTerminateLater with false.
        events.borrow_mut().clear();
        dispatch_macos_inspect_outcome(
            InspectOutcome::Exit,
            || {
                events.borrow_mut().push("begin_exit_false");
                false
            },
            record_reply,
            |_| events.borrow_mut().push("reply_false_then_present"),
        );
        assert_eq!(*events.borrow(), vec!["begin_exit_false", "reply_false"]);

        // Present invokes reply_false_then_present so reply_false runs before present.
        events.borrow_mut().clear();
        dispatch_macos_inspect_outcome(
            InspectOutcome::Present(sample_busy_work()),
            || {
                events.borrow_mut().push("begin_exit");
                true
            },
            record_reply,
            |_| events.borrow_mut().push("reply_false_then_present"),
        );
        assert_eq!(*events.borrow(), vec!["reply_false_then_present"]);

        // MacTerminateHoldGuard invokes its cleanup on drop unless disarmed.
        let cleaned = std::cell::Cell::new(false);
        {
            let _guard = MacTerminateHoldGuard::new(|| cleaned.set(true));
        }
        assert!(
            cleaned.get(),
            "dropping an armed hold guard must run cleanup"
        );

        cleaned.set(false);
        {
            let mut guard = MacTerminateHoldGuard::new(|| cleaned.set(true));
            guard.disarm();
        }
        assert!(
            !cleaned.get(),
            "dropping a disarmed hold guard must not run cleanup"
        );
    }

    #[test]
    fn apply_quit_confirmation_resolution_runs_callback_only_on_matching_confirm() {
        let guard = quit_guard::QuitGuard::default();

        // Matching cancel does not call on_confirm.
        let quit_guard::InspectStart::Started(inspect1) = guard.begin_inspect() else {
            panic!("expected inspect1");
        };
        let (id1, _rx1) = guard.transition_to_prompt(inspect1).expect("prompt 1");
        assert!(guard.acknowledge_webview(id1));
        let mut confirmed = false;
        apply_quit_confirmation_resolution(&guard, id1, false, || confirmed = true);
        assert!(!confirmed);

        // Stale prompt ID does not call on_confirm.
        let quit_guard::InspectStart::Started(inspect2) = guard.begin_inspect() else {
            panic!("expected inspect2");
        };
        let (id2, _rx2) = guard.transition_to_prompt(inspect2).expect("prompt 2");
        assert!(guard.acknowledge_webview(id2));
        apply_quit_confirmation_resolution(&guard, id1, true, || confirmed = true);
        assert!(!confirmed);

        // Matching confirm calls on_confirm once.
        apply_quit_confirmation_resolution(&guard, id2, true, || confirmed = true);
        assert!(confirmed);
    }

    #[test]
    fn send_sidecar_request_removes_pending_entry_when_dropped_on_timeout() {
        let pending: PendingMap = Arc::new(Mutex::new(HashMap::new()));
        let (writer_tx, _writer_rx) = mpsc::unbounded_channel();
        let sidecar = SidecarProcess {
            _child: None,
            writer_tx,
            pending: pending.clone(),
            next_id: AtomicU64::new(1),
        };

        let timed_out = tauri::async_runtime::block_on(async {
            tokio::time::timeout(
                Duration::from_millis(10),
                send_sidecar_request(
                    &sidecar,
                    proto::request::Method::InspectSoloBusyWork(
                        proto::InspectSoloBusyWorkRequest {},
                    ),
                ),
            )
            .await
        });
        assert!(timed_out.is_err(), "request without reply must time out");
        assert!(
            pending.lock().unwrap().is_empty(),
            "dropping a timed-out send_sidecar_request future must remove its pending entry"
        );
    }
}
