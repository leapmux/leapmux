//! Windows: catch a minimize through the `Resized` event tao already delivers.
//!
//! `WM_SIZE` with `SIZE_MINIMIZED` reaches tao as `WindowEvent::Resized`, and
//! `is_minimized()` is `IsIconic(hwnd)` -- the operating system's own answer,
//! not a guess from the reported size. So the shell's existing
//! `on_window_event` handler is the whole hook, and there is no window
//! subclass to install.
//!
//! A `WM_SYSCOMMAND` / `SC_MINIMIZE` subclass was the obvious alternative and
//! is wrong here: the app draws its own titlebar, whose minimize button calls
//! `w.minimize()` and therefore `ShowWindow(SW_MINIMIZE)` directly, raising no
//! `WM_SYSCOMMAND` at all. The subclass would miss the app's own button while
//! catching the ones it does not own.

use std::sync::Arc;

use tauri::{Manager, Window};

use super::TrayState;

/// Answer a `Resized` event on the main window.
///
/// Re-entrancy terminates on its own, but NOT because the state clears.
/// `is_minimized()` is `IsIconic`, and `SW_HIDE` leaves `WS_MINIMIZE` set, so a
/// window that is both hidden and minimized still answers true. It stops one
/// level down: `hide()` only clears tao's own `VISIBLE` flag, and
/// `WindowFlags::apply_diff` returns without a Win32 call when the diff is
/// empty, so a second `hide()` does nothing and raises no further `WM_SIZE`.
pub(crate) fn on_resized(window: &Window) {
    let app = window.app_handle();
    let Some(state) = app.try_state::<Arc<TrayState>>() else {
        return;
    };
    let Some(webview) = app.get_webview_window("main") else {
        return;
    };
    if webview.is_minimized().unwrap_or(false) {
        super::handle_minimize(state.inner(), &webview);
    }
}
