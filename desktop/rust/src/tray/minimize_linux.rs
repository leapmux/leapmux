//! Linux: catch a minimize through GTK, and restore the window after one.
//!
//! Tauri 2 has no `WindowEvent::Minimized` and tao emits none, but GTK reports
//! the transition on the window's own `window-state-event`. That is the same
//! signal tao itself reads to track the state (`platform_impl/linux/window.rs`),
//! and connecting a second handler is safe as long as it propagates.

use std::sync::Arc;

// Through `gtk::glib`, never a direct `glib` dependency: a second entry in
// Cargo.toml can resolve to a different minor version than the one gtk 0.18
// re-exports, and the two `Propagation` types would then not be the same type.
// `tabfix_linux.rs` reaches glib the same way.
use gtk::glib;
use gtk::prelude::{GtkWindowExt, WidgetExt};
use tauri::WebviewWindow;

use super::TrayState;

/// Install the minimize hook on the main window.
pub(crate) fn install(window: &WebviewWindow, state: Arc<TrayState>) {
    let Ok(gtk_window) = window.gtk_window() else {
        return;
    };
    let webview = window.clone();
    gtk_window.connect_window_state_event(move |_, event| {
        let changed = event.changed_mask().contains(gdk::WindowState::ICONIFIED);
        let iconified = event
            .new_window_state()
            .contains(gdk::WindowState::ICONIFIED);
        // The RISING edge only. The mask alone also fires when the window is
        // restored, and `hide()` below emits further state events of its own.
        if !(changed && iconified) {
            return glib::Propagation::Proceed;
        }
        // Already hidden means this event came from our own unmap. Without the
        // guard the handler re-enters itself for as long as GTK keeps
        // reporting state.
        if !webview.is_visible().unwrap_or(false) {
            return glib::Propagation::Proceed;
        }
        let state = state.clone();
        let webview = webview.clone();
        // DEFERRED to the next main-loop turn. `hide()` runs inline on the
        // main thread and unmaps the widget, and unmapping a widget from
        // inside its own state-event emission asks GTK to tear down what it is
        // still dispatching.
        glib::idle_add_local_once(move || {
            super::handle_minimize(&state, &webview);
        });
        glib::Propagation::Proceed
    });
}

/// Raise and focus the window after a tray restore.
///
/// tao clears its Linux `minimized` flag only when GTK emits a state event,
/// which cannot happen while the window is unmapped. So after `show()` tao
/// still believes the window is minimized and its `set_focus` returns without
/// doing anything. `present()` deiconifies, maps, raises and focuses in one
/// operation that the window manager honours, which is the only reliable
/// restore here.
pub(crate) fn present(window: &WebviewWindow) {
    if let Ok(gtk_window) = window.gtk_window() {
        gtk_window.present();
    }
}
