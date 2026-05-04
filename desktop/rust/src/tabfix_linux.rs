//! Linux-only fix for Tab / Shift+Tab in the WebView.
//!
//! WebKitGTK consumes Tab and ISO_Left_Tab key events at the GTK widget
//! level for focus-chain traversal *before* delivering them to the
//! WebView's JavaScript event loop, so calling `event.preventDefault()`
//! from a contenteditable handler (e.g. ProseMirror's Tab plugin) is
//! too late to suppress the focus move. macOS uses WKWebView, which
//! delivers Tab keydowns to JS normally — the fix is Linux-only.
//!
//! This mirrors the approach we used in the prior Wails implementation
//! (commit c866006243e8e4a3d948602ea99935f661068e48,
//! `desktop/tabfix_linux.go`): intercept Tab at the GTK window level,
//! consume the GTK event so focus does not move, then synthesize a JS
//! KeyboardEvent against `document.activeElement` so editors and other
//! handlers still see the Tab. If the synthetic event is *not*
//! preventDefaulted, the same JS performs manual focus traversal among
//! focusable elements so ordinary Tab navigation still works outside
//! editors.
use gtk::glib;
use gtk::prelude::{GtkWindowExt, ObjectExt, WidgetExt};
use tauri::WebviewWindow;

const SHIFT_TAB_JS: &str = r#"(function(){
var e=new KeyboardEvent('keydown',{key:'Tab',code:'Tab',shiftKey:true,bubbles:true,cancelable:true});
if(document.activeElement&&document.activeElement.dispatchEvent(e)){
var f=Array.from(document.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"]),[contenteditable]')).filter(function(x){return x.offsetParent!==null});
var i=f.indexOf(document.activeElement);
if(i>0)f[i-1].focus();
}
})()"#;

const TAB_JS: &str = r#"(function(){
var e=new KeyboardEvent('keydown',{key:'Tab',code:'Tab',shiftKey:false,bubbles:true,cancelable:true});
if(document.activeElement&&document.activeElement.dispatchEvent(e)){
var f=Array.from(document.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"]),[contenteditable]')).filter(function(x){return x.offsetParent!==null});
var i=f.indexOf(document.activeElement);
if(i>=0&&i<f.length-1)f[i+1].focus();
}
})()"#;

pub fn install(window: &WebviewWindow) {
    let gtk_window = match window.gtk_window() {
        Ok(w) => w,
        Err(_) => return,
    };
    let webview = window.clone();
    gtk_window.connect_key_press_event(move |w, event| {
        let keyval = event.keyval();
        let is_tab =
            keyval == gdk::keys::constants::Tab || keyval == gdk::keys::constants::ISO_Left_Tab;
        if !is_tab {
            return glib::Propagation::Proceed;
        }

        // Only intercept while focus is inside the WebView. If a native
        // GTK widget (menu, popover, etc.) holds focus, let GTK route
        // Tab to it normally.
        let focus_is_webview = w
            .focused_widget()
            .map(|widget| widget.type_().name().starts_with("WebKit"))
            .unwrap_or(false);
        if !focus_is_webview {
            return glib::Propagation::Proceed;
        }

        let shift = event.state().contains(gdk::ModifierType::SHIFT_MASK);
        let script = if shift { SHIFT_TAB_JS } else { TAB_JS };
        let _ = webview.eval(script);
        glib::Propagation::Stop
    });
}
