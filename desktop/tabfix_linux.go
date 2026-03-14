//go:build linux

package main

/*
#cgo linux pkg-config: gtk+-3.0
#cgo LDFLAGS: -ldl

#include <dlfcn.h>
#include <gtk/gtk.h>

// Resolve WebKitGTK symbols at runtime via dlsym(RTLD_DEFAULT, ...) so we
// don't need to pkg-config webkit2gtk ourselves (which causes libsoup2/3
// conflicts when the webkit2_41 build tag doesn't match Wails).
// The symbols are already loaded by Wails.

typedef void (*evaluate_js_fn)(void *web_view, const char *script,
	gssize length, const char *world_name, const char *source_uri,
	GCancellable *cancellable, GAsyncReadyCallback callback,
	gpointer user_data);

static GType get_webkit_web_view_type() {
	static GType cached = 0;
	if (cached) return cached;
	GType (*fn)(void) = dlsym(RTLD_DEFAULT, "webkit_web_view_get_type");
	if (fn) cached = fn();
	return cached;
}

static void eval_js(void *web_view, const char *script) {
	static evaluate_js_fn fn = NULL;
	static int resolved = 0;
	if (!resolved) {
		resolved = 1;
		// Try the newer API first, fall back to the deprecated one.
		fn = (evaluate_js_fn)dlsym(RTLD_DEFAULT, "webkit_web_view_evaluate_javascript");
		if (!fn) {
			// webkit_web_view_run_javascript has a simpler signature:
			// (WebKitWebView*, const char*, GCancellable*, GAsyncReadyCallback, gpointer)
			// We can cast safely because extra trailing NULLs are harmless in the C ABI.
			fn = (evaluate_js_fn)dlsym(RTLD_DEFAULT, "webkit_web_view_run_javascript");
		}
	}
	if (fn) fn(web_view, script, -1, NULL, NULL, NULL, NULL, NULL);
}

// Intercept Tab/Shift+Tab at the GTK level to prevent focus traversal,
// then inject a synthetic JS KeyboardEvent so ProseMirror can handle it.
//
// We cannot use gtk_window_propagate_key_event() because it walks up the
// widget hierarchy, and each parent's GtkWidget base handler checks key
// bindings — the Tab binding triggers move-focus, causing focus traversal
// despite our handler returning TRUE.
//
// Instead, we suppress the GTK event entirely and dispatch a synthetic
// keydown event to document.activeElement via JS. If ProseMirror is
// focused, it handles Tab (heading convert, list indent, plan mode toggle,
// etc.). If not, we manually move focus to the next/previous focusable
// HTML element.
static gboolean on_tab_key_press(GtkWidget *widget, GdkEventKey *event, gpointer data) {
	if (event->keyval != GDK_KEY_Tab && event->keyval != GDK_KEY_ISO_Left_Tab)
		return FALSE;

	GType wv_type = get_webkit_web_view_type();
	if (!wv_type) return FALSE;

	GtkWidget *focus = gtk_window_get_focus(GTK_WINDOW(widget));
	if (!focus || !G_TYPE_CHECK_INSTANCE_TYPE(focus, wv_type))
		return FALSE;

	gboolean shift = (event->state & GDK_SHIFT_MASK) != 0;

	const char *js = shift ?
		"(function(){"
		"var e=new KeyboardEvent('keydown',{key:'Tab',code:'Tab',shiftKey:true,bubbles:true,cancelable:true});"
		"if(document.activeElement.dispatchEvent(e)){"
		"var f=Array.from(document.querySelectorAll("
		"'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),"
		"textarea:not([disabled]),[tabindex]:not([tabindex=\"-1\"]),[contenteditable]'"
		")).filter(function(x){return x.offsetParent!==null});"
		"var i=f.indexOf(document.activeElement);"
		"if(i>0)f[i-1].focus();"
		"}"
		"})()"
		:
		"(function(){"
		"var e=new KeyboardEvent('keydown',{key:'Tab',code:'Tab',shiftKey:false,bubbles:true,cancelable:true});"
		"if(document.activeElement.dispatchEvent(e)){"
		"var f=Array.from(document.querySelectorAll("
		"'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),"
		"textarea:not([disabled]),[tabindex]:not([tabindex=\"-1\"]),[contenteditable]'"
		")).filter(function(x){return x.offsetParent!==null});"
		"var i=f.indexOf(document.activeElement);"
		"if(i>=0&&i<f.length-1)f[i+1].focus();"
		"}"
		"})()";

	eval_js(focus, js);
	return TRUE;
}

static int tab_handler_installed = 0;

static gboolean install_tab_handler_idle(gpointer data) {
	if (tab_handler_installed) return G_SOURCE_REMOVE;
	tab_handler_installed = 1;

	GList *toplevels = gtk_window_list_toplevels();
	for (GList *l = toplevels; l != NULL; l = l->next) {
		if (GTK_IS_WINDOW(l->data)) {
			g_signal_connect(l->data, "key-press-event",
				G_CALLBACK(on_tab_key_press), NULL);
		}
	}
	g_list_free(toplevels);
	return G_SOURCE_REMOVE;
}

void doInstallTabKeyHandler() {
	g_idle_add(install_tab_handler_idle, NULL);
}
*/
import "C"

func installTabKeyHandler() {
	C.doInstallTabKeyHandler()
}
