//! The tray icon, the window-behaviour policy it governs, and the launch
//! decision that policy feeds.
//!
//! Five account preferences reach the shell here: whether a tray icon exists,
//! what a window close and a window minimize do while it does, whether the
//! operating system starts LeapMux at login, and whether that login launch
//! shows a window. The webview pushes the resolved values through
//! `set_desktop_behavior`; the sidecar caches four of them so the next launch
//! can decide before the webview exists.
//!
//! Everything that DECIDES lives in the pure functions below -- `window_action`,
//! `must_reveal_window`, `launch_visibility`, `is_autostart_launch` -- so the
//! policy is unit-testable without an `AppHandle` or a real window. The rest of
//! this module is the adapter that calls them.

use std::sync::atomic::{AtomicBool, AtomicU8, Ordering};
use std::sync::{Arc, Mutex};

use serde::{Deserialize, Serialize};
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager};

use crate::contracts_generated as contracts;
use crate::proto;

#[cfg(target_os = "linux")]
pub(crate) mod minimize_linux;
#[cfg(target_os = "macos")]
pub(crate) mod minimize_macos;
#[cfg(windows)]
pub(crate) mod minimize_windows;

/// The tray icon's id, and the ids of its two menu items. Single-language
/// names: nothing outside this crate reads them, so they are not contract
/// material the way the setting tokens are.
const TRAY_ID: &str = "main";
const TRAY_SHOW_MENU_ID: &str = "tray-show";
const TRAY_QUIT_MENU_ID: &str = "tray-quit";

/// The argument `tauri-plugin-autostart` records in the login-item entry, and
/// the one `is_autostart_launch` looks for.
pub(crate) const AUTOSTART_ARG: &str = "--autostart";

// --- The decision vocabulary ---

/// What the user asked LeapMux to do when a window closes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum TrayOnClose {
    Tray,
    Quit,
}

/// What the user asked LeapMux to do when a window is minimized.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum TrayOnMinimize {
    Tray,
    Taskbar,
}

/// Whether a login launch shows a window.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum StartMinimized {
    Window,
    Minimized,
}

/// Read one of these enums off the wire by matching the CONTRACT tokens.
///
/// Hand-written rather than `#[serde(rename_all = "lowercase")]`, because that
/// derives the accepted spelling from the Rust VARIANT NAME. The hub validates
/// and the webview parses against `contracts/desktop.json`, and a variant that
/// happens to lowercase into the same string agrees with them by coincidence
/// rather than by construction -- so renaming a token in the contract would
/// leave this shell silently refusing every value the other two now send.
/// Matching the generated constant makes the contract load-bearing here too.
///
/// A serde attribute cannot take a `const`, which is why this is a macro and
/// not one more `#[serde(rename = ...)]`.
macro_rules! deserialize_from_contract_tokens {
    ($ty:ty, $( $token:path => $variant:expr ),+ $(,)?) => {
        impl<'de> Deserialize<'de> for $ty {
            fn deserialize<D: serde::Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
                // `Cow`, not `&str`: a borrowed string only works when the
                // deserializer owns the buffer, which rules out every
                // `from_value` caller -- including the tests.
                let token = <std::borrow::Cow<'de, str>>::deserialize(d)?;
                match token.as_ref() {
                    $( $token => Ok($variant), )+
                    other => Err(serde::de::Error::unknown_variant(other, &[$( $token ),+])),
                }
            }
        }
    };
}

deserialize_from_contract_tokens!(
    TrayOnClose,
    contracts::TRAY_ON_CLOSE_TRAY => TrayOnClose::Tray,
    contracts::TRAY_ON_CLOSE_QUIT => TrayOnClose::Quit,
);

deserialize_from_contract_tokens!(
    TrayOnMinimize,
    contracts::TRAY_ON_MINIMIZE_TRAY => TrayOnMinimize::Tray,
    contracts::TRAY_ON_MINIMIZE_TASKBAR => TrayOnMinimize::Taskbar,
);

deserialize_from_contract_tokens!(
    StartMinimized,
    contracts::START_MINIMIZED_WINDOW => StartMinimized::Window,
    contracts::START_MINIMIZED_MINIMIZED => StartMinimized::Minimized,
);

/// Which window event the policy is answering for.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum WindowIntent {
    CloseRequested,
    Minimized,
}

/// What the shell does about that event.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum WindowAction {
    /// Hide the window; the tray icon is how the user gets it back.
    HideWindow,
    /// Let the close proceed into the ordinary quit path.
    ProceedWithClose,
    /// Leave the window minimized on the taskbar or in the Dock.
    LeaveMinimized,
}

/// The state the main window starts in.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum LaunchVisibility {
    Normal,
    Minimized,
    Hidden,
}

impl LaunchVisibility {
    /// The wire spelling `get_startup_info` reports to the webview, which uses
    /// it to decide whether to show the window after sizing it.
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            Self::Normal => "normal",
            Self::Minimized => "minimized",
            Self::Hidden => "hidden",
        }
    }
}

/// The resolved policy, as the webview last reported it.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct DesktopBehavior {
    pub tray_enabled: bool,
    pub tray_on_close: TrayOnClose,
    pub tray_on_minimize: TrayOnMinimize,
    pub start_on_login: bool,
    pub start_minimized: StartMinimized,
}

impl Default for DesktopBehavior {
    /// The built-in defaults, which are also what a fresh config decodes to.
    fn default() -> Self {
        Self {
            tray_enabled: false,
            tray_on_close: TrayOnClose::Tray,
            tray_on_minimize: TrayOnMinimize::Taskbar,
            start_on_login: false,
            start_minimized: StartMinimized::Window,
        }
    }
}

/// The payload field a refusal belongs to. See `BehaviorRefusal`.
pub(crate) const REFUSAL_TRAY: &str = "trayEnabled";
pub(crate) const REFUSAL_START_ON_LOGIN: &str = "startOnLogin";

/// Something the operating system refused, and which choice it belongs to.
///
/// Two of the five settings can fail outside the application: a Linux desktop
/// with no status-icon library cannot show a tray, and an operating system can
/// refuse a login item. Both look like "LeapMux ignores my settings" when they
/// stay in a log, so the command reports them and the Preferences row prints
/// the message beside the control the user just moved.
///
/// `setting` is the PAYLOAD FIELD NAME of the choice that failed, so the
/// webview can find that row without a second vocabulary for the same five
/// things. `a_refusal_names_a_field_of_the_payload_it_answers` pins the two
/// together.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub(crate) struct BehaviorRefusal {
    pub setting: &'static str,
    pub message: String,
}

impl BehaviorRefusal {
    pub(crate) fn tray(message: String) -> Self {
        Self {
            setting: REFUSAL_TRAY,
            message,
        }
    }

    pub(crate) fn start_on_login(message: String) -> Self {
        Self {
            setting: REFUSAL_START_ON_LOGIN,
            message,
        }
    }
}

// --- The pure policy ---

/// What to do about `intent`, given the tray's EFFECTIVE state.
///
/// `tray_enabled` here is whether an icon actually exists, not what the user
/// asked for. Without a tray there is no way back from a hidden window, so
/// every branch that would hide falls through to the ordinary behaviour. That
/// rule is written once, here, and both callers read it.
pub(crate) fn window_action(
    tray_enabled: bool,
    on_close: TrayOnClose,
    on_minimize: TrayOnMinimize,
    intent: WindowIntent,
) -> WindowAction {
    match intent {
        WindowIntent::CloseRequested => {
            if tray_enabled && on_close == TrayOnClose::Tray {
                WindowAction::HideWindow
            } else {
                WindowAction::ProceedWithClose
            }
        }
        WindowIntent::Minimized => {
            if tray_enabled && on_minimize == TrayOnMinimize::Tray {
                WindowAction::HideWindow
            } else {
                WindowAction::LeaveMinimized
            }
        }
    }
}

/// Whether the shell must bring the main window back right now.
///
/// A PREDICATE over the current state, deliberately, and not a
/// "was enabled, now disabled" transition. Stated this way it also repairs a
/// tray that failed to build after a successful hide, a window left hidden by
/// a mode switch, and a first push that disables a tray the cached config had
/// enabled at launch. A transition test would miss all three.
pub(crate) fn must_reveal_window(tray_enabled: bool, window_visible: bool) -> bool {
    !tray_enabled && !window_visible
}

/// The state the main window starts in.
///
/// `start_minimized` governs the LOGIN launch alone: starting LeapMux by hand
/// always opens a window, which is what every comparable application does.
/// Without a tray, "minimized" means the taskbar or the Dock, because hiding
/// with no icon to restore from would strand the user.
pub(crate) fn launch_visibility(
    autostart_launch: bool,
    start_minimized: StartMinimized,
    tray_enabled: bool,
) -> LaunchVisibility {
    if !autostart_launch || start_minimized == StartMinimized::Window {
        return LaunchVisibility::Normal;
    }
    if tray_enabled {
        LaunchVisibility::Hidden
    } else {
        LaunchVisibility::Minimized
    }
}

/// Whether this process was started by the operating system's login item.
///
/// A WHOLE-argument match. A prefix or a `contains` would also accept
/// `--autostartle` from an unrelated tool and `--autostart=1` from a user who
/// guessed the syntax, and either would silently hide a hand-started window.
pub(crate) fn is_autostart_launch<I: IntoIterator<Item = String>>(args: I) -> bool {
    args.into_iter().any(|arg| arg == AUTOSTART_ARG)
}

// --- Token and proto bridges ---

impl TrayOnClose {
    pub(crate) fn from_proto(v: proto::TrayOnClose) -> Self {
        match v {
            proto::TrayOnClose::Quit => Self::Quit,
            // UNSPECIFIED is a fresh config, which means the built-in default.
            _ => Self::Tray,
        }
    }

    pub(crate) fn to_proto(self) -> proto::TrayOnClose {
        match self {
            Self::Quit => proto::TrayOnClose::Quit,
            Self::Tray => proto::TrayOnClose::Tray,
        }
    }
}

impl TrayOnMinimize {
    pub(crate) fn from_proto(v: proto::TrayOnMinimize) -> Self {
        match v {
            proto::TrayOnMinimize::Tray => Self::Tray,
            _ => Self::Taskbar,
        }
    }

    pub(crate) fn to_proto(self) -> proto::TrayOnMinimize {
        match self {
            Self::Tray => proto::TrayOnMinimize::Tray,
            Self::Taskbar => proto::TrayOnMinimize::Taskbar,
        }
    }
}

impl StartMinimized {
    pub(crate) fn from_proto(v: proto::StartMinimized) -> Self {
        match v {
            proto::StartMinimized::Minimized => Self::Minimized,
            _ => Self::Window,
        }
    }

    pub(crate) fn to_proto(self) -> proto::StartMinimized {
        match self {
            Self::Minimized => proto::StartMinimized::Minimized,
            Self::Window => proto::StartMinimized::Window,
        }
    }
}

impl DesktopBehavior {
    /// The four values the sidecar caches. `start_on_login` is absent because
    /// the operating system's registration is that setting's state.
    pub(crate) fn from_config(cfg: &proto::DesktopConfig) -> Self {
        Self {
            tray_enabled: cfg.tray_enabled,
            tray_on_close: TrayOnClose::from_proto(cfg.tray_on_close()),
            tray_on_minimize: TrayOnMinimize::from_proto(cfg.tray_on_minimize()),
            start_on_login: false,
            start_minimized: StartMinimized::from_proto(cfg.start_minimized()),
        }
    }

    /// Whether the two behaviours differ in anything the sidecar stores, so an
    /// unchanged push skips the write.
    pub(crate) fn cache_differs(&self, other: &Self) -> bool {
        self.tray_enabled != other.tray_enabled
            || self.tray_on_close != other.tray_on_close
            || self.tray_on_minimize != other.tray_on_minimize
            || self.start_minimized != other.start_minimized
    }
}

// --- The live state ---

/// The launch decision, readable once by the webview and thereafter inert.
pub(crate) struct LaunchState {
    visibility: LaunchVisibility,
    consumed: AtomicBool,
}

impl LaunchState {
    pub(crate) fn new(visibility: LaunchVisibility) -> Self {
        Self {
            visibility,
            consumed: AtomicBool::new(false),
        }
    }

    /// The decision, ONCE. `switch_mode` navigates back to the launcher, which
    /// remounts and asks again; without the latch that second answer would
    /// re-hide the window the user is looking at.
    pub(crate) fn take(&self) -> LaunchVisibility {
        if self.consumed.swap(true, Ordering::SeqCst) {
            LaunchVisibility::Normal
        } else {
            self.visibility
        }
    }

    /// The decision without consuming it, for the startup safety net.
    ///
    /// The RAW decision, unlike `take`. The safety net asks what THIS launch
    /// decided, and it must get the same answer whether or not the webview
    /// already read it -- the webview reads it within milliseconds, so a
    /// `Normal` answer once consumed would make the net reveal, five seconds
    /// in, the window a hidden login launch deliberately left in the tray.
    pub(crate) fn peek(&self) -> LaunchVisibility {
        self.visibility
    }
}

/// The tray icon and the policy that reads it.
///
/// The three policy fields are ATOMICS rather than one `Mutex<..>`, and that is
/// a deadlock fix rather than an optimisation. Disabling the tray must reveal a
/// hidden window, and on Linux `window.show()` runs INLINE on the main thread,
/// maps the GTK window, and re-enters our own `window-state-event` handler. A
/// `std::sync::Mutex` held across that call is a same-thread double lock. The
/// cost is a torn read: a close could see a fresh `enabled` beside a stale
/// `on_close`. Both values were valid moments earlier and only change when a
/// person edits a preference, so the worst case is one close obeying the
/// previous choice.
pub(crate) struct TrayState {
    /// Whether an icon actually EXISTS, which is not the same as what the user
    /// asked for: the build fails on a Linux desktop with no indicator library.
    /// The policy reads this, so a tray that could not be created can never
    /// hide a window.
    enabled: AtomicBool,
    on_close: AtomicU8,
    on_minimize: AtomicU8,
    /// Built at most once and never dropped; visibility is toggled instead.
    icon: Mutex<Option<TrayIcon<tauri::Wry>>>,
    /// What the sidecar was last told, so an unchanged push writes nothing.
    cached: Mutex<Option<DesktopBehavior>>,
}

/// `AtomicU8` stand-ins for the two policy enums, so the live policy can be
/// read without a lock. Private to this module: `close_code` / `minimize_code`
/// and the two accessors on `TrayState` are the only conversions, so the codes
/// never escape into a signature.
const CLOSE_TRAY: u8 = 0;
const CLOSE_QUIT: u8 = 1;
const MINIMIZE_TASKBAR: u8 = 0;
const MINIMIZE_TRAY: u8 = 1;

impl TrayState {
    pub(crate) fn new() -> Self {
        let defaults = DesktopBehavior::default();
        Self {
            enabled: AtomicBool::new(false),
            on_close: AtomicU8::new(close_code(defaults.tray_on_close)),
            on_minimize: AtomicU8::new(minimize_code(defaults.tray_on_minimize)),
            icon: Mutex::new(None),
            cached: Mutex::new(None),
        }
    }

    /// Whether an icon exists right now.
    pub(crate) fn is_enabled(&self) -> bool {
        self.enabled.load(Ordering::SeqCst)
    }

    fn on_close(&self) -> TrayOnClose {
        if self.on_close.load(Ordering::SeqCst) == CLOSE_QUIT {
            TrayOnClose::Quit
        } else {
            TrayOnClose::Tray
        }
    }

    fn on_minimize(&self) -> TrayOnMinimize {
        if self.on_minimize.load(Ordering::SeqCst) == MINIMIZE_TRAY {
            TrayOnMinimize::Tray
        } else {
            TrayOnMinimize::Taskbar
        }
    }

    /// The policy answer for `intent`, read from the live state.
    pub(crate) fn window_action(&self, intent: WindowIntent) -> WindowAction {
        window_action(
            self.is_enabled(),
            self.on_close(),
            self.on_minimize(),
            intent,
        )
    }

    fn store_policy(&self, behavior: &DesktopBehavior) {
        self.on_close
            .store(close_code(behavior.tray_on_close), Ordering::SeqCst);
        self.on_minimize
            .store(minimize_code(behavior.tray_on_minimize), Ordering::SeqCst);
    }

    /// Bring the tray into the requested state, and record what was achieved.
    ///
    /// Returns the error the caller reports to the webview. `enabled` records
    /// the EFFECTIVE result either way, so a failure downgrades the policy
    /// rather than leaving it lying.
    pub(crate) fn apply(&self, app: &AppHandle, behavior: &DesktopBehavior) -> Result<(), String> {
        self.store_policy(behavior);
        if !behavior.tray_enabled {
            self.enabled.store(false, Ordering::SeqCst);
            if let Some(icon) = self.icon.lock().ok().and_then(|guard| guard.clone()) {
                let _ = icon.set_visible(false);
            }
            return Ok(());
        }
        match self.ensure_icon(app) {
            Ok(()) => {
                self.enabled.store(true, Ordering::SeqCst);
                Ok(())
            }
            Err(err) => {
                self.enabled.store(false, Ordering::SeqCst);
                Err(err)
            }
        }
    }

    /// Build the icon on first use, then show it.
    ///
    /// The `TrayIcon` is never dropped and never removed by id. Tauri's
    /// `remove_tray_by_id` resolves the id to the first matching entry and
    /// never prunes its vector, so a rebuild under the same id leaves the old
    /// icon alive; and `TrayIconBuilder::on_menu_event` appends to an app-wide
    /// listener list on every build. Toggling visibility has neither problem.
    fn ensure_icon(&self, app: &AppHandle) -> Result<(), String> {
        let mut guard = self
            .icon
            .lock()
            .map_err(|_| "tray icon state is poisoned".to_string())?;
        if let Some(icon) = guard.as_ref() {
            icon.set_visible(true)
                .map_err(|err| format!("show the tray icon: {err}"))?;
            return Ok(());
        }
        let icon = build_tray(app)?;
        icon.set_visible(true)
            .map_err(|err| format!("show the tray icon: {err}"))?;
        *guard = Some(icon);
        Ok(())
    }

    /// Whether the sidecar needs a fresh cache write.
    ///
    /// A pure question. Recording is `record_cache`, and the two are separate
    /// so that only a write that SUCCEEDED moves the mirror.
    pub(crate) fn cache_needs_write(&self, behavior: &DesktopBehavior) -> bool {
        let Ok(guard) = self.cached.lock() else {
            // A poisoned lock must not silently stop the cache from
            // converging; write, and let the next push try again.
            return true;
        };
        guard.as_ref().is_none_or(|prev| prev.cache_differs(behavior))
    }

    /// Record what the sidecar now holds.
    ///
    /// Two callers. At launch it seeds the mirror from the config the shell
    /// just read, so the first push does not rewrite values already on disk.
    /// After a push it runs only once the write SUCCEEDED -- recording before
    /// the write would let one failed RPC leave the cache stale for as long as
    /// the user does not change the values again.
    pub(crate) fn record_cache(&self, behavior: &DesktopBehavior) {
        if let Ok(mut guard) = self.cached.lock() {
            *guard = Some(*behavior);
        }
    }
}

fn close_code(v: TrayOnClose) -> u8 {
    match v {
        TrayOnClose::Quit => CLOSE_QUIT,
        TrayOnClose::Tray => CLOSE_TRAY,
    }
}

fn minimize_code(v: TrayOnMinimize) -> u8 {
    match v {
        TrayOnMinimize::Tray => MINIMIZE_TRAY,
        TrayOnMinimize::Taskbar => MINIMIZE_TASKBAR,
    }
}

// --- The icon ---

/// The tray image. macOS needs a monochrome TEMPLATE, which it recolours to
/// match the menu bar; the other two draw the app's own colours.
#[cfg(target_os = "macos")]
fn tray_image() -> tauri::image::Image<'static> {
    tauri::include_image!("icons/tray-template.png")
}

#[cfg(not(target_os = "macos"))]
fn tray_image() -> tauri::image::Image<'static> {
    tauri::include_image!("icons/tray.png")
}

fn build_tray(app: &AppHandle) -> Result<TrayIcon<tauri::Wry>, String> {
    // Linux resolves the indicator library lazily and PANICS when it is
    // missing, inside a GTK callback. Check first, so an absent library is an
    // error the user reads instead of an abort.
    #[cfg(target_os = "linux")]
    if !minimize_linux::appindicator_available() {
        return Err(
            "LeapMux could not create a tray icon on this desktop. \
             Install libayatana-appindicator3 (or an equivalent status-icon \
             library) and turn the tray icon on again."
                .to_string(),
        );
    }

    let show = MenuItem::with_id(app, TRAY_SHOW_MENU_ID, "Show LeapMux", true, None::<&str>)
        .map_err(|err| format!("build the tray menu: {err}"))?;
    let quit = MenuItem::with_id(app, TRAY_QUIT_MENU_ID, "Quit LeapMux", true, None::<&str>)
        .map_err(|err| format!("build the tray menu: {err}"))?;
    let separator =
        PredefinedMenuItem::separator(app).map_err(|err| format!("build the tray menu: {err}"))?;
    let menu = Menu::with_items(app, &[&show, &separator, &quit])
        .map_err(|err| format!("build the tray menu: {err}"))?;

    TrayIconBuilder::with_id(TRAY_ID)
        .icon(tray_image())
        // macOS recolours a template image to match the menu bar; elsewhere
        // this is a no-op.
        .icon_as_template(cfg!(target_os = "macos"))
        .tooltip("LeapMux")
        .menu(&menu)
        // macOS opens the menu on a left click, which is the platform
        // convention. Windows and Linux keep left-click for "show the window"
        // and open the menu on a right click.
        .show_menu_on_left_click(cfg!(target_os = "macos"))
        .on_menu_event(|app, event| {
            if event.id() == TRAY_SHOW_MENU_ID {
                show_main_window(app);
            } else if event.id() == TRAY_QUIT_MENU_ID {
                crate::request_app_exit(app);
            }
        })
        .on_tray_icon_event(|tray, event| {
            // A left click with the button released, so a press that turns
            // into a drag does not raise the window.
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                show_main_window(tray.app_handle());
            }
        })
        .build(app)
        .map_err(|err| format!("create the tray icon: {err}"))
}

// --- Window helpers ---

/// Bring the main window back from every hidden or minimized combination.
///
/// The order is load-bearing on all three platforms: `set_focus` is a no-op
/// while the window is still minimized (macOS, Linux) or not yet visible
/// (Windows), so it has to come last.
///
/// `run_on_main_thread` because Linux needs GTK calls and neither the
/// single-instance callback nor a tray event is guaranteed to arrive on the
/// main thread. It runs the closure inline when already there, so this costs
/// nothing on the common path.
pub(crate) fn show_main_window(app: &AppHandle) {
    let handle = app.clone();
    let _ = app.run_on_main_thread(move || {
        let Some(window) = handle.get_webview_window("main") else {
            return;
        };
        let _ = window.unminimize();
        let _ = window.show();
        let _ = window.set_focus();
        // tao clears its Linux `minimized` flag only when GTK emits a state
        // event, which cannot happen while the window is unmapped -- so the
        // calls above leave it believing the window is still minimized and
        // `set_focus` silently does nothing. `present()` deiconifies, maps,
        // raises and focuses in one operation the window manager honours.
        #[cfg(target_os = "linux")]
        minimize_linux::present(&window);
    });
}

/// Apply `visibility` to the main window at launch.
///
/// The shell owns this rather than the webview, because the webview does not
/// exist on the distributed-reattach route: the shell navigates straight to the
/// hub and `LauncherView` never mounts.
pub(crate) fn apply_launch_visibility(app: &AppHandle, visibility: LaunchVisibility) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    match visibility {
        LaunchVisibility::Hidden => {}
        LaunchVisibility::Minimized => {
            let _ = window.show();
            let _ = window.minimize();
        }
        LaunchVisibility::Normal => {
            let _ = window.show();
        }
    }
}

/// The policy answer for a minimize, and the hide it may call for.
///
/// The three platform hooks funnel here so the decision is made once.
pub(crate) fn handle_minimize(state: &TrayState, window: &tauri::WebviewWindow) {
    if state.window_action(WindowIntent::Minimized) != WindowAction::HideWindow {
        return;
    }
    // macOS must leave the miniaturized state before it leaves the screen; see
    // `minimize_macos::prepare_hide`.
    #[cfg(target_os = "macos")]
    minimize_macos::prepare_hide(window);
    let _ = window.hide();
}

/// Install the platform's minimize hook on the main window.
pub(crate) fn install_minimize_hook(window: &tauri::WebviewWindow, state: Arc<TrayState>) {
    #[cfg(target_os = "linux")]
    minimize_linux::install(window, state);
    #[cfg(target_os = "macos")]
    minimize_macos::install(window, state);
    // Windows needs no hook of its own: tao reports a minimize as a `Resized`
    // event, which `main.rs` already receives. See `minimize_windows`.
    #[cfg(windows)]
    {
        let _ = (window, state);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn actions(tray: bool, on_close: TrayOnClose, on_minimize: TrayOnMinimize) -> (WindowAction, WindowAction) {
        (
            window_action(tray, on_close, on_minimize, WindowIntent::CloseRequested),
            window_action(tray, on_close, on_minimize, WindowIntent::Minimized),
        )
    }

    #[test]
    fn close_hides_to_tray_when_enabled_and_configured_for_tray() {
        let (close, _) = actions(true, TrayOnClose::Tray, TrayOnMinimize::Taskbar);
        assert_eq!(close, WindowAction::HideWindow);
    }

    #[test]
    fn close_quits_when_on_close_is_quit() {
        let (close, _) = actions(true, TrayOnClose::Quit, TrayOnMinimize::Tray);
        assert_eq!(close, WindowAction::ProceedWithClose);
    }

    // The safety property the whole module rests on: without an icon there is
    // no way back from a hidden window, so no choice may hide one.
    #[test]
    fn close_quits_when_the_tray_is_disabled_whatever_on_close_says() {
        for on_close in [TrayOnClose::Tray, TrayOnClose::Quit] {
            let (close, _) = actions(false, on_close, TrayOnMinimize::Tray);
            assert_eq!(
                close,
                WindowAction::ProceedWithClose,
                "on_close {on_close:?} must not hide without a tray"
            );
        }
    }

    #[test]
    fn minimize_hides_to_tray_when_enabled_and_configured_for_tray() {
        let (_, minimize) = actions(true, TrayOnClose::Quit, TrayOnMinimize::Tray);
        assert_eq!(minimize, WindowAction::HideWindow);
    }

    #[test]
    fn minimize_stays_on_the_taskbar_when_configured_for_taskbar() {
        let (_, minimize) = actions(true, TrayOnClose::Tray, TrayOnMinimize::Taskbar);
        assert_eq!(minimize, WindowAction::LeaveMinimized);
    }

    #[test]
    fn minimize_stays_on_the_taskbar_when_the_tray_is_disabled() {
        let (_, minimize) = actions(false, TrayOnClose::Tray, TrayOnMinimize::Tray);
        assert_eq!(minimize, WindowAction::LeaveMinimized);
    }

    #[test]
    fn disabling_the_tray_reveals_a_hidden_window() {
        assert!(must_reveal_window(false, false));
    }

    #[test]
    fn disabling_the_tray_leaves_a_visible_window_alone() {
        assert!(!must_reveal_window(false, true));
    }

    #[test]
    fn an_enabled_tray_never_forces_the_window_open() {
        assert!(!must_reveal_window(true, true));
        assert!(!must_reveal_window(true, false));
    }

    #[test]
    fn launch_is_normal_for_a_hand_launch_even_when_start_minimized_is_set() {
        assert_eq!(
            launch_visibility(false, StartMinimized::Minimized, true),
            LaunchVisibility::Normal
        );
    }

    #[test]
    fn login_launch_starts_hidden_when_the_tray_is_enabled() {
        assert_eq!(
            launch_visibility(true, StartMinimized::Minimized, true),
            LaunchVisibility::Hidden
        );
    }

    #[test]
    fn login_launch_starts_minimized_when_the_tray_is_disabled() {
        assert_eq!(
            launch_visibility(true, StartMinimized::Minimized, false),
            LaunchVisibility::Minimized
        );
    }

    #[test]
    fn login_launch_is_normal_when_start_minimized_is_window() {
        assert_eq!(
            launch_visibility(true, StartMinimized::Window, true),
            LaunchVisibility::Normal
        );
    }

    #[test]
    fn autostart_flag_is_matched_as_a_whole_argument() {
        let args = |extra: &str| vec!["leapmux-desktop".to_string(), extra.to_string()];
        assert!(is_autostart_launch(args("--autostart")));
        for near_miss in ["--autostart=1", "--autostartle", "-autostart", "autostart"] {
            assert!(
                !is_autostart_launch(args(near_miss)),
                "{near_miss} must not be read as the login-launch flag"
            );
        }
        assert!(!is_autostart_launch(vec!["leapmux-desktop".to_string()]));
    }

    #[test]
    fn behavior_tokens_round_trip_through_the_proto_enums() {
        for v in [TrayOnClose::Tray, TrayOnClose::Quit] {
            assert_eq!(TrayOnClose::from_proto(v.to_proto()), v);
        }
        for v in [TrayOnMinimize::Tray, TrayOnMinimize::Taskbar] {
            assert_eq!(TrayOnMinimize::from_proto(v.to_proto()), v);
        }
        for v in [StartMinimized::Window, StartMinimized::Minimized] {
            assert_eq!(StartMinimized::from_proto(v.to_proto()), v);
        }
    }

    // Every token the contract declares must reach its variant through the
    // REAL deserialize path. The hub validates against these same strings, so
    // one this shell refuses is a preference the user can set and nothing
    // obeys.
    #[test]
    fn every_contract_token_deserializes_to_its_variant() {
        let parse_close = |token: &str| {
            serde_json::from_value::<TrayOnClose>(serde_json::Value::String(token.to_string()))
        };
        assert_eq!(
            parse_close(contracts::TRAY_ON_CLOSE_TRAY).unwrap(),
            TrayOnClose::Tray
        );
        assert_eq!(
            parse_close(contracts::TRAY_ON_CLOSE_QUIT).unwrap(),
            TrayOnClose::Quit
        );

        let parse_minimize = |token: &str| {
            serde_json::from_value::<TrayOnMinimize>(serde_json::Value::String(token.to_string()))
        };
        assert_eq!(
            parse_minimize(contracts::TRAY_ON_MINIMIZE_TRAY).unwrap(),
            TrayOnMinimize::Tray
        );
        assert_eq!(
            parse_minimize(contracts::TRAY_ON_MINIMIZE_TASKBAR).unwrap(),
            TrayOnMinimize::Taskbar
        );

        let parse_start = |token: &str| {
            serde_json::from_value::<StartMinimized>(serde_json::Value::String(token.to_string()))
        };
        assert_eq!(
            parse_start(contracts::START_MINIMIZED_WINDOW).unwrap(),
            StartMinimized::Window
        );
        assert_eq!(
            parse_start(contracts::START_MINIMIZED_MINIMIZED).unwrap(),
            StartMinimized::Minimized
        );
    }

    #[test]
    fn unknown_behavior_tokens_fall_back_to_the_documented_default() {
        assert_eq!(
            TrayOnClose::from_proto(proto::TrayOnClose::Unspecified),
            TrayOnClose::Tray
        );
        assert_eq!(
            TrayOnMinimize::from_proto(proto::TrayOnMinimize::Unspecified),
            TrayOnMinimize::Taskbar
        );
        assert_eq!(
            StartMinimized::from_proto(proto::StartMinimized::Unspecified),
            StartMinimized::Window
        );
    }

    // A fresh config must decode to the built-in defaults, or a first launch
    // would create a tray nobody asked for.
    #[test]
    fn a_fresh_config_reads_as_the_built_in_defaults() {
        let cfg = proto::DesktopConfig::default();
        let behavior = DesktopBehavior::from_config(&cfg);
        assert!(!behavior.tray_enabled);
        assert_eq!(behavior.tray_on_close, TrayOnClose::Tray);
        assert_eq!(behavior.tray_on_minimize, TrayOnMinimize::Taskbar);
        assert_eq!(behavior.start_minimized, StartMinimized::Window);
        assert_eq!(
            launch_visibility(true, behavior.start_minimized, behavior.tray_enabled),
            LaunchVisibility::Normal
        );
    }

    #[test]
    fn cache_comparison_ignores_the_login_item_and_sees_every_stored_field() {
        let base = DesktopBehavior::default();
        // start_on_login is not cached: the OS registration is its state.
        let mut login = base;
        login.start_on_login = true;
        assert!(!base.cache_differs(&login));

        for mutate in [
            (|b: &mut DesktopBehavior| b.tray_enabled = true) as fn(&mut DesktopBehavior),
            |b: &mut DesktopBehavior| b.tray_on_close = TrayOnClose::Quit,
            |b: &mut DesktopBehavior| b.tray_on_minimize = TrayOnMinimize::Tray,
            |b: &mut DesktopBehavior| b.start_minimized = StartMinimized::Minimized,
        ] {
            let mut changed = base;
            mutate(&mut changed);
            assert!(base.cache_differs(&changed), "a stored field must be seen");
        }
    }

    #[test]
    fn the_launch_decision_is_reported_once() {
        let state = LaunchState::new(LaunchVisibility::Hidden);
        assert_eq!(state.peek(), LaunchVisibility::Hidden);
        assert_eq!(state.take(), LaunchVisibility::Hidden);
        // A mode switch remounts the launcher, which asks again. The second
        // answer must not re-hide the window the user is looking at.
        assert_eq!(state.take(), LaunchVisibility::Normal);
    }

    // The safety net asks what THIS launch decided, five seconds in, and the
    // webview consumes the decision within milliseconds. A `peek` that
    // answered `Normal` once consumed would therefore make the net reveal the
    // window that a hidden login launch deliberately left in the tray.
    #[test]
    fn peeking_still_reports_a_consumed_decision() {
        let state = LaunchState::new(LaunchVisibility::Hidden);
        assert_eq!(state.take(), LaunchVisibility::Hidden);
        assert_eq!(state.peek(), LaunchVisibility::Hidden);
    }

    // A cache write that FAILED must be retried, not skipped for as long as
    // the user leaves the values alone -- the next launch reads that cache
    // before the webview exists, so a stale copy decides the tray and the
    // window state for a whole session.
    #[test]
    fn the_cache_mirror_moves_only_when_a_write_is_recorded() {
        let state = TrayState::new();
        let behavior = DesktopBehavior::default();

        assert!(state.cache_needs_write(&behavior), "nothing is recorded yet");
        assert!(
            state.cache_needs_write(&behavior),
            "asking must not record: an unrecorded write is a failed one"
        );

        state.record_cache(&behavior);
        assert!(!state.cache_needs_write(&behavior));

        let mut changed = behavior;
        changed.tray_enabled = true;
        assert!(state.cache_needs_write(&changed));
    }

    // A poisoned mirror must make the cache CONVERGE, not stall. The failing
    // direction here is one extra RPC per push; the other direction leaves the
    // next launch deciding the tray and the window state from a stale copy,
    // with nothing to repair it for the rest of the install's life.
    #[test]
    fn a_poisoned_cache_mirror_still_writes() {
        let state = Arc::new(TrayState::new());
        let behavior = DesktopBehavior::default();
        state.record_cache(&behavior);
        assert!(!state.cache_needs_write(&behavior));

        let poisoner = state.clone();
        let _ = std::thread::spawn(move || {
            let _guard = poisoner.cached.lock().expect("a fresh mutex is not poisoned");
            panic!("poison the mirror");
        })
        .join();

        assert!(state.cached.is_poisoned());
        assert!(state.cache_needs_write(&behavior));
    }

    #[test]
    fn launch_visibility_wire_tokens_are_distinct() {
        let tokens = [
            LaunchVisibility::Normal.as_str(),
            LaunchVisibility::Minimized.as_str(),
            LaunchVisibility::Hidden.as_str(),
        ];
        let unique: std::collections::HashSet<_> = tokens.iter().collect();
        assert_eq!(unique.len(), tokens.len());
    }

    // The payload crosses the Tauri boundary as JSON, and its field names are
    // not contract material -- so a one-sided rename in platformBridge.ts
    // would otherwise surface only as a preference that silently does nothing.
    #[test]
    fn set_desktop_behavior_payload_deserializes() {
        let payload = serde_json::json!({
            "trayEnabled": true,
            "trayOnClose": "quit",
            "trayOnMinimize": "tray",
            "startOnLogin": true,
            "startMinimized": "minimized",
        });
        let behavior: DesktopBehavior =
            serde_json::from_value(payload).expect("the webview payload must deserialize");
        assert_eq!(
            behavior,
            DesktopBehavior {
                tray_enabled: true,
                tray_on_close: TrayOnClose::Quit,
                tray_on_minimize: TrayOnMinimize::Tray,
                start_on_login: true,
                start_minimized: StartMinimized::Minimized,
            }
        );
    }

    // A refusal is placed on the Preferences row that owns the field it
    // names, so a `setting` the payload does not carry would put the message
    // nowhere and the toggle would read "on" with nothing behind it.
    #[test]
    fn a_refusal_names_a_field_of_the_payload_it_answers() {
        let payload = serde_json::json!({
            "trayEnabled": true,
            "trayOnClose": "quit",
            "trayOnMinimize": "tray",
            "startOnLogin": true,
            "startMinimized": "minimized",
        });
        // The payload really is the one the command takes, so the field names
        // below are the live ones and not a copy that can drift.
        serde_json::from_value::<DesktopBehavior>(payload.clone())
            .expect("the webview payload must deserialize");
        for refusal in [
            BehaviorRefusal::tray("x".to_string()),
            BehaviorRefusal::start_on_login("x".to_string()),
        ] {
            assert!(
                payload.get(refusal.setting).is_some(),
                "{} is not a field of the payload",
                refusal.setting
            );
        }

        assert_eq!(
            serde_json::to_value(BehaviorRefusal::tray("no status-icon library".to_string()))
                .expect("a refusal must serialize"),
            serde_json::json!({ "setting": "trayEnabled", "message": "no status-icon library" }),
        );
    }

    #[test]
    fn set_desktop_behavior_payload_refuses_an_unknown_token() {
        let payload = serde_json::json!({
            "trayEnabled": true,
            "trayOnClose": "minimize",
            "trayOnMinimize": "tray",
            "startOnLogin": false,
            "startMinimized": "window",
        });
        serde_json::from_value::<DesktopBehavior>(payload)
            .expect_err("a token no contract declares must be refused");
    }
}
