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
pub(crate) mod appindicator_linux;
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

impl TrayOnClose {
    /// The `AtomicU8` stand-in for each variant, so `TrayState` can read the
    /// live policy without a lock. Beside the type, so a new variant is one
    /// edit rather than a hunt through four module constants.
    ///
    /// The codes of the two tray enums do NOT agree: `Tray` is 0 here and 1 in
    /// `TrayOnMinimize`. That is exactly why each table belongs to its own
    /// type -- as free constants in one namespace, `CLOSE_TRAY` and
    /// `MINIMIZE_TRAY` were two similarly named `u8`s with different values.
    const TRAY: u8 = 0;
    const QUIT: u8 = 1;

    /// An exhaustive match, NOT `#[repr(u8)]` plus `self as u8`: a new variant
    /// must be a compile error here, not an automatic discriminant that
    /// `from_code` then silently maps to `Tray`.
    fn code(self) -> u8 {
        match self {
            Self::Tray => Self::TRAY,
            Self::Quit => Self::QUIT,
        }
    }

    /// `code` is the only writer of the atomic and it writes one of the two
    /// values above, so the fallback arm is unreachable. It cannot be removed:
    /// no match over `u8` is exhaustive.
    fn from_code(code: u8) -> Self {
        match code {
            Self::QUIT => Self::Quit,
            _ => Self::Tray,
        }
    }
}

/// What the user asked LeapMux to do when a window is minimized.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum TrayOnMinimize {
    Tray,
    Taskbar,
}

impl TrayOnMinimize {
    /// See `TrayOnClose::TRAY` for why each enum owns its own table.
    const TASKBAR: u8 = 0;
    const TRAY: u8 = 1;

    fn code(self) -> u8 {
        match self {
            Self::Tray => Self::TRAY,
            Self::Taskbar => Self::TASKBAR,
        }
    }

    fn from_code(code: u8) -> Self {
        match code {
            Self::TRAY => Self::Tray,
            _ => Self::Taskbar,
        }
    }
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

/// Which window event the policy answers for.
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
    ///
    /// The tokens come from contracts/desktop.json, like the window-behaviour
    /// ones: this shell writes them and `parseLaunchVisibility` in the webview
    /// reads them, so they are a vocabulary two languages spell. A hand-written
    /// pair would drift in silence, because that parse answers `normal` for
    /// anything it does not recognize -- so a renamed token would show a window
    /// every hidden login launch asked to keep in the tray.
    pub(crate) fn as_str(self) -> &'static str {
        match self {
            Self::Normal => contracts::LAUNCH_VISIBILITY_NORMAL,
            Self::Minimized => contracts::LAUNCH_VISIBILITY_MINIMIZED,
            Self::Hidden => contracts::LAUNCH_VISIBILITY_HIDDEN,
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
    /// The built-in defaults.
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

impl DesktopBehavior {
    /// The four values a launch reads, without the one it must not.
    pub(crate) fn window(&self) -> WindowBehavior {
        WindowBehavior {
            tray_enabled: self.tray_enabled,
            tray_on_close: self.tray_on_close,
            tray_on_minimize: self.tray_on_minimize,
            start_minimized: self.start_minimized,
        }
    }
}

/// The four values that decide the tray and the initial window.
///
/// `start_on_login` is NOT among them, and this type is how that exclusion
/// becomes structural rather than a rule somebody has to remember. The
/// operating system's login-item registration is that setting's state, so a
/// launch has nothing to read; the previous single type had to invent
/// `start_on_login: false` for every config it decoded, and a comment claiming
/// the field was "absent" while it held a plausible-looking lie.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct WindowBehavior {
    pub tray_enabled: bool,
    pub tray_on_close: TrayOnClose,
    pub tray_on_minimize: TrayOnMinimize,
    pub start_minimized: StartMinimized,
}

impl Default for WindowBehavior {
    /// The built-in defaults, which are also what a fresh config decodes to.
    fn default() -> Self {
        DesktopBehavior::default().window()
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
///
/// `launch_was_wrong` covers the other way the shell can strand a window. The
/// launch decides from a DEVICE CACHE, and the first push is the moment the
/// account's real values arrive; when they say a window and the launch left
/// none, the cache was stale and the window comes back. Without it a machine
/// whose cached `start_minimized` disagrees with the account keeps no window
/// for the whole session, and only the next launch repairs it.
pub(crate) fn must_reveal_window(
    tray_enabled: bool,
    window_visible: bool,
    launch_was_wrong: bool,
) -> bool {
    if window_visible {
        return false;
    }
    !tray_enabled || launch_was_wrong
}

/// Downgrade a launch the user could not undo into one they can.
///
/// `Hidden` needs a tray icon, because that icon is the only way back. Without
/// one, "no window anywhere" is the state to prevent, so the window is shown.
/// `launch_visibility` already applies this rule when it decides; the startup
/// safety net applies it again, because the tray can fail to build between the
/// decision and the deadline.
pub(crate) fn reachable_visibility(
    visibility: LaunchVisibility,
    tray_enabled: bool,
) -> LaunchVisibility {
    match visibility {
        LaunchVisibility::Hidden if !tray_enabled => LaunchVisibility::Normal,
        other => other,
    }
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

/// Bridge one behaviour enum to and from its CONTRACT TOKEN.
///
/// The token is what the sidecar stores and what crosses the wire, so this is
/// the one normalization in the system: an empty token is a fresh config, and
/// an unrecognized one can only come from a file a person edited. Both mean the
/// setting's documented default.
///
/// The rule lives HERE, beside the policy that reads the value, and nowhere
/// else. The Go sidecar used to re-apply it while translating to a proto enum,
/// which made two authorities over one rule and hid a new contract token behind
/// a silent default on both sides.
macro_rules! token_bridge {
    ($ty:ident, default $default:ident, $( $token:path => $variant:ident ),+ $(,)?) => {
        impl $ty {
            pub(crate) fn from_token(token: &str) -> Self {
                match token {
                    $( $token => Self::$variant, )+
                    _ => Self::$default,
                }
            }

            /// An exhaustive match, so a new variant is a compile error here
            /// rather than a value that reaches the wire as an empty string.
            pub(crate) fn to_token(self) -> &'static str {
                match self {
                    $( Self::$variant => $token, )+
                }
            }
        }
    };
}

token_bridge!(
    TrayOnClose,
    default Tray,
    contracts::TRAY_ON_CLOSE_TRAY => Tray,
    contracts::TRAY_ON_CLOSE_QUIT => Quit,
);
token_bridge!(
    TrayOnMinimize,
    default Taskbar,
    contracts::TRAY_ON_MINIMIZE_TRAY => Tray,
    contracts::TRAY_ON_MINIMIZE_TASKBAR => Taskbar,
);
token_bridge!(
    StartMinimized,
    default Window,
    contracts::START_MINIMIZED_WINDOW => Window,
    contracts::START_MINIMIZED_MINIMIZED => Minimized,
);

impl WindowBehavior {
    /// The four values the sidecar caches. There is no `start_on_login` to
    /// decode, because this type does not carry one.
    pub(crate) fn from_config(cfg: &proto::DesktopConfig) -> Self {
        Self {
            tray_enabled: cfg.tray_enabled,
            tray_on_close: TrayOnClose::from_token(&cfg.tray_on_close),
            tray_on_minimize: TrayOnMinimize::from_token(&cfg.tray_on_minimize),
            start_minimized: StartMinimized::from_token(&cfg.start_minimized),
        }
    }
}

// --- The live state ---

/// The launch decision, readable once by the webview and thereafter inert.
pub(crate) struct LaunchState {
    visibility: LaunchVisibility,
    /// Whether the operating system started this process from the login item.
    /// Kept so the first push can decide the launch again against the account's
    /// real values; see `launch_was_wrong`.
    autostart_launch: bool,
    consumed: AtomicBool,
    reconciled: AtomicBool,
}

impl LaunchState {
    pub(crate) fn new(visibility: LaunchVisibility, autostart_launch: bool) -> Self {
        Self {
            visibility,
            autostart_launch,
            consumed: AtomicBool::new(false),
            reconciled: AtomicBool::new(false),
        }
    }

    /// Whether the launch left the window out of the way on a decision that the
    /// account's real values contradict.
    ///
    /// The launch reads a DEVICE CACHE, which the account can have moved past
    /// on another machine. This decides the launch again from the values the
    /// first push carries, and reports that the cache was stale.
    ///
    /// It answers true at most ONCE. After the first push the user owns the
    /// window, and a later push that happens to resolve to `Normal` must not
    /// drag a window they deliberately hid to the tray back on screen.
    pub(crate) fn launch_was_wrong(
        &self,
        start_minimized: StartMinimized,
        tray_enabled: bool,
    ) -> bool {
        if self.visibility == LaunchVisibility::Normal {
            return false;
        }
        if self.reconciled.swap(true, Ordering::SeqCst) {
            return false;
        }
        launch_visibility(self.autostart_launch, start_minimized, tray_enabled)
            == LaunchVisibility::Normal
    }

    /// The decision, ONCE. `switch_mode` navigates back to the launcher, which
    /// remounts and asks again; without the latch that second answer would
    /// re-hide the window the user now sees.
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
    /// Serializes `set_desktop_behavior`. Tauri gives each invocation its own
    /// task, and two that overlap can otherwise reach the sidecar in the
    /// opposite order to the one the user chose, which leaves the device cache
    /// holding the older set -- and the next launch decides the tray and the
    /// window state from it, before the webview exists to correct it.
    ///
    /// A TOKIO mutex, because the command holds it across the `await` on the
    /// sidecar. It never guards the policy reads, which stay lock-free.
    pub(crate) push_lock: tokio::sync::Mutex<()>,
}

impl TrayState {
    pub(crate) fn new() -> Self {
        let defaults = WindowBehavior::default();
        Self {
            enabled: AtomicBool::new(false),
            on_close: AtomicU8::new(defaults.tray_on_close.code()),
            on_minimize: AtomicU8::new(defaults.tray_on_minimize.code()),
            icon: Mutex::new(None),
            push_lock: tokio::sync::Mutex::new(()),
        }
    }

    /// Whether an icon exists right now.
    pub(crate) fn is_enabled(&self) -> bool {
        self.enabled.load(Ordering::SeqCst)
    }

    fn on_close(&self) -> TrayOnClose {
        TrayOnClose::from_code(self.on_close.load(Ordering::SeqCst))
    }

    fn on_minimize(&self) -> TrayOnMinimize {
        TrayOnMinimize::from_code(self.on_minimize.load(Ordering::SeqCst))
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

    fn store_policy(&self, behavior: &WindowBehavior) {
        self.on_close
            .store(behavior.tray_on_close.code(), Ordering::SeqCst);
        self.on_minimize
            .store(behavior.tray_on_minimize.code(), Ordering::SeqCst);
    }

    /// Bring the tray into the requested state, and record what was achieved.
    ///
    /// Returns the error the caller reports to the webview. `enabled` records
    /// the EFFECTIVE result either way, so a failure downgrades the policy
    /// rather than leaving it lying.
    pub(crate) fn apply(&self, app: &AppHandle, behavior: &WindowBehavior) -> Result<(), String> {
        self.store_policy(behavior);
        if !behavior.tray_enabled {
            return self.hide_icon();
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

    /// Take the icon off screen, and record whether it went.
    ///
    /// The mirror of `ensure_icon`, and it reports the same two failures: a
    /// poisoned lock and a call the platform refused. `enabled` follows what
    /// ACTUALLY happened, so an icon that is still on screen still reads as
    /// enabled -- it is still a way back to the window. Claiming it is gone
    /// would let `must_reveal_window` yank a window the user hid on purpose,
    /// and would leave the failure with no message on any row.
    fn hide_icon(&self) -> Result<(), String> {
        let guard = self
            .icon
            .lock()
            .map_err(|_| "tray icon state is poisoned".to_string())?;
        let Some(icon) = guard.as_ref() else {
            // No icon was ever built, so there is nothing to take off screen.
            self.enabled.store(false, Ordering::SeqCst);
            return Ok(());
        };
        icon.set_visible(false)
            .map_err(|err| format!("hide the tray icon: {err}"))?;
        self.enabled.store(false, Ordering::SeqCst);
        Ok(())
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
        if guard.is_none() {
            // RECORDED before it is shown, and this order is load-bearing. A
            // `set_visible` that fails after a successful build must not drop
            // the icon: the guard would stay `None`, the next enable would
            // build a SECOND icon under the same id, and each build appends
            // another app-wide menu listener that nothing prunes -- so one
            // "Show LeapMux" click would run the handler once per build.
            *guard = Some(build_tray(app)?);
        }
        let icon = guard.as_ref().expect("the icon is recorded above");
        icon.set_visible(true)
            .map_err(|err| format!("show the tray icon: {err}"))
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
    if !appindicator_linux::available() {
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

/// Bring the main window up at launch: the saved size and mode, then
/// `visibility`.
///
/// The shell owns this rather than the webview, because the webview does not
/// exist on the distributed-reattach route: the shell navigates straight to the
/// hub and `LauncherView` -- the only caller of `restoreWindowGeometry` -- never
/// mounts. `geometry` is the persisted config on that route, and `None` from
/// the startup safety net, which repairs the visibility alone.
///
/// The order matches the webview's: size first, so a Wayland compositor sees
/// the final size at the first map, then the mode, then the reveal. Fullscreen
/// is the exception and comes last, because macOS performs that transition on a
/// visible window only.
pub(crate) fn apply_launch_visibility(
    app: &AppHandle,
    visibility: LaunchVisibility,
    geometry: Option<&proto::DesktopConfig>,
) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    // No geometry means the safety net, which repairs the visibility alone and
    // has no saved mode to apply -- so it takes the ordinary windowed one.
    let mode = geometry.map_or(contracts::WINDOW_MODE_NORMAL, |cfg| {
        cfg.window_mode.as_str()
    });
    if let Some(cfg) = geometry {
        if cfg.window_width > 0 && cfg.window_height > 0 {
            let _ = window.set_size(tauri::LogicalSize::new(
                f64::from(cfg.window_width),
                f64::from(cfg.window_height),
            ));
        }
    }
    // Maximized applies to a hidden window too: the flag survives an unmapped
    // window, so the first "Show LeapMux" produces the window the user left.
    if mode == contracts::WINDOW_MODE_MAXIMIZED {
        let _ = window.maximize();
    }
    match visibility {
        LaunchVisibility::Hidden => return,
        LaunchVisibility::Minimized => {
            let _ = window.show();
            let _ = window.minimize();
            return;
        }
        LaunchVisibility::Normal => {
            let _ = window.show();
        }
    }
    if mode == contracts::WINDOW_MODE_FULLSCREEN {
        let _ = window.set_fullscreen(true);
    }
}

/// The policy answer for a minimize, and the hide it may call for.
///
/// The three platform hooks funnel here so the decision is made once. Linux and
/// Windows REACT to a minimize the operating system already performed. macOS
/// INTERCEPTS the request before AppKit performs it, and reads the answer to
/// decide whether to let the ordinary minimize proceed.
///
/// Returns whether the window was hidden.
pub(crate) fn handle_minimize(state: &TrayState, window: &tauri::WebviewWindow) -> bool {
    if state.window_action(WindowIntent::Minimized) != WindowAction::HideWindow {
        return false;
    }
    let _ = window.hide();
    true
}

/// Build the tray from the device cache, decide the launch, and register both
/// with the application.
///
/// Everything `setup` needs to bootstrap this module, in one call. The config
/// LOAD stays with the caller: reading it needs the sidecar transport, which
/// this module deliberately knows nothing about.
pub(crate) fn install(
    app: &AppHandle,
    cached: &proto::DesktopConfig,
    autostart_launch: bool,
) -> Arc<LaunchState> {
    let behavior = WindowBehavior::from_config(cached);

    let state = Arc::new(TrayState::new());
    if let Err(err) = state.apply(app, &behavior) {
        // A tray that cannot be created is not a launch failure. The policy
        // records the effective state, so nothing will hide a window the user
        // could not get back.
        eprintln!("leapmux-desktop: {err}");
    }

    let visibility = launch_visibility(autostart_launch, behavior.start_minimized, state.is_enabled());
    if let Some(window) = app.get_webview_window("main") {
        install_minimize_hook(&window, state.clone());
    }
    app.manage(state);

    let launch = Arc::new(LaunchState::new(visibility, autostart_launch));
    app.manage(launch.clone());
    launch
}

/// How long the shell waits for the webview to show the window before it does
/// so itself. The deadline runs from the START of `setup`, so it limits the
/// blocking calls inside it as well as the frontend.
pub(crate) const STARTUP_REVEAL_DEADLINE: std::time::Duration =
    std::time::Duration::from_secs(5);

/// Show the main window if nothing else did within `STARTUP_REVEAL_DEADLINE`.
///
/// The net exists for a frontend that never runs -- a JS error, a webview that
/// fails to load -- which would otherwise leave a process with no window and no
/// way to reach it.
///
/// It routes through the launch decision rather than testing one visibility
/// value, so every launch the shell can decide is honoured here too. A
/// `Minimized` launch is mapped and minimized, which is where the launch asked
/// for it and a place the taskbar can reach; without that rule the net raised a
/// full window in front of the user five seconds into a login they asked to
/// start out of the way.
///
/// `peek`, not `take`: the net asks what THIS launch decided, and the webview
/// consumes the decision within milliseconds of starting, so a consuming read
/// would answer `Normal` and reveal the window that a hidden launch left in the
/// tray on purpose.
pub(crate) fn spawn_startup_safety_net(handle: AppHandle, autostart_launch: bool) {
    std::thread::spawn(move || {
        std::thread::sleep(STARTUP_REVEAL_DEADLINE);
        let Some(window) = handle.get_webview_window("main") else {
            return;
        };
        // The frontend did its job, so the net has nothing to repair. A probe
        // that fails reads as NOT visible, which reveals: that direction costs
        // a raised window, and the other one costs an app the user cannot see.
        if window.is_visible().unwrap_or(false) {
            return;
        }
        let visibility = match handle.try_state::<Arc<LaunchState>>() {
            Some(launch) => launch.peek(),
            // `setup` has not decided yet, so this fires while a blocking call
            // inside it is still running. A hand launch always ends with a
            // visible window, so reveal. A LOGIN launch may legitimately end
            // hidden, and `setup` reveals it a moment later on whichever route
            // it takes, so leave it alone.
            None if autostart_launch => return,
            None => LaunchVisibility::Normal,
        };
        let tray_enabled = handle
            .try_state::<Arc<TrayState>>()
            .is_some_and(|tray| tray.is_enabled());
        apply_launch_visibility(&handle, reachable_visibility(visibility, tray_enabled), None);
    });
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

    // The answer the macOS override reads at the moment of the click, from the
    // LIVE state rather than the free function above. It calls `super` -- the
    // ordinary minimize -- for every answer except `HideWindow`, so a state
    // that reports `HideWindow` while no icon exists would hide a window with
    // nothing to bring it back, and one that reports `LeaveMinimized` after the
    // user chose the tray would put the window in the Dock they asked to skip.
    #[test]
    fn the_live_state_hides_on_minimize_only_with_an_icon_and_the_tray_choice() {
        let state = TrayState::new();
        let choose = |tray_enabled: bool, on_minimize: TrayOnMinimize| {
            state.enabled.store(tray_enabled, Ordering::SeqCst);
            state.store_policy(&WindowBehavior {
                tray_enabled,
                tray_on_minimize: on_minimize,
                ..WindowBehavior::default()
            });
            state.window_action(WindowIntent::Minimized)
        };

        assert_eq!(choose(true, TrayOnMinimize::Tray), WindowAction::HideWindow);

        // The three answers that must leave the ordinary minimize alone. Each
        // one reaches `super` on macOS, so the window still goes to the Dock.
        assert_eq!(
            choose(true, TrayOnMinimize::Taskbar),
            WindowAction::LeaveMinimized
        );
        assert_eq!(
            choose(false, TrayOnMinimize::Tray),
            WindowAction::LeaveMinimized
        );
        assert_eq!(
            choose(false, TrayOnMinimize::Taskbar),
            WindowAction::LeaveMinimized
        );

        // And back, because the override reads the state on every click. A
        // preference the user changes mid-session must reach the next minimize.
        assert_eq!(choose(true, TrayOnMinimize::Tray), WindowAction::HideWindow);
    }

    #[test]
    fn disabling_the_tray_reveals_a_hidden_window() {
        assert!(must_reveal_window(false, false, false));
    }

    #[test]
    fn disabling_the_tray_leaves_a_visible_window_alone() {
        assert!(!must_reveal_window(false, true, false));
    }

    #[test]
    fn an_enabled_tray_never_forces_the_window_open() {
        assert!(!must_reveal_window(true, true, false));
        assert!(!must_reveal_window(true, false, false));
    }

    // The other way the shell can strand a window: the launch decided from a
    // device cache the account has since moved past.
    #[test]
    fn a_wrong_launch_reveals_a_window_even_with_a_tray() {
        assert!(must_reveal_window(true, false, true));
    }

    #[test]
    fn a_wrong_launch_never_raises_a_window_that_is_already_up() {
        assert!(!must_reveal_window(true, true, true));
    }

    // A hidden launch needs an icon to come back from. The net applies the
    // rule again, because the tray can fail to build after the decision.
    #[test]
    fn a_hidden_launch_without_a_tray_becomes_a_visible_one() {
        assert_eq!(
            reachable_visibility(LaunchVisibility::Hidden, false),
            LaunchVisibility::Normal
        );
    }

    #[test]
    fn reachable_visibility_leaves_every_other_launch_alone() {
        assert_eq!(
            reachable_visibility(LaunchVisibility::Hidden, true),
            LaunchVisibility::Hidden
        );
        for tray in [true, false] {
            for launch in [LaunchVisibility::Normal, LaunchVisibility::Minimized] {
                assert_eq!(reachable_visibility(launch, tray), launch);
            }
        }
    }

    // The stale-cache repair, which `must_reveal_window` consumes. The launch
    // hid the window because the CACHED `start_minimized` said so; the first
    // push carries the account's real value, and it says a window.
    #[test]
    fn a_launch_the_account_contradicts_reports_itself_wrong_once() {
        let state = LaunchState::new(LaunchVisibility::Hidden, true);
        assert!(state.launch_was_wrong(StartMinimized::Window, true));
        // ONCE. After the first push the user owns the window, and a later
        // push must not drag one they hid to the tray back on screen.
        assert!(!state.launch_was_wrong(StartMinimized::Window, true));
    }

    #[test]
    fn a_launch_the_account_agrees_with_reports_nothing() {
        let state = LaunchState::new(LaunchVisibility::Hidden, true);
        assert!(!state.launch_was_wrong(StartMinimized::Minimized, true));
    }

    // A hand launch already shows a window, so there is nothing to repair --
    // and the latch must stay unspent for the case that does need it.
    #[test]
    fn a_normal_launch_is_never_wrong() {
        let state = LaunchState::new(LaunchVisibility::Normal, false);
        assert!(!state.launch_was_wrong(StartMinimized::Window, true));
        assert!(!state.launch_was_wrong(StartMinimized::Window, false));
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

    // The live policy crosses two `AtomicU8`s whose tables both spell "TRAY"
    // with DIFFERENT values (0 for a close, 1 for a minimize). A swapped pair
    // would invert one setting and pass every other test in this file, and
    // nothing covered `store_policy` or the two decoders before this.
    #[test]
    fn the_live_policy_round_trips_every_stored_variant() {
        let state = TrayState::new();
        assert_eq!(state.on_close(), TrayOnClose::Tray, "a fresh state is the default");
        assert_eq!(state.on_minimize(), TrayOnMinimize::Taskbar);

        for on_close in [TrayOnClose::Tray, TrayOnClose::Quit] {
            for on_minimize in [TrayOnMinimize::Tray, TrayOnMinimize::Taskbar] {
                state.store_policy(&WindowBehavior {
                    tray_on_close: on_close,
                    tray_on_minimize: on_minimize,
                    ..WindowBehavior::default()
                });
                assert_eq!(state.on_close(), on_close);
                assert_eq!(state.on_minimize(), on_minimize);
            }
        }
    }

    // The token is what the sidecar stores, so a variant that does not survive
    // the trip out and back is a preference the next launch reads as something
    // else. `to_token` must also emit the CONTRACT token, not a literal that
    // happens to match: the hub validates writes against the same strings.
    #[test]
    fn behavior_variants_round_trip_through_their_contract_tokens() {
        for v in [TrayOnClose::Tray, TrayOnClose::Quit] {
            assert_eq!(TrayOnClose::from_token(v.to_token()), v);
        }
        for v in [TrayOnMinimize::Tray, TrayOnMinimize::Taskbar] {
            assert_eq!(TrayOnMinimize::from_token(v.to_token()), v);
        }
        for v in [StartMinimized::Window, StartMinimized::Minimized] {
            assert_eq!(StartMinimized::from_token(v.to_token()), v);
        }

        assert_eq!(TrayOnClose::Quit.to_token(), contracts::TRAY_ON_CLOSE_QUIT);
        assert_eq!(
            TrayOnMinimize::Tray.to_token(),
            contracts::TRAY_ON_MINIMIZE_TRAY
        );
        assert_eq!(
            StartMinimized::Minimized.to_token(),
            contracts::START_MINIMIZED_MINIMIZED
        );
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

    // The one normalization in the system, and it lives here rather than in
    // the sidecar. An EMPTY token is a fresh config or one written before the
    // field existed; an unrecognized one can only come from a hand-edited file.
    // Both mean the setting's documented default.
    #[test]
    fn an_empty_or_unknown_token_falls_back_to_the_documented_default() {
        for token in ["", "bogus"] {
            assert_eq!(TrayOnClose::from_token(token), TrayOnClose::Tray);
            assert_eq!(TrayOnMinimize::from_token(token), TrayOnMinimize::Taskbar);
            assert_eq!(StartMinimized::from_token(token), StartMinimized::Window);
        }
    }

    // A fresh config must decode to the built-in defaults, or a first launch
    // would create a tray nobody asked for.
    #[test]
    fn a_fresh_config_reads_as_the_built_in_defaults() {
        let cfg = proto::DesktopConfig::default();
        let behavior = WindowBehavior::from_config(&cfg);
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
    fn the_launch_decision_is_reported_once() {
        let state = LaunchState::new(LaunchVisibility::Hidden, true);
        assert_eq!(state.peek(), LaunchVisibility::Hidden);
        assert_eq!(state.take(), LaunchVisibility::Hidden);
        // A mode switch remounts the launcher, which asks again. The second
        // answer must not re-hide the window the user now sees.
        assert_eq!(state.take(), LaunchVisibility::Normal);
    }

    // The safety net asks what THIS launch decided, five seconds in, and the
    // webview consumes the decision within milliseconds. A `peek` that
    // answered `Normal` once consumed would therefore make the net reveal the
    // window that a hidden login launch deliberately left in the tray.
    #[test]
    fn peeking_still_reports_a_consumed_decision() {
        let state = LaunchState::new(LaunchVisibility::Hidden, true);
        assert_eq!(state.take(), LaunchVisibility::Hidden);
        assert_eq!(state.peek(), LaunchVisibility::Hidden);
    }

    // The four fields the sidecar caches, decoded from the tokens it stores.
    #[test]
    fn the_cached_config_decodes_every_field() {
        let cfg = proto::DesktopConfig {
            tray_enabled: true,
            tray_on_close: contracts::TRAY_ON_CLOSE_QUIT.to_string(),
            tray_on_minimize: contracts::TRAY_ON_MINIMIZE_TRAY.to_string(),
            start_minimized: contracts::START_MINIMIZED_MINIMIZED.to_string(),
            ..Default::default()
        };

        let behavior = WindowBehavior::from_config(&cfg);
        assert!(behavior.tray_enabled);
        assert_eq!(behavior.tray_on_close, TrayOnClose::Quit);
        assert_eq!(behavior.tray_on_minimize, TrayOnMinimize::Tray);
        assert_eq!(behavior.start_minimized, StartMinimized::Minimized);
    }

    // The login item is not a field of the cached type AT ALL, which is how
    // the exclusion stays true without anybody remembering it. The pushed
    // payload carries it; the projection the sidecar receives does not, so no
    // write can leak it into a file the operating system already owns.
    #[test]
    fn the_cached_projection_drops_the_login_item() {
        let pushed = DesktopBehavior {
            tray_enabled: true,
            tray_on_close: TrayOnClose::Quit,
            tray_on_minimize: TrayOnMinimize::Tray,
            start_on_login: true,
            start_minimized: StartMinimized::Minimized,
        };
        // Every field the cache keeps survives the projection...
        let cached = pushed.window();
        assert_eq!(cached.tray_enabled, pushed.tray_enabled);
        assert_eq!(cached.tray_on_close, pushed.tray_on_close);
        assert_eq!(cached.tray_on_minimize, pushed.tray_on_minimize);
        assert_eq!(cached.start_minimized, pushed.start_minimized);
        // ...and `start_on_login` cannot: two payloads that differ only there
        // project to the same cached value, so the file cannot record it.
        let mut without = pushed;
        without.start_on_login = false;
        assert_eq!(without.window(), cached);
    }

    // Each variant must report the CONTRACT token, not a literal that happens
    // to match today. `parseLaunchVisibility` in the webview answers `normal`
    // for every token it does not recognize, so a rename on one side alone
    // would show a window on each hidden login launch and fail nowhere.
    #[test]
    fn launch_visibility_reports_the_contract_tokens() {
        assert_eq!(
            LaunchVisibility::Normal.as_str(),
            contracts::LAUNCH_VISIBILITY_NORMAL
        );
        assert_eq!(
            LaunchVisibility::Minimized.as_str(),
            contracts::LAUNCH_VISIBILITY_MINIMIZED
        );
        assert_eq!(
            LaunchVisibility::Hidden.as_str(),
            contracts::LAUNCH_VISIBILITY_HIDDEN
        );

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
