//! macOS: catch a minimize through `NSWindowDidMiniaturizeNotification`.
//!
//! Every way a window can be miniaturized converges on `performMiniaturize:` --
//! the yellow traffic light, Cmd+M, the Window menu's Minimize item, and a
//! title-bar double click -- and every one of them posts this notification. So
//! one observer covers them all, where intercepting any single affordance would
//! leave the others behaving differently from the preference.
//!
//! AppKit offers no veto. There is no `windowShouldMiniaturize:`, and the
//! notification is posted AFTER the animation finishes, so the window visibly
//! genies into the Dock before the tile disappears. That artefact is accepted:
//! the alternative is to intercept only the affordances the app itself owns,
//! which makes the yellow button disobey a setting the menu item honours.

use std::ptr::NonNull;
use std::sync::Arc;

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::AnyObject;
use objc2_app_kit::NSWindowDidMiniaturizeNotification;
use objc2_foundation::{NSNotification, NSNotificationCenter};
use tauri::WebviewWindow;

use super::TrayState;

/// Install the minimize hook on the main window.
pub(crate) fn install(window: &WebviewWindow, state: Arc<TrayState>) {
    let Ok(ns_window) = window.ns_window() else {
        return;
    };
    let Some(object) = NonNull::new(ns_window.cast::<AnyObject>()) else {
        return;
    };
    let webview = window.clone();
    let block = RcBlock::new(move |_: NonNull<NSNotification>| {
        // Posted on the main thread, because the observer is registered with a
        // nil queue.
        super::handle_minimize(&state, &webview);
    });
    // SAFETY: `object` is the live NSWindow that tauri owns and outlives the
    // observer, and the block is copied by the notification center and only
    // invoked on the main thread.
    let observer = unsafe {
        NSNotificationCenter::defaultCenter().addObserverForName_object_queue_usingBlock(
            Some(NSWindowDidMiniaturizeNotification),
            Some(object.as_ref()),
            None,
            &block,
        )
    };
    // The observer lives for the process. Dropping the token DEREGISTERS it,
    // which would leave the hook silently installed-but-dead, so ownership goes
    // to the notification center instead.
    let _ = Retained::into_raw(observer);
}

/// Take the window out of the Dock before hiding it.
///
/// `orderOut:` on a still-miniaturized window leaves it miniaturized, and tao's
/// `set_focus` is a no-op while `isMiniaturized()` holds -- so the next
/// "Show LeapMux" would produce an unfocused window behind whatever the user
/// moved on to. Deminiaturizing first, in the same run-loop turn, is what makes
/// the restore path work.
pub(crate) fn prepare_hide(window: &WebviewWindow) {
    let _ = window.unminimize();
}
